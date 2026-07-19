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
