package proxy

import (
	"net/http"
	"strconv"
	"time"
)

// RateWindow is the current state of one provider rate-limit window (e.g.
// Anthropic's 5-hour/weekly plan windows, or OpenAI's per-minute request/token
// limits). Unknown numeric fields are -1; an unknown Reset is the zero time.
type RateWindow struct {
	Label       string    `json:"label"`
	Utilization float64   `json:"utilization"` // percent used, -1 if unknown
	Remaining   int64     `json:"remaining"`   // -1 if unknown
	Limit       int64     `json:"limit"`       // -1 if unknown
	Reset       time.Time `json:"reset,omitempty"`
	Status      string    `json:"status,omitempty"`
}

// Limits is the latest rate-limit snapshot for one provider.
type Limits struct {
	Provider string       `json:"provider"`
	At       time.Time    `json:"at"`
	Windows  []RateWindow `json:"windows"`
}

// parseRateLimits extracts current rate-limit window state from a provider
// response's headers. Best-effort: missing/unparseable fields are left unknown,
// and a window is only included if at least one field was found.
func parseRateLimits(provider string, h http.Header, now time.Time) Limits {
	l := Limits{Provider: provider, At: now}
	switch provider {
	case "anthropic":
		// Claude Code plan windows: anthropic-ratelimit-unified-<5h|7d>-*.
		for _, w := range []string{"5h", "7d"} {
			rw := RateWindow{Label: w, Utilization: -1, Remaining: -1, Limit: -1}
			got := false
			p := "anthropic-ratelimit-unified-" + w + "-"
			if v := h.Get(p + "utilization"); v != "" {
				if f, err := strconv.ParseFloat(v, 64); err == nil {
					rw.Utilization = f
					got = true
				}
			}
			if v := h.Get(p + "remaining"); v != "" {
				if n, err := strconv.ParseInt(v, 10, 64); err == nil {
					rw.Remaining = n
					got = true
				}
			}
			if v := h.Get(p + "reset"); v != "" {
				if t, ok := parseReset(v, now); ok {
					rw.Reset = t
					got = true
				}
			}
			if v := h.Get(p + "status"); v != "" {
				rw.Status = v
				got = true
			}
			if got {
				l.Windows = append(l.Windows, rw)
			}
		}
	default:
		// OpenAI-compatible (openai, openrouter): x-ratelimit-*-<requests|tokens>.
		for _, kind := range []string{"requests", "tokens"} {
			rw := RateWindow{Label: kind, Utilization: -1, Remaining: -1, Limit: -1}
			got := false
			if v := h.Get("x-ratelimit-limit-" + kind); v != "" {
				if n, err := strconv.ParseInt(v, 10, 64); err == nil {
					rw.Limit = n
					got = true
				}
			}
			if v := h.Get("x-ratelimit-remaining-" + kind); v != "" {
				if n, err := strconv.ParseInt(v, 10, 64); err == nil {
					rw.Remaining = n
					got = true
				}
			}
			if rw.Limit > 0 && rw.Remaining >= 0 {
				rw.Utilization = float64(rw.Limit-rw.Remaining) / float64(rw.Limit) * 100
			}
			if v := h.Get("x-ratelimit-reset-" + kind); v != "" {
				if t, ok := parseReset(v, now); ok {
					rw.Reset = t
					got = true
				}
			}
			if got {
				l.Windows = append(l.Windows, rw)
			}
		}
	}
	return l
}

// parseReset interprets a reset header value as an absolute time. It accepts an
// RFC3339 timestamp (Anthropic), a Go duration relative to now (OpenAI, e.g.
// "6m0s"), or an integer (unix seconds if epoch-scale, else seconds from now).
func parseReset(v string, now time.Time) (time.Time, bool) {
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t, true
	}
	if d, err := time.ParseDuration(v); err == nil {
		return now.Add(d), true
	}
	if n, err := strconv.ParseInt(v, 10, 64); err == nil {
		if n > 1_000_000_000 { // unix epoch seconds
			return time.Unix(n, 0), true
		}
		return now.Add(time.Duration(n) * time.Second), true
	}
	return time.Time{}, false
}
