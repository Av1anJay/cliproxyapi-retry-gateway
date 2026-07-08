package main

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"gopkg.in/yaml.v3"
)

func TestDecodeConfigAcceptsQuotedReasoningEquals(t *testing.T) {
	raw := []byte(`reasoning_equals:
  - "516"
  - "1034"
  - "1552"
  - "2070"
`)
	cfg, err := decodeConfig(raw)
	if err != nil {
		t.Fatalf("decodeConfig returned error: %v", err)
	}
	want := intList{516, 1034, 1552, 2070}
	if !reflect.DeepEqual(cfg.ReasoningEquals, want) {
		t.Fatalf("ReasoningEquals = %#v, want %#v", cfg.ReasoningEquals, want)
	}
}

func TestDecodeConfigAcceptsQuotedScalarsFromManagementUI(t *testing.T) {
	raw := []byte(`enabled: "true"
reasoning_match_mode: manual
guard_retry_attempts: "3"
non_stream_status_code: "503"
intercept_streaming: "true"
intercept_non_streaming: "false"
retry_upstream_capacity_errors: "true"
log_match: "false"
upstream_providers: "codex, claude"
`)
	cfg, err := decodeConfig(raw)
	if err != nil {
		t.Fatalf("decodeConfig returned error: %v", err)
	}
	if !bool(cfg.Enabled) || cfg.ReasoningMatchMode != matchModeManual || int(cfg.GuardRetryAttempts) != 3 || int(cfg.NonStreamStatusCode) != 503 {
		t.Fatalf("decoded scalar config = %#v", cfg)
	}
	if !bool(cfg.InterceptStreaming) || bool(cfg.InterceptNonStreaming) || !bool(cfg.RetryCapacityErrors) || bool(cfg.LogMatch) {
		t.Fatalf("decoded bool config = %#v", cfg)
	}
	if !reflect.DeepEqual(cfg.UpstreamProviders, stringList{"codex", "claude"}) {
		t.Fatalf("UpstreamProviders = %#v", cfg.UpstreamProviders)
	}
}

func TestDecodeConfigBlankManagementFormPreservesRetryDefaults(t *testing.T) {
	raw := []byte(`enabled: true
priority: 0
reasoning_match_mode: ""
reasoning_equals: []
intercept_streaming: false
intercept_non_streaming: false
guard_retry_attempts: 0
non_stream_status_code: 0
retry_upstream_capacity_errors: false
log_match: false
upstream_providers: []
`)
	cfg, err := decodeConfig(raw)
	if err != nil {
		t.Fatalf("decodeConfig returned error: %v", err)
	}
	defaults := defaultPluginConfig()
	if !bool(cfg.InterceptStreaming) || !bool(cfg.InterceptNonStreaming) || int(cfg.GuardRetryAttempts) != int(defaults.GuardRetryAttempts) || int(cfg.NonStreamStatusCode) != int(defaults.NonStreamStatusCode) || !bool(cfg.RetryCapacityErrors) || !bool(cfg.LogMatch) {
		t.Fatalf("blank management form did not preserve defaults: %#v", cfg)
	}
	if !reflect.DeepEqual(cfg.UpstreamProviders, defaults.UpstreamProviders) {
		t.Fatalf("UpstreamProviders = %#v, want %#v", cfg.UpstreamProviders, defaults.UpstreamProviders)
	}
}

func TestDecodeConfigPartialYAMLKeepsDefaults(t *testing.T) {
	cfg, err := decodeConfig([]byte("enabled: true\npriority: 0\n"))
	if err != nil {
		t.Fatalf("decodeConfig returned error: %v", err)
	}
	defaults := defaultPluginConfig()
	if !reflect.DeepEqual(cfg.ReasoningEquals, defaults.ReasoningEquals) || cfg.ReasoningMatchMode != defaults.ReasoningMatchMode || !bool(cfg.InterceptStreaming) || !bool(cfg.InterceptNonStreaming) || int(cfg.GuardRetryAttempts) != 5 || int(cfg.NonStreamStatusCode) != 502 || !bool(cfg.RetryCapacityErrors) || !bool(cfg.LogMatch) || !reflect.DeepEqual(cfg.UpstreamProviders, defaults.UpstreamProviders) {
		t.Fatalf("partial config = %#v, want defaults overlay", cfg)
	}
}

func TestIntListAcceptsCommaSeparatedScalar(t *testing.T) {
	var got intList
	if err := yaml.Unmarshal([]byte(`"516, 1034, 1552"`), &got); err != nil {
		t.Fatalf("yaml.Unmarshal returned error: %v", err)
	}
	want := intList{516, 1034, 1552}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestIntListRejectsNonNumericStrings(t *testing.T) {
	var got intList
	if err := yaml.Unmarshal([]byte(`["516", "oops"]`), &got); err == nil {
		t.Fatal("expected an error for non-numeric strings")
	}
}

func TestDefaultRouteIsCodexOnly(t *testing.T) {
	cfg := defaultPluginConfig()
	if !routeMatches(cfg, rpcModelRouteRequest{ModelRouteRequest: pluginapi.ModelRouteRequest{RequestedModel: "codex/gpt-5"}}) {
		t.Fatal("default route should match codex-prefixed model")
	}
	if routeMatches(cfg, rpcModelRouteRequest{ModelRouteRequest: pluginapi.ModelRouteRequest{RequestedModel: "gpt-5"}}) {
		t.Fatal("default route should not hijack generic model")
	}
	if routeMatches(cfg, rpcModelRouteRequest{ModelRouteRequest: pluginapi.ModelRouteRequest{RequestedModel: "claude-sonnet-4"}}) {
		t.Fatal("default route should not hijack claude model")
	}
}

func TestRouteWildcardAllowsExplicitAllRequests(t *testing.T) {
	cfg := defaultPluginConfig()
	cfg.UpstreamProviders = stringList{"*"}
	if !routeMatches(cfg, rpcModelRouteRequest{ModelRouteRequest: pluginapi.ModelRouteRequest{RequestedModel: "gpt-5"}}) {
		t.Fatal("wildcard route should match generic model")
	}
}

func TestRouteFallsBackToRequestBodyModel(t *testing.T) {
	cfg := defaultPluginConfig()
	req := rpcModelRouteRequest{ModelRouteRequest: pluginapi.ModelRouteRequest{Body: []byte(`{"model":"codex-max"}`)}}
	if !routeMatches(cfg, req) {
		t.Fatal("route should match model parsed from request body")
	}
}

func decodeRouteEnvelope(t *testing.T, raw []byte) pluginapi.ModelRouteResponse {
	t.Helper()
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if !env.OK {
		t.Fatalf("envelope not ok: %#v", env.Error)
	}
	var resp pluginapi.ModelRouteResponse
	if err := json.Unmarshal(env.Result, &resp); err != nil {
		t.Fatalf("decode route result: %v", err)
	}
	return resp
}
