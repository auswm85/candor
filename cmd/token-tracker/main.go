package main

import (
	"context"
	"log"
	"os"
	"os/signal"
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
