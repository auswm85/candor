package tui

import (
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/auswm85/token-tracker/internal/alert"
	"github.com/auswm85/token-tracker/internal/app"
	"github.com/auswm85/token-tracker/internal/auth"
	"github.com/auswm85/token-tracker/internal/config"
	"github.com/auswm85/token-tracker/internal/cost"
	"github.com/auswm85/token-tracker/internal/store"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

var (
	titleStyle    = lipgloss.NewStyle().Bold(true)
	activeTab     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("231")).Background(lipgloss.Color("62")).Padding(0, 1)
	inactiveTab   = lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Padding(0, 1)
	dimStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	sectionHeader = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("111"))
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

type tab int

const (
	tabLive tab = iota
	tabHistory
	tabAlerts
)

type model struct {
	cfg     *config.Config
	alerter *alert.Checker
	store   *store.Store
	engine  *cost.Engine
	state   state
	step    step

	// dashboard state
	tab        tab
	today      float64
	month      float64
	projected  float64
	daily      []store.DayCost
	topModels  []store.ModelUsage
	cacheSaved float64
	cacheExtra float64
	notified   int // highest budget threshold already alerted this month
	spendErr   string

	// onboarding state
	pickIndex  int
	input      textinput.Model
	inputErr   string
	configured []string
	skipped    []string
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

// WithStore attaches a store so the dashboard can display recorded spend.
func (m model) WithStore(st *store.Store) model {
	m.store = st
	return m
}

// WithEngine attaches a cost engine so the dashboard can compute cache impact.
func (m model) WithEngine(e *cost.Engine) model {
	m.engine = e
	return m
}

func NewProgram(m model, alerter *alert.Checker) *tea.Program {
	m.alerter = alerter
	return tea.NewProgram(m)
}

type spendMsg struct {
	today      float64
	month      float64
	projected  float64
	daily      []store.DayCost
	topModels  []store.ModelUsage
	cacheSaved float64
	cacheExtra float64
	notified   int
	err        error
}

type tickMsg struct{}

// loadSpend reads today/month totals, the 30-day daily history, and the current
// month projection from the store.
func (m model) loadSpend() tea.Msg {
	if m.store == nil {
		return spendMsg{}
	}
	now := time.Now()
	startOfDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	startOfMonth := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())

	today, err := m.store.TotalCostSince(startOfDay)
	if err != nil {
		return spendMsg{err: err}
	}
	month, err := m.store.TotalCostSince(startOfMonth)
	if err != nil {
		return spendMsg{err: err}
	}
	daily, err := m.store.DailyCostSince(now.AddDate(0, 0, -30))
	if err != nil {
		return spendMsg{err: err}
	}

	// Project the month forward at the current burn rate.
	daysElapsed := now.Sub(startOfMonth).Hours() / 24
	if daysElapsed < 1 {
		daysElapsed = 1
	}
	projected := month / daysElapsed * 30

	notified := 0
	if v, err := m.store.GetConfigState("alert_notified_" + now.Format("2006-01")); err == nil && v != "" {
		notified, _ = strconv.Atoi(v)
	}

	// Per-model breakdown (this month) → top models + aggregate cache impact.
	usage, err := m.store.ModelUsageSince(startOfMonth)
	if err != nil {
		return spendMsg{err: err}
	}
	var saved, extra float64
	if m.engine != nil {
		for _, u := range usage {
			s, x := m.engine.CacheImpact(u.Provider, u.Model, u.Cached, u.CacheWrite)
			saved += s
			extra += x
		}
	}

	return spendMsg{
		today: today, month: month, projected: projected, daily: daily,
		topModels: usage, cacheSaved: saved, cacheExtra: extra, notified: notified,
	}
}

func tick() tea.Cmd {
	return tea.Tick(5*time.Second, func(time.Time) tea.Msg { return tickMsg{} })
}

func (m model) Init() tea.Cmd {
	if m.state == stateDashboard && m.store != nil {
		return tea.Batch(m.loadSpend, tick())
	}
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
			if m.store != nil {
				return m, tea.Batch(m.loadSpend, tick())
			}
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
		case "r":
			return m, m.loadSpend
		case "1":
			m.tab = tabLive
		case "2":
			m.tab = tabHistory
		case "3":
			m.tab = tabAlerts
		case "tab", "right", "l":
			m.tab = (m.tab + 1) % 3
		case "shift+tab", "left", "h":
			m.tab = (m.tab + 2) % 3
		}
	case spendMsg:
		if msg.err != nil {
			m.spendErr = msg.err.Error()
		} else {
			m.spendErr = ""
			m.today = msg.today
			m.month = msg.month
			m.projected = msg.projected
			m.daily = msg.daily
			m.topModels = msg.topModels
			m.cacheSaved = msg.cacheSaved
			m.cacheExtra = msg.cacheExtra
			m.notified = msg.notified
		}
	case tickMsg:
		return m, tea.Batch(m.loadSpend, tick())
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
		fmt.Fprintf(&b, "%s%s%s\n", prefix, p, status)
	}
	b.WriteString("\n[Y]es  [N]o  [↑/↓] navigate  [q] quit\n")
	return b.String()
}

func (m model) viewInputKey() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Enter your %s API key:\n\n", providers[m.pickIndex])
	fmt.Fprintf(&b, "  %s\n", m.input.View())
	if m.inputErr != "" {
		fmt.Fprintf(&b, "\n  ⚠ %s\n", m.inputErr)
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
			fmt.Fprintf(&b, "  ✓ %s\n", p)
		}
	}
	if len(m.skipped) > 0 {
		b.WriteString("Skipped:\n")
		for _, p := range m.skipped {
			fmt.Fprintf(&b, "  ✗ %s\n", p)
		}
	}
	b.WriteString("\nPress Enter to start the dashboard.\n")
	b.WriteString("Press q to quit.\n")
	return b.String()
}

func (m model) viewDashboard() string {
	var b strings.Builder

	// Title + tab strip + inline nav hint, with a rule underneath so the strip
	// reads as navigation rather than a heading.
	b.WriteString(titleStyle.Render("token-tracker"))
	b.WriteString("   ")
	b.WriteString("\n")
	b.WriteString(m.tabBar())
	b.WriteString("  ")
	b.WriteString(dimStyle.Render("←/→ or Tab · 1·2·3"))
	b.WriteString("\n")
	b.WriteString(dimStyle.Render(strings.Repeat("─", 52)))
	b.WriteString("\n\n")

	switch m.tab {
	case tabHistory:
		b.WriteString(m.renderHistory())
	case tabAlerts:
		b.WriteString(m.renderAlerts())
	default:
		b.WriteString(m.renderLive())
	}

	if m.spendErr != "" {
		fmt.Fprintf(&b, "\n⚠ %s\n", m.spendErr)
	}
	b.WriteString("\n")
	b.WriteString(dimStyle.Render("←/→ switch tab   ·   r refresh   ·   q quit"))
	b.WriteString("\n")
	return b.String()
}

// tabBar renders Live/History/Alerts as tabs — the active one highlighted, each
// prefixed with its number key so the shortcut is obvious.
func (m model) tabBar() string {
	names := []string{"Live", "History", "Alerts"}
	tabs := make([]string, len(names))
	for i, n := range names {
		label := fmt.Sprintf("%d %s", i+1, n)
		if tab(i) == m.tab {
			tabs[i] = activeTab.Render(label)
		} else {
			tabs[i] = inactiveTab.Render(label)
		}
	}
	return strings.Join(tabs, " ")
}

func (m model) renderLive() string {
	var b strings.Builder

	// --- Spend ---
	fmt.Fprintf(&b, "%s\n", sectionHeader.Render("Spend"))
	fmt.Fprintf(&b, "  Today      $%.2f\n", m.today)
	budget := m.cfg.Defaults.MonthlyBudgetUSD
	if budget > 0 {
		pct := m.month / budget * 100
		flag := ""
		if pct >= 90 {
			flag = "  ⚠"
		}
		fmt.Fprintf(&b, "  Month      $%.2f / $%.0f   %s%s\n", m.month, budget, progressBar(pct), flag)
	} else {
		fmt.Fprintf(&b, "  Month      $%.2f\n", m.month)
	}
	fmt.Fprintf(&b, "  Projected  $%.2f  (at current rate)\n", m.projected)

	// --- Top models (this month) ---
	if len(m.topModels) > 0 {
		fmt.Fprintf(&b, "\n%s\n", sectionHeader.Render("Top models (this month)"))
		max := m.topModels[0].CostUSD // rows are cost-desc
		for i, u := range m.topModels {
			if i >= 5 {
				break
			}
			name := u.Provider + "/" + u.Model
			if len(name) > 30 {
				name = name[:29] + "…"
			}
			bars := 0
			if max > 0 {
				bars = int(u.CostUSD / max * 12)
			}
			perM := 0.0
			if tot := u.Input + u.Cached + u.CacheWrite + u.Output; tot > 0 {
				perM = u.CostUSD / float64(tot) * 1_000_000
			}
			fmt.Fprintf(&b, "  %-30s $%7.2f  %-12s $%.2f/M\n",
				name, u.CostUSD, strings.Repeat("▓", bars), perM)
		}
	}

	// --- Cache impact (this month) ---
	if m.engine != nil && (m.cacheSaved > 0 || m.cacheExtra > 0) {
		fmt.Fprintf(&b, "\n%s\n", sectionHeader.Render("Cache impact (this month)"))
		fmt.Fprintf(&b, "  Saved via cache reads:   $%.2f\n", m.cacheSaved)
		fmt.Fprintf(&b, "  Extra via cache writes:  $%.2f\n", m.cacheExtra)
		fmt.Fprintf(&b, "  Net cache effect:        %+.2f\n", m.cacheExtra-m.cacheSaved)
	}

	// --- Proxy ---
	listen := app.ProxyListen(m.cfg)
	fmt.Fprintf(&b, "\n%s\n", sectionHeader.Render("Proxy"))
	if m.cfg.Proxy.Enabled {
		fmt.Fprintf(&b, "  ✓ listening on %s\n", listen)
	} else {
		b.WriteString("  ✗ off — run `tt proxy`, or set proxy.enabled: true\n")
	}
	provs := make([]string, 0)
	for name := range app.ProxyUpstreams(m.cfg) {
		provs = append(provs, name)
	}
	sort.Strings(provs)
	fmt.Fprintf(&b, "  point a tool's base URL at  http://%s/<provider>/…\n", listen)
	fmt.Fprintf(&b, "  providers: %s\n", strings.Join(provs, " · "))

	// --- Empty state ---
	if m.today == 0 && m.month == 0 && len(m.daily) == 0 {
		b.WriteString("\nNo usage recorded yet — run a tool through the proxy and spend appears here.\n")
	}
	return b.String()
}

func (m model) renderHistory() string {
	if len(m.daily) == 0 {
		return "No usage recorded in the last 30 days."
	}
	maxCost := 0.0
	for _, d := range m.daily {
		if d.CostUSD > maxCost {
			maxCost = d.CostUSD
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", sectionHeader.Render("Daily cost — last 30 days"))
	const width = 30
	for _, d := range m.daily {
		bars := 0
		if maxCost > 0 {
			bars = int(d.CostUSD / maxCost * width)
		}
		// Show the MM-DD suffix of the YYYY-MM-DD day string.
		label := d.Day
		if len(label) >= 5 {
			label = label[len(label)-5:]
		}
		fmt.Fprintf(&b, "%s  %s %7.2f\n", label, strings.Repeat("▓", bars), d.CostUSD)
	}
	return b.String()
}

func (m model) renderAlerts() string {
	var b strings.Builder
	budget := m.cfg.Defaults.MonthlyBudgetUSD
	if budget <= 0 {
		return "No monthly budget configured (defaults.monthly_budget_usd).\nSet one in config.yaml to enable alerts."
	}

	pct := m.projected / budget * 100
	fmt.Fprintf(&b, "Monthly budget:  $%.0f\n", budget)
	fmt.Fprintf(&b, "Projected spend: $%.2f (%.0f%% of budget)\n\n", m.projected, pct)
	b.WriteString("Thresholds:\n")

	thresholds := m.cfg.Defaults.AlertThresholds
	if len(thresholds) == 0 {
		b.WriteString("  (none configured)\n")
		return b.String()
	}
	for _, t := range thresholds {
		mark, note := "○", "not yet"
		if int(pct) >= t {
			mark = "✓"
			note = "crossed"
		}
		if t <= m.notified {
			note += ", notified"
		}
		fmt.Fprintf(&b, "  %s  %3d%%   %s\n", mark, t, note)
	}
	return b.String()
}

// progressBar renders a 10-cell bar for a 0-100 percentage.
func progressBar(pct float64) string {
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	filled := int(pct / 10)
	return "[" + strings.Repeat("▓", filled) + strings.Repeat("░", 10-filled) + fmt.Sprintf("] %.0f%%", pct)
}
