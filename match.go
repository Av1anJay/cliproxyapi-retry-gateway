package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
)

// matchMode values
const (
	matchModeFormula = "formula_518n_minus_2"
	matchModeManual  = "manual"
)

// capacityErrorMarker is the substring in upstream error messages that triggers a capacity retry.
const capacityErrorMarker = "Selected model is at capacity. Please try a different model."

// reasonReasonTokenMatch is the rule name emitted in logs when reasoning_tokens matches.
const reasonReasonTokenMatch = "reasoning_tokens"

// matchReasoningTokens decides whether the response body's usage.reasoning_tokens
// triggers a retry per the configured rule.
//
// Returns (matched, rule, reasonDetail).
func matchReasoningTokens(body []byte, cfg pluginConfig) (bool, string, string) {
	if len(body) == 0 {
		return false, "", ""
	}
	// usage.reasoning_tokens — works for OpenAI Responses and Codex chat completions bodies.
	rt := gjson.GetBytes(body, "usage.reasoning_tokens")
	if !rt.Exists() {
		return false, "", ""
	}
	if rt.Type != gjson.Number {
		// null / missing / non-numeric → not a match for the manual/formula path.
		return false, "", ""
	}
	value := int(rt.Int())
	if value <= 0 {
		return false, "", ""
	}

	mode := strings.TrimSpace(cfg.ReasoningMatchMode)
	switch mode {
	case matchModeManual:
		for _, v := range cfg.ReasoningEquals {
			if v == value {
				return true, reasonReasonTokenMatch, fmt.Sprintf("reasoning_tokens=%d (manual list hit)", value)
			}
		}
		return false, "", ""
	case matchModeFormula, "":
		// formula_518n_minus_2: value >= 516 && (value + 2) % 518 === 0
		if value >= 516 && (value+2)%518 == 0 {
			return true, reasonReasonTokenMatch, fmt.Sprintf("reasoning_tokens=%d (518*n-2 formula hit)", value)
		}
		return false, "", ""
	default:
		return false, "", ""
	}
}

// matchUpstreamCapacityError checks if the response body contains an upstream
// "model at capacity" error that we should retry through.
func matchUpstreamCapacityError(body []byte, statusCode int, cfg pluginConfig) bool {
	if !bool(cfg.RetryCapacityErrors) {
		return false
	}
	if statusCode < 400 && statusCode != 0 {
		// capacity errors come as 4xx/5xx; skip 2xx.
		// statusCode==0 means "unknown" (stream chunk), so still probe body.
	}
	if len(body) == 0 {
		return false
	}
	// JSON error envelope: {"error":{"message":"Selected model is at capacity..."}}
	msg := gjson.GetBytes(body, "error.message")
	if !msg.Exists() {
		// some providers render the marker at top level
		msg = gjson.GetBytes(body, "message")
	}
	if !msg.Exists() {
		return false
	}
	return strings.Contains(msg.String(), capacityErrorMarker)
}

// parseCountTokensResponse extracts an input_tokens count from a model.execute
// response body for the count_tokens RPC. Not critical — used for the executor
// capability contract; we return 0 since we don't proxy token counting.
func parseCountTokensResponse(body []byte) int {
	if len(body) == 0 {
		return 0
	}
	v := gjson.GetBytes(body, "input_tokens")
	if !v.Exists() {
		return 0
	}
	return int(v.Int())
}

// jsonifyStats returns the current stats as a JSON byte slice for the management API.
func jsonifyStats() []byte {
	statsMu.Lock()
	defer statsMu.Unlock()
	out, _ := json.Marshal(stats)
	return out
}
