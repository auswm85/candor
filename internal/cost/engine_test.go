package cost

import (
	"testing"
	"time"
)

func TestEngine_Compute(t *testing.T) {
	prices := Prices{
		"openai": {
			"gpt-4o": {
				InputPer1M:        2.50,
				CachedInputPer1M:  0.3125,
				CacheWritePer1M:   3.125,
				OutputPer1M:       10.00,
			},
		},
	}
	e := New(prices)

	tests := []struct {
		name                   string
		provider, model         string
		input, cached, write, output int64
		want                   float64
	}{
		{
			name:     "base input only",
			provider: "openai", model: "gpt-4o",
			input: 1_000_000, cached: 0, write: 0, output: 0,
			want: 2.50,
		},
		{
			name:     "cached input at 12.5%",
			provider: "openai", model: "gpt-4o",
			input: 0, cached: 1_000_000, write: 0, output: 0,
			want: 0.3125,
		},
		{
			name:     "cache write at 125%",
			provider: "openai", model: "gpt-4o",
			input: 0, cached: 0, write: 1_000_000, output: 0,
			want: 3.125,
		},
		{
			name:     "mixed with output",
			provider: "openai", model: "gpt-4o",
			input: 500_000, cached: 200_000, write: 50_000, output: 100_000,
			want: 1.25 + 0.0625 + 0.15625 + 1.00, // 2.46875
		},
		{
			name:     "unknown model returns 0",
			provider: "openai", model: "nonexistent",
			input: 1_000_000, cached: 0, write: 0, output: 0,
			want: 0,
		},
		{
			name:     "unknown provider returns 0",
			provider: "nonexistent", model: "gpt-4o",
			input: 1_000_000, cached: 0, write: 0, output: 0,
			want: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := e.Compute(tt.provider, tt.model, tt.input, tt.cached, tt.write, tt.output)
			if abs(got-tt.want) > 0.001 {
				t.Errorf("Compute() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestEngine_ProjectMonthly(t *testing.T) {
	e := New(nil)
	now := time.Now()

	tests := []struct {
		name         string
		since        time.Time
		current      float64
		wantMonth    float64
	}{
		{
			name:      "10 days at $33 = ~$99/month",
			since:     now.Add(-240 * time.Hour), // 10 days
			current:   33.00,
			wantMonth: 99.00,
		},
		{
			name:      "full month at $100 = $100",
			since:     now.Add(-720 * time.Hour), // 30 days
			current:   100.00,
			wantMonth: 100.00,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := e.ProjectMonthly("openai", tt.since, tt.current)
			if abs(got-tt.wantMonth) > 1.0 {
				t.Errorf("ProjectMonthly() = %.2f, want %.2f", got, tt.wantMonth)
			}
		})
	}
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}