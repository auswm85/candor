# candor

Local-first, live LLM cost tracking. A transparent local **proxy** sits in front of your coding harness (Claude Code, OpenCode, …) and records per-request spend as each response streams back — cache-aware, priced in real time, projected against your budget, and surfaced in a full-screen terminal dashboard.

**Local-only.** No cloud, no telemetry, no account, no admin keys. Your inference key is forwarded to the provider untouched and never stored. Everything lives in a single Go binary and a local SQLite database.

## Features

- **Live per-request tracking** — point a harness's base URL at the proxy (or wrap it with `candor run`); usage is recorded as each response streams back.
- **Cache-aware cost model** — separate accounting for cache-read, cache-write, and base input tokens (Anthropic's `cache_read`/`cache_creation`, OpenAI's `cached_tokens`), priced correctly per tier.
- **Rate-limit window tracking** — reads providers' rate-limit response headers, so the dashboard shows your **Claude Code plan windows** (the 5-hour and weekly caps, from `anthropic-ratelimit-unified-*`) and OpenAI/OpenRouter per-minute limits — utilization bars with reset countdowns, captured live from real traffic.
- **Projected monthly spend + alerts** — extrapolates burn rate against your budget and fires an OS notification (macOS/Linux/Windows) once per threshold crossed each month.
- **Multi-provider** — handles OpenAI-compatible (OpenAI, OpenRouter, local servers) and Anthropic protocols. OpenRouter cost comes straight from the provider; others are priced by the engine.
- **Dynamic pricing** — model prices are fetched from OpenRouter's public catalog (no auth), cached, and refreshed daily; a bundled table keeps it working offline. No manual price tables.
- **Full-screen terminal UI** — persistent sidebar (at-a-glance spend, this-session burn rate, proxy status) and tabbed panels: **Live** (24h trend sparkline, live activity feed, top models, cache impact, rate-limit windows), 30-day **History** chart, and **Alerts**.

## Quick Start

```sh
go install github.com/auswm85/candor/cmd/candor@latest

# Open the dashboard (hosts the proxy too)
candor

# In another shell, run a harness through the proxy — nothing persistent:
candor run -- claude

# One-shot queries
candor spend today             # today's spend
candor spend month --by-model  # this month, broken down by model
candor status                  # db size, month/today spend, proxy state
```

Budget alerts are configured in `config.yaml` (`defaults.monthly_budget_usd` and `defaults.alert_thresholds`); candor fires an OS notification the first time projected spend crosses each threshold in a month.

## Routing a harness through candor

The proxy forwards each request to the real provider untouched and taps usage from the response. There are two ways to point a harness at it.

**Recommended: `candor run` (nothing persistent).** Wrap your harness and its LLM traffic is routed through the proxy for that run only — no global config, nothing to undo, and your plain `claude` still goes straight to the provider:

```sh
candor run -- claude              # Claude Code, usage tracked
candor run -- opencode            # any harness that reads provider base-URL env vars
candor run --provider anthropic -- claude   # scope to one provider
```

If the proxy isn't running, `candor run` launches your harness **directly** (straight to the provider) and just skips tracking — it never breaks your workflow.

**Or set the base URL yourself** (persists until you unset it), using the provider name as the first path segment:

```sh
# Claude Code (Anthropic protocol — captures cache-read/cache-creation tokens):
ANTHROPIC_BASE_URL=http://127.0.0.1:7879/anthropic claude

# OpenAI-compatible tools (OpenCode w/ OpenAI, etc.):
#   base URL → http://127.0.0.1:7879/openai/v1
#   OpenRouter → http://127.0.0.1:7879/openrouter/api/v1
```

### Two ways to run candor itself

```sh
# All-in-one: proxy + live dashboard in one terminal
candor

# ...or run the proxy as an always-on background service and open the dashboard
# on demand from any shell (it attaches to the running proxy over /stats):
candor proxy      # or install the launchd/systemd unit: candor service
candor tui        # read-only viewer — live feed + burn rate included
```

`candor` auto-detects an already-running proxy and attaches as a viewer instead of binding a second one, so either workflow just works.

Notes:

- Requires a harness that supports a custom base URL (Claude Code, OpenCode, Aider, Cline, …). Tools that hardcode their endpoint can't be proxied.
- The proxy is **fail-open**: usage tapping runs after your bytes are forwarded and can never break or stall a request — a parsing bug costs a metric, not your response. Anthropic request bodies are forwarded byte-for-byte, so prompt caching is preserved.
- On an API key, the engine prices the captured tokens — **actual cost**. On a subscription (Pro/Max OAuth) login there's no per-token billing, so the same figure is an **API-equivalent estimate**: what that usage would cost at list price. Token, cache, and rate-limit-window figures are accurate either way.

> **Subscription note.** Routing a Pro/Max (OAuth) login through _any_ proxy carries a small, undocumented risk that Anthropic reclassifies the traffic as third-party — so candor is safest for **API-billed** traffic (OpenRouter, OpenAI, Anthropic API key, local servers). candor keeps Anthropic requests byte-faithful to minimize this, but if you're on a subscription and only want plan-window visibility, that's your call to make.

## Configuration

Config lives at `~/.config/candor/config.yaml`. See `configs/config.example.yaml` for the full schema.

Key settings:

- `defaults.monthly_budget_usd`: your monthly budget for projections
- `defaults.alert_thresholds`: % thresholds for notifications (default: `[50, 75, 90, 100]`)
- `proxy.*`: listen address, upstreams, request-body cap
- `pricing.source`: dynamic price catalog URL (default OpenRouter's public models API; `""` disables)

## Architecture

```
  harness (Claude Code / OpenCode)
        │ base_url (via `candor run` or ANTHROPIC_BASE_URL)
        ▼
┌───────────────────────────────────────────────┐
│  candor                                        │
│                                                │
│  ┌──────────────┐        ┌─────────────┐       │
│  │    proxy     │───────>│ cost engine │       │
│  │ (per request)│        └──────┬──────┘       │
│  └──────┬───────┘               ▼              │
│         │              ┌─────────────────┐     │
│         └─────────────>│  recorder       │     │
│                        │  + SQLite store │     │
│                        └────────┬────────┘     │
│    alert loop (budget) ─────────┤              │
│                                 ▼              │
│                           ┌──────────┐         │
│                           │   TUI    │         │
│                           │ (bubble) │         │
│                           └──────────┘         │
└───────────────────────────────────────────────┘
```

The proxy forwards each request to the real provider and taps usage (and rate-limit headers) from the response; the recorder prices it and writes to SQLite. A timer projects monthly spend and fires budget alerts. The TUI reads persisted spend from the store and the live feed from the proxy's `/stats` endpoint.

## Development

```sh
go build ./cmd/candor           # build the single binary
go test -race -count=1 ./...     # run all tests
go vet ./...                     # static analysis
golangci-lint run                # lint (.golangci.yml; needs v2 built with go1.26)
```

See `CLAUDE.md` for full commands and architecture notes.

## License

MIT
