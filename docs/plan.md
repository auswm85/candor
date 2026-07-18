# token-tracker — Implementation Plan

## 1. Project Overview

**What it does:** A long-running local daemon that polls LLM provider usage endpoints every N minutes, applies cache-aware cost rules from a YAML config, projects monthly burn, and surfaces it via a bubbletea TUI + an embedded web dashboard.

**Stack:** Go (single binary), SQLite (modernc.org/sqlite — pure-Go, no CGO), bubbletea (TUI), Svelte/SvelteKit static (embedded web UI).

**Local-first:** All data and API keys stay on the machine. Keys stored in OS keychain (macOS Keychain, Windows Credential Manager, Linux libsecret). No cloud dependency.

**Differentiation vs existing tools:**

- _Helicone/Langfuse/Portkey_ — cloud-hosted, require routing traffic through their gateway. token-tracker reads usage directly from provider billing APIs, never intercepts your traffic.
- _OpenAI's own dashboard_ — slow, web-only, no cost projection, no cache-read vs cache-write breakdown by project.
- _Vercel AI Gateway_ — couples to their gateway. token-tracker is provider-agnostic.
- **Cache-aware cost model** is the killer feature: the July 2026 OpenAI cache-write pricing change punishes task-oriented workloads, and no existing tool surfaces cache-read vs cache-write costs separately.

## 2. v1 Scope

### Supported providers (Strategy A — poll official billing APIs only)

- **OpenAI** — `GET /v1/organization/costs?start_time=...&end_time=...&group_by[]=model&group_by[]=line_item`
- **Anthropic** — `GET /v1/organizations/usage_report/messages` + `GET /v1/organizations/cost_report` (requires Admin API key — `sk-ant-admin01-...`)
- **OpenRouter** — `GET /api/v1/usage`

### Explicitly NOT in v1

- Local/self-hosted OpenAI-compatible providers (Ollama, vLLM, LM Studio, LocalAI, llama.cpp server) — no billing API to poll; would require proxy mode, deferred to v1.1
- Cloud providers without dedicated usage APIs (some Groq tiers, Anyscale, Fireworks)
- Bedrock / Vertex AI (different auth model, deferred)

## 3. Project Structure

```
token-tracker/
├── go.mod                         # module: github.com/auswm85/token-tracker
├── CLAUDE.md                      # conventions, commands, architecture
├── README.md
├── Makefile                       # build/test/run/lint targets
├── .github/workflows/
│   ├── ci.yml                     # go test + lint + build on push
│   └── release.yml                # goreleaser on tag
├── cmd/
│   ├── token-tracker/             # main daemon entrypoint
│   │   └── main.go
│   └── tt/                        # short-form CLI for one-shot queries
│       └── main.go
├── internal/
│   ├── poll/                      # scheduler
│   ├── provider/
│   │   ├── openai/                # /v1/organization/costs
│   │   ├── anthropic/             # /v1/organizations/usage_report/messages + /cost_report
│   │   └── openrouter/            # /api/v1/usage
│   ├── cost/                      # cost rules engine (YAML-driven)
│   ├── store/                     # SQLite via sqlc + migrations
│   ├── alert/                     # threshold checker + notifiers
│   ├── tui/                       # bubbletea views
│   ├── web/                       # net/http server + embedded static
│   └── config/                    # viper, keyring integration
├── pkg/                           # reserved for future public API
├── web/                           # Svelte/SvelteKit source
│   ├── package.json
│   └── src/
├── configs/
│   └── config.example.yaml
└── db/migrations/
```

### Key dependencies

- `charmbracelet/bubbletea` + `lipgloss` — TUI
- `charmbracelet/bubbles` — table/spinner/progress components
- `spf13/viper` — config (YAML + env var overlay)
- `zalando/go-keyring` — OS keychain for API keys
- `modernc.org/sqlite` — pure-Go SQLite (no CGO)
- `sqlc` — type-safe SQL codegen
- `golang-migrate/migrate` — schema migrations
- `go.uber.org/zap` — structured logging
- `spf13/cobra` — CLI subcommand parsing

## 4. Architecture

### 4.1 Process model

Single Go binary, single process. Three concurrent goroutines:

```
┌─────────────────────────────────────────────────────────────┐
│  token-tracker daemon                                        │
│                                                             │
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

- **Poll loop:** ticks every 5 min (configurable). Fetches incremental usage from each provider for the window `[last_fetched, now)`. Writes raw rows to SQLite.
- **Cost engine:** pure function. Input = (provider, model, cache_read_tokens, cache_write_tokens, input_tokens, output_tokens, tier). Output = USD cost. Reads pricing from `config.yaml`.
- **Alert checker:** runs after each poll. Compares projected monthly spend against thresholds. Emits OS-native notifications.
- **TUI:** subscribes to store updates via a Go channel, redraws on change.
- **Web:** net/http server on `127.0.0.1:7878`, serves embedded Svelte build + JSON API.

### 4.2 Cache-aware cost model (the differentiator)

OpenAI's pricing (post July 2026) has three input-token tiers per model:

1. **Cache read** — ~10% of base input price
2. **Cache write (creation)** — ~125% of base input price (the new tax)
3. **Base input (no cache)** — 100%

Cost rule schema in `config.yaml`:

```yaml
providers:
  openai:
    models:
      gpt-4o:
        input_per_1m: 2.50
        cached_input_per_1m: 0.3125 # cache_read
        cache_write_per_1m: 3.125 # cache_creation
        output_per_1m: 10.00
      gpt-4o-mini:
        input_per_1m: 0.15
        cached_input_per_1m: 0.01875
        cache_write_per_1m: 0.1875
        output_per_1m: 0.60
  anthropic:
    models:
      claude-sonnet-4-5:
        input_per_1m: 3.00
        cached_input_per_1m: 0.30
        cache_write_per_1m: 3.75
        output_per_1m: 15.00
```

**Pricing drift mitigation:** A `tt prices diff` subcommand fetches provider pricing pages, diffs against current YAML, prints a `git diff`-style report. User reviews and manually updates.

### 4.3 Per-provider polling notes

**OpenAI:**

- Endpoint: `GET /v1/organization/costs?start_time=...&end_time=...&group_by[]=model&group_by[]=line_item&limit=1000`
- Returns `usage_type: prompt_tokens`, `cached_tokens`, `completion_tokens` — already includes cache breakdown.
- Pagination: cursor-based via `after` param.
- Rate limit: 60 req/min on org endpoints.

**Anthropic (requires Admin API key):**

- Usage endpoint: `GET /v1/organizations/usage_report/messages?starting_at=...&ending_at=...&group_by[]=model&bucket_width=1d`
- Cost endpoint: `GET /v1/organizations/cost_report?starting_at=...&ending_at=...&group_by[]=model`
- Auth: `x-api-key: $ANTHROPIC_ADMIN_KEY` (Admin API key — `sk-ant-admin01-...`)
- Returns token counts with `cached_input`, `cache_creation`, `output` breakdowns — cache tracking is built in
- Cost endpoint returns USD as decimal strings; data freshness ~5 min; supports 1-min polling
- Pagination: cursor-based via `next_page` / `has_more`

**OpenRouter:**

- Endpoint: `GET /api/v1/usage`
- Auth: `Authorization: Bearer <key>` header.
- Returns per-model aggregation with token counts (input, output, cache_read, cache_write) and cost in USD — already includes cache breakdown.
- OpenRouter uses prefixed model IDs (e.g., `openai/gpt-4o`, `anthropic/claude-sonnet-4.5`). The `models.name` column stores full prefixed IDs.

## 5. Data Model (SQLite)

```sql
-- migrations/001_init.sql

CREATE TABLE providers (
    id   INTEGER PRIMARY KEY,
    name TEXT NOT NULL UNIQUE            -- 'openai' | 'anthropic' | 'openrouter'
);

CREATE TABLE models (
    id          INTEGER PRIMARY KEY,
    provider_id INTEGER NOT NULL REFERENCES providers(id),
    name        TEXT NOT NULL,           -- 'gpt-4o' or 'openai/gpt-4o'
    UNIQUE(provider_id, name)
);

CREATE TABLE usage_records (
    id                   INTEGER PRIMARY KEY,
    provider_id          INTEGER NOT NULL REFERENCES providers(id),
    model_id             INTEGER NOT NULL REFERENCES models(id),
    bucket_start         TEXT NOT NULL,  -- ISO8601 UTC, 5-min bucket
    bucket_end           TEXT NOT NULL,
    input_tokens         INTEGER NOT NULL,
    cached_input_tokens  INTEGER NOT NULL DEFAULT 0,
    cache_write_tokens   INTEGER NOT NULL DEFAULT 0,
    output_tokens        INTEGER NOT NULL,
    cost_usd             REAL NOT NULL,
    raw_payload          TEXT,           -- full API response JSON for debugging
    fetched_at           TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE(provider_id, model_id, bucket_start)
);

CREATE INDEX idx_usage_time ON usage_records(bucket_start);
CREATE INDEX idx_usage_model ON usage_records(model_id, bucket_start);

CREATE TABLE alerts (
    id              INTEGER PRIMARY KEY,
    name            TEXT NOT NULL,
    threshold_usd   REAL NOT NULL,
    window          TEXT NOT NULL,       -- 'daily' | 'monthly'
    channel         TEXT NOT NULL,       -- 'terminal' (v1) | 'slack' | 'email' (v1.1)
    enabled         INTEGER NOT NULL DEFAULT 1,
    last_triggered_at TEXT,
    created_at      TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE config_state (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);  -- tracks last_poll_per_provider, schema_version, etc.
```

## 6. Configuration

**File:** `~/.config/token-tracker/config.yaml` (macOS/Linux) or `%APPDATA%\token-tracker\config.yaml` (Windows).

```yaml
poll_interval: 5m
database: ~/.local/share/token-tracker/tokens.db
web:
  enabled: true
  listen: 127.0.0.1:7878
tui:
  refresh: 1s
defaults:
  monthly_budget_usd: 100
  alert_thresholds: [50, 75, 90, 100]

providers:
  openai:
    enabled: true
    keyring_key: token-tracker/openai-api-key
  anthropic:
    enabled: true
    keyring_key: token-tracker/anthropic-api-key
  openrouter:
    enabled: true
    keyring_key: token-tracker/openrouter-api-key

prices:
  # ... see §4.2
```

**Secret handling:** API keys NEVER in config file. Always loaded via `go-keyring` from OS keychain. First-run `tt auth` subcommand prompts for keys and stores them.

## 7. TUI Design (bubbletea)

Three primary views:

1. **Live dashboard** (`tt` or `token-tracker` daemon)

```
┌─ token-tracker ────────────────────────────────────────────┐
│ Today: $4.32 / $10 daily budget    ▓▓▓▓▓▓░░░░ 43%          │
│ Month: $87.50 / $100 budget        ▓▓▓▓▓▓▓▓▓░ 87% ⚠       │
│                                                             │
│ Top models (last 24h):                                      │
│  gpt-4o              $2.41  ▓▓▓▓▓▓▓▓▓▓▓▓▓▓░░░░  $1.20/M    │
│  gpt-4o-mini         $1.13  ▓▓▓▓▓▓░░░░░░░░░░░░  $0.06/M    │
│  claude-sonnet       $0.78  ▓▓▓▓░░░░░░░░░░░░░░░  $1.10/M    │
│                                                             │
│ Cache impact (24h):                                         │
│   Saved via cache read:  $1.84 (-30%)                       │
│   Extra via cache write: $0.92 (+15%)                        │
│                                                             │
│ [1] Live  [2] History  [3] Alerts  [q] Quit               │
└─────────────────────────────────────────────────────────────┘
```

2. **History view** — line chart of daily cost over last 30 days, filterable by model.

3. **Alerts view** — list configured thresholds, current projected spend, last triggered.

**Library:** `charmbracelet/bubbletea` + `bubbles/table` + `bubbles/progress`.

## 8. Web Dashboard

**Tech:** Svelte/SvelteKit built to static files, served from `internal/web/static/` via Go 1.22+ `net/http` with `embed.FS`. Single binary, no separate Node process.

- Build: `npm run build` in `web/` outputs to `internal/web/static/`.
- API: REST JSON, same backend, paths like `GET /api/usage?range=24h`, `GET /api/alerts`, `POST /api/alerts`.
- Auth: bind to `127.0.0.1` only; no auth in v1 (localhost trust model).

**Pages:** same three views as TUI plus a settings page.

**Why Svelte not Next.js:** For an embedded static dashboard SvelteKit's static adapter is the lightest option (~50KB gzipped vs MB for Next.js). Alternative: plain HTML + htmx (simpler but less polished).

## 9. CLI Subcommands

```bash
tt                          # launch TUI
tt spend today              # one-shot: print today's spend
tt spend month --by-model   # one-shot: monthly breakdown by model
tt auth                     # set API keys (stores in OS keychain)
tt alert add --monthly 75   # add alert at 75% of monthly budget
tt prices diff              # fetch provider pricing pages, diff vs config
tt prices list              # print current pricing table
tt daemon                   # run as background daemon
tt status                   # daemon health, last poll time, DB size
```

## 10. Alerting (v1)

- Thresholds are % of `monthly_budget_usd` (e.g., 50/75/90/100%).
- Triggered when projected monthly spend crosses threshold (projection = current_spend + extrapolated_remaining_time_at_avg_rate).
- Channels in v1: **terminal notification only.**
  - macOS: `osascript -e 'display notification "..." with title "token-tracker"'`
  - Linux: `notify-send`
  - Windows: `msg` or `BurntToast` PowerShell
- v1.1 escalation: Slack webhook, email (Resend).

## 11. Testing Strategy

- **Unit tests**: cost engine (pure functions, table-driven tests), config parsing, alert logic.
- **Integration tests**: SQLite store with `:memory:` DB, mocked provider HTTP responses.
- **HTTP mock**: `http.HandlerFunc` returning fixture JSON from `testdata/openai_costs_*.json`.
- **TUI snapshot tests**: `charmbracelet/bubbletea` `teatest` package — record golden output, diff on change.
- **CI**: GitHub Actions on push — `go test -race ./...`, `golangci-lint run`, `govulncheck ./...`. Mirror flowbee's path-split workflow pattern: `backend.yml` for `**/*.go`, `frontend.yml` for `web/**`.

## 12. Milestones

### M0 — Spike (resolved — Anthropic docs confirmed)

- [x] Anthropic: `GET /v1/organizations/usage_report/messages` exists, requires Admin API key, includes cache tracking
- [x] OpenRouter: `GET /api/v1/usage` works with standard API key, includes cache breakdown
- [x] Decision: ship 3-provider v1 (OpenAI, Anthropic, OpenRouter)

### M1 — Skeleton (4 days)

- [ ] `go mod init`, repo scaffolding
- [ ] SQLite migrations, sqlc-generated store code
- [ ] Config loader (viper + keyring)
- [ ] OpenAI provider adapter (polling + pagination)
- [ ] OpenRouter provider adapter
- [ ] Anthropic provider adapter (usage_report + cost_report, requires Admin API key)
- [ ] Cost engine (pure function, fully unit-tested)
- [ ] `tt spend today` CLI — first end-to-end vertical slice

### M2 — Daemon + Alerts (3 days)

- [ ] Poll loop with backoff + retry
- [ ] Alert checker
- [ ] `tt daemon` command + launchd/systemd unit generation
- [ ] Terminal notifications on all 3 platforms

### M3 — TUI (3 days)

- [ ] bubbletea live dashboard view
- [ ] History view (30-day chart)
- [ ] Alerts view
- [ ] Snapshot tests

### M4 — Web Dashboard (4 days)

- [ ] Svelte/SvelteKit static build pipeline
- [ ] JSON API endpoints
- [ ] 3 pages mirroring TUI
- [ ] Embedded into Go binary via `embed.FS`

### M5 — Polish & Release (2 days)

- [ ] `tt prices diff` (scrape provider pricing pages)
- [ ] goreleaser config (Homebrew tap, dmg, deb)
- [ ] README with install/quickstart
- [ ] CLAUDE.md following flowbee conventions
- [ ] v0.1.0 tag

**Total estimate: ~17 working days part-time (3-4 weeks).**

### v1.1 stretch goals

- Proxy mode for OpenAI-compatible local providers (Ollama, vLLM, LM Studio)
- Slack + Email alert channels
- Project tagging
- Weekly digest email
- CSV/Parquet export
- Mistral, Cohere, Together, Groq adapters

## 13. Risks & Mitigations

| Risk                                                                    | Severity | Mitigation                                                                                                                        |
| ----------------------------------------------------------------------- | -------- | --------------------------------------------------------------------------------------------------------------------------------- |
| Anthropic Admin API key requirement (not standard API key)              | Low      | Documented in config as `keyring_key: token-tracker/anthropic-admin-key`. Users generate via Console → Settings → Admin API keys. |
| OpenAI deprecates `/v1/organization/costs` as they did `/v1/usage`      | Medium   | Pin API version; monitor changelog. `tt prices diff` keeps you aware of changes.                                                  |
| OpenAI deprecates `/v1/organization/costs` as they did `/v1/usage`      | Medium   | Pin API version; monitor changelog. `tt prices diff` keeps you aware of changes.                                                  |
| Cache-read vs cache-write breakdown not available in aggregate endpoint | Medium   | OpenAI's `/v1/organization/costs` returns `usage_type: cached_tokens` — verify in M0.                                             |
| Pricing drift (providers change prices)                                 | Medium   | `tt prices diff` command, no auto-update.                                                                                         |
| Go learning curve (no existing Go projects in this directory)           | Medium   | Stick to stdlib where possible; budget 2 extra days in M1 for ramp-up.                                                            |
| TUI snapshot tests brittle                                              | Low      | Use `teatest` golden files, review diffs in PR.                                                                                   |
| OS keyring cross-platform edge cases                                    | Low      | `zalando/go-keyring` well-maintained; fallback to env var with warning.                                                           |

## 14. Alternatives Considered

- **Rust + Tauri** (matches flowbee stack): would reuse existing skills. Rejected because long-running daemon + SQLite + polling is simpler in Go, and a bubbletea TUI is a better fit for a dev tool than a full Tauri desktop app.
- **TypeScript + Next.js** (matches hypelnk stack): Next.js is a poor fit for a background daemon. Would need separate Node daemon + Next dashboard — two processes.
- **Pure CLI (no daemon)**: rejected because projection and alerts need continuous state; one-shot polling would hit provider rate limits.

## 15. Open Questions (for the user)

1. **Module path:** `github.com/auswm85/token-tracker` or different?
2. **GitHub repo:** public or private? (Affects Homebrew tap publishing.)
3. **v1.1 proxy decision:** defer or decide now?
