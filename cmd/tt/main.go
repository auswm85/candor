package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/auswm85/token-tracker/internal/app"
	"github.com/auswm85/token-tracker/internal/auth"
	"github.com/auswm85/token-tracker/internal/config"
	"github.com/auswm85/token-tracker/internal/lock"
	"github.com/auswm85/token-tracker/internal/store"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// readSecret reads a secret without echoing it when stdin is a terminal, and
// falls back to a plain line read when input is piped (scripts, tests).
func readSecret(reader *bufio.Reader) (string, error) {
	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		b, err := term.ReadPassword(fd)
		fmt.Println() // ReadPassword swallows the newline
		return strings.TrimSpace(string(b)), err
	}
	line, err := reader.ReadString('\n')
	return strings.TrimSpace(line), err
}

var rootCmd = &cobra.Command{
	Use:   "tt",
	Short: "token-tracker — local-first LLM cost monitor",
	RunE: func(cmd *cobra.Command, args []string) error {
		return cmd.Help()
	},
}

var authCmd = &cobra.Command{
	Use:   "auth [provider]",
	Short: "Configure API keys for LLM providers",
	Long: `Set or view API keys for supported LLM providers.
Keys are stored in your OS keychain.

Providers: openai, anthropic, openrouter

Examples:
  tt auth              Interactive setup for all providers
  tt auth openai       Set OpenAI API key
  tt auth --list       Show which providers are configured`,
	RunE: func(cmd *cobra.Command, args []string) error {
		list, _ := cmd.Flags().GetBool("list")
		if list {
			configured := auth.ListConfiguredProviders()
			if len(configured) == 0 {
				fmt.Println("No providers configured.")
				return nil
			}
			fmt.Println("Configured providers:")
			for _, p := range configured {
				fmt.Printf("  ✓ %s\n", p)
			}
			return nil
		}

		providers := []string{"openai", "anthropic", "openrouter"}
		if len(args) > 0 {
			providers = args
		}

		reader := bufio.NewReader(os.Stdin)
		for _, p := range providers {
			if auth.HasProviderKey(p) {
				fmt.Printf("%s: already configured. Overwrite? (y/N): ", p)
				answer, _ := reader.ReadString('\n')
				answer = strings.TrimSpace(strings.ToLower(answer))
				if answer != "y" && answer != "yes" {
					fmt.Printf("  Skipping %s.\n", p)
					continue
				}
			}
			fmt.Printf("Enter %s API key: ", p)
			key, err := readSecret(reader)
			if err != nil {
				return fmt.Errorf("read input: %w", err)
			}
			if key == "" {
				fmt.Printf("  Skipping %s (empty key).\n", p)
				continue
			}
			if err := auth.SetProviderKey(p, key); err != nil {
				return fmt.Errorf("store %s key: %w", p, err)
			}
			fmt.Printf("  ✓ %s configured.\n", p)
		}
		return nil
	},
}

var clearCmd = &cobra.Command{
	Use:   "clear [provider]",
	Short: "Clear stored API keys",
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) > 0 {
			for _, p := range args {
				if err := auth.ClearProviderKey(p); err != nil {
					return fmt.Errorf("clear %s: %w", p, err)
				}
				fmt.Printf("  Cleared %s.\n", p)
			}
			return nil
		}
		for _, p := range []string{"openai", "anthropic", "openrouter"} {
			if auth.HasProviderKey(p) {
				if err := auth.ClearProviderKey(p); err != nil {
					return fmt.Errorf("clear %s: %w", p, err)
				}
				fmt.Printf("  Cleared %s.\n", p)
			}
		}
		return nil
	},
}

var spendCmd = &cobra.Command{
	Use:   "spend [today|month]",
	Short: "Show recorded LLM spend",
	Long: `Print recorded spend for a period.

Examples:
  tt spend today             Today's total spend
  tt spend month             This month's total spend
  tt spend month --by-model  This month's spend, broken down by model`,
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

		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("config: %w", err)
		}
		st, err := store.Open(cfg.Database)
		if err != nil {
			return fmt.Errorf("open store: %w", err)
		}
		defer func() { _ = st.Close() }()
		if err := st.Migrate(); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}

		byModel, _ := cmd.Flags().GetBool("by-model")
		if byModel {
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
		if err := st.Migrate(); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
		fmt.Printf("Migrations applied. Database: %s\n", cfg.Database)
		return nil
	},
}

var daemonCmd = &cobra.Command{
	Use:   "daemon",
	Short: "Run the polling daemon in the foreground (headless, no TUI)",
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
		if err := st.Migrate(); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}

		lk, err := lock.Acquire(filepath.Join(filepath.Dir(cfg.Database), "daemon.lock"))
		if err != nil {
			if errors.Is(err, lock.ErrLocked) {
				return fmt.Errorf("another token-tracker daemon is already running")
			}
			return fmt.Errorf("acquire lock: %w", err)
		}
		defer func() { _ = lk.Release() }()

		scheduler := app.NewScheduler(cfg, st)
		if scheduler == nil {
			return fmt.Errorf("no providers configured; run `tt auth` first")
		}

		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()

		log.Printf("token-tracker daemon started (interval %s, db %s)",
			app.ParseInterval(cfg.PollInterval, 5*time.Minute), cfg.Database)
		scheduler.Start(ctx) // blocks until signalled
		log.Print("daemon stopped")
		return nil
	},
}

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show daemon health: last poll, database, providers",
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
		if err := st.Migrate(); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}

		last, _ := st.GetConfigState("last_poll")
		if last == "" {
			last = "never"
		}
		size := "n/a"
		if fi, err := os.Stat(cfg.Database); err == nil {
			size = humanBytes(fi.Size())
		}
		now := time.Now()
		monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
		month, _ := st.TotalCostSince(monthStart)

		configured := auth.ListConfiguredProviders()
		if len(configured) == 0 {
			configured = []string{"(none — run `tt auth`)"}
		}

		fmt.Printf("Database:     %s (%s)\n", cfg.Database, size)
		fmt.Printf("Poll every:   %s\n", app.ParseInterval(cfg.PollInterval, 5*time.Minute))
		fmt.Printf("Last poll:    %s\n", last)
		fmt.Printf("Providers:    %s\n", strings.Join(configured, ", "))
		fmt.Printf("Month spend:  $%.2f\n", month)
		return nil
	},
}

var serviceCmd = &cobra.Command{
	Use:   "service",
	Short: "Print an OS service unit (launchd/systemd) for the daemon",
	Long: `Print a service definition that runs 'tt daemon' at login/boot.

Redirect it into the right location, for example:
  macOS:  tt service > ~/Library/LaunchAgents/dev.token-tracker.plist
  Linux:  tt service > ~/.config/systemd/user/token-tracker.service`,
	RunE: func(cmd *cobra.Command, args []string) error {
		exe, err := os.Executable()
		if err != nil {
			exe = "tt"
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

const launchdPlist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>dev.token-tracker</string>
    <key>ProgramArguments</key>
    <array>
        <string>%s</string>
        <string>daemon</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
</dict>
</plist>
`

const systemdUnit = `[Unit]
Description=token-tracker LLM cost monitor
After=network-online.target

[Service]
ExecStart=%s daemon
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

var proxyCmd = &cobra.Command{
	Use:   "proxy",
	Short: "Run the local usage-tracking proxy for live per-request spend",
	Long: `Start a transparent reverse proxy that forwards your LLM calls to their
real provider and records token usage in real time. Point a tool's base URL at
it, using the provider name as the first path segment:

  OpenAI:      http://127.0.0.1:7879/openai/v1
  OpenRouter:  http://127.0.0.1:7879/openrouter/api/v1

Your normal inference key is forwarded untouched — no admin key needed.`,
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
		if err := st.Migrate(); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}

		upstreams := app.ProxyUpstreams(cfg)
		listen := app.ProxyListen(cfg)
		if err := app.ValidateProxyListen(listen, cfg.Proxy.AllowNonLoopback); err != nil {
			return fmt.Errorf("proxy: %w", err)
		}
		srv := &http.Server{
			Addr:              listen,
			Handler:           app.BuildProxy(cfg, app.BuildRecorder(cfg, st)),
			ReadHeaderTimeout: 10 * time.Second,
			IdleTimeout:       120 * time.Second,
			MaxHeaderBytes:    1 << 20,
			// No WriteTimeout: streamed responses can run for many minutes.
		}

		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		go func() {
			<-ctx.Done()
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := srv.Shutdown(shutdownCtx); err != nil {
				log.Printf("proxy shutdown: %v", err)
			}
		}()

		fmt.Printf("token-tracker proxy listening on http://%s\n", listen)
		for name := range upstreams {
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

  tt run -- claude
  tt run --provider anthropic -- claude
  tt run -- opencode`,
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
			fmt.Fprintf(os.Stderr, "▸ routing %s through the proxy at http://%s (usage tracked)\n", name, listen)
		} else {
			fmt.Fprintf(os.Stderr, "⚠ proxy not reachable at http://%s — running %s directly; usage will NOT be tracked.\n  start it with `token-tracker` or `tt proxy`.\n", listen, name)
		}

		// Catch terminal signals so this wrapper doesn't die before the child;
		// the child (foreground, same process group) still receives them and
		// handles Ctrl-C itself. Notify (not Ignore) so the child resets to the
		// default disposition across exec rather than inheriting SIG_IGN.
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

func init() {
	authCmd.Flags().Bool("list", false, "List configured providers")
	spendCmd.Flags().Bool("by-model", false, "Break spend down by model")
	// Flags before the command; everything after the first positional is the
	// child command line (so `tt run claude --foo` passes --foo to claude).
	runCmd.Flags().SetInterspersed(false)
	runCmd.Flags().StringSlice("provider", nil, "Provider base URLs to route (default: all configured); repeatable")
	rootCmd.AddCommand(authCmd)
	rootCmd.AddCommand(clearCmd)
	rootCmd.AddCommand(spendCmd)
	rootCmd.AddCommand(migrateCmd)
	rootCmd.AddCommand(daemonCmd)
	rootCmd.AddCommand(statusCmd)
	rootCmd.AddCommand(serviceCmd)
	rootCmd.AddCommand(proxyCmd)
	rootCmd.AddCommand(runCmd)
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
