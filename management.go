package main

import (
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const (
	managementStatusPath = "/plugins/codex-retry-gateway/status"
	managementConfigPath = "/plugins/codex-retry-gateway/config"
	resourcePanelPath    = "/panel"
)

// manifestRegistration declares management routes and a browser panel.
//
// API routes are exposed by CPA under /v0/management/<path>.
// Resource routes are exposed under /v0/resource/plugins/codex-retry-gateway/<path>.
func manifestRegistration() pluginapi.ManagementRegistrationResponse {
	return pluginapi.ManagementRegistrationResponse{
		Routes: []pluginapi.ManagementRoute{
			{
				Method:      http.MethodGet,
				Path:        managementStatusPath,
				Description: "Runtime interception stats since plugin load.",
			},
			{
				Method:      http.MethodGet,
				Path:        managementConfigPath,
				Description: "Current plugin configuration snapshot.",
			},
		},
		Resources: []pluginapi.ResourceRoute{
			{
				Path:        resourcePanelPath,
				Menu:        "Codex Retry Gateway",
				Description: "Runtime dashboard for codex-retry-gateway.",
			},
		},
	}
}

// handleManagementRoute answers Management API and resource-panel requests from the host.
func handleManagementRoute(raw []byte) ([]byte, error) {
	var req pluginapi.ManagementRequest
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &req)
	}

	switch routeSuffix(req.Path) {
	case managementStatusPath:
		return okJSON(jsonifyStats())
	case managementConfigPath:
		cfg := loadedConfig()
		body, _ := json.MarshalIndent(cfg, "", "  ")
		return okJSON(body)
	case resourcePanelPath:
		return okHTML(renderPanelHTML())
	default:
		body, _ := json.Marshal(map[string]string{
			"error": "not_found",
			"path":  req.Path,
		})
		return okEnvelope(pluginapi.ManagementResponse{
			StatusCode: http.StatusNotFound,
			Headers:    http.Header{"Content-Type": []string{"application/json"}},
			Body:       body,
		})
	}
}

func routeSuffix(path string) string {
	path = strings.TrimRight(strings.TrimSpace(path), "/")
	for _, suffix := range []string{managementStatusPath, managementConfigPath, resourcePanelPath} {
		if path == suffix || strings.HasSuffix(path, suffix) {
			return suffix
		}
	}
	return path
}

func okJSON(body []byte) ([]byte, error) {
	return okEnvelope(pluginapi.ManagementResponse{
		StatusCode: http.StatusOK,
		Headers: http.Header{
			"Content-Type": []string{"application/json; charset=utf-8"},
		},
		Body: body,
	})
}

func okHTML(html string) ([]byte, error) {
	return okEnvelope(pluginapi.ManagementResponse{
		StatusCode: http.StatusOK,
		Headers: http.Header{
			"Content-Type":  []string{"text/html; charset=utf-8"},
			"Cache-Control": []string{"no-store"},
		},
		Body: []byte(html),
	})
}

func renderPanelHTML() string {
	statsJSON := jsonifyStats()
	cfg, _ := json.MarshalIndent(loadedConfig(), "", "  ")
	var statsMap map[string]any
	_ = json.Unmarshal(statsJSON, &statsMap)

	cards := []panelCard{
		{"Total Requests", fmtStat(statsMap["total_requests"])},
		{"Checked Responses", fmtStat(statsMap["checked_responses"])},
		{"Rule Matches", fmtStat(statsMap["rule_matches"])},
		{"Actual Intercepts", fmtStat(statsMap["actual_interceptions"])},
		{"Internal Retries", fmtStat(statsMap["internal_retries"])},
		{"Success After Retry", fmtStat(statsMap["success_after_retry"])},
		{"Returned 502", fmtStat(statsMap["returned_502"])},
	}

	data := panelData{
		Title:     "Codex Retry Gateway",
		Stats:     cards,
		StatsJSON: template.JS(statsJSON),
		Config:    string(cfg),
	}
	var b strings.Builder
	_ = panelTemplate.Execute(&b, data)
	return b.String()
}

func fmtStat(v any) string {
	switch n := v.(type) {
	case float64:
		return fmt.Sprintf("%.0f", n)
	case int64:
		return fmt.Sprintf("%d", n)
	case int:
		return fmt.Sprintf("%d", n)
	case string:
		return n
	default:
		return "0"
	}
}

type panelCard struct {
	Label string
	Value string
}

type panelData struct {
	Title     string
	Stats     []panelCard
	StatsJSON template.JS
	Config    string
}

var panelTemplate = template.Must(template.New("panel").Parse(`<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>{{ .Title }}</title>
  <style>
    :root { color-scheme: dark; --bg:#0b1020; --card:#121a2f; --muted:#93a4bf; --text:#e5edf8; --accent:#68e1fd; --bad:#ff6b7a; --ok:#8df7a7; }
    * { box-sizing: border-box; }
    body { margin:0; min-height:100vh; font-family: ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif; background: radial-gradient(circle at top left, #1a2b5c, var(--bg) 45%); color:var(--text); }
    main { width:min(1120px, calc(100% - 32px)); margin:32px auto; }
    header { display:flex; justify-content:space-between; gap:16px; align-items:flex-start; margin-bottom:24px; }
    h1 { margin:0; font-size:32px; letter-spacing:-0.04em; }
    p { color:var(--muted); margin:8px 0 0; }
    .badge { border:1px solid rgba(104,225,253,.45); color:var(--accent); border-radius:999px; padding:8px 12px; font-size:13px; background:rgba(104,225,253,.08); }
    .grid { display:grid; grid-template-columns: repeat(auto-fit, minmax(180px, 1fr)); gap:14px; }
    .card { background:linear-gradient(180deg, rgba(255,255,255,.06), rgba(255,255,255,.025)); border:1px solid rgba(255,255,255,.08); border-radius:18px; padding:18px; box-shadow:0 20px 45px rgba(0,0,0,.25); }
    .label { color:var(--muted); font-size:13px; }
    .value { margin-top:8px; font-size:30px; font-weight:800; letter-spacing:-0.04em; }
    .wide { margin-top:16px; display:grid; grid-template-columns: minmax(0, 1fr) minmax(0, 1fr); gap:16px; }
    pre { overflow:auto; white-space:pre-wrap; line-height:1.45; background:#080d19; border:1px solid rgba(255,255,255,.08); border-radius:14px; padding:16px; color:#d7e2f2; min-height:220px; }
    .hint { font-size:13px; color:var(--muted); margin-top:18px; }
    @media (max-width: 840px) { .wide { grid-template-columns: 1fr; } header { flex-direction:column; } }
  </style>
</head>
<body>
  <main>
    <header>
      <div>
        <h1>{{ .Title }}</h1>
        <p>Runtime dashboard for reasoning_tokens retry interception.</p>
      </div>
      <div class="badge">refresh page to update</div>
    </header>

    <section class="grid">
      {{ range .Stats }}
      <div class="card"><div class="label">{{ .Label }}</div><div class="value">{{ .Value }}</div></div>
      {{ end }}
    </section>

    <section class="wide">
      <div class="card">
        <div class="label">Raw Stats</div>
        <pre id="stats">{{ .StatsJSON }}</pre>
      </div>
      <div class="card">
        <div class="label">Current Config</div>
        <pre>{{ .Config }}</pre>
      </div>
    </section>

    <div class="hint">API endpoints: <code>/v0/management/plugins/codex-retry-gateway/status</code>, <code>/v0/management/plugins/codex-retry-gateway/config</code>. Panel resource: <code>/v0/resource/plugins/codex-retry-gateway/panel</code>.</div>
  </main>
</body>
</html>`))
