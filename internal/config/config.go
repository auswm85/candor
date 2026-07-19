package config

import (
	"os"
	"path/filepath"

	"github.com/spf13/viper"
)

type Config struct {
	PollInterval string     `mapstructure:"poll_interval"`
	Database     string     `mapstructure:"database"`
	TUI          TUICfg     `mapstructure:"tui"`
	Defaults     Defaults   `mapstructure:"defaults"`
	Providers    ProvCfg    `mapstructure:"providers"`
	Proxy        ProxyCfg   `mapstructure:"proxy"`
	Pricing      PricingCfg `mapstructure:"pricing"`
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
}

type ProvCfg struct {
	OpenAI     ProviderEntry `mapstructure:"openai"`
	Anthropic  ProviderEntry `mapstructure:"anthropic"`
	OpenRouter ProviderEntry `mapstructure:"openrouter"`
}

type ProviderEntry struct {
	Enabled    bool   `mapstructure:"enabled"`
	KeyringKey string `mapstructure:"keyring_key"`
}

func Load() (*Config, error) {
	v := viper.New()
	v.SetConfigName("config")
	v.SetConfigType("yaml")

	configDir := filepath.Join(os.Getenv("HOME"), ".config", "token-tracker")
	v.AddConfigPath(configDir)
	v.AddConfigPath(".")

	v.SetDefault("poll_interval", "5m")
	v.SetDefault("database", filepath.Join(os.Getenv("HOME"), ".local", "share", "token-tracker", "tokens.db"))
	v.SetDefault("tui.refresh", "1s")
	v.SetDefault("defaults.monthly_budget_usd", 100)
	v.SetDefault("defaults.alert_thresholds", []int{50, 75, 90, 100})
	v.SetDefault("providers.openai.enabled", true)
	v.SetDefault("providers.anthropic.enabled", true)
	v.SetDefault("providers.openrouter.enabled", true)
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
	return cfg, nil
}
