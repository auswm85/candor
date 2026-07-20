package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/auswm85/candor/internal/app"
	"github.com/auswm85/candor/internal/config"
	"github.com/auswm85/candor/internal/lock"
	"github.com/auswm85/candor/internal/store"
	"github.com/auswm85/candor/internal/tui"
	"github.com/spf13/cobra"
)

// alertInterval is how often the daemon/proxy re-projects monthly spend to fire
// budget-threshold notifications.
const alertInterval = time.Minute

// Build metadata, injected at release time via -ldflags "-X main.version=… -X
// main.commit=… -X main.date=…". Defaults describe a plain `go build`/`go install`.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

var rootCmd = &cobra.Command{
	Use:   "candor",
	Short: "candor — local-first, live LLM cost tracking via a transparent proxy",
	Long: `candor records live per-request LLM spend by sitting in front of a coding
harness as a transparent proxy. Run it with no arguments to open the dashboard;
see the subcommands for the proxy, a per-run wrapper, and one-off queries.`,
	Version: version,
	RunE:    runDashboard,
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print version, commit, and build date",
	RunE: func(cmd *cobra.Command, args []string) error {
		fmt.Printf("candor %s (commit %s, built %s)\n", version, commit, date)
		return nil
	},
}

// openStore loads config and opens+migrates the database — the common prelude
// for every command.
func openStore() (*config.Config, *store.Store, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, nil, fmt.Errorf("config: %w", err)
	}
	st, err := store.Open(cfg.Database)
	if err != nil {
		return nil, nil, fmt.Errorf("open store: %w", err)
	}
	if _, err := st.Migrate(); err != nil {
		_ = st.Close()
		return nil, nil, fmt.Errorf("migrate: %w", err)
	}
	return cfg, st, nil
}

// runDashboard is the default (no-subcommand) action: open the full-screen
// dashboard, and — unless a proxy is already running — host the live proxy
// alongside it.
func runDashboard(cmd *cobra.Command, args []string) error {
	cfg, st, err := openStore()
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	// Single-instance: prevent a second dashboard from fighting for the port.
	lk, err := lock.Acquire(filepath.Join(filepath.Dir(cfg.Database), "daemon.lock"))
	if err != nil {
		if errors.Is(err, lock.ErrLocked) {
			return errors.New("another candor dashboard is already running")
		}
		return fmt.Errorf("acquire lock: %w", err)
	}
	defer func() { _ = lk.Release() }()

	// The bubbletea TUI owns the terminal, so redirect log output to a file next
	// to the database instead of letting it corrupt the screen.
	logPath := filepath.Join(filepath.Dir(cfg.Database), "daemon.log")
	rotateLog(logPath)
	if lf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
		_ = os.Chmod(logPath, 0o600)
		defer func() { _ = lf.Close() }()
		log.SetOutput(lf)
	} else {
		// Can't open the log file — discard logs rather than let them corrupt
		// the bubbletea-owned terminal via stderr.
		log.SetOutput(io.Discard)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)

	listen := app.ProxyListen(cfg)
	engine := app.BuildEngine(cfg) // one price load, shared by the TUI and recorder
	m := tui.NewModel(cfg).WithStore(st).WithEngine(engine)

	// Host the live proxy alongside the dashboard — unless one is already running
	// (e.g. a background service), in which case attach as a viewer via /stats.
	var proxySrv *http.Server
	switch {
	case !cfg.Proxy.Enabled:
		// proxy disabled: dashboard shows persisted data only
	case app.ProxyHealthy(listen, 300*time.Millisecond):
		log.Printf("proxy already running at %s; dashboard attaching as viewer", listen)
		m = m.WithStatsURL("http://" + listen + "/stats")
	default:
		if err := app.ValidateProxyListen(listen, cfg.Proxy.AllowNonLoopback); err != nil {
			return fmt.Errorf("proxy: %w", err)
		}
		if err := app.ValidateUpstreams(app.ProxyUpstreams(cfg)); err != nil {
			return fmt.Errorf("proxy: %w", err)
		}
		// Bind synchronously so a port-in-use error is surfaced to the user
		// instead of the dashboard silently coming up without a proxy.
		ln, err := net.Listen("tcp", listen)
		if err != nil {
			return fmt.Errorf("proxy listen on %s: %w", listen, err)
		}
		rec := app.BuildRecorder(st, engine)
		m = m.WithRecorder(rec)
		proxySrv = newProxyServer(listen, app.BuildProxy(cfg, rec))
		go func() {
			log.Printf("proxy listening on http://%s", listen)
			if err := proxySrv.Serve(ln); err != nil && err != http.ErrServerClosed {
				log.Printf("proxy: %v", err)
			}
		}()
	}

	app.StartAlertLoop(ctx, cfg, st, alertInterval)

	p := tui.NewProgram(m)
	go func() {
		<-sig
		cancel()
		p.Quit()
	}()
	_, runErr := p.Run()

	// Tear down in order: stop background workers, then drain the proxy so any
	// in-flight request finishes before the deferred store close — otherwise a
	// request landing mid-shutdown would write to a closing DB.
	cancel()
	if proxySrv != nil {
		shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
		if err := proxySrv.Shutdown(shutdownCtx); err != nil {
			log.Printf("proxy shutdown: %v", err)
		}
		cancelShutdown()
	}
	if runErr != nil {
		return fmt.Errorf("tui: %w", runErr)
	}
	return nil
}

var proxyCmd = &cobra.Command{
	Use:   "proxy",
	Short: "Run the live-usage proxy headless (for a background service)",
	Long: `Start the transparent reverse proxy without a dashboard. Point a tool's base
URL at it, using the provider name as the first path segment:

  OpenAI:      http://127.0.0.1:7879/openai/v1
  OpenRouter:  http://127.0.0.1:7879/openrouter/api/v1
  Anthropic:   http://127.0.0.1:7879/anthropic

Your normal inference key is forwarded untouched — no admin key needed.
Also fires budget-threshold notifications while it runs.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, st, err := openStore()
		if err != nil {
			return err
		}
		defer func() { _ = st.Close() }()

		listen := app.ProxyListen(cfg)
		if err := app.ValidateProxyListen(listen, cfg.Proxy.AllowNonLoopback); err != nil {
			return fmt.Errorf("proxy: %w", err)
		}
		if err := app.ValidateUpstreams(app.ProxyUpstreams(cfg)); err != nil {
			return fmt.Errorf("proxy: %w", err)
		}
		srv := newProxyServer(listen, app.BuildProxy(cfg, app.BuildRecorder(st, app.BuildEngine(cfg))))

		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		app.StartAlertLoop(ctx, cfg, st, alertInterval)
		go shutdownOnDone(ctx, srv)

		fmt.Printf("candor proxy listening on http://%s\n", listen)
		for name := range app.ProxyUpstreams(cfg) {
			fmt.Printf("  %-10s → set base URL to http://%s/%s/...\n", name, listen, name)
		}
		fmt.Println("Press Ctrl-C to stop.")
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			return err
		}
		return nil
	},
}

var runCmd = &cobra.Command{
	Use:   "run [--provider name]... -- <command> [args...]",
	Short: "Run a harness with its LLM traffic routed through the proxy (nothing persistent)",
	Long: `Run a coding harness (Claude Code, OpenCode, …) with its provider base-URL
env vars pointed at the local proxy for that child process ONLY. Usage is
tracked without changing anything globally — your normal ` + "`claude`" + ` invocation
still goes straight to the provider, untouched.

If the proxy isn't reachable the command still runs (straight to the provider);
usage just isn't recorded, so it never breaks your workflow.

  candor run -- claude
  candor run --provider anthropic -- claude
  candor run -- opencode`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		providers, _ := cmd.Flags().GetStringSlice("provider")
		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("config: %w", err)
		}
		listen := app.ProxyListen(cfg)

		name, rest := args[0], args[1:]
		env := os.Environ()
		if app.ProxyHealthy(listen, 500*time.Millisecond) {
			env = append(env, app.ProxyChildEnv(cfg, listen, providers)...)
			// OpenCode ignores base-URL env vars for its OpenRouter/Anthropic
			// providers, so route it via OPENCODE_CONFIG_CONTENT instead — a
			// highest-precedence, transient config override (merged over the
			// user's config; nothing persistent).
			if filepath.Base(name) == "opencode" {
				if content, err := app.OpenCodeConfigContent(cfg, listen, providers); err == nil {
					env = append(env, "OPENCODE_CONFIG_CONTENT="+content)
				}
			}
			fmt.Fprintf(os.Stderr, "▸ routing %s through the proxy at http://%s (usage tracked)\n", name, listen)
		} else {
			fmt.Fprintf(os.Stderr, "⚠ proxy not reachable at http://%s — running %s directly; usage will NOT be tracked.\n  start it with `candor` or `candor proxy`.\n", listen, name)
		}

		// Catch terminal signals so this wrapper doesn't die before the child;
		// the child (foreground, same process group) still receives them and
		// handles Ctrl-C itself.
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
		defer signal.Stop(sigCh)
		go func() {
			for range sigCh {
			}
		}()

		child := exec.Command(name, rest...)
		child.Stdin, child.Stdout, child.Stderr = os.Stdin, os.Stdout, os.Stderr
		child.Env = env
		if err := child.Run(); err != nil {
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				os.Exit(ee.ExitCode())
			}
			return fmt.Errorf("run %s: %w", name, err)
		}
		return nil
	},
}

var tuiCmd = &cobra.Command{
	Use:   "tui",
	Short: "Open the dashboard as a read-only viewer attached to a running proxy",
	Long: `Open the live dashboard without starting a proxy. It reads persisted spend
from the database and pulls the live activity feed + session burn rate from a
running proxy's /stats endpoint (start one with ` + "`candor`" + ` or ` + "`candor proxy`" + `).
Safe to run in a separate shell alongside the proxy.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, st, err := openStore()
		if err != nil {
			return err
		}
		defer func() { _ = st.Close() }()
		statsURL := "http://" + app.ProxyListen(cfg) + "/stats"
		m := tui.NewModel(cfg).WithStore(st).WithEngine(app.BuildEngine(cfg)).WithStatsURL(statsURL)
		if _, err := tui.NewProgram(m).Run(); err != nil {
			return fmt.Errorf("tui: %w", err)
		}
		return nil
	},
}

var spendCmd = &cobra.Command{
	Use:   "spend [today|month]",
	Short: "Show recorded LLM spend",
	Long: `Print recorded spend for a period.

Examples:
  candor spend today             Today's total spend
  candor spend month             This month's total spend
  candor spend month --by-model  This month's spend, broken down by model`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		period := "today"
		if len(args) > 0 {
			period = strings.ToLower(args[0])
		}
		now := time.Now()
		var since time.Time
		switch period {
		case "today":
			since = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
		case "month":
			since = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
		default:
			return fmt.Errorf("unknown period %q (use 'today' or 'month')", period)
		}

		_, st, err := openStore()
		if err != nil {
			return err
		}
		defer func() { _ = st.Close() }()

		if byModel, _ := cmd.Flags().GetBool("by-model"); byModel {
			rows, err := st.CostByModelSince(since)
			if err != nil {
				return fmt.Errorf("query: %w", err)
			}
			if len(rows) == 0 {
				fmt.Printf("No usage recorded since %s.\n", since.Format("2006-01-02"))
				return nil
			}
			var total float64
			for _, r := range rows {
				fmt.Printf("  %-32s $%8.2f\n", r.Provider+"/"+r.Model, r.CostUSD)
				total += r.CostUSD
			}
			fmt.Printf("  %-32s $%8.2f\n", "TOTAL", total)
			return nil
		}

		total, err := st.TotalCostSince(since)
		if err != nil {
			return fmt.Errorf("query: %w", err)
		}
		label := strings.ToUpper(period[:1]) + period[1:]
		fmt.Printf("%s spend: $%.2f\n", label, total)
		return nil
	},
}

var exportCmd = &cobra.Command{
	Use:   "export --since <YYYY-MM-DD> [--until <YYYY-MM-DD>] [--format csv|json]",
	Short: "Export raw usage rows to stdout (CSV or JSON)",
	Long: `Dump recorded usage rows for analysis or reimbursement.

  candor export --since 2026-01-01 > jan.csv
  candor export --since 2026-01-01 --until 2026-01-31 --format json

--until is inclusive (covers the whole day); omit it to export up to now.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		sinceStr, _ := cmd.Flags().GetString("since")
		untilStr, _ := cmd.Flags().GetString("until")
		format, _ := cmd.Flags().GetString("format")
		if sinceStr == "" {
			return errors.New("--since is required (YYYY-MM-DD)")
		}
		if format != "csv" && format != "json" {
			return fmt.Errorf("unknown --format %q (use csv or json)", format)
		}
		since, err := time.ParseInLocation("2006-01-02", sinceStr, time.Local)
		if err != nil {
			return fmt.Errorf("parse --since: %w", err)
		}
		var until time.Time
		if untilStr != "" {
			u, err := time.ParseInLocation("2006-01-02", untilStr, time.Local)
			if err != nil {
				return fmt.Errorf("parse --until: %w", err)
			}
			until = u.AddDate(0, 0, 1) // inclusive of the --until day
		}

		_, st, err := openStore()
		if err != nil {
			return err
		}
		defer func() { _ = st.Close() }()

		rows, err := st.ExportRows(since, until)
		if err != nil {
			return fmt.Errorf("export: %w", err)
		}
		if format == "json" {
			return writeExportJSON(os.Stdout, rows)
		}
		return writeExportCSV(os.Stdout, rows)
	},
}

func writeExportCSV(w io.Writer, rows []store.ExportRow) error {
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"bucket_start", "provider", "model", "input", "cache_read", "cache_write", "output", "cost_usd"})
	for _, r := range rows {
		_ = cw.Write([]string{
			r.BucketStart.UTC().Format(time.RFC3339),
			r.Provider, r.Model,
			strconv.FormatInt(r.Input, 10),
			strconv.FormatInt(r.CacheRead, 10),
			strconv.FormatInt(r.CacheWrite, 10),
			strconv.FormatInt(r.Output, 10),
			strconv.FormatFloat(r.CostUSD, 'f', 6, 64),
		})
	}
	cw.Flush()
	return cw.Error()
}

func writeExportJSON(w io.Writer, rows []store.ExportRow) error {
	type tokens struct {
		Input      int64 `json:"input"`
		CacheRead  int64 `json:"cache_read"`
		CacheWrite int64 `json:"cache_write"`
		Output     int64 `json:"output"`
	}
	type rec struct {
		BucketStart string  `json:"bucket_start"`
		Provider    string  `json:"provider"`
		Model       string  `json:"model"`
		Tokens      tokens  `json:"tokens"`
		CostUSD     float64 `json:"cost_usd"`
	}
	out := make([]rec, 0, len(rows))
	for _, r := range rows {
		out = append(out, rec{
			BucketStart: r.BucketStart.UTC().Format(time.RFC3339),
			Provider:    r.Provider,
			Model:       r.Model,
			Tokens:      tokens{r.Input, r.CacheRead, r.CacheWrite, r.Output},
			CostUSD:     r.CostUSD,
		})
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show database size, spend, and whether the proxy is running",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, st, err := openStore()
		if err != nil {
			return err
		}
		defer func() { _ = st.Close() }()

		size := "n/a"
		if fi, err := os.Stat(cfg.Database); err == nil {
			size = humanBytes(fi.Size())
		}
		now := time.Now()
		dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
		monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
		today, err := st.TotalCostSince(dayStart)
		if err != nil {
			return fmt.Errorf("query today's spend: %w", err)
		}
		month, err := st.TotalCostSince(monthStart)
		if err != nil {
			return fmt.Errorf("query month's spend: %w", err)
		}

		projected := app.ProjectMonthValue(month, now)
		budget := cfg.Defaults.MonthlyBudgetUSD

		listen := app.ProxyListen(cfg)
		proxyUp := app.ProxyHealthy(listen, 300*time.Millisecond)

		if asJSON, _ := cmd.Flags().GetBool("json"); asJSON {
			rep := map[string]any{
				"database":      cfg.Database,
				"db_size_bytes": dbSize(cfg.Database),
				"proxy_up":      proxyUp,
				"today_usd":     today,
				"month_usd":     month,
				"projected_usd": projected,
			}
			if proxyUp {
				rep["proxy_url"] = "http://" + listen
			}
			if budget > 0 {
				rep["budget_usd"] = budget
				rep["budget_used_pct"] = projected / budget * 100
			}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(rep)
		}

		proxyState := "not running"
		if proxyUp {
			proxyState = "running at http://" + listen
		}
		fmt.Printf("Database:    %s (%s)\n", cfg.Database, size)
		fmt.Printf("Proxy:       %s\n", proxyState)
		fmt.Printf("Today:       $%.2f\n", today)
		fmt.Printf("This month:  $%.2f\n", month)
		if budget > 0 {
			fmt.Printf("Projected:   $%.2f  (%.0f%% of $%.0f budget)\n", projected, projected/budget*100, budget)
		} else {
			fmt.Printf("Projected:   $%.2f\n", projected)
		}
		return nil
	},
}

const (
	logMaxBytes = 10 << 20 // rotate daemon.log once it passes 10 MiB
	logKeep     = 3        // keep daemon.log.1 .. .3
)

// rotateLog rotates path (daemon.log → .1 → .2 → .3, oldest dropped) when it has
// grown past logMaxBytes. Called at dashboard startup; candor's log volume is
// low enough that per-launch rotation keeps the file bounded.
func rotateLog(path string) { rotateLogAt(path, logMaxBytes) }

func rotateLogAt(path string, maxBytes int64) {
	fi, err := os.Stat(path)
	if err != nil || fi.Size() < maxBytes {
		return
	}
	_ = os.Remove(fmt.Sprintf("%s.%d", path, logKeep))
	for i := logKeep - 1; i >= 1; i-- {
		_ = os.Rename(fmt.Sprintf("%s.%d", path, i), fmt.Sprintf("%s.%d", path, i+1))
	}
	_ = os.Rename(path, path+".1")
}

// dbSize returns the database file size in bytes, or 0 if unavailable.
func dbSize(path string) int64 {
	if fi, err := os.Stat(path); err == nil {
		return fi.Size()
	}
	return 0
}

var migrateCmd = &cobra.Command{
	Use:   "migrate",
	Short: "Apply pending database migrations",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("config: %w", err)
		}
		st, err := store.Open(cfg.Database)
		if err != nil {
			return fmt.Errorf("open store: %w", err)
		}
		defer func() { _ = st.Close() }()
		applied, err := st.Migrate()
		if err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
		if applied == 0 {
			fmt.Printf("Already up to date. Database: %s\n", cfg.Database)
		} else {
			fmt.Printf("%d migration(s) applied. Database: %s\n", applied, cfg.Database)
		}
		return nil
	},
}

var serviceCmd = &cobra.Command{
	Use:   "service",
	Short: "Print an OS service unit (launchd/systemd) that runs the proxy",
	Long: `Print a service definition that runs 'candor proxy' at login/boot.

Redirect it into the right location, for example:
  macOS:  candor service > ~/Library/LaunchAgents/dev.candor.plist
  Linux:  candor service > ~/.config/systemd/user/candor.service`,
	RunE: func(cmd *cobra.Command, args []string) error {
		exe, err := os.Executable()
		if err != nil {
			exe = "candor"
		}
		switch runtime.GOOS {
		case "darwin":
			fmt.Printf(launchdPlist, exe)
		case "linux":
			fmt.Printf(systemdUnit, exe)
		default:
			return fmt.Errorf("service generation not supported on %s", runtime.GOOS)
		}
		return nil
	},
}

// newProxyServer builds the http.Server used for the live proxy. No WriteTimeout:
// streamed responses can run for many minutes.
func newProxyServer(addr string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20, // 1 MiB
	}
}

// shutdownOnDone gracefully stops srv when ctx is cancelled.
func shutdownOnDone(ctx context.Context, srv *http.Server) {
	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("proxy shutdown: %v", err)
	}
}

const launchdPlist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>dev.candor</string>
    <key>ProgramArguments</key>
    <array>
        <string>%s</string>
        <string>proxy</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
</dict>
</plist>
`

const systemdUnit = `[Unit]
Description=candor LLM cost proxy
After=network-online.target

[Service]
ExecStart=%s proxy
Restart=on-failure

[Install]
WantedBy=default.target
`

// humanBytes formats a byte count as a short human-readable string.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}

func main() {
	// Flags before the command; everything after the first positional is the
	// child command line (so `candor run claude --foo` passes --foo to claude).
	runCmd.Flags().SetInterspersed(false)
	runCmd.Flags().StringSlice("provider", nil, "Provider base URLs to route (default: all configured); repeatable")
	spendCmd.Flags().Bool("by-model", false, "Break spend down by model")
	exportCmd.Flags().String("since", "", "Start date, inclusive (YYYY-MM-DD) — required")
	exportCmd.Flags().String("until", "", "End date, inclusive (YYYY-MM-DD); default: now")
	exportCmd.Flags().String("format", "csv", "Output format: csv or json")
	statusCmd.Flags().Bool("json", false, "Output status as JSON")

	rootCmd.AddCommand(proxyCmd, runCmd, tuiCmd, spendCmd, exportCmd, statusCmd, migrateCmd, serviceCmd, versionCmd)
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
