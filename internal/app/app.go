// Package app holds wiring shared by the daemon and CLI: constructing provider
// adapters from configured keys and assembling the poll scheduler.
package app

import (
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
)

// BuildProviders constructs an adapter for each enabled provider that has a
// stored API key.
func BuildProviders(cfg *config.Config) []provider.Provider {
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

// NewScheduler assembles a poll scheduler from configured providers. It returns
// nil (no error) when no providers are configured, so callers can decide how to
// handle that case.
func NewScheduler(cfg *config.Config, st *store.Store) *poll.Scheduler {
	providers := BuildProviders(cfg)
	if len(providers) == 0 {
		return nil
	}
	engine := cost.New(cost.DefaultPrices())
	alerter := alert.New(cfg, st)
	return poll.New(providers, st, engine, alerter, ParseInterval(cfg.PollInterval, 5*time.Minute))
}

// ParseInterval parses a Go duration string, falling back on any parse error or
// non-positive value.
func ParseInterval(s string, fallback time.Duration) time.Duration {
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return fallback
	}
	return d
}
