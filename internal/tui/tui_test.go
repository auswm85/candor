package tui

import (
	"os"
	"strings"
	"testing"

	"github.com/auswm85/token-tracker/internal/auth"
	"github.com/auswm85/token-tracker/internal/config"
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
