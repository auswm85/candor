package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/auswm85/token-tracker/internal/alert"
	"github.com/auswm85/token-tracker/internal/auth"
	"github.com/auswm85/token-tracker/internal/config"
	"github.com/auswm85/token-tracker/internal/cost"
	"github.com/auswm85/token-tracker/internal/poll"
	"github.com/auswm85/token-tracker/internal/provider"
	"github.com/auswm85/token-tracker/internal/provider/anthropic"
	"github.com/auswm85/token-tracker/internal/provider/openai"
	"github.com/auswm85/token-tracker/internal/provider/openrouter"
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
	if providers := buildProviders(cfg); len(providers) > 0 {
		interval := parseInterval(cfg.PollInterval, 5*time.Minute)
		engine := cost.New(cost.DefaultPrices())
		scheduler := poll.New(providers, st, engine, interval)
		go scheduler.Start(ctx)
	}

	alerter := alert.New(cfg)

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

// buildProviders constructs an adapter for each enabled provider that has a
// stored API key.
func buildProviders(cfg *config.Config) []provider.Provider {
	var providers []provider.Provider
	add := func(name string, entry config.ProviderEntry, make func(key string) provider.Provider) {
		if !entry.Enabled {
			return
		}
		key, err := auth.GetProviderKey(name)
		if err != nil || key == "" {
			return
		}
		providers = append(providers, make(key))
	}
	add("openai", cfg.Providers.OpenAI, func(k string) provider.Provider { return openai.New(k) })
	add("anthropic", cfg.Providers.Anthropic, func(k string) provider.Provider { return anthropic.New(k) })
	add("openrouter", cfg.Providers.OpenRouter, func(k string) provider.Provider { return openrouter.New(k) })
	return providers
}

func parseInterval(s string, fallback time.Duration) time.Duration {
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return fallback
	}
	return d
}
