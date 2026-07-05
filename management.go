package main

import (
	"encoding/json"
	"net/http"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// manifestRegistration declares a management route that exposes runtime stats
// and a minimal config snapshot at:
//   GET /__plugins/codex-retry-gateway/status
//   GET /__plugins/codex-retry-gateway/config
func manifestRegistration() pluginapi.ManagementRegistration {
	return pluginapi.ManagementRegistration{
		Routes: []pluginapi.ManagementRoute{
			{
				Method:      http.MethodGet,
				Path:        "/status",
				Description: "Runtime interception stats since plugin load.",
			},
			{
				Method:      http.MethodGet,
				Path:        "/config",
				Description: "Current plugin configuration snapshot.",
			},
		},
	}
}

// handleManagementRoute answers a management route request from the host.
func handleManagementRoute(raw []byte) ([]byte, error) {
	var req pluginapi.ManagementRequest
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &req)
	}

	switch req.Path {
	case "/status":
		return okEnvelope(pluginapi.ManagementResponse{
			StatusCode: http.StatusOK,
			Headers:    http.Header{"Content-Type": []string{"application/json"}},
			Body:       jsonifyStats(),
		})
	case "/config":
		cfg := loadedConfig()
		body, _ := json.Marshal(cfg)
		return okEnvelope(pluginapi.ManagementResponse{
			StatusCode: http.StatusOK,
			Headers:    http.Header{"Content-Type": []string{"application/json"}},
			Body:       body,
		})
	default:
		return okEnvelope(pluginapi.ManagementResponse{
			StatusCode: http.StatusNotFound,
			Headers:    http.Header{"Content-Type": []string{"application/json"}},
			Body:       []byte(`{"error":"not_found","path":"` + req.Path + `"}`),
		})
	}
}
