package tui

import (
	"fmt"
	"log"
	"strings"

	"github.com/auswm85/token-tracker/internal/alert"
	"github.com/auswm85/token-tracker/internal/auth"
	"github.com/auswm85/token-tracker/internal/config"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

var providers = []string{"openai", "anthropic", "openrouter"}

type state int

const (
	stateOnboarding state = iota
	stateDashboard
)

type step int

const (
	stepWelcome step = iota
	stepPickProvider
	stepInputKey
	stepDone
)

type model struct {
	cfg     *config.Config
	alerter *alert.Checker
	state   state
	step    step

	// onboarding state
	pickIndex        int
	input            textinput.Model
	inputErr         string
	configured       []string
	skipped          []string
}

func NewModel(cfg *config.Config) model {
	m := model{cfg: cfg}

	if len(auth.ListConfiguredProviders()) == 0 {
		m.state = stateOnboarding
		m.step = stepWelcome
	} else {
		m.state = stateDashboard
	}

	return m
}

func NewProgram(m model, alerter *alert.Checker) *tea.Program {
	m.alerter = alerter
	return tea.NewProgram(m)
}

func (m model) Init() tea.Cmd {
	return nil
}

// --- Update ---

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m.state {
	case stateOnboarding:
		return m.updateOnboarding(msg)
	case stateDashboard:
		return m.updateDashboard(msg)
	}
	return m, nil
}

func (m model) updateOnboarding(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m.step {
	case stepWelcome:
		return m.updateWelcome(msg)
	case stepPickProvider:
		return m.updatePickProvider(msg)
	case stepInputKey:
		return m.updateInputKey(msg)
	case stepDone:
		return m.updateDone(msg)
	}
	return m, nil
}

func (m model) updateWelcome(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "enter":
			m.step = stepPickProvider
			m.pickIndex = 0
			return m, nil
		case "q", "ctrl+c":
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m model) updatePickProvider(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "j", "down":
			if m.pickIndex < len(providers)-1 {
				m.pickIndex++
			}
		case "k", "up":
			if m.pickIndex > 0 {
				m.pickIndex--
			}
		case "y":
			// configure this provider
			ti := textinput.New()
			ti.Placeholder = fmt.Sprintf("%s API key", providers[m.pickIndex])
			ti.EchoMode = textinput.EchoPassword
			ti.Focus()
			m.input = ti
			m.inputErr = ""
			m.step = stepInputKey
		case "n":
			m.skipped = append(m.skipped, providers[m.pickIndex])
			if m.pickIndex < len(providers)-1 {
				m.pickIndex++
			} else {
				m.step = stepDone
			}
		case "q", "ctrl+c":
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m model) updateInputKey(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "enter":
			key := strings.TrimSpace(m.input.Value())
			if key == "" {
				m.inputErr = "key cannot be empty"
				return m, nil
			}
			provider := providers[m.pickIndex]
			if err := auth.SetProviderKey(provider, key); err != nil {
				m.inputErr = fmt.Sprintf("failed to store key: %v", err)
				return m, nil
			}
			m.configured = append(m.configured, provider)
			m.input.Reset()
			m.inputErr = ""
			if m.pickIndex < len(providers)-1 {
				m.pickIndex++
				m.step = stepPickProvider
			} else {
				m.step = stepDone
			}
		case "esc":
			// go back to provider picker
			m.input.Reset()
			m.inputErr = ""
			m.step = stepPickProvider
		default:
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(msg)
			return m, cmd
		}
	default:
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m model) updateDone(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "enter":
			m.state = stateDashboard
			log.Printf("onboarding complete: configured=%v skipped=%v", m.configured, m.skipped)
		case "q", "ctrl+c":
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m model) updateDashboard(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		}
	}
	return m, nil
}

// --- View ---

func (m model) View() string {
	switch m.state {
	case stateOnboarding:
		return m.viewOnboarding()
	case stateDashboard:
		return m.viewDashboard()
	}
	return ""
}

func (m model) viewOnboarding() string {
	switch m.step {
	case stepWelcome:
		return m.viewWelcome()
	case stepPickProvider:
		return m.viewPickProvider()
	case stepInputKey:
		return m.viewInputKey()
	case stepDone:
		return m.viewDone()
	}
	return ""
}

func (m model) viewWelcome() string {
	var b strings.Builder
	b.WriteString("╔══════════════════════════════════════════╗\n")
	b.WriteString("║         token-tracker                   ║\n")
	b.WriteString("║    Local-first LLM cost monitoring      ║\n")
	b.WriteString("╚══════════════════════════════════════════╝\n\n")
	b.WriteString("token-tracker polls your LLM provider usage APIs\n")
	b.WriteString("and shows your spend in a live dashboard.\n\n")
	b.WriteString("No keys configured yet.\n")
	b.WriteString("Press Enter to set up your providers.\n")
	b.WriteString("Press q to quit.\n")
	return b.String()
}

func (m model) viewPickProvider() string {
	var b strings.Builder
	b.WriteString("Set up providers\n\n")
	for i, p := range providers {
		prefix := "  "
		if i == m.pickIndex {
			prefix = "> "
		}
		status := ""
		if auth.HasProviderKey(p) {
			status = " [already configured]"
		}
		b.WriteString(fmt.Sprintf("%s%s%s\n", prefix, p, status))
	}
	b.WriteString("\n[Y]es  [N]o  [↑/↓] navigate  [q] quit\n")
	return b.String()
}

func (m model) viewInputKey() string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("Enter your %s API key:\n\n", providers[m.pickIndex]))
	b.WriteString(fmt.Sprintf("  %s\n", m.input.View()))
	if m.inputErr != "" {
		b.WriteString(fmt.Sprintf("\n  ⚠ %s\n", m.inputErr))
	}
	b.WriteString("\n[Enter] confirm  [Esc] back  [q] quit\n")
	return b.String()
}

func (m model) viewDone() string {
	var b strings.Builder
	b.WriteString("Onboarding complete!\n\n")
	if len(m.configured) > 0 {
		b.WriteString("Configured:\n")
		for _, p := range m.configured {
			b.WriteString(fmt.Sprintf("  ✓ %s\n", p))
		}
	}
	if len(m.skipped) > 0 {
		b.WriteString("Skipped:\n")
		for _, p := range m.skipped {
			b.WriteString(fmt.Sprintf("  ✗ %s\n", p))
		}
	}
	b.WriteString("\nPress Enter to start the dashboard.\n")
	b.WriteString("Press q to quit.\n")
	return b.String()
}

func (m model) viewDashboard() string {
	configured := auth.ListConfiguredProviders()
	return fmt.Sprintf(
		"token-tracker — monitoring\n\n"+
			"Configured providers: %s\n"+
			"Web dashboard: http://127.0.0.1:7878\n\n"+
			"Press q to quit.\n",
		strings.Join(configured, ", "),
	)
}
