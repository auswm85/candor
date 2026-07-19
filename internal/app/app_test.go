package app

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/auswm85/candor/internal/config"
)

func TestValidateProxyListen(t *testing.T) {
	cases := []struct {
		addr    string
		allow   bool
		wantErr bool
	}{
		{"127.0.0.1:7879", false, false},
		{"localhost:7879", false, false}, // not an IP; not rejected
		{"[::1]:7879", false, false},
		{"0.0.0.0:7879", false, true},
		{":7879", false, true},
		{"192.168.1.5:7879", false, true},
		{"0.0.0.0:7879", true, false}, // override allows it
		{"192.168.1.5:7879", true, false},
	}
	for _, c := range cases {
		err := ValidateProxyListen(c.addr, c.allow)
		if (err != nil) != c.wantErr {
			t.Errorf("ValidateProxyListen(%q, allow=%v) err=%v, wantErr=%v", c.addr, c.allow, err, c.wantErr)
		}
	}
}

func TestProxyChildEnv(t *testing.T) {
	cfg := &config.Config{}
	cfg.Proxy.Upstreams = map[string]string{
		"anthropic":  "https://api.anthropic.com",
		"openai":     "https://api.openai.com",
		"openrouter": "https://openrouter.ai",
	}
	listen := "127.0.0.1:7879"

	// Scoped to one provider → only that provider's env var, with correct suffix.
	got := ProxyChildEnv(cfg, listen, []string{"anthropic"})
	want := []string{"ANTHROPIC_BASE_URL=http://127.0.0.1:7879/anthropic"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("anthropic-only = %v, want %v", got, want)
	}

	// openai carries the /v1 suffix and both env-var aliases.
	got = ProxyChildEnv(cfg, listen, []string{"openai"})
	for _, w := range []string{
		"OPENAI_BASE_URL=http://127.0.0.1:7879/openai/v1",
		"OPENAI_API_BASE=http://127.0.0.1:7879/openai/v1",
	} {
		if !contains(got, w) {
			t.Errorf("openai env missing %q, got %v", w, got)
		}
	}

	// Empty providers → all configured upstreams covered.
	got = ProxyChildEnv(cfg, listen, nil)
	for _, w := range []string{"ANTHROPIC_BASE_URL", "OPENAI_BASE_URL", "OPENROUTER_BASE_URL"} {
		found := false
		for _, g := range got {
			if strings.HasPrefix(g, w+"=") {
				found = true
			}
		}
		if !found {
			t.Errorf("all-providers env missing %s, got %v", w, got)
		}
	}
}

func TestProjectMonthValue(t *testing.T) {
	// July has 31 days. 10 days elapsed, $50 spent → 50/10*31 = $155.
	jul10 := time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC) // 10 full days into July
	if got := ProjectMonthValue(50, jul10); abs(got-155) > 0.001 {
		t.Errorf("July projection = %.2f, want 155.00 (uses 31 days, not 30)", got)
	}

	// February 2026 has 28 days. 14 days in, $28 → 28/14*28 = $56.
	feb15 := time.Date(2026, 2, 15, 0, 0, 0, 0, time.UTC)
	if got := ProjectMonthValue(28, feb15); abs(got-56) > 0.001 {
		t.Errorf("Feb projection = %.2f, want 56.00", got)
	}

	// December rollover must not panic and uses 31 days.
	dec10 := time.Date(2026, 12, 11, 0, 0, 0, 0, time.UTC)
	if got := ProjectMonthValue(100, dec10); abs(got-310) > 0.001 {
		t.Errorf("Dec projection = %.2f, want 310.00", got)
	}
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

func TestOpenCodeConfigContent(t *testing.T) {
	cfg := &config.Config{}
	cfg.Proxy.Upstreams = map[string]string{
		"openai": "x", "anthropic": "x", "openrouter": "x",
	}
	listen := "127.0.0.1:7879"

	// Scoped to openrouter → only that provider's baseURL, with the /api/v1 path.
	got, err := OpenCodeConfigContent(cfg, listen, []string{"openrouter"})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"provider":{"openrouter":{"options":{"baseURL":"http://127.0.0.1:7879/openrouter/api/v1"}}}}`
	if got != want {
		t.Errorf("scoped =\n %s\nwant\n %s", got, want)
	}

	// All providers → valid JSON containing each proxy baseURL.
	all, err := OpenCodeConfigContent(cfg, listen, nil)
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Provider map[string]struct {
			Options struct {
				BaseURL string `json:"baseURL"`
			} `json:"options"`
		} `json:"provider"`
	}
	if err := json.Unmarshal([]byte(all), &parsed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	cases := map[string]string{
		"anthropic":  "http://127.0.0.1:7879/anthropic",
		"openai":     "http://127.0.0.1:7879/openai/v1",
		"openrouter": "http://127.0.0.1:7879/openrouter/api/v1",
	}
	for p, url := range cases {
		if parsed.Provider[p].Options.BaseURL != url {
			t.Errorf("%s baseURL = %q, want %q", p, parsed.Provider[p].Options.BaseURL, url)
		}
	}
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}
