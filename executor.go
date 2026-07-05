package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// routeModel implements pluginabi.MethodModelRoute. For matching requests we route
// to our own executor (TargetKind=self). Non-matching requests return Handled=false
// so the host's default provider chain takes over — we are transparent for everything
// we don't care about.
func routeModel(raw []byte) ([]byte, error) {
	var req rpcModelRouteRequest
	if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
		return nil, errUnmarshal
	}
	cfg := loadedConfig()
	if !cfg.Enabled {
		return okEnvelope(pluginapi.ModelRouteResponse{Handled: false})
	}

	// Only intercept Codex/chat-completions-flagged requests. We accept everything
	// when UpstreamProviders is empty (default) — the executor will decide per-response.
	// Otherwise we only route matching provider tags.
	matched := routeMatches(cfg, req)
	resp := pluginapi.ModelRouteResponse{
		Handled:    matched,
		TargetKind: pluginapi.ModelRouteTargetSelf,
		Target:     pluginIdentifier,
		Reason:     reasonReasonTokenMatch,
	}
	if !matched {
		resp = pluginapi.ModelRouteResponse{Handled: false}
	}
	return okEnvelope(resp)
}

func routeMatches(cfg pluginConfig, req rpcModelRouteRequest) bool {
	if len(cfg.UpstreamProviders) == 0 {
		// route everything — our executor inspects responses and only retries hits.
		return true
	}
	if req.PluginID != "" {
		pluginID := strings.TrimSpace(req.PluginID)
		for _, p := range cfg.UpstreamProviders {
			if strings.EqualFold(pluginID, strings.TrimSpace(p)) {
				return true
			}
		}
	}
	available := req.AvailableProviders
	for _, p := range cfg.UpstreamProviders {
		for _, avail := range available {
			if strings.EqualFold(strings.TrimSpace(p), strings.TrimSpace(avail)) {
				return true
			}
		}
	}
	// If available providers weren't useful, route through anyway — the host
	// dispatcher will run the request through whatever executor returned Handled
	// for this model. Returning true keeps retry behavior even when metadata is thin.
	// When UpstreamProviders is configured and no overlap is found, bail out.
	return false
}

// executeWithRetry handles non-streaming model.execute RPC: reuses host.model.execute
// to push the request upstream, inspects each response body, retries on match up to
// GuardRetryAttempts, and ultimately returns the configured non_stream_status_code
// when retries are exhausted.
func executeWithRetry(raw []byte) ([]byte, error) {
	var req rpcExecutorRequest
	if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
		return nil, errUnmarshal
	}
	cfg := loadedConfig()
	if !cfg.Enabled || !cfg.InterceptNonStreaming {
		// pass-through — just proxy via host.model.execute once and return.
		return executeHostModelPass(req)
	}

	atomic.AddInt64(&stats.TotalRequests, 1)
	body := requestBodyForHost(req)
	retryBudget := cfg.GuardRetryAttempts
	if retryBudget < 0 {
		retryBudget = 5
	}

	var lastBody []byte
	var lastStatus int
	var lastHeaders http.Header

	for attempt := 0; attempt <= retryBudget; attempt++ {
		atomic.AddInt64(&stats.CheckedResponses, 1)
		payload, status, headers, errRun := hostModelExecute(req)
		if errRun != nil {
			// host error is a 5xx transport failure — return 502 with body.
			atomic.AddInt64(&stats.Returned502, 1)
			return errorEnvelopeForStatus(cfg.NonStreamStatusCode, errRun.Error())
		}
		lastBody = payload
		lastStatus = status
		lastHeaders = headers

		// Capacity error → retry always (counts towards budget).
		if matchUpstreamCapacityError(payload, status, cfg) {
			atomic.AddInt64(&stats.RuleMatches, 1)
			atomic.AddInt64(&stats.InternalRetries, 1)
			if cfg.LogMatch {
				pluginLog("capacity_error_retry attempt=%d/%d status=%d", attempt+1, retryBudget, status)
			}
			continue
		}

		// Reasoning tokens match → retry.
		if matched, _, detail := matchReasoningTokens(payload, cfg); matched {
			atomic.AddInt64(&stats.RuleMatches, 1)
			if attempt+1 <= retryBudget {
				atomic.AddInt64(&stats.InternalRetries, 1)
				if cfg.LogMatch {
					pluginLog("reasoning_tokens_match action=internal_retry remaining=%d %s", retryBudget-attempt, detail)
				}
				continue
			}
			// exhausted — return the configured status
			atomic.AddInt64(&stats.ActualInterceptions, 1)
			atomic.AddInt64(&stats.Returned502, 1)
			if cfg.LogMatch {
				pluginLog("reasoning_tokens_match action=return_status_%d %s", cfg.NonStreamStatusCode, detail)
			}
			return buildStatusResponse(cfg.NonStreamStatusCode, detail, lastHeaders)
		}

		// no match → success path
		if attempt > 0 {
			atomic.AddInt64(&stats.SuccessAfterRetry, 1)
		}
		return okEnvelope(pluginapi.ExecutorResponse{
			Payload: payload,
			Headers: headers,
		})
	}

	// Loop exited without return — exhausted on capacity errors.
	atomic.AddInt64(&stats.ActualInterceptions, 1)
	atomic.AddInt64(&stats.Returned502, 1)
	_ = lastStatus
	return buildStatusResponse(cfg.NonStreamStatusCode, "retry_budget_exhausted", lastHeaders)
}

// executeStreamWithRetry handles streaming responses: we buffer the full stream via
// host.model.execute_stream + stream_read, then inspect the assembled body for
// reasoning_tokens; if matched we retry, otherwise we forward chunks via the
// executor stream chunk channel.
//
// This mirrors how codex-retry-gateway handles strict_502 streaming intercept:
// buffer upstream → inspect → retry or forward.
func executeStreamWithRetry(raw []byte) ([]byte, error) {
	var req rpcExecutorRequest
	if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
		return nil, errUnmarshal
	}
	cfg := loadedConfig()
	if !cfg.Enabled || !cfg.InterceptStreaming {
		return executeStreamPassThrough(req)
	}

	atomic.AddInt64(&stats.TotalRequests, 1)
	retryBudget := cfg.GuardRetryAttempts
	if retryBudget < 0 {
		retryBudget = 5
	}

	var lastBody []byte
	var lastHeaders http.Header

	for attempt := 0; attempt <= retryBudget; attempt++ {
		atomic.AddInt64(&stats.CheckedResponses, 1)
		buf, headers, status, errRun := hostModelStreamBuffered(req)
		if errRun != nil {
			atomic.AddInt64(&stats.Returned502, 1)
			return errorEnvelopeForStatus(cfg.NonStreamStatusCode, errRun.Error())
		}
		lastBody = buf
		lastHeaders = headers

		if matchUpstreamCapacityError(buf, status, cfg) {
			atomic.AddInt64(&stats.RuleMatches, 1)
			atomic.AddInt64(&stats.InternalRetries, 1)
			if cfg.LogMatch {
				pluginLog("stream_capacity_error_retry attempt=%d/%d", attempt+1, retryBudget)
			}
			continue
		}

		if matched, _, detail := matchReasoningTokens(buf, cfg); matched {
			atomic.AddInt64(&stats.RuleMatches, 1)
			if attempt+1 <= retryBudget {
				atomic.AddInt64(&stats.InternalRetries, 1)
				if cfg.LogMatch {
					pluginLog("stream_reasoning_tokens_match action=internal_retry remaining=%d %s", retryBudget-attempt, detail)
				}
				continue
			}
			atomic.AddInt64(&stats.ActualInterceptions, 1)
			atomic.AddInt64(&stats.Returned502, 1)
			if cfg.LogMatch {
				pluginLog("stream_reasoning_tokens_match action=return_status_%d %s", cfg.NonStreamStatusCode, detail)
			}
			return buildStatusResponse(cfg.NonStreamStatusCode, detail, lastHeaders)
		}

		if attempt > 0 {
			atomic.AddInt64(&stats.SuccessAfterRetry, 1)
		}
		// Forward the buffered stream as a single chunk via the executor stream response.
		chunks := []pluginapi.ExecutorStreamChunk{
			{Payload: buf},
		}
		return okEnvelope(rpcExecutorStreamResponse{
			Headers: headers,
			Chunks:  chunks,
		})
	}

	atomic.AddInt64(&stats.ActualInterceptions, 1)
	atomic.AddInt64(&stats.Returned502, 1)
	return buildStatusResponse(cfg.NonStreamStatusCode, "stream_retry_budget_exhausted", lastHeaders)
}

// executeHostModelPass runs a single host.model.execute without any interception and
// returns whatever the host returned (success passthrough).
func executeHostModelPass(req rpcExecutorRequest) ([]byte, error) {
	payload, _, headers, errRun := hostModelExecute(req)
	if errRun != nil {
		return errorEnvelopeForStatus(502, errRun.Error())
	}
	return okEnvelope(pluginapi.ExecutorResponse{
		Payload: payload,
		Headers: headers,
	})
}

// executeStreamPassThrough is a single-pass buffered stream forwarder used when
// intercept_streaming is false.
func executeStreamPassThrough(req rpcExecutorRequest) ([]byte, error) {
	buf, headers, _, errRun := hostModelStreamBuffered(req)
	if errRun != nil {
		return errorEnvelopeForStatus(502, errRun.Error())
	}
	chunks := []pluginapi.ExecutorStreamChunk{{Payload: buf}}
	return okEnvelope(rpcExecutorStreamResponse{
		Headers: headers,
		Chunks:  chunks,
	})
}

// ---- Host API bridge ----—

// hostModelExecute calls host.model.execute with the request, preserving entry
// and exit protocols. We always forward using the client's SourceFormat (which is
// what we received from the host onto the model executor).
func hostModelExecute(req rpcExecutorRequest) ([]byte, int, http.Header, error) {
	entryProtocol := req.SourceFormat
	if entryProtocol == "" {
		entryProtocol = req.Format
	}
	if entryProtocol == "" {
		entryProtocol = "chat-completions"
	}
	exitProtocol := req.Format
	if exitProtocol == "" {
		exitProtocol = entryProtocol
	}
	body := requestBodyForHost(req)

	raw, errCall := callHost(pluginabi.MethodHostModelExecute, hostModelExecutionRequest{
		HostModelExecutionRequest: pluginapi.HostModelExecutionRequest{
			EntryProtocol: entryProtocol,
			ExitProtocol:  exitProtocol,
			Model:         req.Model,
			Stream:        false,
			Body:          body,
			Headers:       req.Headers,
			Query:         req.Query,
			Alt:           req.Alt,
		},
		HostCallbackID: req.HostCallbackID,
	})
	if errCall != nil {
		return nil, 0, nil, errCall
	}
	var resp pluginapi.HostModelExecutionResponse
	if errDecode := json.Unmarshal(raw, &resp); errDecode != nil {
		return nil, 0, nil, errDecode
	}
	if resp.StatusCode >= 400 {
		return resp.Body, resp.StatusCode, resp.Headers, fmt.Errorf("host model status %d", resp.StatusCode)
	}
	return resp.Body, resp.StatusCode, resp.Headers, nil
}

// hostModelStreamBuffered opens a streaming execution and reads chunks until done.
// Returns the full assembled body so the caller can inspect reasoning_tokens.
func hostModelStreamBuffered(req rpcExecutorRequest) ([]byte, http.Header, int, error) {
	entryProtocol := req.SourceFormat
	if entryProtocol == "" {
		entryProtocol = req.Format
	}
	if entryProtocol == "" {
		entryProtocol = "chat-completions"
	}
	exitProtocol := req.Format
	if exitProtocol == "" {
		exitProtocol = entryProtocol
	}
	body := requestBodyForHost(req)

	raw, errCall := callHost(pluginabi.MethodHostModelExecuteStream, hostModelExecutionRequest{
		HostModelExecutionRequest: pluginapi.HostModelExecutionRequest{
			EntryProtocol: entryProtocol,
			ExitProtocol:  exitProtocol,
			Model:         req.Model,
			Stream:        true,
			Body:          body,
			Headers:       req.Headers,
			Query:         req.Query,
			Alt:           req.Alt,
		},
		HostCallbackID: req.HostCallbackID,
	})
	if errCall != nil {
		return nil, nil, 0, errCall
	}
	var resp pluginapi.HostModelStreamResponse
	if errDecode := json.Unmarshal(raw, &resp); errDecode != nil {
		return nil, nil, 0, errDecode
	}
	if resp.StatusCode >= 400 {
		_ = closeHostModelStream(resp.StreamID)
		return nil, nil, resp.StatusCode, fmt.Errorf("host model stream status %d", resp.StatusCode)
	}
	if strings.TrimSpace(resp.StreamID) == "" {
		return nil, nil, 0, fmt.Errorf("host model stream: empty stream_id")
	}
	defer func() { _ = closeHostModelStream(resp.StreamID) }()

	var buf bytes.Buffer
	for {
		chunkRaw, errRead := callHost(pluginabi.MethodHostModelStreamRead, pluginapi.HostModelStreamReadRequest{
			StreamID: resp.StreamID,
		})
		if errRead != nil {
			return nil, nil, 0, errRead
		}
		var chunk pluginapi.HostModelStreamReadResponse
		if errDecode := json.Unmarshal(chunkRaw, &chunk); errDecode != nil {
			return nil, nil, 0, errDecode
		}
		if chunk.Error != "" {
			return nil, nil, 0, fmt.Errorf("%s", chunk.Error)
		}
		if len(chunk.Payload) > 0 {
			buf.Write(chunk.Payload)
		}
		if chunk.Done {
			break
		}
	}
	return buf.Bytes(), resp.Headers, resp.StatusCode, nil
}

func closeHostModelStream(streamID string) error {
	_, errCall := callHost(pluginabi.MethodHostModelStreamClose, pluginapi.HostModelStreamCloseRequest{
		StreamID: streamID,
	})
	return errCall
}

// requestBodyForHost returns the body to forward upstream.
// OriginalRequest is the client's raw body; Payload is the translated body. We
// prefer OriginalRequest to give the upstream provider the same body that was
// captured — this matches how codex-retry-gateway operates (pass-through then
// inspect).
func requestBodyForHost(req rpcExecutorRequest) []byte {
	if len(req.OriginalRequest) > 0 {
		return req.OriginalRequest
	}
	return req.Payload
}

// ---- Envelope helpers ----

func okEnvelope(v any) ([]byte, error) {
	raw, errMarshal := json.Marshal(v)
	if errMarshal != nil {
		return nil, errMarshal
	}
	return marshalRPCEnvelope(raw)
}

func errorEnvelope(code, message string) []byte {
	raw, _ := json.Marshal(envelope{OK: false, Error: &envelopeError{Code: code, Message: message}})
	return raw
}

func errorEnvelopeForStatus(status int, message string) ([]byte, error) {
	raw, _ := json.Marshal(envelope{OK: false, Error: &envelopeError{Code: fmt.Sprintf("http_%d", status), Message: message}})
	return raw, nil
}

// buildStatusResponse builds an ExecutorResponse that carries a synthetic status
// code in Headers (the host will set the final outbound HTTP status from a header
// the framework recognizes). We use the X-CLIProxy-Status header convention.
func buildStatusResponse(statusCode int, detail string, headers http.Header) ([]byte, error) {
	if headers == nil {
		headers = http.Header{}
	} else {
		// defensive copy
		headers = headers.Clone()
	}
	headers.Set("Content-Type", "application/json")
	headers.Set("X-CLIProxy-Retry-Intercepted", "1")
	body := fmt.Sprintf(`{"error":{"message":"retry-gateway: %s","type":"retry_intercepted","code":"retry_exhausted"}}`, strings.ReplaceAll(detail, `"`, `\"`))
	return okEnvelope(pluginapi.ExecutorResponse{
		Payload: []byte(body),
		Headers: headers,
		Metadata: map[string]any{
			"retry_intercepted": true,
			"intercept_detail":  detail,
			"final_status":      statusCode,
		},
	})
}

// rpcExecutorStreamResponse is what the host expects for an executor.execute_stream RPC.
type rpcExecutorStreamResponse struct {
	Headers http.Header                     `json:"headers,omitempty"`
	Chunks  []pluginapi.ExecutorStreamChunk `json:"chunks,omitempty"`
}

// marshalRPCEnvelope wraps a JSON result in the standard plugin envelope.
func marshalRPCEnvelope(result json.RawMessage) ([]byte, error) {
	return json.Marshal(envelope{OK: true, Result: result})
}

// pluginLog writes a line to the plugin host log via host.log.
// Best-effort: drops on error.
func pluginLog(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	_, _ = callHost(pluginabi.MethodHostLog, map[string]string{
		"level":   "info",
		"message": "[" + pluginIdentifier + "] " + msg,
	})
}

// Avoid unused import for io when we wire stream forwarding-only variations.
var _ = io.EOF
var _ context.Context
