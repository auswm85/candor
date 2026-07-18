# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with this repository.

## What token-tracker is

A local-first daemon that polls LLM provider usage APIs (OpenAI, Anthropic, OpenRouter), applies cache-aware cost rules, projects monthly spend, and surfaces results via a bubbletea TUI + embedded web dashboard. API keys stay on your machine (OS keychain). No cloud dependency.

## Commands

```sh
go build ./cmd/token-tracker   # build the daemon
go build ./cmd/tt              # build the short-form CLI
go run ./cmd/token-tracker     # launch daemon with TUI
go run ./cmd/tt                # one-shot commands (spend, alerts, auth)

go test -race -count=1 ./...   # run all tests with race detection
go vet ./...                   # static analysis
golangci-lint run              # full linter suite
go mod tidy                    # clean dependencies

# Database migrations
go run ./cmd/tt migrate        # run pending migrations
```

## Architecture

Single Go binary, single process. Three goroutines:

- **Poll loop** — ticks every 5m, fetches incremental usage from each provider, writes raw rows to SQLite.
- **Cost engine** — pure function: (provider, model, tokens, tier) → USD cost. Reads pricing from YAML config.
- **Alert checker** — runs after each poll, compares projected spend against thresholds, sends OS notification.
- **TUI** — bubbletea views subscribed to store updates via Go channel.
- **Web** — net/http server on 127.0.0.1:7878 serving embedded Svelte build + JSON API.

Polling strategy (Strategy A): only official provider billing/usage APIs. No proxy mode in v1. Each provider has a custom adapter implementing `Provider` interface.

## Key packages

```
internal/
  provider/       Provider interface + per-provider adapters
    openai/       OpenAI /v1/organization/costs adapter
    anthropic/    Anthropic /v1/organizations/usage_messages adapter
    openrouter/   OpenRouter /api/v1/usage adapter
  cost/           Cost engine: pricing rules, projection calculation
  store/          SQLite via sqlc + golang-migrate
  poll/           Scheduling loop, state tracking
  alert/          Threshold checking, OS notifications
  tui/            bubbletea views (dashboard, history, alerts)
  web/            HTTP server + embedded static Svelte build
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

- `modernc.org/sqlite` is pure Go (no CGO). Install `golang-migrate/migrate` for schema migrations.
- The web dashboard uses SvelteKit's static adapter; run `npm run build` in `web/` before embedding into the Go binary.
- API keys are stored in OS keychain via `zalando/go-keyring`. Fallback to env var `OPENAI_API_KEY`, `ANTHROPIC_API_KEY`, `OPENROUTER_API_KEY` with a logged warning.
