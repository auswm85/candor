// Package app holds wiring shared by the daemon and CLI: constructing provider
// adapters from configured keys and assembling the poll scheduler.
package app

import (
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"time"

	"github.com/auswm85/token-tracker/internal/alert"
	"github.com/auswm85/token-tracker/internal/auth"
	"github.com/auswm85/token-tracker/internal/config"
	"github.com/auswm85/token-tracker/internal/cost"
	"github.com/auswm85/token-tracker/internal/poll"
	"github.com/auswm85/token-tracker/internal/pricing"
	"github.com/auswm85/token-tracker/internal/provider"
	"github.com/auswm85/token-tracker/internal/provider/anthropic"
	"github.com/auswm85/token-tracker/internal/provider/openai"
	"github.com/auswm85/token-tracker/internal/provider/openrouter"
	"github.com/auswm85/token-tracker/internal/proxy"
	"github.com/auswm85/token-tracker/internal/store"
)

// defaultUpstreams is the fallback provider→base-URL map when config omits it.
var defaultUpstreams = map[string]string{
	"openai":     "https://api.openai.com",
	"openrouter": "https://openrouter.ai",
	"anthropic":  "https://api.anthropic.com",
}

// ProxyUpstreams returns the configured upstreams, or built-in defaults.
func ProxyUpstreams(cfg *config.Config) map[string]string {
	if len(cfg.Proxy.Upstreams) > 0 {
		return cfg.Proxy.Upstreams
	}
	return defaultUpstreams
}

// ProxyListen returns the configured proxy listen address, or the default.
func ProxyListen(cfg *config.Config) string {
	if cfg.Proxy.Listen != "" {
		return cfg.Proxy.Listen
	}
	return "127.0.0.1:7879"
}

// ValidateProxyListen rejects binding the proxy to a non-loopback address
// unless explicitly allowed — the proxy forwards API keys, so exposing it to the
// network is opt-in.
func ValidateProxyListen(addr string, allowNonLoopback bool) error {
	if allowNonLoopback {
		return nil
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("parse proxy.listen %q: %w", addr, err)
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		return errors.New("proxy.listen binds to all interfaces; set proxy.allow_nonloopback: true to override")
	}
	if ip := net.ParseIP(host); ip != nil && !ip.IsLoopback() {
		return errors.New("proxy.listen is not a loopback address; set proxy.allow_nonloopback: true to override")
	}
	return nil
}

// priceTable loads model pricing dynamically (cached next to the DB), falling
// back to bundled defaults. Called once per engine; the on-disk cache dedupes
// the fetch across the proxy + poll engines within a single daemon start.
func priceTable(cfg *config.Config) cost.Prices {
	return pricing.Load(filepath.Dir(cfg.Database), cfg.Pricing.Source)
}

// BuildEngine returns a cost engine using the dynamic price table — for the TUI
// to compute cache-impact figures.
func BuildEngine(cfg *config.Config) *cost.Engine {
	return cost.New(priceTable(cfg))
}

// BuildProxy constructs the live-usage proxy handler backed by the store.
func BuildProxy(cfg *config.Config, st *store.Store) *proxy.Proxy {
	rec := proxy.NewRecorder(st, cost.New(priceTable(cfg)))
	maxBody := cfg.Proxy.MaxBodyBytes
	if maxBody == 0 {
		maxBody = 16 << 20 // 16 MiB default
	}
	return proxy.NewProxy(ProxyUpstreams(cfg), rec, maxBody)
}

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
	engine := cost.New(priceTable(cfg))
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
