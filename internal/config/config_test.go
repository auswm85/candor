package config

import "testing"

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
