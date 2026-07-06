# codex-retry-gateway — CLIProxyAPI plugin

A [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) plugin that ports the
[nonononull/codex-retry-gateway](https://github.com/nonononull/codex-retry-gateway)
reasoning-token interception gateway into the CLIProxyAPI plugin model.

## What it does

When an upstream model response has `usage.reasoning_tokens` matching the
`518*n - 2` formula (516, 1034, 1552, 2070, 2588, 3106, …), the plugin silently
retries the request through the built-in host executor up to
`guard_retry_attempts` times before finally returning the configured
`non_stream_status_code` (default `502`) to the client.

This captures the **reasoning-degradation interception** behaviour from the
Node.js version while running as an in-process CLIProxyAPI plugin — no extra
port to run, no `config.toml` rewriting, no separate process.

## Match rules

| Mode | Behaviour |
| --- | --- |
| `formula_518n_minus_2` (default) | Matches every `reasoning_tokens >= 516 && (value + 2) % 518 == 0` |
| `manual` | Matches only the explicit values listed in `reasoning_equals` |

Both streaming and non-streaming paths are intercepted: the streaming path
buffers the full upstream response (mirrors the `strict_502` strategy from the
Node gateway), inspects it, and either retries or forwards the buffered chunk.

Capacity errors (`Selected model is at capacity. Please try a different model.`)
are retried independently of the reasoning rule and consume the same retry budget.

## Configuration

Add the built artifact to `plugins.path` and configure under
`plugins.configs.codex-retry-gateway`:

```yaml
plugins:
  path:
    - /absolute/path/to/bin/codex-retry-gateway-go.so
  configs:
    codex-retry-gateway:
      enabled: true
      reasoning_match_mode: formula_518n_minus_2   # or "manual" to use reasoning_equals
      reasoning_equals: [516, 1034, 1552]         # used only in "manual" mode
      intercept_streaming: true
      intercept_non_streaming: true
      guard_retry_attempts: 5                      # 0 = intercept immediately, no retries
      non_stream_status_code: 502
      retry_upstream_capacity_errors: true
      log_match: true
      upstream_providers: []                       # empty = intercept everything;
                                                   # set ["codex"] to only proxy codex ops
                                                   # through the gateway
```

### Config field reference

| Field | Type | Default | Description |
| --- | --- | --- | --- |
| `enabled` | bool | `true` | Master switch |
| `reasoning_equals` | array | `[516, 1034, 1552, 2070, 2588, 3106]` | Manual list used when `reasoning_match_mode=manual` |
| `reasoning_match_mode` | enum | `formula_518n_minus_2` | `formula_518n_minus_2` or `manual` |
| `intercept_streaming` | bool | `true` | Inspect & retry streaming responses |
| `intercept_non_streaming` | bool | `true` | Inspect & retry non-streaming responses |
| `guard_retry_attempts` | int | `5` | Max internal retries before returning `non_stream_status_code` |
| `non_stream_status_code` | int | `502` | Final HTTP status returned to client when retries exhausted |
| `retry_upstream_capacity_errors` | bool | `true` | Retry when upstream reports "model at capacity" |
| `log_match` | bool | `true` | Emit host log lines on every match |
| `upstream_providers` | array | `[]` | Optional provider-key filter (`["codex"]`, `["claude"]`, …). Empty intercepts everything. |

## Install

Download the latest artifact from GitHub Releases:

| Platform | Artifact |
| --- | --- |
| Linux x86_64 | `codex-retry-gateway_<version>_linux_amd64.tar.gz` |
| Linux arm64 | `codex-retry-gateway_<version>_linux_arm64.tar.gz` |
| Windows x86_64 | `codex-retry-gateway_<version>_windows_amd64.zip` |

Example:

```bash
curl -L -o codex-retry-gateway.tar.gz \
  https://github.com/Av1anJay/cliproxyapi-retry-gateway/releases/download/v0.1.1/codex-retry-gateway_0.1.1_linux_amd64.tar.gz
```

Or build locally.

## Build

Requires Go 1.26+ (the CLIProxyAPI module floor). Go 1.21+ can fetch the
required toolchain automatically when `GOTOOLCHAIN=auto` is enabled.

```bash
make linux      # → bin/codex-retry-gateway-go.so
```

Release artifacts are generated with GoReleaser:

```bash
goreleaser release --snapshot --clean
```

## Management API

Two routes are exposed under the plugin management namespace:

| Route | Returns |
| --- | --- |
| `GET /__plugins/codex-retry-gateway/status` | Runtime stats since plugin load (rule matches, retries, 502s) |
| `GET /__plugins/codex-retry-gateway/config`  | Current effective plugin config |

## Architecture

```
client ──► CLIProxyAPI host ──► ModelRouter ──► executor.execute[_stream]
                                       │                │
                                       │ returns         │ host.model.execute[_stream]
                                       │ Handled=self    ▼
                                       │            ┌────────────┐
                                       │            │  retry     │ inspect usage.reasoning_tokens
                                       │            │  loop      │   ├─ match → retry (budget--)
                                       │            │            │   └─ no match → forward chunk
                                       │            └────────────┘
                                       │
                                       └─ Handled=false → built-in executor (transparent)
```

The plugin uses **ModelRouter** + **Executor** capabilities:
1. `model.route` returns `Handled=true, TargetKind=self` for every request (filter
   configurable via `upstream_providers`).
2. `executor.execute[_stream]` forwards the request body to
   `host.model.execute[_stream]` (the built-in chain), inspects the response, and
   retries on match until `guard_retry_attempts` is exhausted, at which point it
   returns a synthetic `non_stream_status_code` response.

This keeps us transparent for every response that doesn't match — non-matching
requests pay one extra round-trip through the plugin RPC but otherwise flow
through the standard auth/executor stack.

## Limitations vs the Node.js original

- **No management UI.** Stats are exposed via the Management API only.
- **No history import / analytics dashboard.** Just the runtime counters.
- **No continuation recovery.** When a streaming response matches, we buffer →
  retry, we do not attempt a Responses continuation write-back. The original
  gateway's `continuation_recovery` strategy is not implemented here.

## License

MIT
