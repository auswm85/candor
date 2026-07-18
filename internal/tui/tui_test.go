package tui

import (
	"os"
	"strings"
	"testing"

	"github.com/auswm85/token-tracker/internal/auth"
	"github.com/auswm85/token-tracker/internal/config"
	"github.com/auswm85/token-tracker/internal/store"
	tea "github.com/charmbracelet/bubbletea"
)

func TestOnboardingWelcomeScreen(t *testing.T) {
	os.Setenv("TOKEN_TRACKER_KEYCHAIN", "file")
	defer os.Unsetenv("TOKEN_TRACKER_KEYCHAIN")

	cfg, _ := config.Load()
	m := NewModel(cfg)
	if m.state != stateOnboarding {
		t.Fatalf("expected stateOnboarding, got %v", m.state)
	}

	view := m.View()
	if !strings.Contains(view, "No keys configured yet") {
		t.Errorf("expected welcome text, got: %s", view)
	}
}

func update(m tea.Model, msg tea.Msg) model {
	updated, _ := m.Update(msg)
	return updated.(model)
}

func TestDashboardTabs(t *testing.T) {
	cfg := &config.Config{}
	cfg.Defaults.MonthlyBudgetUSD = 100
	cfg.Defaults.AlertThresholds = []int{50, 75, 90}

	m := model{
		cfg:       cfg,
		state:     stateDashboard,
		today:     4.00,
		month:     40.00,
		projected: 80.00, // 80% of budget → crosses 50 & 75
		notified:  75,
		daily: []store.DayCost{
			{Day: "2026-07-17", CostUSD: 1.00},
			{Day: "2026-07-18", CostUSD: 2.50},
		},
	}

	// Live tab (default) shows spend.
	if v := m.View(); !strings.Contains(v, "Month:") || !strings.Contains(v, "Projected:") {
		t.Errorf("live tab missing spend, got: %s", v)
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
	// 75% threshold is crossed and notified; 90% is not yet crossed.
	if !strings.Contains(v, "notified") || !strings.Contains(v, "not yet") {
		t.Errorf("alerts tab missing threshold states, got: %s", v)
	}
}

func TestOnboardingFlow(t *testing.T) {
	os.Setenv("TOKEN_TRACKER_KEYCHAIN", "file")
	defer os.Unsetenv("TOKEN_TRACKER_KEYCHAIN")

	cfg, _ := config.Load()
	m := NewModel(cfg)

	// Step 1: Welcome → Enter → provider picker
	m = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.step != stepPickProvider {
		t.Fatalf("expected stepPickProvider, got %v", m.step)
	}
	view := m.View()
	if !strings.Contains(view, "Set up providers") {
		t.Errorf("expected provider picker, got: %s", view)
	}

	// Step 2: Pick OpenAI → Y → input key
	m = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	if m.step != stepInputKey {
		t.Fatalf("expected stepInputKey, got %v", m.step)
	}
	view = m.View()
	if !strings.Contains(view, "Enter your openai API key") {
		t.Errorf("expected key prompt, got: %s", view)
	}

	// Step 3: Type key and confirm
	m = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	m = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	m = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'-'}})
	m = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'t'}})
	m = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	m = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	m = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'t'}})
	m = update(m, tea.KeyMsg{Type: tea.KeyEnter})

	// Should advance to anthropic picker
	if m.step != stepPickProvider {
		t.Fatalf("expected stepPickProvider back, got %v", m.step)
	}
	view = m.View()
	if !strings.Contains(view, "anthropic") {
		t.Errorf("expected anthropic in picker, got: %s", view)
	}

	// Skip anthropic
	m = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})

	// Skip openrouter
	m = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})

	// Should reach done screen
	if m.step != stepDone {
		t.Fatalf("expected stepDone, got %v", m.step)
	}
	view = m.View()
	if !strings.Contains(view, "Onboarding complete") {
		t.Errorf("expected done screen, got: %s", view)
	}

	// Enter to start dashboard
	m = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.state != stateDashboard {
		t.Fatalf("expected stateDashboard, got %v", m.state)
	}
	view = m.View()
	if !strings.Contains(view, "token-tracker") {
		t.Errorf("expected dashboard, got: %s", view)
	}
}

func TestDashboardWhenConfigured(t *testing.T) {
	os.Setenv("TOKEN_TRACKER_KEYCHAIN", "file")
	defer os.Unsetenv("TOKEN_TRACKER_KEYCHAIN")

	// Pre-configure a key so onboarding is skipped
	auth.SetProviderKey("openai", "sk-test")
	defer auth.ClearProviderKey("openai")

	cfg, _ := config.Load()
	m := NewModel(cfg)
	if m.state != stateDashboard {
		t.Fatalf("expected stateDashboard when keys exist, got %v", m.state)
	}
	view := m.View()
	if !strings.Contains(view, "openai") {
		t.Errorf("expected openai in dashboard, got: %s", view)
	}
}
