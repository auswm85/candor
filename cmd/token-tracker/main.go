package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/auswm85/token-tracker/internal/alert"
	"github.com/auswm85/token-tracker/internal/app"
	"github.com/auswm85/token-tracker/internal/config"
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

	// The bubbletea TUI owns the terminal, so redirect log output to a file
	// next to the database instead of letting it corrupt the screen.
	logPath := filepath.Join(filepath.Dir(cfg.Database), "daemon.log")
	if lf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
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

	alerter := alert.New(cfg, st)

	m := tui.NewModel(cfg).WithStore(st)
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
