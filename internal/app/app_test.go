package app

import (
	"strings"
	"testing"

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

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}
