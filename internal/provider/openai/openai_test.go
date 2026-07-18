package openai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPollUsage_AggregatesLineItemsByDayModel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer sk-admin-test" {
			t.Errorf("auth header = %q", got)
		}
		// One day (start_time 1783123200 = 2026-07-04 UTC), one model, three tiers.
		_, _ = w.Write([]byte(`{
			"object":"page","has_more":false,"next_page":null,
			"data":[{"object":"bucket","start_time":1783123200,"end_time":1783209600,"results":[
				{"object":"organization.costs.result","amount":{"value":"0.00042075","currency":"usd"},"quantity":2805.0,"line_item":"gpt-4o-mini-2024-07-18, input"},
				{"object":"organization.costs.result","amount":{"value":"0.0000606","currency":"usd"},"quantity":101.0,"line_item":"gpt-4o-mini-2024-07-18, output"},
				{"object":"organization.costs.result","amount":{"value":"0.00001","currency":"usd"},"quantity":500.0,"line_item":"gpt-4o-mini-2024-07-18, cached input"}
			]}]
		}`))
	}))
	defer srv.Close()

	a := New("sk-admin-test").WithBaseURL(srv.URL)
	records, err := a.PollUsage(context.Background(), time.Now().Add(-24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("got %d records, want 1 (one day+model)", len(records))
	}
	r := records[0]
	if r.Provider != "openai" || r.Model != "gpt-4o-mini-2024-07-18" {
		t.Errorf("provider/model = %s/%s", r.Provider, r.Model)
	}
	if r.InputTokens != 2805 || r.OutputTokens != 101 || r.CachedInputTokens != 500 {
		t.Errorf("tokens in=%d out=%d cached=%d", r.InputTokens, r.OutputTokens, r.CachedInputTokens)
	}
	// Cost is the sum of the three string amounts.
	want := 0.00042075 + 0.0000606 + 0.00001
	if diff := r.CostUSD - want; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("cost = %v, want %v", r.CostUSD, want)
	}
	if r.BucketStart.UTC().Format("2006-01-02") != "2026-07-04" {
		t.Errorf("bucket day = %s", r.BucketStart.UTC().Format("2006-01-02"))
	}
}

func TestPollUsage_UnauthorizedGivesAdminHint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid api key"}}`))
	}))
	defer srv.Close()

	a := New("sk-proj-standard").WithBaseURL(srv.URL)
	_, err := a.PollUsage(context.Background(), time.Now().Add(-24*time.Hour))
	if err == nil || !strings.Contains(err.Error(), "Admin key") {
		t.Fatalf("expected admin-key hint, got: %v", err)
	}
}

func TestParseLineItem(t *testing.T) {
	cases := map[string][2]string{
		"gpt-4o-mini-2024-07-18, input":   {"gpt-4o-mini-2024-07-18", "input"},
		"gpt-4o-2024-08-06, cached input": {"gpt-4o-2024-08-06", "cached input"},
		"gpt-4o":                          {"gpt-4o", ""},
	}
	for in, want := range cases {
		m, tier := parseLineItem(in)
		if m != want[0] || tier != want[1] {
			t.Errorf("parseLineItem(%q) = (%q,%q), want (%q,%q)", in, m, tier, want[0], want[1])
		}
	}
}
