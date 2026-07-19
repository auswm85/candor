package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/viper"
)

type Config struct {
	Database string     `mapstructure:"database"`
	TUI      TUICfg     `mapstructure:"tui"`
	Defaults Defaults   `mapstructure:"defaults"`
	Proxy    ProxyCfg   `mapstructure:"proxy"`
	Pricing  PricingCfg `mapstructure:"pricing"`
}

type PricingCfg struct {
	Source string `mapstructure:"source"` // dynamic price catalog URL; "" disables
}

type ProxyCfg struct {
	Enabled          bool              `mapstructure:"enabled"`
	Listen           string            `mapstructure:"listen"`
	Upstreams        map[string]string `mapstructure:"upstreams"`
	AllowNonLoopback bool              `mapstructure:"allow_nonloopback"` // bind to non-loopback addrs
	MaxBodyBytes     int64             `mapstructure:"max_body_bytes"`    // cap proxied request body
}

type TUICfg struct {
	Refresh string `mapstructure:"refresh"`
}

type Defaults struct {
	MonthlyBudgetUSD float64 `mapstructure:"monthly_budget_usd"`
	AlertThresholds  []int   `mapstructure:"alert_thresholds"`
	DailyDigestHour  int     `mapstructure:"daily_digest_hour"` // 0–23 local hour; -1 disables
}

func Load() (*Config, error) {
	v := viper.New()
	v.SetConfigName("config")
	v.SetConfigType("yaml")

	configDir := filepath.Join(os.Getenv("HOME"), ".config", "candor")
	v.AddConfigPath(configDir)
	v.AddConfigPath(".")

	v.SetDefault("database", filepath.Join(os.Getenv("HOME"), ".local", "share", "candor", "tokens.db"))
	v.SetDefault("tui.refresh", "1s")
	v.SetDefault("defaults.monthly_budget_usd", 100)
	v.SetDefault("defaults.alert_thresholds", []int{50, 75, 90, 100})
	v.SetDefault("defaults.daily_digest_hour", -1) // disabled by default
	v.SetDefault("proxy.enabled", true)
	v.SetDefault("proxy.listen", "127.0.0.1:7879")
	v.SetDefault("proxy.allow_nonloopback", false)
	v.SetDefault("proxy.max_body_bytes", 16777216) // 16 MiB
	v.SetDefault("pricing.source", "https://openrouter.ai/api/v1/models")
	v.SetDefault("proxy.upstreams", map[string]string{
		"openai":     "https://api.openai.com",
		"openrouter": "https://openrouter.ai",
		"anthropic":  "https://api.anthropic.com",
	})

	v.AutomaticEnv()

	if err := v.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			return nil, err
		}
	}

	cfg := &Config{}
	if err := v.Unmarshal(cfg); err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// Validate rejects obviously-wrong settings so problems surface at startup
// rather than as silent misbehavior.
func (c *Config) Validate() error {
	if c.Defaults.MonthlyBudgetUSD < 0 {
		return fmt.Errorf("defaults.monthly_budget_usd must be >= 0, got %v", c.Defaults.MonthlyBudgetUSD)
	}
	for _, t := range c.Defaults.AlertThresholds {
		if t <= 0 || t > 1000 {
			return fmt.Errorf("defaults.alert_thresholds: %d%% out of range (expected 1–1000)", t)
		}
	}
	if c.Proxy.MaxBodyBytes < 0 {
		return fmt.Errorf("proxy.max_body_bytes must be >= 0, got %d", c.Proxy.MaxBodyBytes)
	}
	if h := c.Defaults.DailyDigestHour; h < -1 || h > 23 {
		return fmt.Errorf("defaults.daily_digest_hour must be 0–23 (or -1 to disable), got %d", h)
	}
	return nil
}
