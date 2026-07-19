# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with this repository.

## What candor is

A local-first tool that records live LLM spend by sitting in front of a coding harness as a transparent proxy, applies cache-aware cost rules, projects monthly spend, fires budget alerts, and surfaces everything in a bubbletea TUI. Local-only; no cloud, no telemetry, no account, no admin keys.

**Single ingestion path — the proxy.** A transparent local reverse proxy that coding harnesses (Claude Code, OpenCode, …) point their base URL at (directly, or per-run via `candor run`). It forwards each request to the real provider untouched and taps token usage + rate-limit headers from the response in real time — live, per-request, per-model. The harness's normal inference key is forwarded; nothing is stored. (An earlier polling mode that pulled provider billing APIs was removed — it needed privileged admin keys and was delayed/coarse; git history has it if ever needed.)

## Commands

Single binary, `candor`. Bare `candor` opens the dashboard (and hosts the proxy); subcommands cover the rest.

```sh
go build ./cmd/candor          # build the binary

go run ./cmd/candor            # open the dashboard (TUI + proxy; proxy.enabled defaults true)

# subcommands
go run ./cmd/candor run -- claude   # run a harness routed through the proxy (per-process, nothing persistent; falls back to direct if the proxy is down)
go run ./cmd/candor proxy           # run the proxy headless (for a background service); also fires budget alerts
go run ./cmd/candor tui             # dashboard as a read-only viewer, attached to a running proxy via /stats
go run ./cmd/candor spend today     # today's spend  (also: spend month --by-model)
go run ./cmd/candor export --since 2026-01-01 --format csv|json   # export raw usage rows to stdout
go run ./cmd/candor status          # db size, today/month spend, whether the proxy is running
go run ./cmd/candor service         # print a launchd/systemd unit that runs `candor proxy`
go run ./cmd/candor migrate         # run pending migrations

go test -race -count=1 ./...   # run all tests with race detection
go vet ./...                   # static analysis
golangci-lint run              # full linter suite (config: .golangci.yml; needs v2 built with go1.26)
go mod tidy                    # clean dependencies
```

## Architecture

Single Go binary, single process. Bare `candor` runs the TUI and (guarded by config) hosts the proxy, plus a timer-based budget-alert loop. A `daemon.lock` (flock) enforces one dashboard instance. If a proxy is already running (e.g. a background `candor proxy` service), the dashboard attaches to it as a viewer over `/stats` instead of binding a second one.

- **Proxy** — `internal/proxy`: transparent reverse proxy. First path segment selects the upstream (`/openai/…`, `/anthropic/…`, `/openrouter/…`); a per-provider extractor taps usage from the response (streaming + non-streaming), and rate-limit response headers are parsed into current-window state. The recorder prices each request and writes additively into a per-minute bucket via `store.AddUsage`. Serves `/healthz` (liveness) and `/stats` (live feed/burn/limits JSON for a detached viewer). Fail-open: tapping runs after the client's bytes are forwarded and is panic-isolated, so it can never break a request. Anthropic request bodies are forwarded byte-for-byte (prompt-cache/first-party fidelity).
- **Cost engine** — `internal/cost`: pure function (provider, model, tokens by tier) → USD. Uses dynamic prices with model-name normalization (dated snapshots → base pricing). Provider-supplied cost (OpenRouter) is used directly when present.
- **Alert loop** — `internal/alert` + `app.StartAlertLoop`: a ticker projects monthly spend and fires an OS notification the first time each budget threshold is crossed per month (dedup via `config_state`), logging each firing to `alert_events` for the Alerts-tab history.
- **TUI** — `internal/tui`: full-screen bubbletea; sidebar + tabbed Live / History / Alerts, refreshing from the store (and the proxy's `/stats`) on a tick.

The TUI is the only UI.

## Key packages

```
internal/
  proxy/    Transparent reverse proxy + usage extractors + rate-limit header capture + recorder; serves /healthz and /stats
  cost/     Cost engine: DefaultPrices, model normalization, projection inputs
  store/    SQLite (modernc.org/sqlite); embedded SQL migration runner (no sqlc/golang-migrate)
  alert/    Threshold checking + OS notifications (macOS/Linux/Windows), driven by app.StartAlertLoop
  app/      Shared wiring: proxy, recorder, engine, child-env for `run`, health probe, projection + alert loop
  lock/     Single-instance dashboard lock (flock; no-op on non-unix)
  tui/      Full-screen bubbletea dashboard (sidebar + Live / History / Alerts)
  pricing/  Dynamic model pricing (OpenRouter catalog, cached, bundled fallback)
  config/   viper config loader
```

## Testing

```sh
go test -race -count=1 ./...          # full test suite
go test -run TestCostEngine -v ./...  # cost engine (table-driven)
```

SQLite tests use temp-file/`:memory:` databases. Proxy tests drive the handler end-to-end via `httptest`.

`GOTOOLCHAIN=auto` is default — Go auto-resolves to the version in `go.mod`.

## Environment

Go 1.26+. macOS/Linux/Windows.

## Gotchas

- `modernc.org/sqlite` is pure Go (no CGO). Migrations are embedded SQL run by `store.Migrate()` — no external migration tool. `store.Open` creates the DB parent dir. The DSN uses `_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)` — mattn-style `_journal_mode=WAL` is silently ignored by modernc.
- **The proxy stores nothing sensitive.** It forwards whatever key the client (harness) sends; no keychain, no admin keys.
- The dashboard redirects its log to `<db-dir>/daemon.log` while the TUI owns the terminal — check there, not stdout, for proxy errors.
- The proxy runs by default (`proxy.enabled` defaults true) on `127.0.0.1:7879`; harnesses point their base URL at `http://127.0.0.1:7879/<provider>/…`.
- Model pricing is dynamic (`internal/pricing`, OpenRouter catalog, cached to `<db-dir>/prices.json`), falling back to `cost.DefaultPrices()` offline.
