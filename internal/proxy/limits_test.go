package proxy

import (
	"net/http"
	"testing"
	"time"
)

func TestParseRateLimits_AnthropicUnified(t *testing.T) {
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	h := http.Header{}
	h.Set("anthropic-ratelimit-unified-5h-utilization", "62.5")
	h.Set("anthropic-ratelimit-unified-5h-status", "allowed")
	h.Set("anthropic-ratelimit-unified-5h-reset", now.Add(2*time.Hour).Format(time.RFC3339))
	h.Set("anthropic-ratelimit-unified-7d-utilization", "18")

	l := parseRateLimits("anthropic", h, now)
	if len(l.Windows) != 2 {
		t.Fatalf("windows = %d, want 2: %+v", len(l.Windows), l.Windows)
	}
	w5 := l.Windows[0]
	if w5.Label != "5h" || w5.Utilization != 62.5 || w5.Status != "allowed" {
		t.Errorf("5h window = %+v", w5)
	}
	if !w5.Reset.Equal(now.Add(2 * time.Hour)) {
		t.Errorf("5h reset = %v, want %v", w5.Reset, now.Add(2*time.Hour))
	}
	if l.Windows[1].Label != "7d" || l.Windows[1].Utilization != 18 {
		t.Errorf("7d window = %+v", l.Windows[1])
	}
}

func TestParseRateLimits_OpenAIComputesUtilization(t *testing.T) {
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	h := http.Header{}
	h.Set("x-ratelimit-limit-requests", "1000")
	h.Set("x-ratelimit-remaining-requests", "750") // 25% used
	h.Set("x-ratelimit-reset-requests", "6m0s")

	l := parseRateLimits("openai", h, now)
	if len(l.Windows) == 0 {
		t.Fatal("no windows parsed")
	}
	w := l.Windows[0]
	if w.Label != "requests" || w.Limit != 1000 || w.Remaining != 750 {
		t.Fatalf("window = %+v", w)
	}
	if w.Utilization != 25 {
		t.Errorf("utilization = %v, want 25", w.Utilization)
	}
	if !w.Reset.Equal(now.Add(6 * time.Minute)) {
		t.Errorf("reset = %v, want %v", w.Reset, now.Add(6*time.Minute))
	}
}

func TestParseRateLimits_NoHeadersYieldsNoWindows(t *testing.T) {
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	if l := parseRateLimits("anthropic", http.Header{}, now); len(l.Windows) != 0 {
		t.Errorf("expected no windows, got %+v", l.Windows)
	}
}
