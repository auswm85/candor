package tui

import (
	"github.com/auswm85/token-tracker/internal/alert"
	"github.com/auswm85/token-tracker/internal/config"
	tea "github.com/charmbracelet/bubbletea"
)

type model struct {
	cfg     *config.Config
	alerter *alert.Checker
	ready   bool
}

func NewModel(cfg *config.Config) model {
	return model{cfg: cfg}
}

func NewProgram(m model, alerter *alert.Checker) *tea.Program {
	m.alerter = alerter
	return tea.NewProgram(m)
}

func (m model) Init() tea.Cmd {
	return nil
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m model) View() string {
	return "token-tracker — connecting...\n\nPress q to quit.\n"
}