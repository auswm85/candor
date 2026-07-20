package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/auswm85/candor/internal/config"
	"github.com/auswm85/candor/internal/cost"
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
