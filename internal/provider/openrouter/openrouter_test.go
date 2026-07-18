package openrouter

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPollUsage_MapsAndAggregates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("auth header = %q", got)
		}
		if r.URL.Path != "/api/v1/activity" {
			t.Errorf("path = %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"data":[
			{"date":"2026-07-17","model":"openai/gpt-4o","prompt_tokens":1000,"completion_tokens":200,"reasoning_tokens":0,"usage":0.50},
			{"date":"2026-07-18","model":"openai/gpt-4o","prompt_tokens":2000,"completion_tokens":300,"reasoning_tokens":100,"usage":1.25},
			{"date":"2026-07-18","model":"openai/gpt-4o","prompt_tokens":500,"completion_tokens":50,"reasoning_tokens":0,"usage":0.25}
		]}`))
	}))
	defer srv.Close()

	a := New("test-key").WithBaseURL(srv.URL)
	records, err := a.PollUsage(context.Background(), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 {
		t.Fatalf("got %d records, want 2 (aggregated by day+model)", len(records))
	}

	byDay := map[string]struct {
		in, out int64
		cost    float64
	}{}
	for _, r := range records {
		if r.Provider != "openrouter" || r.Model != "openai/gpt-4o" {
			t.Errorf("unexpected provider/model: %+v", r)
		}
		byDay[r.BucketStart.Format("2006-01-02")] = struct {
			in, out int64
			cost    float64
		}{r.InputTokens, r.OutputTokens, r.CostUSD}
	}

	d17 := byDay["2026-07-17"]
	if d17.in != 1000 || d17.out != 200 || d17.cost != 0.50 {
		t.Errorf("2026-07-17 = %+v", d17)
	}
	// 2026-07-18: two rows summed; output includes reasoning tokens.
	d18 := byDay["2026-07-18"]
	if d18.in != 2500 || d18.out != 450 || d18.cost != 1.50 {
		t.Errorf("2026-07-18 = %+v (want in=2500 out=450 cost=1.50)", d18)
	}
}

func TestPollUsage_ForbiddenGivesProvisioningHint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"message":"Only management keys can fetch activity for an account","code":403}}`))
	}))
	defer srv.Close()

	a := New("inference-key").WithBaseURL(srv.URL)
	_, err := a.PollUsage(context.Background(), time.Time{})
	if err == nil {
		t.Fatal("expected error on 403")
	}
	if !strings.Contains(err.Error(), "provisioning key") {
		t.Errorf("error should mention provisioning key, got: %v", err)
	}
}
