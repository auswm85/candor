package tui

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/auswm85/candor/internal/config"
	"github.com/auswm85/candor/internal/cost"
	"github.com/auswm85/candor/internal/proxy"
	"github.com/auswm85/candor/internal/store"
	tea "github.com/charmbracelet/bubbletea"
)

func TestProviderTag(t *testing.T) {
	cases := map[string]string{
		"openai":     "openai",
		"anthropic":  "anthropic",
		"openrouter": "openrouter",
		"gemini":     "gemini", // unknown provider — still renders
	}
	for name := range cases {
		t.Run(name, func(t *testing.T) {
			got := providerTag(name)
			if !strings.Contains(got, name) {
				t.Errorf("providerTag(%q) = %q, expected to contain %q", name, got, name)
			}
		})
	}
}

func TestShortDur(t *testing.T) {
	cases := map[time.Duration]string{
		30 * time.Second:               "<1m",
		1 * time.Minute:                "1m",
		5*time.Minute + 30*time.Second: "6m", // rounds to 6m
		59 * time.Minute:               "59m",
		1 * time.Hour:                  "1h",
		2*time.Hour + 30*time.Minute:   "2h30m",
		3 * time.Hour:                  "3h",
		24 * time.Hour:                 "24h",
		48*time.Hour + 15*time.Minute:  "48h15m",
	}
	for d, want := range cases {
		t.Run(want, func(t *testing.T) {
			if got := shortDur(d); got != want {
				t.Errorf("shortDur(%v) = %q, want %q", d, got, want)
			}
		})
	}
}

func TestFmtTokens(t *testing.T) {
	cases := map[int64]string{
		0:          "0",
		420:        "420",
		999:        "999",
		1000:       "1.0k",
		15300:      "15.3k",
		999999:     "1000.0k",
		1000000:    "1.0M",
		2500000:    "2.5M",
		1500000000: "1500.0M",
	}
	for n, want := range cases {
		t.Run(want, func(t *testing.T) {
			if got := fmtTokens(n); got != want {
				t.Errorf("fmtTokens(%d) = %q, want %q", n, got, want)
			}
		})
	}
}

func TestStatusDot(t *testing.T) {
	on := statusDot(true)
	if !strings.Contains(on, "●") {
		t.Errorf("statusDot(true) = %q, expected ●", on)
	}
	off := statusDot(false)
	if !strings.Contains(off, "○") {
		t.Errorf("statusDot(false) = %q, expected ○", off)
	}
	if on == off {
		t.Error("statusDot(true) and statusDot(false) should differ in glyph/color")
	}
}

func TestMoney(t *testing.T) {
	cases := map[float64]string{
		0:     "$0.00",
		1:     "$1.00",
		0.50:  "$0.50",
		100:   "$100.00",
		0.001: "$0.00",
	}
	for v, want := range cases {
		if got := money(v); got != want {
			t.Errorf("money(%v) = %q, want %q", v, got, want)
		}
	}
}

func TestBudgetBar(t *testing.T) {
	cases := []struct {
		pct   float64
		width int
		check []string // substrings that must appear
	}{
		{0, 10, []string{"░░", " 0%"}},
		{50, 10, []string{"█", "░", " 50%"}},
		{100, 10, []string{"██████████", " 100%"}},
		{150, 5, []string{"█████", " 150%"}},
		{-10, 8, []string{"░░", " -10%"}}, // negative pct clamped to 0 for bar, label preserved
	}
	for _, c := range cases {
		t.Run("", func(t *testing.T) {
			got := budgetBar(c.pct, c.width)
			for _, chk := range c.check {
				if !strings.Contains(got, chk) {
					t.Errorf("budgetBar(%.0f, %d) = %q, expected to contain %q", c.pct, c.width, got, chk)
				}
			}
		})
	}
}

func TestBurnPerHour(t *testing.T) {
	// No start time → 0.
	m := model{sessStart: time.Time{}}
	if got := m.burnPerHour(); got != 0 {
		t.Errorf("burnPerHour with zero start = %v, want 0", got)
	}

	// Just now → < 1 minute → 0.
	m = model{sessStart: time.Now()}
	if got := m.burnPerHour(); got != 0 {
		t.Errorf("burnPerHour just started = %v, want 0", got)
	}

	// 1 hour ago, $5 spent → $5/hr.
	m = model{sessStart: time.Now().Add(-1 * time.Hour), sessCost: 5.0}
	if got := m.burnPerHour(); absFloat(got-5.0) > 0.01 {
		t.Errorf("burnPerHour = %v, want ~5.0", got)
	}

	// 30 min ago, $3 spent → $6/hr.
	m = model{sessStart: time.Now().Add(-30 * time.Minute), sessCost: 3.0}
	if got := m.burnPerHour(); absFloat(got-6.0) > 0.01 {
		t.Errorf("burnPerHour = %v, want ~6.0", got)
	}
}

func absFloat(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

func update(m tea.Model, msg tea.Msg) model {
	updated, _ := m.Update(msg)
	return updated.(model)
}

func TestParseRefresh(t *testing.T) {
	cases := map[string]time.Duration{
		"1s":       time.Second,
		"2500ms":   2500 * time.Millisecond,
		"":         5 * time.Second,        // invalid → fallback
		"garbage":  5 * time.Second,        // invalid → fallback
		"0s":       5 * time.Second,        // non-positive → fallback
		"-1s":      5 * time.Second,        // negative → fallback
		"2562048h": 5 * time.Second,        // overflows ParseDuration → fallback
		"10ms":     250 * time.Millisecond, // below floor → floored
	}
	for in, want := range cases {
		if got := parseRefresh(in); got != want {
			t.Errorf("parseRefresh(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestMoneyFine(t *testing.T) {
	cases := map[float64]string{
		0:            "$0.00",
		0.65886:      "$0.66",
		0.010076:     "$0.01",
		0.0022983744: "$0.0023",
		0.0003275:    "$0.0003",
		0.00005:      "$0.000050",
	}
	for v, want := range cases {
		if got := moneyFine(v); got != want {
			t.Errorf("moneyFine(%v) = %q, want %q", v, got, want)
		}
	}
}

func TestDashboardRenders(t *testing.T) {
	m := NewModel(&config.Config{})
	view := m.View()
	if !strings.Contains(view, "candor") || !strings.Contains(view, "At a glance") {
		t.Errorf("expected dashboard chrome, got: %s", view)
	}
}

func TestDashboardTabs(t *testing.T) {
	cfg := &config.Config{}
	cfg.Defaults.MonthlyBudgetUSD = 100
	cfg.Defaults.AlertThresholds = []int{50, 75, 90}

	m := model{
		cfg:       cfg,
		today:     4.00,
		month:     40.00,
		projected: 80.00, // 80% of budget → crosses 50 & 75
		notified:  75,
		daily: []store.DayCost{
			{Day: "2026-07-17", CostUSD: 1.00},
			{Day: "2026-07-18", CostUSD: 2.50},
		},
	}

	// Live tab (default) shows spend and proxy sections.
	if v := m.View(); !strings.Contains(v, "Month") || !strings.Contains(v, "Projected") || !strings.Contains(v, "Proxy") {
		t.Errorf("live tab missing spend/proxy, got: %s", v)
	}

	// With per-model data + engine, Top models and Cache impact appear.
	m.engine = cost.New(cost.DefaultPrices())
	m.topModels = []store.ModelUsage{
		{Provider: "anthropic", Model: "claude-sonnet-4-5", Input: 1000, Cached: 500, CacheWrite: 100, Output: 200, CostUSD: 2.0},
	}
	m.cacheSaved, m.cacheExtra = 1.5, 0.5
	if v := m.View(); !strings.Contains(v, "Top models") || !strings.Contains(v, "claude-sonnet-4-5") || !strings.Contains(v, "Cache impact") {
		t.Errorf("live tab missing top-models/cache sections, got: %s", v)
	}

	// Switch to History.
	m = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("2")})
	if m.tab != tabHistory {
		t.Fatalf("expected tabHistory, got %v", m.tab)
	}
	if v := m.View(); !strings.Contains(v, "07-18") || !strings.Contains(v, "Daily cost") {
		t.Errorf("history tab missing chart, got: %s", v)
	}

	// Switch to Alerts.
	m = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("3")})
	if m.tab != tabAlerts {
		t.Fatalf("expected tabAlerts, got %v", m.tab)
	}
	v := m.View()
	if !strings.Contains(v, "Thresholds:") {
		t.Errorf("alerts tab missing thresholds, got: %s", v)
	}
	if !strings.Contains(v, "notified") || !strings.Contains(v, "not yet") {
		t.Errorf("alerts tab missing threshold states, got: %s", v)
	}
}

// newTestStore opens a migrated temp-file SQLite store, closed on cleanup.
func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Migrate(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// seedUsage records one usage row for provider/model at time t and returns the row.
func seedUsage(t *testing.T, s *store.Store, provider, modelName string, at time.Time, cost float64) store.UsageRow {
	t.Helper()
	pid, err := s.ProviderID(provider)
	if err != nil {
		t.Fatal(err)
	}
	mid, err := s.ModelID(pid, modelName)
	if err != nil {
		t.Fatal(err)
	}
	row := store.UsageRow{
		ProviderID: pid, ModelID: mid,
		BucketStart: at, BucketEnd: at.Add(time.Minute),
		InputTokens: 1000, CachedInputTokens: 500, CacheWriteTokens: 100, OutputTokens: 200,
		CostUSD: cost,
	}
	if err := s.AddUsage(row); err != nil {
		t.Fatal(err)
	}
	return row
}

func TestLoadSpend_NilStore(t *testing.T) {
	msg := NewModel(&config.Config{}).loadSpend()
	sm, ok := msg.(spendMsg)
	if !ok {
		t.Fatalf("loadSpend returned %T, want spendMsg", msg)
	}
	if sm.err != nil {
		t.Errorf("nil store err = %v, want nil", sm.err)
	}
	if sm.today != 0 || sm.month != 0 || sm.projected != 0 || len(sm.daily) != 0 || len(sm.topModels) != 0 {
		t.Errorf("nil store should return zero spendMsg, got %+v", sm)
	}
}

func TestLoadSpend(t *testing.T) {
	st := newTestStore(t)
	engine := cost.New(cost.DefaultPrices())
	now := time.Now()
	seedUsage(t, st, "anthropic", "claude-sonnet-4-5", now, 2.00)

	if err := st.SetConfigState("alert_notified_"+now.Format("2006-01"), "75"); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordAlert(75, 90, 100); err != nil {
		t.Fatal(err)
	}

	m := NewModel(&config.Config{}).WithStore(st).WithEngine(engine)
	sm := m.loadSpend().(spendMsg)

	if sm.err != nil {
		t.Fatalf("loadSpend err = %v", sm.err)
	}
	if sm.today != 2.00 {
		t.Errorf("today = %v, want 2.00", sm.today)
	}
	if sm.month != 2.00 {
		t.Errorf("month = %v, want 2.00", sm.month)
	}
	if sm.projected < sm.month {
		t.Errorf("projected %v should be >= month %v", sm.projected, sm.month)
	}
	if len(sm.daily) != 1 {
		t.Errorf("daily = %v, want 1 entry", sm.daily)
	}
	if len(sm.hourly) != 1 {
		t.Errorf("hourly = %v, want 1 entry", sm.hourly)
	}
	if sm.tokens24h != 1800 {
		t.Errorf("tokens24h = %v, want 1800", sm.tokens24h)
	}
	if len(sm.topModels) != 1 {
		t.Fatalf("topModels = %v, want 1 entry", sm.topModels)
	}
	if u := sm.topModels[0]; u.Provider != "anthropic" || u.Model != "claude-sonnet-4-5" || u.CostUSD != 2.00 {
		t.Errorf("topModels[0] = %+v", u)
	}
	wantSaved, wantExtra := engine.CacheImpact("anthropic", "claude-sonnet-4-5", 500, 100)
	if sm.cacheSaved != wantSaved || sm.cacheExtra != wantExtra {
		t.Errorf("cache impact = (%v, %v), want (%v, %v)", sm.cacheSaved, sm.cacheExtra, wantSaved, wantExtra)
	}
	if sm.notified != 75 {
		t.Errorf("notified = %v, want 75", sm.notified)
	}
	if len(sm.recentAlerts) != 1 || sm.recentAlerts[0].ThresholdPct != 75 {
		t.Errorf("recentAlerts = %v, want 1 entry @75%%", sm.recentAlerts)
	}
	// No recorder / stats URL → no live session data.
	if len(sm.feed) != 0 || sm.sessReq != 0 || sm.sessCost != 0 {
		t.Errorf("expected no live session data, got feed=%v req=%v cost=%v", sm.feed, sm.sessReq, sm.sessCost)
	}
}

func TestLoadSpend_NoEngine(t *testing.T) {
	st := newTestStore(t)
	seedUsage(t, st, "openai", "gpt-5", time.Now(), 1.25)

	m := NewModel(&config.Config{}).WithStore(st) // no engine attached
	sm := m.loadSpend().(spendMsg)
	if sm.err != nil {
		t.Fatalf("loadSpend err = %v", sm.err)
	}
	if len(sm.topModels) != 1 {
		t.Errorf("topModels = %v, want 1 entry", sm.topModels)
	}
	if sm.cacheSaved != 0 || sm.cacheExtra != 0 {
		t.Errorf("no engine → cache impact should be 0, got (%v, %v)", sm.cacheSaved, sm.cacheExtra)
	}
}

func TestLoadSpend_StoreError(t *testing.T) {
	st := newTestStore(t)
	_ = st.Close() // force every query to fail
	m := NewModel(&config.Config{}).WithStore(st)
	sm := m.loadSpend().(spendMsg)
	if sm.err == nil {
		t.Error("expected error from a closed store, got nil")
	}
}

func TestLoadSpend_Recorder(t *testing.T) {
	st := newTestStore(t)
	engine := cost.New(cost.DefaultPrices())
	rec := proxy.NewRecorder(st, engine)

	// Record 10 events — the dashboard feed is capped at 8.
	for i := 0; i < 10; i++ {
		if err := rec.Record("openai", proxy.Usage{Model: "gpt-5", InputTokens: 100, OutputTokens: 50, CostUSD: 0.50}); err != nil {
			t.Fatal(err)
		}
	}

	m := NewModel(&config.Config{}).WithStore(st).WithRecorder(rec)
	sm := m.loadSpend().(spendMsg)
	if sm.err != nil {
		t.Fatalf("loadSpend err = %v", sm.err)
	}
	if len(sm.feed) != 8 {
		t.Errorf("feed = %d events, want capped at 8", len(sm.feed))
	}
	if sm.sessReq != 10 {
		t.Errorf("sessReq = %v, want 10", sm.sessReq)
	}
	if sm.sessCost != 5.00 {
		t.Errorf("sessCost = %v, want 5.00", sm.sessCost)
	}
	if sm.sessStart.IsZero() {
		t.Error("sessStart should be set from the recorder snapshot")
	}
}

func TestLoadSpend_StatsURL(t *testing.T) {
	st := newTestStore(t)
	stats := proxy.Stats{
		Requests:    3,
		SessionCost: 1.25,
		Started:     time.Now().Add(-time.Hour),
		Recent:      []proxy.Event{{Provider: "anthropic", Model: "claude-sonnet-4-5", CostUSD: 1.25}},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(stats)
	}))
	t.Cleanup(srv.Close)

	// Viewer mode: store attached, no in-process recorder → live data via /stats.
	m := NewModel(&config.Config{}).WithStore(st).WithStatsURL(srv.URL)
	sm := m.loadSpend().(spendMsg)
	if sm.err != nil {
		t.Fatalf("loadSpend err = %v", sm.err)
	}
	if sm.sessReq != 3 || sm.sessCost != 1.25 {
		t.Errorf("sess = (%v, %v), want (3, 1.25)", sm.sessReq, sm.sessCost)
	}
	if len(sm.feed) != 1 || sm.feed[0].Model != "claude-sonnet-4-5" {
		t.Errorf("feed = %v", sm.feed)
	}
}

func TestFetchStats(t *testing.T) {
	t.Run("valid JSON", func(t *testing.T) {
		want := proxy.Stats{
			Requests:    7,
			SessionCost: 3.25,
			Started:     time.Now().Truncate(time.Second),
			Recent:      []proxy.Event{{Provider: "openai", Model: "gpt-5", CostUSD: 0.5}},
			Limits:      []proxy.Limits{{Provider: "openai"}},
		}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(want)
		}))
		defer srv.Close()

		got, err := fetchStats(srv.URL)
		if err != nil {
			t.Fatalf("fetchStats err = %v", err)
		}
		if got.Requests != 7 || got.SessionCost != 3.25 || len(got.Recent) != 1 || len(got.Limits) != 1 {
			t.Errorf("fetchStats = %+v", got)
		}
	})

	t.Run("HTTP error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer srv.Close()

		if _, err := fetchStats(srv.URL); err == nil || !strings.Contains(err.Error(), "500") {
			t.Errorf("expected 500 status error, got %v", err)
		}
	})

	t.Run("malformed body", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("not json"))
		}))
		defer srv.Close()

		if _, err := fetchStats(srv.URL); err == nil {
			t.Error("expected decode error, got nil")
		}
	})
}

func TestInit(t *testing.T) {
	// No store → no refresh loop.
	if cmd := NewModel(&config.Config{}).Init(); cmd != nil {
		t.Error("Init without store should return nil cmd")
	}

	// With a store the first tick fires immediately.
	st := newTestStore(t)
	cmd := NewModel(&config.Config{}).WithStore(st).Init()
	if cmd == nil {
		t.Fatal("Init with store should arm the first tick")
	}
	if _, ok := cmd().(tickMsg); !ok {
		t.Errorf("Init cmd produced %T, want tickMsg", cmd())
	}
}

func TestWithOptions(t *testing.T) {
	st := newTestStore(t)
	engine := cost.New(cost.DefaultPrices())
	rec := proxy.NewRecorder(st, engine)

	m := NewModel(&config.Config{}).
		WithStore(st).
		WithEngine(engine).
		WithRecorder(rec).
		WithStatsURL("http://127.0.0.1:7879/stats")

	if m.store != st || m.engine != engine || m.recorder != rec || m.statsURL == "" {
		t.Errorf("With* options did not attach: %+v", m)
	}
	if p := NewProgram(m); p == nil {
		t.Error("NewProgram returned nil")
	}
}

func key(s string) tea.KeyMsg {
	switch s {
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "shift+tab":
		return tea.KeyMsg{Type: tea.KeyShiftTab}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "ctrl+c":
		return tea.KeyMsg{Type: tea.KeyCtrlC}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
}

func TestUpdateTabKeys(t *testing.T) {
	cases := []struct {
		key  string
		from tab
		want tab
	}{
		{"1", tabAlerts, tabLive},
		{"2", tabLive, tabHistory},
		{"3", tabLive, tabAlerts},
		{"tab", tabLive, tabHistory},
		{"tab", tabAlerts, tabLive},  // wraps forward
		{"j", tabHistory, tabAlerts}, // vim-style next
		{"down", tabHistory, tabAlerts},
		{"shift+tab", tabLive, tabAlerts}, // wraps backward
		{"k", tabLive, tabAlerts},         // vim-style prev
		{"up", tabAlerts, tabHistory},
		{"x", tabHistory, tabHistory}, // unknown key leaves tab alone
	}
	for _, c := range cases {
		t.Run(c.key, func(t *testing.T) {
			m := update(model{tab: c.from}, key(c.key))
			if m.tab != c.want {
				t.Errorf("%q from tab %d → tab %d, want %d", c.key, c.from, m.tab, c.want)
			}
		})
	}
}

func TestUpdateQuit(t *testing.T) {
	for _, k := range []string{"q", "ctrl+c"} {
		t.Run(k, func(t *testing.T) {
			_, cmd := model{}.Update(key(k))
			if cmd == nil {
				t.Fatalf("%q should return a quit cmd", k)
			}
			if _, ok := cmd().(tea.QuitMsg); !ok {
				t.Errorf("%q cmd produced %T, want tea.QuitMsg", k, cmd())
			}
		})
	}
}

func TestUpdateRefreshGating(t *testing.T) {
	// Not loading → r kicks off a refresh.
	m, cmd := model{}.Update(key("r"))
	if cmd == nil {
		t.Error("r while idle should start a loadSpend cmd")
	}
	if !m.(model).loading {
		t.Error("r while idle should set loading")
	}

	// Already loading → r is a no-op.
	m, cmd = model{loading: true}.Update(key("r"))
	if cmd != nil {
		t.Error("r while loading should be a no-op")
	}
	if !m.(model).loading {
		t.Error("loading flag should stay set")
	}
}

func TestUpdateTick(t *testing.T) {
	// Idle → tick starts a load and re-arms the heartbeat.
	m, cmd := model{refresh: time.Hour}.Update(tickMsg{})
	if cmd == nil || !m.(model).loading {
		t.Errorf("idle tick should load + re-arm, got cmd=%v loading=%v", cmd, m.(model).loading)
	}

	// Loading → tick only re-arms (no overlapping refresh).
	m, cmd = model{refresh: time.Hour, loading: true}.Update(tickMsg{})
	if cmd == nil {
		t.Error("tick while loading should still re-arm the heartbeat")
	}
	if !m.(model).loading {
		t.Error("loading flag should stay set")
	}
}

func TestUpdateSpendMsg(t *testing.T) {
	// Error → spendErr set, data untouched.
	m := update(model{loading: true}, spendMsg{err: errors.New("boom")})
	if m.spendErr != "boom" {
		t.Errorf("spendErr = %q, want %q", m.spendErr, "boom")
	}
	if m.loading {
		t.Error("spendMsg should clear loading")
	}

	// Success → all fields copied, spendErr cleared, updatedAt stamped.
	start := time.Now().Add(-time.Hour)
	m = update(model{loading: true, spendErr: "stale"}, spendMsg{
		today: 1.5, month: 10, projected: 20,
		daily:        []store.DayCost{{Day: "2026-07-19", CostUSD: 1.5}},
		hourly:       []store.HourCost{{Hour: "2026-07-19T10", CostUSD: 1.5}},
		tokens24h:    1234,
		topModels:    []store.ModelUsage{{Provider: "openai", Model: "gpt-5", CostUSD: 1.5}},
		cacheSaved:   0.5,
		cacheExtra:   0.25,
		notified:     50,
		recentAlerts: []store.AlertEvent{{ThresholdPct: 50}},
		feed:         []proxy.Event{{Provider: "openai", Model: "gpt-5"}},
		limits:       []proxy.Limits{{Provider: "openai"}},
		sessReq:      3,
		sessCost:     1.5,
		sessStart:    start,
	})
	if m.spendErr != "" {
		t.Errorf("spendErr = %q, want cleared", m.spendErr)
	}
	if m.today != 1.5 || m.month != 10 || m.projected != 20 || m.tokens24h != 1234 {
		t.Errorf("spend fields not copied: %+v", m)
	}
	if len(m.daily) != 1 || len(m.hourly) != 1 || len(m.topModels) != 1 || len(m.recentAlerts) != 1 {
		t.Errorf("slice fields not copied: %+v", m)
	}
	if m.cacheSaved != 0.5 || m.cacheExtra != 0.25 || m.notified != 50 {
		t.Errorf("cache/alert fields not copied: %+v", m)
	}
	if len(m.feed) != 1 || len(m.limits) != 1 || m.sessReq != 3 || m.sessCost != 1.5 || !m.sessStart.Equal(start) {
		t.Errorf("session fields not copied: %+v", m)
	}
	if m.updatedAt.IsZero() {
		t.Error("updatedAt should be stamped on success")
	}
}

func TestUpdateWindowSize(t *testing.T) {
	m := update(model{cfg: &config.Config{}}, tea.WindowSizeMsg{Width: 120, Height: 40})
	if m.width != 120 || m.height != 40 {
		t.Errorf("size = (%d, %d), want (120, 40)", m.width, m.height)
	}
	// With a known terminal size the view fills the frame.
	if v := m.View(); !strings.Contains(v, "candor") {
		t.Errorf("view lost chrome after resize: %s", v)
	}
}

func TestSparkline(t *testing.T) {
	hours := func(costs ...float64) []store.HourCost {
		out := make([]store.HourCost, len(costs))
		for i, c := range costs {
			out[i] = store.HourCost{Hour: "h", CostUSD: c}
		}
		return out
	}

	t.Run("no data", func(t *testing.T) {
		if got := (model{}).sparkline(); !strings.Contains(got, "no activity in the last 24h") {
			t.Errorf("sparkline() = %q", got)
		}
	})

	t.Run("scales to max", func(t *testing.T) {
		got := model{hourly: hours(0, 5, 10)}.sparkline()
		if !strings.Contains(got, "▁▄█") {
			t.Errorf("sparkline(0,5,10) = %q, want levels ▁▄█", got)
		}
		if !strings.Contains(got, "$15.00 total") {
			t.Errorf("sparkline(0,5,10) = %q, want $15.00 total", got)
		}
	})

	t.Run("single point", func(t *testing.T) {
		got := model{hourly: hours(2.5)}.sparkline()
		if !strings.Contains(got, "█") || !strings.Contains(got, "$2.50 total") {
			t.Errorf("sparkline(2.5) = %q", got)
		}
	})

	t.Run("all zero", func(t *testing.T) {
		got := model{hourly: hours(0, 0)}.sparkline()
		if !strings.Contains(got, "▁▁") || !strings.Contains(got, "$0.00 total") {
			t.Errorf("sparkline(0,0) = %q", got)
		}
	})

	t.Run("trims to last 24", func(t *testing.T) {
		vals := make([]float64, 30) // 30 × 1.0 → last 24 kept, total over kept only
		for i := range vals {
			vals[i] = 1.0
		}
		got := model{hourly: hours(vals...)}.sparkline()
		if n := strings.Count(got, "█"); n != 24 {
			t.Errorf("sparkline(30 pts) rendered %d bars, want 24", n)
		}
		if !strings.Contains(got, "$24.00 total") {
			t.Errorf("sparkline(30 pts) = %q, want $24.00 total", got)
		}
	})
}

func TestRenderLive(t *testing.T) {
	t.Run("empty state", func(t *testing.T) {
		got := model{}.renderLive(80)
		for _, want := range []string{"24h trend", "Live activity", "waiting for requests…", "no activity in the last 24h", "No usage yet"} {
			if !strings.Contains(got, want) {
				t.Errorf("empty renderLive missing %q, got: %s", want, got)
			}
		}
	})

	t.Run("with data", func(t *testing.T) {
		m := model{
			engine:    cost.New(cost.DefaultPrices()),
			hourly:    []store.HourCost{{Hour: "h1", CostUSD: 1}, {Hour: "h2", CostUSD: 2}},
			tokens24h: 1800,
			today:     3.0,
			month:     10.0,
			feed: []proxy.Event{{
				At:       time.Date(2026, 7, 19, 14, 5, 6, 0, time.Local),
				Provider: "openai", Model: "gpt-5-codex-extra-long-name",
				Input: 1000, Output: 500, CostUSD: 0.0123,
			}},
			topModels: []store.ModelUsage{
				{Provider: "anthropic", Model: "claude-sonnet-4-5", Input: 1000, Cached: 500, CacheWrite: 100, Output: 200, CostUSD: 2.0},
			},
			cacheSaved: 1.5, cacheExtra: 0.5,
			limits: []proxy.Limits{{
				Provider: "anthropic",
				Windows: []proxy.RateWindow{
					{Label: "5h", Utilization: 60, Remaining: -1, Reset: time.Now().Add(2 * time.Hour)},
					{Label: "7d", Utilization: -1, Remaining: 42},
					{Label: "1m", Utilization: -1, Remaining: -1},
				},
			}},
		}
		got := m.renderLive(80)
		for _, want := range []string{
			"1.8k tokens",                 // tokens24h suffix
			"openai",                      // feed provider tag
			"gpt-5-codex-extra-long-name", // model name shown in full at this width
			"14:05:06",                    // feed timestamp
			"1.5k tok",                    // feed token total
			"Top models", "Cache impact", "Net cache effect",
			"Rate limits",
			"anthropic 5h", "60%", // utilization window → budget bar
			"anthropic 7d", "42 left", // remaining-only window
			"anthropic 1m", "—", // unknown window
			"resets in", // future reset countdown
		} {
			if !strings.Contains(got, want) {
				t.Errorf("renderLive missing %q, got: %s", want, got)
			}
		}
	})

	t.Run("narrow width truncates model name", func(t *testing.T) {
		m := model{
			engine: cost.New(cost.DefaultPrices()),
			feed: []proxy.Event{{
				At:       time.Date(2026, 7, 19, 14, 5, 6, 0, time.Local),
				Provider: "openai", Model: "gpt-5-codex-extra-long-name",
				Input: 1000, Output: 500, CostUSD: 0.0123,
			}},
		}
		// width 60 → nameW clamps to 18, so the 27-char name is truncated.
		got := m.renderLive(60)
		if !strings.Contains(got, "gpt-5-codex-extra…") {
			t.Errorf("renderLive(60) should truncate long model name, got: %s", got)
		}
		if strings.Contains(got, "gpt-5-codex-extra-long-name") {
			t.Errorf("renderLive(60) should not show full long model name, got: %s", got)
		}
	})
}

func TestRenderSidebar(t *testing.T) {
	cfg := &config.Config{}
	cfg.Defaults.MonthlyBudgetUSD = 100
	cfg.Proxy.Enabled = true

	m := model{cfg: cfg, month: 40, sessCost: 1.5, sessReq: 7}
	got := m.renderSidebar()
	for _, want := range []string{"Live", "History", "Alerts", "At a glance", "This session", "Proxy on", "$40.00"} {
		if !strings.Contains(got, want) {
			t.Errorf("sidebar missing %q, got: %s", want, got)
		}
	}
}

func TestRenderAlerts(t *testing.T) {
	t.Run("no budget", func(t *testing.T) {
		got := model{cfg: &config.Config{}}.renderAlerts(80)
		if !strings.Contains(got, "No monthly budget configured") {
			t.Errorf("renderAlerts = %q", got)
		}
	})

	t.Run("no thresholds", func(t *testing.T) {
		cfg := &config.Config{}
		cfg.Defaults.MonthlyBudgetUSD = 100
		got := model{cfg: cfg}.renderAlerts(80)
		if !strings.Contains(got, "(none configured)") {
			t.Errorf("renderAlerts = %q", got)
		}
	})

	t.Run("recent alerts history", func(t *testing.T) {
		cfg := &config.Config{}
		cfg.Defaults.MonthlyBudgetUSD = 100
		cfg.Defaults.AlertThresholds = []int{50}
		m := model{
			cfg:       cfg,
			projected: 60,
			recentAlerts: []store.AlertEvent{
				{FiredAt: time.Now(), ThresholdPct: 50, ProjectedUSD: 90, BudgetUSD: 100},
			},
		}
		got := m.renderAlerts(80)
		for _, want := range []string{"Recent alerts", "50%", "$90 / $100"} {
			if !strings.Contains(got, want) {
				t.Errorf("renderAlerts missing %q, got: %s", want, got)
			}
		}
	})
}

func TestHeaderHint(t *testing.T) {
	if got := (model{}).headerHint(); got != "" {
		t.Errorf("zero updatedAt hint = %q, want empty", got)
	}
	if got := (model{updatedAt: time.Now()}).headerHint(); got != "updated just now" {
		t.Errorf("fresh updatedAt hint = %q", got)
	}
	if got := (model{updatedAt: time.Now().Add(-90 * time.Second)}).headerHint(); !strings.HasPrefix(got, "updated ") || !strings.HasSuffix(got, "s ago") {
		t.Errorf("stale updatedAt hint = %q, want \"updated Ns ago\"", got)
	}
}
