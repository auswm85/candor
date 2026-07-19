// Package app holds wiring shared by the daemon and CLI: assembling the proxy,
// cost engine, and budget-alert loop from config.
package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"sort"
	"time"

	"github.com/auswm85/candor/internal/alert"
	"github.com/auswm85/candor/internal/config"
	"github.com/auswm85/candor/internal/cost"
	"github.com/auswm85/candor/internal/pricing"
	"github.com/auswm85/candor/internal/proxy"
	"github.com/auswm85/candor/internal/store"
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
// back to bundled defaults.
func priceTable(cfg *config.Config) cost.Prices {
	return pricing.Load(filepath.Dir(cfg.Database), cfg.Pricing.Source)
}

// BuildEngine returns a cost engine using the dynamic price table — for the TUI
// to compute cache-impact figures.
func BuildEngine(cfg *config.Config) *cost.Engine {
	return cost.New(priceTable(cfg))
}

// BuildRecorder constructs the proxy usage recorder (shared with the TUI so it
// can show the live activity feed and burn rate).
func BuildRecorder(cfg *config.Config, st *store.Store) *proxy.Recorder {
	return proxy.NewRecorder(st, cost.New(priceTable(cfg)))
}

// BuildProxy constructs the live-usage proxy handler around a recorder.
func BuildProxy(cfg *config.Config, rec *proxy.Recorder) *proxy.Proxy {
	maxBody := cfg.Proxy.MaxBodyBytes
	if maxBody == 0 {
		maxBody = 16 << 20 // 16 MiB default
	}
	return proxy.NewProxy(ProxyUpstreams(cfg), rec, maxBody)
}

// childEnvVars maps a provider to the base-URL env vars coding harnesses read,
// plus the path suffix that follows the provider segment on the proxy (so the
// forwarded path matches the real provider's API root).
var childEnvVars = map[string]struct {
	vars   []string
	suffix string
}{
	"anthropic":  {[]string{"ANTHROPIC_BASE_URL"}, ""},
	"openai":     {[]string{"OPENAI_BASE_URL", "OPENAI_API_BASE"}, "/v1"},
	"openrouter": {[]string{"OPENROUTER_BASE_URL", "OPENROUTER_API_BASE"}, "/api/v1"},
}

// ProxyChildEnv returns KEY=VALUE overrides that point a child harness's
// provider base URLs at the local proxy. When providers is empty it covers every
// configured upstream. Applied to a child process only, so the user's normal
// harness invocation is left completely untouched (no persistent config).
func ProxyChildEnv(cfg *config.Config, listen string, providers []string) []string {
	if len(providers) == 0 {
		for p := range ProxyUpstreams(cfg) {
			providers = append(providers, p)
		}
	}
	sort.Strings(providers)
	var out []string
	for _, p := range providers {
		m, ok := childEnvVars[p]
		if !ok {
			continue
		}
		url := "http://" + listen + "/" + p + m.suffix
		for _, v := range m.vars {
			out = append(out, v+"="+url)
		}
	}
	return out
}

// OpenCodeConfigContent builds an OpenCode config JSON that overrides each
// provider's baseURL to point at the local proxy. It's meant to be passed to
// OpenCode via the OPENCODE_CONFIG_CONTENT env var — the highest-precedence
// config source, merged over the user's config, so only baseURL changes and
// only for that one process. Providers empty → all configured upstreams.
func OpenCodeConfigContent(cfg *config.Config, listen string, providers []string) (string, error) {
	if len(providers) == 0 {
		for p := range ProxyUpstreams(cfg) {
			providers = append(providers, p)
		}
	}
	sort.Strings(providers)
	prov := make(map[string]any, len(providers))
	for _, p := range providers {
		m, ok := childEnvVars[p]
		if !ok {
			continue
		}
		url := "http://" + listen + "/" + p + m.suffix
		prov[p] = map[string]any{"options": map[string]any{"baseURL": url}}
	}
	b, err := json.Marshal(map[string]any{"provider": prov})
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// ProxyHealthy reports whether a proxy is answering on listen within timeout,
// via its /healthz endpoint.
func ProxyHealthy(listen string, timeout time.Duration) bool {
	client := &http.Client{Timeout: timeout}
	resp, err := client.Get("http://" + listen + "/healthz")
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// ProjectMonth extrapolates month-to-date spend to a full-month projection at
// the current burn rate.
func ProjectMonth(st *store.Store, now time.Time) (float64, error) {
	start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	month, err := st.TotalCostSince(start)
	if err != nil {
		return 0, err
	}
	days := now.Sub(start).Hours() / 24
	if days < 1 {
		days = 1
	}
	return month / days * 30, nil
}

// StartAlertLoop periodically projects monthly spend and fires budget-threshold
// notifications — the timer-based replacement for the old poll-driven checker.
// No-op when no budget/thresholds are configured. Runs until ctx is cancelled.
func StartAlertLoop(ctx context.Context, cfg *config.Config, st *store.Store, interval time.Duration) {
	if cfg.Defaults.MonthlyBudgetUSD <= 0 || len(cfg.Defaults.AlertThresholds) == 0 {
		return
	}
	checker := alert.New(cfg, st)
	check := func() {
		if p, err := ProjectMonth(st, time.Now()); err == nil {
			_, _ = checker.Check(p)
		}
	}
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		check()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				check()
			}
		}
	}()
}
