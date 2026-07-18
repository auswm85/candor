package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/auswm85/token-tracker/internal/alert"
	"github.com/auswm85/token-tracker/internal/config"
	"github.com/auswm85/token-tracker/internal/tui"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_ = ctx // used by poll scheduler (M2 milestone)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)

	alerter := alert.New(cfg)

	m := tui.NewModel(cfg)
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