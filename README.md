# token-tracker

Local-first LLM cost tracker. A transparent local **proxy** captures live, per-request spend from coding harnesses (Claude Code, OpenCode, …), and an optional **poll loop** pulls provider usage APIs for periodic totals. Applies cache-aware cost rules, projects monthly spend, and surfaces it in a terminal UI (bubbletea).

**Local-only.** No cloud dependency, no telemetry, no account. In proxy mode your inference key is forwarded to the provider, never stored; polling keys live in your OS keychain (macOS Keychain, Windows Credential Manager, Linux libsecret).

## Features

- **Live per-request tracking (proxy)** — point a coding harness's base URL at the local proxy; usage is recorded as each response streams back, no admin keys needed. See [Live tracking](#live-tracking-proxy-mode).
- **Cache-aware cost model** — separate tracking for cache-read, cache-write, and base input tokens (Anthropic's `cache_read`/`cache_creation`, OpenAI's `cached_tokens`).
- **Multi-provider** — proxy handles OpenAI-compatible (OpenAI, OpenRouter) and Anthropic protocols; polling adapters cover all three. OpenRouter cost comes straight from the provider; others are priced by the engine.
- **Local-first** — single Go binary, SQLite database. No cloud.
- **Projected monthly spend + alerts** — extrapolates burn rate against your budget, fires an OS notification (macOS/Linux/Windows) once per threshold crossed each month.
- **Rate-limit window tracking** — the proxy reads providers' rate-limit response headers, so the dashboard shows your **Claude Code plan windows** (the 5-hour and weekly caps subscription users actually watch, from `anthropic-ratelimit-unified-*`) and OpenAI/OpenRouter per-minute request/token limits — utilization bars with reset countdowns, captured live from real traffic (no extra probe calls).
- **Terminal UI** — full-screen bubbletea dashboard with a persistent sidebar (at-a-glance spend, this-session burn rate, proxy status) and tabbed panels: **Live** (24h trend sparkline, live activity feed, top models, cache impact, rate-limit windows), 30-day **History** chart, and **Alerts**.

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

## Live tracking (proxy mode)

For real-time, per-request cost — e.g. watching a coding harness like **Claude
Code** or **OpenCode** as it works — run the transparent proxy and point the
tool's base URL at it. Your normal inference key is forwarded untouched (no admin
key needed), and usage is recorded as each response streams back.

Two ways to run it:

```sh
# All-in-one: proxy + live dashboard in one terminal
token-tracker

# ...or run the proxy as an always-on background service and open the dashboard
# on demand from any shell (it attaches to the running proxy over /stats):
tt proxy          # or install the launchd/systemd unit: tt service
tt tui            # read-only viewer — live feed + burn rate included
```

`token-tracker` auto-detects an already-running proxy and attaches as a viewer
instead of binding a second one, so either workflow just works.

**Recommended: `tt run` (nothing persistent).** Wrap your harness and its LLM
traffic is routed through the proxy for that run only — no global config, nothing
to undo, and your plain `claude` still goes straight to the provider:

```sh
tt run -- claude              # Claude Code, usage tracked
tt run -- opencode            # any harness that reads provider base-URL env vars
tt run --provider anthropic -- claude   # scope to one provider
```

If the proxy isn't running, `tt run` launches your harness **directly** (straight
to the provider) and just skips tracking — it never breaks your workflow.

**Or set the base URL yourself** (persists until you unset it), using the
provider name as the first path segment:

```sh
# Claude Code (Anthropic protocol — captures cache-read/cache-creation tokens):
ANTHROPIC_BASE_URL=http://127.0.0.1:7879/anthropic claude

# OpenAI-compatible tools (OpenCode w/ OpenAI, etc.):
#   base URL → http://127.0.0.1:7879/openai/v1
#   OpenRouter → http://127.0.0.1:7879/openrouter/api/v1
```

Notes:

- Requires a harness that supports a custom base URL (Claude Code, OpenCode, Aider, Cline, …). Tools that hardcode their endpoint can't be proxied.
- The proxy is **fail-open**: usage tapping runs after your bytes are forwarded and can never break or stall a request — a parsing bug costs a metric, not your response.
- On an API key, the engine prices the captured tokens — **actual cost**. On a subscription (Pro/Max OAuth) login there's no per-token billing, so the same figure is an **API-equivalent estimate**: what that usage would cost at list price. Token and cache counts are accurate either way, and dated model IDs (e.g. `claude-sonnet-4-5-20250929`) resolve to current pricing automatically.

## Configuration

Config lives at `~/.config/token-tracker/config.yaml`. See `configs/config.example.yaml` for the full schema.

Key settings:

- `poll_interval`: how often to poll provider APIs (default: `5m`)
- `defaults.monthly_budget_usd`: your monthly budget for projections
- `defaults.alert_thresholds`: % thresholds for notifications (default: `[50, 75, 90, 100]`)
- `proxy.*`: live-tracking proxy (see above)
- `pricing.source`: dynamic price catalog URL (default OpenRouter's public models API; `""` disables)

> **Pricing is dynamic.** Model prices are fetched from OpenRouter's public model
> catalog (no auth) on daemon start, cached to `<db-dir>/prices.json`, and
> refreshed daily — falling back to a bundled table offline. No manual price
> tracking or recompiles. (OpenRouter-proxied traffic doesn't need it — cost comes
> straight from the response.)

Polling API keys are stored in your OS keychain via `go-keyring` — never in the config file. Proxy mode forwards your inference key and stores nothing.

## Architecture

```
  harness (Claude Code / OpenCode)                provider usage APIs
        │ base_url                                       ▲ every N min
        ▼                                                │
┌─────────────────────────────────────────────────────────────┐
│  token-tracker daemon                                        │
│                                                              │
│  ┌──────────────┐        ┌──────────────┐   ┌─────────────┐  │
│  │    proxy     │──┐  ┌──│  poll loop   │──>│ cost engine │  │
│  │ (per request)│  │  │  │ (poll+alert) │   └──────┬──────┘  │
│  └──────────────┘  ▼  ▼  └──────────────┘          ▼         │
│                 ┌──────────────┐          ┌─────────────────┐│
│                 │   recorder   │─────────>│  SQLite store   ││
│                 └──────────────┘          └────────┬────────┘│
│                                                    ▼         │
│                                              ┌──────────┐    │
│                                              │   TUI    │    │
│                                              │ (bubble) │    │
│                                              └──────────┘    │
└─────────────────────────────────────────────────────────────┘
```

Proxy forwards each request to the real provider and taps usage from the
response; the poll loop pulls official usage APIs on an interval. Both cost
their records and write to SQLite, which the TUI reads.

See `docs/plan.md` for the full implementation plan.

## Provider Support

| Provider                  | Endpoint                                                                                         | Status                                                                                                                                                             |
| ------------------------- | ------------------------------------------------------------------------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| Anthropic                 | `GET /v1/organizations/usage_report/messages` + `GET /v1/organizations/usage_report/claude_code` | **Working** — requires Admin API key, covers API + Claude Code costs, includes cache tracking                                                                      |
| OpenRouter                | `GET /api/v1/activity`                                                                           | **Working** — requires a provisioning key (openrouter.ai/settings/provisioning-keys); per-day, per-model cost + tokens                                             |
| OpenAI                    | `GET /v1/organization/costs`                                                                     | **Working** — requires an Admin key (`platform.openai.com/settings/organization/admin-keys`, self-serve incl. personal accounts); per-day, per-model cost + tokens |
| Ollama / vLLM / LM Studio | N/A (no billing API)                                                                             | No polling, but **proxy mode works** — add a `proxy.upstreams` entry pointing at the local OpenAI-compatible server                                                |

## Development

```sh
go build ./cmd/token-tracker   # build the daemon (TUI + proxy + poll)
go build ./cmd/tt              # build the CLI
go test -race -count=1 ./...   # run all tests
go vet ./...                   # static analysis
golangci-lint run              # lint (.golangci.yml; needs v2 built with go1.26)
```

See `CLAUDE.md` for full commands and architecture notes.

## License

MIT
