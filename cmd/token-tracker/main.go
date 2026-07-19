package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/auswm85/token-tracker/internal/alert"
	"github.com/auswm85/token-tracker/internal/app"
	"github.com/auswm85/token-tracker/internal/config"
	"github.com/auswm85/token-tracker/internal/lock"
	"github.com/auswm85/token-tracker/internal/store"
	"github.com/auswm85/token-tracker/internal/tui"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	st, err := store.Open(cfg.Database)
	if err != nil {
		log.Fatalf("store: %v", err)
	}
	defer func() { _ = st.Close() }()
	if err := st.Migrate(); err != nil {
		log.Fatalf("migrate: %v", err)
	}

	// Single-instance: prevent a second daemon from double-polling / fighting
	// for the proxy port.
	lk, err := lock.Acquire(filepath.Join(filepath.Dir(cfg.Database), "daemon.lock"))
	if err != nil {
		if errors.Is(err, lock.ErrLocked) {
			log.Fatal("another token-tracker daemon is already running")
		}
		log.Fatalf("acquire lock: %v", err)
	}
	defer func() { _ = lk.Release() }()

	// The bubbletea TUI owns the terminal, so redirect log output to a file
	// next to the database instead of letting it corrupt the screen.
	logPath := filepath.Join(filepath.Dir(cfg.Database), "daemon.log")
	if lf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
		_ = os.Chmod(logPath, 0o600) // fix perms on a pre-existing file
		defer func() { _ = lf.Close() }()
		log.SetOutput(lf)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)

	// Start the poll loop if any providers are configured.
	if scheduler := app.NewScheduler(cfg, st); scheduler != nil {
		go scheduler.Start(ctx)
	}

	// Start the live-usage proxy alongside the TUI when enabled.
	if cfg.Proxy.Enabled {
		if err := app.ValidateProxyListen(app.ProxyListen(cfg), cfg.Proxy.AllowNonLoopback); err != nil {
			log.Fatalf("proxy: %v", err)
		}
		proxySrv := &http.Server{
			Addr:              app.ProxyListen(cfg),
			Handler:           app.BuildProxy(cfg, st),
			ReadHeaderTimeout: 10 * time.Second,
			IdleTimeout:       120 * time.Second,
			MaxHeaderBytes:    1 << 20, // 1 MiB
			// No WriteTimeout: streamed responses can run for many minutes.
		}
		go func() {
			log.Printf("proxy listening on http://%s", proxySrv.Addr)
			if err := proxySrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Printf("proxy: %v", err)
			}
		}()
		go func() {
			<-ctx.Done()
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := proxySrv.Shutdown(shutdownCtx); err != nil {
				log.Printf("proxy shutdown: %v", err)
			}
		}()
	}

	alerter := alert.New(cfg, st)

	m := tui.NewModel(cfg).WithStore(st).WithEngine(app.BuildEngine(cfg))
	p := tui.NewProgram(m, alerter)

	go func() {
		<-sig
		cancel()
		p.Quit()
	}()

	if _, err := p.Run(); err != nil {
		log.Fatalf("tui: %v", err)
	}
}
