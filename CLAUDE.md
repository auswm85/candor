# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with this repository.

## What token-tracker is

A local-first daemon that tracks LLM spend, applies cache-aware cost rules, projects monthly spend, and surfaces results via a bubbletea TUI. Local-only; no cloud dependency.

Two ingestion paths:

- **Proxy mode (primary).** A transparent local reverse proxy that coding harnesses (Claude Code, OpenCode, …) point their base URL at. It forwards each request to the real provider untouched and taps token usage from the response in real time — live, per-request, per-model. No admin/management keys needed (the harness's normal inference key is forwarded). This is the path built for the actual use case; see the proxy section below.
- **Polling mode (secondary).** Ticks every N minutes and pulls each provider's official usage/billing API. Accurate for monthly totals but coarse and delayed, and each provider gates it behind a privileged key (Anthropic Admin, OpenAI Admin, OpenRouter provisioning). Kept for periodic monitoring; see `docs/plan.md` for why it's no longer primary.

## Commands

```sh
go build ./cmd/token-tracker   # build the daemon (TUI + poll loop + proxy)
go build ./cmd/tt              # build the short-form CLI

go run ./cmd/token-tracker     # launch daemon: TUI + proxy (proxy.enabled defaults true)

# tt subcommands
go run ./cmd/tt proxy          # run the live-usage proxy standalone
go run ./cmd/tt daemon         # headless poll loop (no TUI)
go run ./cmd/tt spend today    # today's spend  (also: spend month --by-model)
go run ./cmd/tt status         # last poll, DB size, month spend
go run ./cmd/tt auth [prov]    # store a provider key in the OS keychain (polling only)
go run ./cmd/tt service        # print a launchd/systemd unit for the daemon
go run ./cmd/tt migrate        # run pending migrations

go test -race -count=1 ./...   # run all tests with race detection
go vet ./...                   # static analysis
golangci-lint run              # full linter suite (config: .golangci.yml; needs v2 built with go1.26)
go mod tidy                    # clean dependencies
```

## Architecture

Single Go binary, single process. The `token-tracker` daemon runs the TUI, and (guarded by config) the proxy and poll loop concurrently. A `daemon.lock` (flock) enforces one instance.

- **Proxy** — `internal/proxy`: transparent reverse proxy. First path segment selects the upstream (`/openai/…`, `/anthropic/…`, `/openrouter/…`); a per-provider extractor taps usage from the response (streaming + non-streaming). The recorder prices it and writes additively into a per-minute bucket via `store.AddUsage`.
- **Poll loop** — `internal/poll`: ticks every N minutes, fetches incremental usage from each configured provider, costs it, and upserts (authoritative) via `store.InsertUsage`. Also records `last_poll` and runs the alert checker.
- **Cost engine** — `internal/cost`: pure function (provider, model, tokens by tier) → USD. Uses `DefaultPrices()` with model-name normalization (dated snapshots → base pricing). Provider-supplied cost (OpenRouter) is used directly when present.
- **Alert checker** — `internal/alert`: after each poll, projects monthly spend and fires an OS notification the first time each budget threshold is crossed per month (dedup via `config_state`).
- **TUI** — `internal/tui`: bubbletea; tabbed Live / History / Alerts, refreshing from the store on a tick.

Web dashboard (`internal/web`) is **planned, not built** — the directory is currently empty.

Each poll provider is a custom adapter implementing the `Provider` interface. Note the earlier "Strategy A / no proxy" framing in older docs is superseded — proxy mode is now primary (see `docs/plan.md`).

## Key packages

```
internal/
  proxy/          Transparent reverse proxy + per-provider usage extractors + recorder
  provider/       Provider interface + per-provider polling adapters
    openai/       OpenAI  GET /v1/organization/costs (Admin key)
    anthropic/    Anthropic usage_report/messages + /claude_code (Admin key)
    openrouter/   OpenRouter GET /api/v1/activity (provisioning key)
  cost/           Cost engine: DefaultPrices, model normalization, projection
  store/          SQLite (modernc.org/sqlite); embedded SQL migration runner (no sqlc/golang-migrate)
  poll/           Scheduling loop; costs + persists; records last_poll; runs alerts
  alert/          Threshold checking, OS notifications (macOS/Linux/Windows)
  app/            Shared wiring: build providers, scheduler, proxy from config
  lock/           Single-instance daemon lock (flock; no-op on non-unix)
  tui/            bubbletea tabbed views (Live / History / Alerts)
  web/            (planned — empty)
  config/         viper config loader + go-keyring integration
```

## Provider adapter interface

```go
type Provider interface {
    Name() string
    PollUsage(ctx context.Context, since time.Time) ([]UsageRecord, error)
}
```

Each adapter maps the provider's native API response shape into `UsageRecord`.

## Testing

```sh
go test -race -count=1 ./...          # full test suite
go test -run TestCostEngine -v ./...  # cost engine (table-driven)
```

Test mocks: `testdata/openai_costs_*.json` for HTTP fixtures. SQLite tests use `:memory:` databases.

`GOTOOLCHAIN=auto` is default — Go auto-resolves to the version in `go.mod`. No manual SDK management needed.

## Environment

Go 1.26+, Node 22+ (for web dashboard build). macOS/Linux/Windows.

## Gotchas

- `modernc.org/sqlite` is pure Go (no CGO). Migrations are embedded SQL run by `store.Migrate()` — no external migration tool. `store.Open` creates the DB parent dir.
- **Proxy vs polling keys are different.** Proxy mode forwards whatever key the client (harness) sends and stores nothing. Polling mode stores privileged keys in the OS keychain via `tt auth` (`zalando/go-keyring`), falling back to env vars.
- The daemon redirects its log to `<db-dir>/daemon.log` while the TUI owns the terminal — check there, not stdout, for poll/proxy errors.
- The proxy runs by default (`proxy.enabled` defaults true) on `127.0.0.1:7879`; harnesses point their base URL at `http://127.0.0.1:7879/<provider>/…`.
- Web dashboard is not built yet (`internal/web` is empty); don't reference it as if it exists.
