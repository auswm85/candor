# token-tracker

Local-first LLM cost monitoring daemon. Polls provider usage APIs (OpenAI, Anthropic, OpenRouter), applies cache-aware cost rules, projects monthly spend, and surfaces results via a terminal UI (bubbletea) + embedded web dashboard.

**Your API keys never leave your machine.** Keys are stored in your OS keychain (macOS Keychain, Windows Credential Manager, Linux libsecret). No cloud dependency, no telemetry, no account required.

## Features

- **Cache-aware cost model** — separate tracking for cache-read, cache-write, and base input tokens. The July 2026 OpenAI cache-write pricing change is fully accounted for.
- **Multi-provider** — OpenAI, Anthropic, and OpenRouter in v1. Each provider has a custom adapter for its billing API.
- **Local-first** — single Go binary, SQLite database, OS keychain for secrets. No cloud.
- **Projected monthly spend** — extrapolates current burn rate against your budget, alerts at configurable thresholds.
- **TUI + Web** — bubbletea terminal dashboard (live alongside your editor) + embedded Svelte web dashboard on `127.0.0.1:7878`.
- **OS notifications** — macOS, Linux, and Windows native alerts when crossing budget thresholds.

## Quick Start

```sh
# Install
go install github.com/auswm85/token-tracker/cmd/tt@latest

# Set up API keys
tt auth                    # prompts for OpenAI, Anthropic, OpenRouter keys

# Configure pricing (optional — edit ~/.config/token-tracker/config.yaml)
# See configs/config.example.yaml for the full schema

# Launch the daemon with TUI
tt

# One-shot commands
tt spend today
tt spend month --by-model
tt alert add --monthly 75
```

## Configuration

Config lives at `~/.config/token-tracker/config.yaml`. See `configs/config.example.yaml` for the full schema.

Key settings:

- `poll_interval`: how often to poll provider APIs (default: `5m`)
- `defaults.monthly_budget_usd`: your monthly budget for projections
- `defaults.alert_thresholds`: % thresholds for notifications (default: `[50, 75, 90, 100]`)
- `web.listen`: web dashboard address (default: `127.0.0.1:7878`)

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

| Provider                  | Endpoint                                                                            | Status                                                                      |
| ------------------------- | ----------------------------------------------------------------------------------- | --------------------------------------------------------------------------- |
| OpenAI                    | `GET /v1/organization/costs`                                                        | v1 — includes cache-read/write breakdown                                    |
| Anthropic                 | `GET /v1/organizations/usage_report/messages` + `GET /v1/organizations/usage_report/claude_code` | v1 — requires Admin API key, covers API + Claude Code costs, includes cache tracking |
| OpenRouter                | `GET /api/v1/usage`                                                                 | v1 — includes per-model cost breakdown                                      |
| Ollama / vLLM / LM Studio | N/A (no billing API)                                                                | v1.1 deferred — proxy mode TBD                                              |

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
