package config

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestConfigValidate(t *testing.T) {
	base := func() *Config {
		c := &Config{}
		c.Defaults.MonthlyBudgetUSD = 100
		c.Defaults.AlertThresholds = []int{50, 75, 90, 100}
		c.Proxy.MaxBodyBytes = 1 << 20
		return c
	}

	if err := base().Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}

	cases := []struct {
		name   string
		mutate func(*Config)
	}{
		{"negative budget", func(c *Config) { c.Defaults.MonthlyBudgetUSD = -1 }},
		{"zero threshold", func(c *Config) { c.Defaults.AlertThresholds = []int{0} }},
		{"threshold too high", func(c *Config) { c.Defaults.AlertThresholds = []int{2000} }},
		{"negative body cap", func(c *Config) { c.Proxy.MaxBodyBytes = -1 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := base()
			tc.mutate(c)
			if err := c.Validate(); err == nil {
				t.Errorf("expected validation error for %s", tc.name)
			}
		})
	}
}

// writeConfig writes body as <dir>/config.yaml, creating dir.
func writeConfig(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoad(t *testing.T) {
	// Each subtest gets a fresh HOME and an empty cwd so no stray config.yaml
	// leaks in from the package directory or a previous case.
	t.Run("defaults when no config file exists", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Chdir(t.TempDir())

		cfg, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if want := filepath.Join(home, ".local", "share", "candor", "tokens.db"); cfg.Database != want {
			t.Errorf("Database = %q, want %q", cfg.Database, want)
		}
		if cfg.TUI.Refresh != "1s" {
			t.Errorf("TUI.Refresh = %q, want 1s", cfg.TUI.Refresh)
		}
		if cfg.Defaults.MonthlyBudgetUSD != 100 {
			t.Errorf("MonthlyBudgetUSD = %v, want 100", cfg.Defaults.MonthlyBudgetUSD)
		}
		if !slices.Equal(cfg.Defaults.AlertThresholds, []int{50, 75, 90, 100}) {
			t.Errorf("AlertThresholds = %v, want [50 75 90 100]", cfg.Defaults.AlertThresholds)
		}
		if cfg.Defaults.DailyDigestHour != -1 {
			t.Errorf("DailyDigestHour = %d, want -1 (disabled)", cfg.Defaults.DailyDigestHour)
		}
		if !cfg.Proxy.Enabled {
			t.Error("Proxy.Enabled = false, want true (default)")
		}
		if cfg.Proxy.Listen != "127.0.0.1:7879" {
			t.Errorf("Proxy.Listen = %q, want 127.0.0.1:7879", cfg.Proxy.Listen)
		}
		if cfg.Proxy.AllowNonLoopback {
			t.Error("Proxy.AllowNonLoopback = true, want false (default)")
		}
		if cfg.Proxy.MaxBodyBytes != 16777216 {
			t.Errorf("Proxy.MaxBodyBytes = %d, want 16777216", cfg.Proxy.MaxBodyBytes)
		}
		if cfg.Pricing.Source != "https://openrouter.ai/api/v1/models" {
			t.Errorf("Pricing.Source = %q, want openrouter catalog URL", cfg.Pricing.Source)
		}
		wantUp := map[string]string{
			"openai":     "https://api.openai.com",
			"openrouter": "https://openrouter.ai",
			"anthropic":  "https://api.anthropic.com",
		}
		if len(cfg.Proxy.Upstreams) != len(wantUp) {
			t.Fatalf("Proxy.Upstreams = %v, want %d entries", cfg.Proxy.Upstreams, len(wantUp))
		}
		for k, want := range wantUp {
			if cfg.Proxy.Upstreams[k] != want {
				t.Errorf("Proxy.Upstreams[%q] = %q, want %q", k, cfg.Proxy.Upstreams[k], want)
			}
		}
	})

	// NB: keep "$" out of subtest names — it lands in t.TempDir() paths and
	// viper's file search silently skips paths containing "$".
	t.Run("config file in home config dir overrides defaults", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Chdir(t.TempDir())
		writeConfig(t, filepath.Join(home, ".config", "candor"), `
database: /tmp/override.db
tui:
  refresh: 5s
defaults:
  monthly_budget_usd: 250
  alert_thresholds: [25, 50]
  daily_digest_hour: 9
proxy:
  enabled: false
  listen: "127.0.0.1:9999"
  max_body_bytes: 1024
pricing:
  source: ""
`)

		cfg, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Database != "/tmp/override.db" {
			t.Errorf("Database = %q, want /tmp/override.db", cfg.Database)
		}
		if cfg.TUI.Refresh != "5s" {
			t.Errorf("TUI.Refresh = %q, want 5s", cfg.TUI.Refresh)
		}
		if cfg.Defaults.MonthlyBudgetUSD != 250 {
			t.Errorf("MonthlyBudgetUSD = %v, want 250", cfg.Defaults.MonthlyBudgetUSD)
		}
		if !slices.Equal(cfg.Defaults.AlertThresholds, []int{25, 50}) {
			t.Errorf("AlertThresholds = %v, want [25 50]", cfg.Defaults.AlertThresholds)
		}
		if cfg.Defaults.DailyDigestHour != 9 {
			t.Errorf("DailyDigestHour = %d, want 9", cfg.Defaults.DailyDigestHour)
		}
		if cfg.Proxy.Enabled {
			t.Error("Proxy.Enabled = true, want false (overridden)")
		}
		if cfg.Proxy.Listen != "127.0.0.1:9999" {
			t.Errorf("Proxy.Listen = %q, want 127.0.0.1:9999", cfg.Proxy.Listen)
		}
		if cfg.Proxy.MaxBodyBytes != 1024 {
			t.Errorf("Proxy.MaxBodyBytes = %d, want 1024", cfg.Proxy.MaxBodyBytes)
		}
		if cfg.Pricing.Source != "" {
			t.Errorf("Pricing.Source = %q, want empty (disabled)", cfg.Pricing.Source)
		}
		// Untouched defaults survive.
		if cfg.Proxy.AllowNonLoopback {
			t.Error("Proxy.AllowNonLoopback = true, want false (default)")
		}
		if cfg.Proxy.Upstreams["anthropic"] != "https://api.anthropic.com" {
			t.Errorf("Proxy.Upstreams[anthropic] = %q, want default", cfg.Proxy.Upstreams["anthropic"])
		}
	})

	t.Run("config file in current directory is picked up", func(t *testing.T) {
		home := t.TempDir() // no config under HOME
		t.Setenv("HOME", home)
		cwd := t.TempDir()
		writeConfig(t, cwd, "defaults:\n  monthly_budget_usd: 42\n")
		t.Chdir(cwd)

		cfg, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Defaults.MonthlyBudgetUSD != 42 {
			t.Errorf("MonthlyBudgetUSD = %v, want 42 (from ./config.yaml)", cfg.Defaults.MonthlyBudgetUSD)
		}
	})

	t.Run("home config dir takes precedence over current directory", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		writeConfig(t, filepath.Join(home, ".config", "candor"), "defaults:\n  monthly_budget_usd: 111\n")
		cwd := t.TempDir()
		writeConfig(t, cwd, "defaults:\n  monthly_budget_usd: 222\n")
		t.Chdir(cwd)

		cfg, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		// Load adds $HOME/.config/candor to the search path before ".".
		if cfg.Defaults.MonthlyBudgetUSD != 111 {
			t.Errorf("MonthlyBudgetUSD = %v, want 111 (home config should win)", cfg.Defaults.MonthlyBudgetUSD)
		}
	})

	t.Run("invalid yaml returns an error", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Chdir(t.TempDir())
		writeConfig(t, filepath.Join(home, ".config", "candor"), "{{{ not yaml")

		if _, err := Load(); err == nil {
			t.Fatal("expected error for invalid YAML, got nil")
		}
	})

	t.Run("invalid values fail validation", func(t *testing.T) {
		cases := []struct {
			name string
			yaml string
		}{
			{"negative budget", "defaults:\n  monthly_budget_usd: -1\n"},
			{"zero threshold", "defaults:\n  alert_thresholds: [0]\n"},
			{"threshold above 1000", "defaults:\n  alert_thresholds: [1001]\n"},
			{"negative body cap", "proxy:\n  max_body_bytes: -5\n"},
			{"digest hour above 23", "defaults:\n  daily_digest_hour: 24\n"},
			{"digest hour below -1", "defaults:\n  daily_digest_hour: -2\n"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				home := t.TempDir()
				t.Setenv("HOME", home)
				t.Chdir(t.TempDir())
				writeConfig(t, filepath.Join(home, ".config", "candor"), tc.yaml)

				if _, err := Load(); err == nil {
					t.Errorf("expected Load to reject %s", tc.name)
				}
			})
		}
	})

	t.Run("boundary values accepted", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Chdir(t.TempDir())
		writeConfig(t, filepath.Join(home, ".config", "candor"), `
defaults:
  monthly_budget_usd: 0
  alert_thresholds: [1, 1000]
  daily_digest_hour: 23
proxy:
  max_body_bytes: 0
`)
		cfg, err := Load()
		if err != nil {
			t.Fatalf("boundary config rejected: %v", err)
		}
		if cfg.Defaults.DailyDigestHour != 23 {
			t.Errorf("DailyDigestHour = %d, want 23", cfg.Defaults.DailyDigestHour)
		}
	})

	t.Run("environment variable overrides", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Chdir(t.TempDir())
		t.Setenv("DATABASE", "/tmp/env-override.db")

		cfg, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Database != "/tmp/env-override.db" {
			t.Errorf("Database = %q, want /tmp/env-override.db (env override)", cfg.Database)
		}
	})
}
