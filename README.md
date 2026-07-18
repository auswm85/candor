# token-tracker

Local-first LLM cost monitoring daemon. Polls provider usage APIs (OpenAI, Anthropic, OpenRouter), applies cache-aware cost rules, projects monthly spend, and surfaces results via a terminal UI (bubbletea) + embedded web dashboard.

**Your API keys never leave your machine.** Keys are stored in your OS keychain (macOS Keychain, Windows Credential Manager, Linux libsecret). No cloud dependency, no telemetry, no account required.

> **Status: early development.** Anthropic polling (API + Claude Code) works end-to-end, with the cost engine, alerting, and CLI. The OpenAI and OpenRouter adapters and the web dashboard are still in progress — see the [provider table](#provider-support) and [`docs/plan.md`](docs/plan.md).

## Features

- **Cache-aware cost model** — separate tracking for cache-read, cache-write, and base input tokens. The July 2026 OpenAI cache-write pricing change is fully accounted for.
- **Multi-provider** — Anthropic today (API + Claude Code); OpenAI and OpenRouter adapters in progress. Each provider has a custom adapter for its billing API.
- **Local-first** — single Go binary, SQLite database, OS keychain for secrets. No cloud.
- **Projected monthly spend** — extrapolates current burn rate against your budget, notifies once per threshold crossed each month.
- **OS notifications** — macOS, Linux, and Windows native alerts when projected spend crosses a budget threshold.
- **Terminal UI** — bubbletea dashboard showing today/month spend against budget. _(Web dashboard planned.)_

## Quick Start

```sh
# Install both binaries: the TUI daemon and the short-form CLI
go install github.com/auswm85/token-tracker/cmd/token-tracker@latest
go install github.com/auswm85/token-tracker/cmd/tt@latest

# Set up API keys (stored in your OS keychain)
tt auth                    # prompts for OpenAI, Anthropic, OpenRouter keys

# Launch the live TUI dashboard (foreground: polls + UI)
token-tracker

# ...or run the poller headless in the background
tt daemon

# One-shot commands
tt spend today             # today's spend
tt spend month --by-model  # this month, broken down by model
tt status                  # last poll, DB size, month-to-date spend
tt service                 # print a launchd/systemd unit for `tt daemon`
tt migrate                 # apply database migrations
```

Budget alerts are configured in `config.yaml` (`defaults.monthly_budget_usd` and
`defaults.alert_thresholds`); the daemon fires an OS notification the first time
projected spend crosses each threshold in a month.

## Configuration

Config lives at `~/.config/token-tracker/config.yaml`. See `configs/config.example.yaml` for the full schema.

Key settings:

- `poll_interval`: how often to poll provider APIs (default: `5m`)
- `defaults.monthly_budget_usd`: your monthly budget for projections
- `defaults.alert_thresholds`: % thresholds for notifications (default: `[50, 75, 90, 100]`)
- `web.listen`: web dashboard address (default: `127.0.0.1:7878`)

> **Note:** pricing currently uses built-in defaults (`internal/cost.DefaultPrices`).
> The `prices:` section in `config.example.yaml` documents the intended override
> schema but is not yet loaded — config-driven pricing is a planned change.

API keys are stored in your OS keychain via `go-keyring` — never in the config file.

## Architecture

```
┌─────────────────────────────────────────────────────────────┐
│  token-tracker daemon                                        │
│                                                              │
│  ┌──────────────┐   ┌──────────────┐   ┌──────────────────┐ │
│  │  poll loop   │──>│ cost engine  │──>│  SQLite store    │ │
│  │  (every 5m)  │   │ (apply rules)│   │                  │ │
│  └──────────────┘   └──────────────┘   └───────┬──────────┘ │
│        │                                       │            │
│        │                                  ┌────┴────┐       │
│        │                                  │         │       │
│        ▼                                  ▼         ▼       │
│  ┌──────────────┐                  ┌──────────┐ ┌──────────┐│
│  │  alert       │                  │   TUI    │ │   Web    ││
│  │  checker     │                  │ (bubble) │ │ (HTTP)   ││
│  └──────────────┘                  └──────────┘ └──────────┘│
└─────────────────────────────────────────────────────────────┘
```

See `docs/plan.md` for the full implementation plan.

## Provider Support

| Provider                  | Endpoint                                                                                         | Status                                                                                        |
| ------------------------- | ------------------------------------------------------------------------------------------------ | --------------------------------------------------------------------------------------------- |
| Anthropic                 | `GET /v1/organizations/usage_report/messages` + `GET /v1/organizations/usage_report/claude_code` | **Working** — requires Admin API key, covers API + Claude Code costs, includes cache tracking |
| OpenRouter                | `GET /api/v1/activity`                                                                           | **Working** — requires a provisioning key (openrouter.ai/settings/provisioning-keys); per-day, per-model cost + tokens |
| OpenAI                    | `GET /v1/organization/costs`                                                                     | Planned — adapter stubbed (`not yet implemented`)                                             |
| Ollama / vLLM / LM Studio | N/A (no billing API)                                                                             | v1.1 deferred — proxy mode TBD                                                                |

## Development

```sh
go build ./cmd/token-tracker   # build the daemon
go build ./cmd/tt              # build the CLI
go test -race -count=1 ./...   # run all tests
go vet ./...                   # static analysis

# Web dashboard (requires Node 22+)
cd web && npm ci && npm run build
```

See `CLAUDE.md` for full commands and architecture notes.

## License

MIT
