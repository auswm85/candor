package tui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/auswm85/candor/internal/app"
	"github.com/auswm85/candor/internal/config"
	"github.com/auswm85/candor/internal/cost"
	"github.com/auswm85/candor/internal/proxy"
	"github.com/auswm85/candor/internal/store"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Palette — a small, consistent set of colors used across the dashboard.
const (
	clrAccent = lipgloss.Color("111") // headings, active nav
	clrGreen  = lipgloss.Color("42")
	clrYellow = lipgloss.Color("214")
	clrRed    = lipgloss.Color("203")
	clrText   = lipgloss.Color("252")
	clrDim    = lipgloss.Color("245")
	clrFaint  = lipgloss.Color("240") // borders, rules
)

var (
	brandStyle    = lipgloss.NewStyle().Bold(true).Foreground(clrAccent)
	dimStyle      = lipgloss.NewStyle().Foreground(clrDim)
	faintStyle    = lipgloss.NewStyle().Foreground(clrFaint)
	sectionHeader = lipgloss.NewStyle().Bold(true).Foreground(clrAccent)

	activeNav   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("231")).Background(lipgloss.Color("62"))
	inactiveNav = lipgloss.NewStyle().Foreground(clrDim)

	panelStyle = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(clrFaint).Padding(0, 1)

	// sparkline levels, low → high
	sparkLevels = []rune("▁▂▃▄▅▆▇█")
)

// providerTag colors a provider name for the activity feed.
func providerTag(name string) string {
	color := clrDim
	switch name {
	case "openai":
		color = clrGreen
	case "anthropic":
		color = clrYellow
	case "openrouter":
		color = lipgloss.Color("141") // purple
	}
	return lipgloss.NewStyle().Foreground(color).Render(name)
}

type tab int

const (
	tabLive tab = iota
	tabHistory
	tabAlerts
)

type model struct {
	cfg      *config.Config
	store    *store.Store
	engine   *cost.Engine
	recorder *proxy.Recorder // in-process live data (all-in-one mode)
	statsURL string          // remote proxy /stats (viewer mode); used when recorder is nil
	refresh  time.Duration   // dashboard refresh interval (from tui.refresh config)
	loading  bool            // a loadSpend is in flight — skip new ones so refreshes don't overlap

	// terminal size (from tea.WindowSizeMsg)
	width  int
	height int

	// dashboard state
	tab          tab
	today        float64
	month        float64
	projected    float64
	daily        []store.DayCost
	hourly       []store.HourCost
	tokens24h    int64
	topModels    []store.ModelUsage
	cacheSaved   float64
	cacheExtra   float64
	notified     int // highest budget threshold already alerted this month
	recentAlerts []store.AlertEvent
	spendErr     string

	// live session state (from the in-process proxy recorder)
	feed      []proxy.Event
	limits    []proxy.Limits
	sessReq   int
	sessCost  float64
	sessStart time.Time
	updatedAt time.Time
}

func NewModel(cfg *config.Config) model {
	return model{cfg: cfg, refresh: parseRefresh(cfg.TUI.Refresh)}
}

// parseRefresh turns the configured tui.refresh string into a duration, falling
// back to 5s and flooring at 250ms so a typo/tiny value can't busy-loop the UI.
func parseRefresh(s string) time.Duration {
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 5 * time.Second
	}
	if d < 250*time.Millisecond {
		return 250 * time.Millisecond
	}
	return d
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

// WithRecorder attaches the in-process proxy recorder so the dashboard can show
// the live activity feed and session burn rate without a DB round-trip.
func (m model) WithRecorder(r *proxy.Recorder) model {
	m.recorder = r
	return m
}

// WithStatsURL points the dashboard at a running proxy's /stats endpoint for
// live data — used by the detached viewer (`candor tui`) when there's no
// in-process recorder.
func (m model) WithStatsURL(url string) model {
	m.statsURL = url
	return m
}

func NewProgram(m model) *tea.Program {
	// Alt-screen: take over the terminal (clears prior scrollback on boot,
	// restores it on exit) like Claude Code / OpenCode.
	return tea.NewProgram(m, tea.WithAltScreen())
}

type spendMsg struct {
	today        float64
	month        float64
	projected    float64
	daily        []store.DayCost
	hourly       []store.HourCost
	tokens24h    int64
	topModels    []store.ModelUsage
	cacheSaved   float64
	cacheExtra   float64
	notified     int
	recentAlerts []store.AlertEvent
	err          error

	// live session snapshot (nil recorder → zero values)
	feed      []proxy.Event
	limits    []proxy.Limits
	sessReq   int
	sessCost  float64
	sessStart time.Time
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
	hourly, err := m.store.HourlyCostSince(now.Add(-24 * time.Hour))
	if err != nil {
		return spendMsg{err: err}
	}
	tokens24h, err := m.store.TotalTokensSince(now.Add(-24 * time.Hour))
	if err != nil {
		return spendMsg{err: err}
	}

	projected := app.ProjectMonthValue(month, now)

	notified := 0
	if v, err := m.store.GetConfigState("alert_notified_" + now.Format("2006-01")); err == nil && v != "" {
		notified, _ = strconv.Atoi(v)
	}
	recentAlerts, err := m.store.RecentAlerts(5)
	if err != nil {
		return spendMsg{err: err}
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

	msg := spendMsg{
		today: today, month: month, projected: projected, daily: daily, hourly: hourly,
		tokens24h: tokens24h, topModels: usage, cacheSaved: saved, cacheExtra: extra,
		notified: notified, recentAlerts: recentAlerts,
	}
	// Live session data comes from the in-process recorder (all-in-one mode) or,
	// when detached, from a running proxy's /stats endpoint (viewer mode).
	var stats *proxy.Stats
	if m.recorder != nil {
		s := m.recorder.Snapshot(8)
		stats = &s
	} else if m.statsURL != "" {
		if s, err := fetchStats(m.statsURL); err == nil {
			stats = &s
		}
	}
	if stats != nil {
		feed := stats.Recent
		if len(feed) > 8 {
			feed = feed[:8]
		}
		msg.feed = feed
		msg.limits = stats.Limits
		msg.sessReq = stats.Requests
		msg.sessCost = stats.SessionCost
		msg.sessStart = stats.Started
	}
	return msg
}

// fetchStats pulls the live session snapshot from a running proxy's /stats.
func fetchStats(url string) (proxy.Stats, error) {
	client := &http.Client{Timeout: 800 * time.Millisecond}
	resp, err := client.Get(url)
	if err != nil {
		return proxy.Stats{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return proxy.Stats{}, fmt.Errorf("stats: %s", resp.Status)
	}
	var s proxy.Stats
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		return proxy.Stats{}, err
	}
	return s, nil
}

func tick(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg { return tickMsg{} })
}

func (m model) Init() tea.Cmd {
	if m.store != nil {
		return tick(0) // fire the first refresh immediately, then heartbeat on tick
	}
	return nil
}

// --- Update ---

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if ws, ok := msg.(tea.WindowSizeMsg); ok {
		m.width = ws.Width
		m.height = ws.Height
	}
	return m.updateDashboard(msg)
}

func (m model) updateDashboard(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "r":
			if m.loading {
				return m, nil // a refresh is already running
			}
			m.loading = true
			return m, m.loadSpend
		case "1":
			m.tab = tabLive
		case "2":
			m.tab = tabHistory
		case "3":
			m.tab = tabAlerts
		case "tab", "down", "j":
			m.tab = (m.tab + 1) % 3
		case "shift+tab", "up", "k":
			m.tab = (m.tab + 2) % 3
		}
	case spendMsg:
		m.loading = false
		if msg.err != nil {
			m.spendErr = msg.err.Error()
		} else {
			m.spendErr = ""
			m.today = msg.today
			m.month = msg.month
			m.projected = msg.projected
			m.daily = msg.daily
			m.hourly = msg.hourly
			m.tokens24h = msg.tokens24h
			m.topModels = msg.topModels
			m.cacheSaved = msg.cacheSaved
			m.cacheExtra = msg.cacheExtra
			m.notified = msg.notified
			m.recentAlerts = msg.recentAlerts
			m.feed = msg.feed
			m.limits = msg.limits
			m.sessReq = msg.sessReq
			m.sessCost = msg.sessCost
			m.sessStart = msg.sessStart
			m.updatedAt = time.Now()
		}
	case tickMsg:
		// Steady heartbeat: always re-arm, but only issue a load when the previous
		// one has finished — so a slow /stats fetch can't stack up overlapping refreshes.
		if m.loading {
			return m, tick(m.refresh)
		}
		m.loading = true
		return m, tea.Batch(m.loadSpend, tick(m.refresh))
	}
	return m, nil
}

// --- View ---

func (m model) View() string {
	return m.viewDashboard()
}

func (m model) viewDashboard() string {
	width := m.width
	if width < 72 {
		width = 92 // sensible default before the first WindowSizeMsg (and in tests)
	}

	sidebarContent := m.renderSidebar()

	// Right panel fills the rest. Each panel's border adds 2, so with a
	// fixed 24-wide sidebar (→ 26 rendered) mainInner = width - 26 - 2.
	mainInner := width - 28
	if mainInner < 36 {
		mainInner = 36
	}
	var content string
	switch m.tab {
	case tabHistory:
		content = m.renderHistory(mainInner)
	case tabAlerts:
		content = m.renderAlerts(mainInner)
	default:
		content = m.renderLive(mainInner)
	}

	// Give both panels the same inner height so their bottom borders align and
	// (when the terminal size is known) they fill the screen like a real app.
	innerH := lipgloss.Height(sidebarContent)
	if h := lipgloss.Height(content); h > innerH {
		innerH = h
	}
	if m.height > 0 && m.height-4 > innerH {
		innerH = m.height - 4 // header + footer + top/bottom border
	}

	sidebar := panelStyle.Width(24).Height(innerH).Render(sidebarContent)
	main := panelStyle.Width(mainInner).Height(innerH).Render(content)
	body := lipgloss.JoinHorizontal(lipgloss.Top, sidebar, main)

	header := brandStyle.Render("● candor")
	if hint := m.headerHint(); hint != "" {
		header += "  " + dimStyle.Render(hint)
	}

	footer := dimStyle.Render("Tab ↑/↓ switch · 1·2·3 jump · r refresh · q quit")

	return header + "\n" + body + "\n" + footer + "\n"
}

// headerHint reports how fresh the on-screen figures are.
func (m model) headerHint() string {
	if m.updatedAt.IsZero() {
		return ""
	}
	secs := int(time.Since(m.updatedAt).Seconds())
	if secs <= 0 {
		return "updated just now"
	}
	return fmt.Sprintf("updated %ds ago", secs)
}

// renderSidebar builds the persistent left column: navigation, at-a-glance
// spend, this-session burn rate, and proxy status.
func (m model) renderSidebar() string {
	var b strings.Builder

	// Navigation — the active tab is highlighted with a caret so it reads as nav.
	for i, n := range []string{"Live", "History", "Alerts"} {
		if tab(i) == m.tab {
			fmt.Fprintf(&b, "%s\n", activeNav.Render(fmt.Sprintf("▸ %-8s", n)))
		} else {
			fmt.Fprintf(&b, "%s\n", inactiveNav.Render(fmt.Sprintf("  %-8s", n)))
		}
	}
	rule := faintStyle.Render(strings.Repeat("─", 20))

	// At a glance.
	fmt.Fprintf(&b, "%s\n%s\n", rule, sectionHeader.Render("At a glance"))
	fmt.Fprintf(&b, "Today     %s\n", money(m.today))
	budget := m.cfg.Defaults.MonthlyBudgetUSD
	if budget > 0 {
		fmt.Fprintf(&b, "Month     %s\n", money(m.month))
		fmt.Fprintf(&b, "%s\n", budgetBar(m.month/budget*100, 14))
	} else {
		fmt.Fprintf(&b, "Month     %s\n", money(m.month))
	}
	fmt.Fprintf(&b, "Projected %s\n", money(m.projected))

	// This session (from the in-process proxy recorder).
	fmt.Fprintf(&b, "%s\n%s\n", rule, sectionHeader.Render("This session"))
	fmt.Fprintf(&b, "Spend     %s\n", money(m.sessCost))
	fmt.Fprintf(&b, "Requests  %d\n", m.sessReq)
	fmt.Fprintf(&b, "Burn      %s/hr\n", money(m.burnPerHour()))

	// Proxy status.
	b.WriteString(rule + "\n")
	if m.cfg.Proxy.Enabled {
		fmt.Fprintf(&b, "%s Proxy on\n%s",
			statusDot(true), dimStyle.Render(app.ProxyListen(m.cfg)))
	} else {
		fmt.Fprintf(&b, "%s Proxy off", statusDot(false))
	}
	return b.String()
}

func (m model) renderLive(width int) string {
	var b strings.Builder

	// --- 24h trend sparkline ---
	trend := m.sparkline()
	if m.tokens24h > 0 {
		trend += dimStyle.Render("  ·  " + fmtTokens(m.tokens24h) + " tokens")
	}
	fmt.Fprintf(&b, "%s\n  %s\n", sectionHeader.Render("24h trend"), trend)

	// --- Live activity feed ---
	fmt.Fprintf(&b, "\n%s\n", sectionHeader.Render("Live activity"))
	if len(m.feed) == 0 {
		fmt.Fprintf(&b, "  %s\n", dimStyle.Render("waiting for requests…"))
	} else {
		for _, e := range m.feed {
			name := e.Model
			if len(name) > 18 {
				name = name[:17] + "…"
			}
			// Pad provider tag with plain spaces so ANSI color codes don't skew
			// column alignment.
			prov := providerTag(e.Provider)
			if pad := 10 - len(e.Provider); pad > 0 {
				prov += strings.Repeat(" ", pad)
			}
			tokens := e.Input + e.Cached + e.CacheWrite + e.Output
			fmt.Fprintf(&b, "  %s  %s  %-18s %s  %s\n",
				dimStyle.Render(e.At.Format("15:04:05")), prov, name,
				dimStyle.Render(fmt.Sprintf("%7s tok", fmtTokens(tokens))), moneyFine(e.CostUSD))
		}
	}

	// --- Top models (this month) ---
	if len(m.topModels) > 0 {
		fmt.Fprintf(&b, "\n%s\n", sectionHeader.Render("Top models (this month)"))
		maxCost := m.topModels[0].CostUSD // rows are cost-desc
		for i, u := range m.topModels {
			if i >= 5 {
				break
			}
			name := u.Provider + "/" + u.Model
			if len(name) > 28 {
				name = name[:27] + "…"
			}
			bars := 0
			if maxCost > 0 {
				bars = int(u.CostUSD / maxCost * 12)
			}
			perM := 0.0
			if tot := u.Input + u.Cached + u.CacheWrite + u.Output; tot > 0 {
				perM = u.CostUSD / float64(tot) * 1_000_000
			}
			bar := lipgloss.NewStyle().Foreground(clrAccent).Render(strings.Repeat("▓", bars))
			fmt.Fprintf(&b, "  %-28s $%7.2f  %-12s $%.2f/M\n", name, u.CostUSD, bar, perM)
		}
	}

	// --- Cache impact (this month) ---
	if m.engine != nil && (m.cacheSaved > 0 || m.cacheExtra > 0) {
		fmt.Fprintf(&b, "\n%s\n", sectionHeader.Render("Cache impact (this month)"))
		fmt.Fprintf(&b, "  Saved via cache reads:   %s\n",
			lipgloss.NewStyle().Foreground(clrGreen).Render(money(m.cacheSaved)))
		fmt.Fprintf(&b, "  Extra via cache writes:  %s\n", money(m.cacheExtra))
		net := m.cacheExtra - m.cacheSaved
		netColor := clrGreen // net saving (negative) is good
		if net > 0 {
			netColor = clrYellow
		}
		fmt.Fprintf(&b, "  Net cache effect:        %s\n",
			lipgloss.NewStyle().Foreground(netColor).Render(fmt.Sprintf("%+.2f", net)))
	}

	// --- Rate limits (provider plan / per-minute windows) ---
	if len(m.limits) > 0 {
		fmt.Fprintf(&b, "\n%s\n", sectionHeader.Render("Rate limits"))
		for _, lm := range m.limits {
			for _, wnd := range lm.Windows {
				label := fmt.Sprintf("%s %s", lm.Provider, wnd.Label)
				line := fmt.Sprintf("  %-20s ", label)
				switch {
				case wnd.Utilization >= 0:
					line += budgetBar(wnd.Utilization, 12)
				case wnd.Remaining >= 0:
					line += fmt.Sprintf("%d left", wnd.Remaining)
				default:
					line += dimStyle.Render("—")
				}
				if !wnd.Reset.IsZero() {
					if d := time.Until(wnd.Reset); d > 0 {
						line += dimStyle.Render("  · resets in " + shortDur(d))
					}
				}
				fmt.Fprintf(&b, "%s\n", line)
			}
		}
	}

	// --- Empty state ---
	if m.today == 0 && m.month == 0 && len(m.daily) == 0 && len(m.feed) == 0 {
		fmt.Fprintf(&b, "\n%s\n", dimStyle.Render(
			"No usage yet — point a harness at the proxy and spend appears here."))
	}
	return strings.TrimRight(b.String(), "\n")
}

// shortDur formats a duration compactly for reset countdowns: 2h13m, 6m, 45s.
func shortDur(d time.Duration) string {
	// Sub-minute durations round to "<1m" — check before rounding so 30s doesn't
	// round up to a misleading "1m".
	if d < time.Minute {
		return "<1m"
	}
	d = d.Round(time.Minute)
	h := d / time.Hour
	m := (d % time.Hour) / time.Minute
	switch {
	case h > 0 && m > 0:
		return fmt.Sprintf("%dh%dm", h, m)
	case h > 0:
		return fmt.Sprintf("%dh", h)
	default:
		return fmt.Sprintf("%dm", m)
	}
}

// sparkline renders the last-24h hourly cost trend as a compact block chart.
func (m model) sparkline() string {
	vals := make([]float64, 0, len(m.hourly))
	for _, h := range m.hourly {
		vals = append(vals, h.CostUSD)
	}
	if len(vals) == 0 {
		return dimStyle.Render("no activity in the last 24h")
	}
	if len(vals) > 24 {
		vals = vals[len(vals)-24:]
	}
	maxV := 0.0
	for _, v := range vals {
		if v > maxV {
			maxV = v
		}
	}
	var total float64
	var sb strings.Builder
	for _, v := range vals {
		total += v
		level := 0
		if maxV > 0 {
			level = int(v / maxV * float64(len(sparkLevels)-1))
		}
		sb.WriteRune(sparkLevels[level])
	}
	spark := lipgloss.NewStyle().Foreground(clrAccent).Render(sb.String())
	return fmt.Sprintf("%s  %s total", spark, money(total))
}

func (m model) renderHistory(width int) string {
	if len(m.daily) == 0 {
		return dimStyle.Render("No usage recorded in the last 30 days.")
	}
	maxCost := 0.0
	for _, d := range m.daily {
		if d.CostUSD > maxCost {
			maxCost = d.CostUSD
		}
	}

	barW := width - 18
	if barW < 10 {
		barW = 10
	}
	barStyle := lipgloss.NewStyle().Foreground(clrAccent)

	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", sectionHeader.Render("Daily cost — last 30 days"))
	for _, d := range m.daily {
		bars := 0
		if maxCost > 0 {
			bars = int(d.CostUSD / maxCost * float64(barW))
		}
		// Show the MM-DD suffix of the YYYY-MM-DD day string.
		label := d.Day
		if len(label) >= 5 {
			label = label[len(label)-5:]
		}
		fmt.Fprintf(&b, "%s  %s %7.2f\n", label, barStyle.Render(strings.Repeat("▓", bars)), d.CostUSD)
	}
	return strings.TrimRight(b.String(), "\n")
}

func (m model) renderAlerts(width int) string {
	var b strings.Builder
	budget := m.cfg.Defaults.MonthlyBudgetUSD
	if budget <= 0 {
		return dimStyle.Render(
			"No monthly budget configured (defaults.monthly_budget_usd).\n" +
				"Set one in config.yaml to enable alerts.")
	}

	pct := m.projected / budget * 100
	fmt.Fprintf(&b, "Monthly budget:  $%.0f\n", budget)
	fmt.Fprintf(&b, "Projected spend: $%.2f (%.0f%% of budget)\n", m.projected, pct)
	fmt.Fprintf(&b, "%s\n\n", budgetBar(pct, 24))
	b.WriteString("Thresholds:\n")

	thresholds := m.cfg.Defaults.AlertThresholds
	if len(thresholds) == 0 {
		b.WriteString("  (none configured)")
		return b.String()
	}
	for _, t := range thresholds {
		mark, note, color := "○", "not yet", clrDim
		if int(pct) >= t {
			mark, note, color = "✓", "crossed", clrYellow
		}
		if t <= m.notified {
			note += ", notified"
		}
		fmt.Fprintf(&b, "  %s  %3d%%   %s\n",
			lipgloss.NewStyle().Foreground(color).Render(mark), t,
			lipgloss.NewStyle().Foreground(color).Render(note))
	}

	// History of notifications actually fired.
	if len(m.recentAlerts) > 0 {
		fmt.Fprintf(&b, "\n%s\n", sectionHeader.Render("Recent alerts"))
		for _, a := range m.recentAlerts {
			fmt.Fprintf(&b, "  %s  %s  $%.0f / $%.0f\n",
				dimStyle.Render(a.FiredAt.Local().Format("Jan 02 15:04")),
				lipgloss.NewStyle().Foreground(clrYellow).Render(fmt.Sprintf("%3d%%", a.ThresholdPct)),
				a.ProjectedUSD, a.BudgetUSD)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// burnPerHour extrapolates this session's spend to an hourly rate.
func (m model) burnPerHour() float64 {
	if m.sessStart.IsZero() {
		return 0
	}
	h := time.Since(m.sessStart).Hours()
	if h < 1.0/60 { // < 1 minute in: too little signal to extrapolate
		return 0
	}
	return m.sessCost / h
}

func money(v float64) string { return fmt.Sprintf("$%.2f", v) }

// moneyFine formats a cost with enough precision to be non-zero for small
// per-request amounts (many requests cost a fraction of a cent), while keeping
// cent precision for larger figures.
func moneyFine(v float64) string {
	switch {
	case v >= 0.01 || v <= 0:
		return fmt.Sprintf("$%.2f", v)
	case v >= 0.0001:
		return fmt.Sprintf("$%.4f", v)
	default:
		return fmt.Sprintf("$%.6f", v)
	}
}

// fmtTokens renders a token count compactly: 1.2M, 15.3k, 420.
func fmtTokens(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	default:
		return fmt.Sprintf("%d", n)
	}
}

// statusDot returns a colored ● — green when on, red when off.
func statusDot(on bool) string {
	// Distinguish by glyph as well as color: a filled dot when on, hollow when
	// off, so the state is still legible in a no-color terminal (and colorblind-safe).
	c, glyph := clrRed, "○"
	if on {
		c, glyph = clrGreen, "●"
	}
	return lipgloss.NewStyle().Foreground(c).Render(glyph)
}

// budgetBar renders a color-graded bar (green→yellow→red) for a 0-100+ percentage.
func budgetBar(pct float64, width int) string {
	// Clamp only the bar fill to [0,100]; the label still shows the true pct
	// (e.g. a negative value renders an empty bar but keeps its "-10%" label).
	shown := pct
	if shown < 0 {
		shown = 0
	}
	if shown > 100 {
		shown = 100
	}
	filled := int(shown / 100 * float64(width))
	if filled > width {
		filled = width
	}
	color := clrGreen
	switch {
	case pct >= 90:
		color = clrRed
	case pct >= 50:
		color = clrYellow
	}
	bar := lipgloss.NewStyle().Foreground(color).Render(strings.Repeat("█", filled)) +
		faintStyle.Render(strings.Repeat("░", width-filled))
	return fmt.Sprintf("%s %.0f%%", bar, pct)
}
