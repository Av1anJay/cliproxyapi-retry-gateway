package main

/*
#include <stdint.h>
#include <stdlib.h>

typedef struct {
	void* ptr;
	size_t len;
} cliproxy_buffer;

typedef int (*cliproxy_host_call_fn)(void*, const char*, const uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_host_free_fn)(void*, size_t);

typedef struct {
	uint32_t abi_version;
	void* host_ctx;
	cliproxy_host_call_fn call;
	cliproxy_host_free_fn free_buffer;
} cliproxy_host_api;

typedef int (*cliproxy_plugin_call_fn)(char*, uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_plugin_free_fn)(void*, size_t);
typedef void (*cliproxy_plugin_shutdown_fn)(void);

typedef struct {
	uint32_t abi_version;
	cliproxy_plugin_call_fn call;
	cliproxy_plugin_free_fn free_buffer;
	cliproxy_plugin_shutdown_fn shutdown;
} cliproxy_plugin_api;

extern int cliproxyPluginCall(char*, uint8_t*, size_t, cliproxy_buffer*);
extern void cliproxyPluginFree(void*, size_t);
extern void cliproxyPluginShutdown(void);

static const cliproxy_host_api* stored_host;

static void store_host_api(const cliproxy_host_api* host) {
	stored_host = host;
}

static int call_host_api(const char* method, const uint8_t* request, size_t request_len, cliproxy_buffer* response) {
	if (stored_host == NULL || stored_host->call == NULL) {
		return 1;
	}
	return stored_host->call(stored_host->host_ctx, method, request, request_len, response);
}

static void free_host_buffer(void* ptr, size_t len) {
	if (stored_host != NULL && stored_host->free_buffer != NULL && ptr != NULL) {
		stored_host->free_buffer(ptr, len);
	}
}
*/
import "C"

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"unsafe"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"gopkg.in/yaml.v3"
)

const pluginIdentifier = "codex-retry-gateway"

var (
	currentConfig atomic.Value
	statsMu       sync.Mutex
	stats         = &runtimeStats{}
)

// runtimeStats tracks interception statistics since plugin load (process lifetime).
type runtimeStats struct {
	TotalRequests       int64 `json:"total_requests"`
	CheckedResponses    int64 `json:"checked_responses"`
	RuleMatches         int64 `json:"rule_matches"`
	ActualInterceptions int64 `json:"actual_interceptions"`
	InternalRetries     int64 `json:"internal_retries"`
	SuccessAfterRetry   int64 `json:"success_after_retry"`
	Returned502         int64 `json:"returned_502"`
}

// flexBool/flexInt/intList/stringList accept the loose YAML shapes produced by
// CPA's generic management UI. Empty strings/nulls are treated as "not set" so
// they do not overwrite plugin defaults during hot reload.
type flexBool bool

type flexInt int

type intList []int

type stringList []string

func (b *flexBool) UnmarshalYAML(node *yaml.Node) error {
	if b == nil || yamlNodeEmpty(node) {
		return nil
	}
	switch node.Kind {
	case yaml.ScalarNode:
		raw := strings.TrimSpace(node.Value)
		if raw == "" {
			return nil
		}
		value, err := strconv.ParseBool(raw)
		if err != nil {
			return fmt.Errorf("invalid boolean %q", raw)
		}
		*b = flexBool(value)
		return nil
	default:
		return fmt.Errorf("expected boolean, got YAML kind %d", node.Kind)
	}
}

func (i *flexInt) UnmarshalYAML(node *yaml.Node) error {
	if i == nil || yamlNodeEmpty(node) {
		return nil
	}
	switch node.Kind {
	case yaml.ScalarNode:
		raw := strings.TrimSpace(node.Value)
		if raw == "" {
			return nil
		}
		value, err := strconv.Atoi(raw)
		if err != nil {
			return fmt.Errorf("invalid integer %q", raw)
		}
		*i = flexInt(value)
		return nil
	default:
		return fmt.Errorf("expected integer, got YAML kind %d", node.Kind)
	}
}

func (l *intList) UnmarshalYAML(node *yaml.Node) error {
	if l == nil || yamlNodeEmpty(node) {
		return nil
	}
	var out []int
	appendOne := func(raw string) error {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return nil
		}
		value, err := strconv.Atoi(raw)
		if err != nil {
			return fmt.Errorf("invalid integer %q", raw)
		}
		out = append(out, value)
		return nil
	}

	switch node.Kind {
	case yaml.SequenceNode:
		for _, item := range node.Content {
			if item == nil {
				continue
			}
			if err := appendOne(item.Value); err != nil {
				return err
			}
		}
	case yaml.ScalarNode:
		for _, part := range strings.Split(node.Value, ",") {
			if err := appendOne(part); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("expected integer list, got YAML kind %d", node.Kind)
	}
	if len(out) > 0 {
		*l = out
	}
	return nil
}

func (l *stringList) UnmarshalYAML(node *yaml.Node) error {
	if l == nil || yamlNodeEmpty(node) {
		return nil
	}
	var out []string
	appendOne := func(raw string) {
		raw = strings.TrimSpace(raw)
		if raw != "" {
			out = append(out, raw)
		}
	}
	switch node.Kind {
	case yaml.SequenceNode:
		for _, item := range node.Content {
			if item != nil {
				appendOne(item.Value)
			}
		}
	case yaml.ScalarNode:
		for _, part := range strings.Split(node.Value, ",") {
			appendOne(part)
		}
	default:
		return fmt.Errorf("expected string list, got YAML kind %d", node.Kind)
	}
	if len(out) > 0 {
		*l = out
	}
	return nil
}

func yamlNodeEmpty(node *yaml.Node) bool {
	if node == nil || node.Kind == 0 {
		return true
	}
	if node.Tag == "!!null" {
		return true
	}
	return node.Kind == yaml.ScalarNode && strings.TrimSpace(node.Value) == ""
}

// pluginConfig is the per-request configuration loaded from plugins.configs.codex-retry-gateway.
type pluginConfig struct {
	Enabled               flexBool   `yaml:"enabled"`
	ReasoningEquals       intList    `yaml:"reasoning_equals"`
	ReasoningMatchMode    string     `yaml:"reasoning_match_mode"`
	InterceptStreaming    flexBool   `yaml:"intercept_streaming"`
	InterceptNonStreaming flexBool   `yaml:"intercept_non_streaming"`
	GuardRetryAttempts    flexInt    `yaml:"guard_retry_attempts"`
	NonStreamStatusCode   flexInt    `yaml:"non_stream_status_code"`
	RetryCapacityErrors   flexBool   `yaml:"retry_upstream_capacity_errors"`
	LogMatch              flexBool   `yaml:"log_match"`
	UpstreamProviders     stringList `yaml:"upstream_providers"`
}

type envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *envelopeError  `json:"error,omitempty"`
}

type envelopeError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type lifecycleRequest struct {
	ConfigYAML []byte `json:"config_yaml"`
}

type registration struct {
	SchemaVersion uint32                 `json:"schema_version"`
	Metadata      pluginapi.Metadata     `json:"metadata"`
	Capabilities  registrationCapability `json:"capabilities"`
}

type registrationCapability struct {
	ModelRouter           bool     `json:"model_router"`
	Executor              bool     `json:"executor"`
	ExecutorModelScope    string   `json:"executor_model_scope"`
	ExecutorInputFormats  []string `json:"executor_input_formats"`
	ExecutorOutputFormats []string `json:"executor_output_formats"`
	ManagementAPI         bool     `json:"management_api"`
}

type rpcExecutorRequest struct {
	pluginapi.ExecutorRequest
	StreamID       string `json:"stream_id,omitempty"`
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type rpcModelRouteRequest struct {
	pluginapi.ModelRouteRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type hostModelExecutionRequest struct {
	pluginapi.HostModelExecutionRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

func main() {}

//export cliproxy_plugin_init
func cliproxy_plugin_init(host *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	if plugin == nil {
		return 1
	}
	C.store_host_api(host)
	plugin.abi_version = C.uint32_t(pluginabi.ABIVersion)
	plugin.call = C.cliproxy_plugin_call_fn(C.cliproxyPluginCall)
	plugin.free_buffer = C.cliproxy_plugin_free_fn(C.cliproxyPluginFree)
	plugin.shutdown = C.cliproxy_plugin_shutdown_fn(C.cliproxyPluginShutdown)
	return 0
}

//export cliproxyPluginCall
func cliproxyPluginCall(method *C.char, request *C.uint8_t, requestLen C.size_t, response *C.cliproxy_buffer) C.int {
	if response != nil {
		response.ptr = nil
		response.len = 0
	}
	if method == nil {
		writeResponse(response, errorEnvelope("invalid_method", "method is required"))
		return 1
	}
	var requestBytes []byte
	if request != nil && requestLen > 0 {
		requestBytes = C.GoBytes(unsafe.Pointer(request), C.int(requestLen))
	}
	raw, errHandle := handleMethod(C.GoString(method), requestBytes)
	if errHandle != nil {
		writeResponse(response, errorEnvelope("plugin_error", errHandle.Error()))
		return 1
	}
	writeResponse(response, raw)
	return 0
}

//export cliproxyPluginFree
func cliproxyPluginFree(ptr unsafe.Pointer, _ C.size_t) {
	if ptr != nil {
		C.free(ptr)
	}
}

//export cliproxyPluginShutdown
func cliproxyPluginShutdown() {}

// callHost marshals payload and invokes the in-process host ABI.
func callHost(method string, payload any) (json.RawMessage, error) {
	rawPayload, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		return nil, fmt.Errorf("marshal host callback %s: %w", method, errMarshal)
	}
	cMethod := C.CString(method)
	defer C.free(unsafe.Pointer(cMethod))

	var response C.cliproxy_buffer
	var requestPtr *C.uint8_t
	if len(rawPayload) > 0 {
		cPayload := C.CBytes(rawPayload)
		if cPayload == nil {
			return nil, fmt.Errorf("allocate host callback %s", method)
		}
		defer C.free(cPayload)
		requestPtr = (*C.uint8_t)(cPayload)
	}
	callCode := C.call_host_api(cMethod, requestPtr, C.size_t(len(rawPayload)), &response)
	var rawResponse []byte
	if response.ptr != nil && response.len > 0 {
		rawResponse = C.GoBytes(response.ptr, C.int(response.len))
	}
	if response.ptr != nil {
		C.free_host_buffer(response.ptr, response.len)
	}
	if len(rawResponse) == 0 {
		return nil, fmt.Errorf("host callback %s returned no response, code=%d", method, int(callCode))
	}

	var env envelope
	if errUnmarshal := json.Unmarshal(rawResponse, &env); errUnmarshal != nil {
		return nil, fmt.Errorf("decode host envelope %s: %w", method, errUnmarshal)
	}
	if !env.OK {
		if env.Error != nil {
			return nil, fmt.Errorf("%s: %s", env.Error.Code, env.Error.Message)
		}
		return nil, fmt.Errorf("host callback %s failed", method)
	}
	if callCode != 0 {
		return nil, fmt.Errorf("host callback %s returned code=%d", method, int(callCode))
	}
	return append(json.RawMessage(nil), env.Result...), nil
}

// writeResponse copies a byte slice into the C-side response buffer.
func writeResponse(response *C.cliproxy_buffer, raw []byte) {
	if response == nil || len(raw) == 0 {
		return
	}
	ptr := C.CBytes(raw)
	if ptr == nil {
		return
	}
	response.ptr = ptr
	response.len = C.size_t(len(raw))
}

func handleMethod(method string, request []byte) ([]byte, error) {
	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		if errConfigure := configure(request); errConfigure != nil {
			return nil, errConfigure
		}
		return okEnvelope(pluginRegistration())
	case pluginabi.MethodModelRoute:
		return routeModel(request)
	case pluginabi.MethodExecutorIdentifier:
		return okEnvelope(map[string]string{"identifier": pluginIdentifier})
	case pluginabi.MethodExecutorExecute:
		return executeWithRetry(request)
	case pluginabi.MethodExecutorExecuteStream:
		return executeStreamWithRetry(request)
	case pluginabi.MethodExecutorCountTokens:
		return okEnvelope(pluginapi.ExecutorResponse{Payload: []byte(`{"input_tokens":0}`)})
	case pluginabi.MethodManagementRegister:
		return okEnvelope(manifestRegistration())
	case pluginabi.MethodManagementHandle:
		return handleManagementRoute(request)
	default:
		return errorEnvelope("unknown_method", "unknown method: "+method), nil
	}
}

// ---- Configuration ----

func configure(raw []byte) error {
	var req lifecycleRequest
	if len(raw) > 0 {
		if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
			return errUnmarshal
		}
	}
	cfg := defaultPluginConfig()
	if len(req.ConfigYAML) > 0 {
		decoded, errDecode := decodeConfig(req.ConfigYAML)
		if errDecode != nil {
			return errDecode
		}
		cfg = decoded
	}
	currentConfig.Store(cfg)
	return nil
}

func defaultPluginConfig() pluginConfig {
	return pluginConfig{
		Enabled:               flexBool(true),
		ReasoningEquals:       intList{516, 1034, 1552, 2070, 2588, 3106},
		ReasoningMatchMode:    "formula_518n_minus_2",
		InterceptStreaming:    flexBool(true),
		InterceptNonStreaming: flexBool(true),
		GuardRetryAttempts:    flexInt(5),
		NonStreamStatusCode:   flexInt(502),
		RetryCapacityErrors:   flexBool(true),
		LogMatch:              flexBool(true),
		UpstreamProviders:     stringList{"codex"},
	}
}

func decodeConfig(raw []byte) (pluginConfig, error) {
	cfg := defaultPluginConfig()
	if errUnmarshal := yaml.Unmarshal(raw, &cfg); errUnmarshal != nil {
		return pluginConfig{}, errUnmarshal
	}
	cfg = repairBlankManagementDefaults(cfg)
	cfg.ReasoningMatchMode = strings.TrimSpace(cfg.ReasoningMatchMode)
	if cfg.ReasoningMatchMode == "" {
		cfg.ReasoningMatchMode = "formula_518n_minus_2"
	}
	if cfg.GuardRetryAttempts < 0 {
		cfg.GuardRetryAttempts = flexInt(5)
	}
	if cfg.NonStreamStatusCode == 0 {
		cfg.NonStreamStatusCode = flexInt(502)
	}
	cfg.UpstreamProviders = normalizeProviderList(cfg.UpstreamProviders)
	if len(cfg.UpstreamProviders) == 0 {
		cfg.UpstreamProviders = defaultPluginConfig().UpstreamProviders
	}
	return cfg, nil
}

func repairBlankManagementDefaults(cfg pluginConfig) pluginConfig {
	// CPA's generic config UI has no default-value schema. On first save, it can
	// submit an empty form as false/0/empty values for every field. That should not
	// disable the plugin's retry behavior. Treat this exact zero-state as "unset".
	if cfg.ReasoningMatchMode == "" &&
		len(cfg.ReasoningEquals) > 0 &&
		!bool(cfg.InterceptStreaming) &&
		!bool(cfg.InterceptNonStreaming) &&
		cfg.GuardRetryAttempts == 0 &&
		cfg.NonStreamStatusCode == 0 &&
		!bool(cfg.RetryCapacityErrors) &&
		!bool(cfg.LogMatch) {
		defaults := defaultPluginConfig()
		cfg.InterceptStreaming = defaults.InterceptStreaming
		cfg.InterceptNonStreaming = defaults.InterceptNonStreaming
		cfg.GuardRetryAttempts = defaults.GuardRetryAttempts
		cfg.NonStreamStatusCode = defaults.NonStreamStatusCode
		cfg.RetryCapacityErrors = defaults.RetryCapacityErrors
		cfg.LogMatch = defaults.LogMatch
	}
	return cfg
}

func normalizeProviderList(in stringList) stringList {
	out := make(stringList, 0, len(in))
	seen := map[string]struct{}{}
	for _, item := range in {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		key := strings.ToLower(item)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, item)
	}
	return out
}

func loadedConfig() pluginConfig {
	if v, ok := currentConfig.Load().(pluginConfig); ok {
		return v
	}
	return defaultPluginConfig()
}

func pluginRegistration() registration {
	cfg := loadedConfig()
	configFields := []pluginapi.ConfigField{
		{Name: "enabled", Type: pluginapi.ConfigFieldTypeBoolean, Description: "Enable the retry gateway interception logic."},
		{Name: "reasoning_equals", Type: pluginapi.ConfigFieldTypeArray, Description: "Manual list of reasoning_tokens values to match. Used when reasoning_match_mode=manual."},
		{Name: "reasoning_match_mode", Type: pluginapi.ConfigFieldTypeEnum, EnumValues: []string{"formula_518n_minus_2", "manual"}, Description: "Match mode: formula (518*n-2) or manual list."},
		{Name: "intercept_streaming", Type: pluginapi.ConfigFieldTypeBoolean, Description: "Intercept matching streaming responses."},
		{Name: "intercept_non_streaming", Type: pluginapi.ConfigFieldTypeBoolean, Description: "Intercept matching non-streaming responses."},
		{Name: "guard_retry_attempts", Type: pluginapi.ConfigFieldTypeInteger, Description: "Max internal retry attempts before returning non_stream_status_code."},
		{Name: "non_stream_status_code", Type: pluginapi.ConfigFieldTypeInteger, Description: "HTTP status code returned to client when retries exhausted (default 502)."},
		{Name: "retry_upstream_capacity_errors", Type: pluginapi.ConfigFieldTypeBoolean, Description: "Retry on upstream 'Selected model is at capacity' errors."},
		{Name: "log_match", Type: pluginapi.ConfigFieldTypeBoolean, Description: "Log match details to plugin host log."},
		{Name: "upstream_providers", Type: pluginapi.ConfigFieldTypeArray, Description: "Provider/model prefixes routed through the plugin. Default is [\"codex\"]; use [\"*\"] only to route all requests."},
	}
	_ = cfg
	return registration{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata: pluginapi.Metadata{
			Name:             "codex-retry-gateway",
			Version:          "0.1.0",
			Author:           "Av1anJay",
			GitHubRepository: "https://github.com/AvianJay/cliproxyapi-retry-gateway",
			ConfigFields:     configFields,
		},
		Capabilities: registrationCapability{
			ModelRouter:           true,
			Executor:              true,
			ExecutorModelScope:    string(pluginapi.ExecutorModelScopeBoth),
			ExecutorInputFormats:  []string{"chat-completions", "responses"},
			ExecutorOutputFormats: []string{"chat-completions", "responses"},
			ManagementAPI:         true,
		},
	}
}
