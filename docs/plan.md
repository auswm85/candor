# candor — Design & Roadmap

Living design doc for candor. Reflects the current architecture and where it's
headed. For day-to-day commands and conventions see `CLAUDE.md`; for user-facing
usage see `README.md`.

## 1. What candor is

A local-first tool that records **live, per-request LLM spend** by sitting in
front of a coding harness (Claude Code, OpenCode, …) as a **transparent
reverse proxy**. It prices each request with a cache-aware cost engine, tracks
provider rate-limit windows, projects monthly spend, fires budget alerts, and
surfaces everything in a full-screen terminal dashboard.

**Principles:**

- **Local-only.** No cloud, no telemetry, no account. Single Go binary + SQLite.
- **No privileged keys.** The proxy forwards the harness's own inference key and
  stores nothing. There are no admin/management keys to configure.
- **Never break the request.** Usage tapping is fail-open and runs after the
  client's bytes are forwarded; a parser bug costs a metric, not a response.
- **First-party fidelity.** Anthropic request bodies are forwarded byte-for-byte
  so prompt caching (and first-party classification) is preserved.

**How it differs from the field:** the crowded tools are _log parsers_
(ccusage, tokscale, codeburn) that read harness session files after the fact,
and _heavyweight gateways_ (LiteLLM, Helicone, Portkey) that need a server + DB +
account. candor occupies the gap: a lightweight, local, single-binary,
transparent-proxy tap with correct cache-tier pricing — live and per-request,
with no infra.

## 2. Architecture

Single Go binary, single process. Bare `candor` runs the TUI and (guarded by
config) hosts the proxy, plus a timer-based budget-alert loop. A `daemon.lock`
(flock) enforces one dashboard instance. If a proxy is already running (e.g. a
background `candor proxy` service), the dashboard attaches to it as a viewer over
`/stats` instead of binding a second one.

```
  harness (Claude Code / OpenCode)
        │ base_url (via `candor run` or ANTHROPIC_BASE_URL)
        ▼
┌───────────────────────────────────────────────┐
│  candor                                        │
│  ┌──────────────┐        ┌─────────────┐       │
│  │    proxy     │───────>│ cost engine │       │
│  │ (per request)│        └──────┬──────┘       │
│  └──────┬───────┘               ▼              │
│         │              ┌─────────────────┐     │
│         └─────────────>│ recorder + store│     │
│                        └────────┬────────┘     │
│    alert loop (budget) ─────────┤              │
│                                 ▼              │
│                           ┌──────────┐         │
│                           │   TUI    │         │
│                           └──────────┘         │
└───────────────────────────────────────────────┘
```

- **proxy** (`internal/proxy`) — transparent reverse proxy. First path segment
  selects the upstream (`/openai/…`, `/anthropic/…`, `/openrouter/…`). A
  per-provider extractor taps token usage from the response (streaming +
  non-streaming), and rate-limit response headers are parsed into current-window
  state. Serves `/healthz` (liveness) and `/stats` (live feed/burn/limits JSON).
- **recorder** (`internal/proxy`) — prices each request via the engine and writes
  additively into a per-minute bucket (`store.AddUsage`); keeps an in-memory ring
  of recent events + session counters + latest rate-limit windows for `/stats`.
- **cost engine** (`internal/cost`) — pure function (provider, model, tokens by
  tier) → USD, with model-name normalization (dated snapshots → base pricing).
  Provider-supplied cost (OpenRouter) is used directly when present.
- **alert loop** (`internal/alert` + `app.StartAlertLoop`) — a ticker projects
  monthly spend and fires an OS notification the first time each budget threshold
  is crossed per month (dedup via `config_state`).
- **TUI** (`internal/tui`) — full-screen bubbletea; sidebar + tabbed Live /
  History / Alerts. Reads persisted spend from the store and live data from the
  in-process recorder or a remote proxy's `/stats`.

### Capture: `candor run` vs. base-URL override

The recommended entry is `candor run -- <harness>`, which sets the provider
base-URL env vars for the child process **only** — nothing persistent, and if the
proxy is down the harness runs directly (untracked) rather than breaking. A
manual `ANTHROPIC_BASE_URL=…` override is the alternative for harnesses driven by
config files.

## 3. Cost model (the differentiator)

Anthropic and OpenAI both price input tokens in three tiers — base input, cache
read (cheap), cache write/creation (a premium). candor accounts for each
separately (`cache_read`/`cache_creation`, `cached_tokens`) and prices them per
tier, which most tools get wrong (e.g. ccusage prices cache creation at the
5-minute tier while Claude Code mostly uses the 1-hour tier).

**Pricing is dynamic:** fetched from OpenRouter's public model catalog (no auth)
on start, cached to `<db-dir>/prices.json`, refreshed daily, with a bundled table
for offline use — no manual price tracking. OpenRouter-proxied traffic doesn't
need it; cost comes straight from the response.

## 4. Rate-limit windows

The proxy reads providers' rate-limit response headers and exposes them in the
dashboard:

- **Anthropic** `anthropic-ratelimit-unified-5h-*` / `-7d-*` — the Claude Code
  plan windows (utilization + reset), which subscription users actually watch and
  which Claude Code itself doesn't surface to hooks.
- **OpenAI / OpenRouter** `x-ratelimit-*-requests|tokens` — per-minute limits.

Captured live from real traffic (no extra probe calls), rendered as utilization
bars with reset countdowns.

## 5. Data model (SQLite)

`modernc.org/sqlite` (pure Go, no CGO). Embedded SQL migrations run by
`store.Migrate()`; DSN uses `_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)`.

- `providers(id, name)` and `models(id, provider_id, name)` — interned lookups.
- `usage_records(provider_id, model_id, bucket_start, bucket_end, input_tokens,
cached_input_tokens, cache_write_tokens, output_tokens, cost_usd, …)` — usage
  accumulated into per-minute buckets; the recorder adds additively so live proxy
  writes land within a refresh tick.
- `config_state(key, value)` — small key/value store (e.g. per-month alert dedup).
- `alert_events(fired_at, threshold_pct, projected_usd, budget_usd)` — audit log
  of budget-threshold notifications actually fired (one row per notification).

## 6. CLI surface

Single binary, `candor`. Bare `candor` opens the dashboard.

```
candor                       # dashboard (TUI + proxy, or attach to a running proxy)
candor run -- <harness>      # run a harness routed through the proxy (per-process, nothing persistent)
candor proxy                 # headless proxy for a background service; also fires budget alerts
candor tui                   # dashboard as a read-only viewer of a running proxy
candor spend today|month     # one-shot spend (--by-model for a breakdown)
candor status                # db size, today/month spend, whether the proxy is running
candor service               # print a launchd/systemd unit that runs `candor proxy`
candor migrate               # apply pending migrations
```

## 7. TUI design

Full-screen (alt-screen) bubbletea app: a persistent left **sidebar** (nav +
at-a-glance spend, budget bar, this-session burn rate, proxy status) beside a
tabbed main panel:

- **Live** — 24h trend sparkline, live activity feed (from the recorder ring),
  top models with $/M, cache impact, rate-limit windows.
- **History** — 30-day daily-cost bar chart.
- **Alerts** — budget, projected spend, threshold crossed/notified state, and a
  history of recently fired alerts.

## 8. Status (shipped)

- [x] Transparent reverse proxy, per-request usage capture, streaming +
      non-streaming (OpenAI-compatible + Anthropic protocols).
- [x] Fail-open, panic-isolated tapping; byte-faithful Anthropic request bodies.
- [x] Cache-aware cost engine + dynamic pricing (OpenRouter catalog, cached,
      bundled fallback).
- [x] Additive per-minute storage; `/healthz` + `/stats` endpoints.
- [x] `candor run` per-process wrapper (fail-safe when the proxy is down).
- [x] Rate-limit window capture + dashboard panel.
- [x] Full-screen sidebar TUI (Live / History / Alerts) with detached viewer.
- [x] Timer-based budget projection + OS notifications (macOS/Linux/Windows).
- [x] Alert history (`alert_events`) — fired notifications logged and shown on the Alerts tab.
- [x] Single-binary consolidation; single-instance lock.

## 9. Roadmap

**Near-term**

- Per-request event log / drill-down (persist the recorder ring for history).
- `candor doctor` — verify the proxy is up and a harness is correctly routed.
- Onboarding-free first run polish: clearer empty-state "point a harness here".
- goreleaser (Homebrew tap, deb, signed macOS binary); `v0.1.0` tag.

**Subscription-safe capture (the honest gap)**

The proxy is ideal for **API-billed** traffic. For Claude Code **subscription**
(OAuth) logins, routing through any proxy carries a small, undocumented risk of
being reclassified as third-party. A first-party capture path would serve that
audience without the risk:

- Read Claude Code's own statusline `rate_limits`/usage payload (zero
  interposition — lowest risk), or
- An in-process `fetch()`-hook launcher (cccost-style) for full-fidelity live
  per-request data while staying genuinely first-party.

**Later**

- OpenAI-compatible local providers via proxy (Ollama, vLLM, LM Studio).
- Project/session tagging; CSV/Parquet export; weekly digest.
- Additional alert channels (Slack, email).

## 10. Dropped / non-goals (history)

- **Polling mode** — pulling provider billing APIs on an interval. Removed: it
  required privileged admin/provisioning keys, was delayed and coarse (OpenRouter
  refuses the current UTC day), and was off-message for a live-tracking tool. The
  proxy covers the live use case; git history retains the adapters if ever needed.
- **Embedded web dashboard** — a SvelteKit static UI. Dropped: the TUI already
  covers spend, top models, cache impact, history, and alerts, and a browser view
  would add a Node toolchain for a read-only duplicate. (If ever wanted, the cheap
  path is one HTML page + a JSON endpoint on the proxy's existing HTTP server.)

## 11. Risks & mitigations

| Risk                                                          | Mitigation                                                                                                                                 |
| ------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------ |
| Subscription traffic reclassified as third-party by Anthropic | Keep Anthropic requests byte-faithful; position candor for API-billed traffic; document the caveat; build a first-party capture path (§9). |
| Proxy sits in the paid-inference critical path                | Fail-open, panic-isolated tap; no `WriteTimeout`; the `run` wrapper falls back to direct when the proxy is down.                           |
| Pricing drift                                                 | Dynamic pricing from OpenRouter's catalog, daily refresh, bundled offline fallback.                                                        |
| A crash takes the proxy down mid-session                      | Single-instance lock + `KeepAlive`/`Restart` service unit for auto-restart; `run` never depends on a persistent global route.              |
| Harness doesn't support a custom base URL                     | Documented limitation; a first-party capture path (§9) would cover these.                                                                  |
