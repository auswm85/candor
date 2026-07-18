package anthropic

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPollMessages(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "usage_report/messages") {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Write([]byte(`{
			"data": [{
				"starting_at": "2026-07-17T00:00:00Z",
				"ending_at": "2026-07-18T00:00:00Z",
				"results": [{
					"model": "claude-sonnet-4-5",
					"uncached_input_tokens": 1500,
					"cache_read_input_tokens": 200,
					"cache_creation": {
						"ephemeral_1h_input_tokens": 1000,
						"ephemeral_5m_input_tokens": 500
					},
					"output_tokens": 500
				}]
			}],
			"has_more": false,
			"next_page": null
		}`))
	}))
	defer server.Close()

	a := New("sk-ant-admin01-test-key").WithBaseURL(server.URL)
	records, err := a.pollMessages(context.Background(), time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("got %d records, want 1", len(records))
	}
	r := records[0]
	if r.Model != "claude-sonnet-4-5" {
		t.Errorf("model = %q", r.Model)
	}
	if r.InputTokens != 1500 || r.CachedInputTokens != 200 || r.CacheWriteTokens != 1500 || r.OutputTokens != 500 {
		t.Errorf("tokens mismatch: input=%d cached=%d write=%d output=%d",
			r.InputTokens, r.CachedInputTokens, r.CacheWriteTokens, r.OutputTokens)
	}
}

func TestPollClaudeCode(t *testing.T) {
	fixed := time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC) // same day as since
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "claude_code") {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Write([]byte(`{
			"data": [{
				"date": "2026-07-17T00:00:00Z",
				"model_breakdown": [{
					"model": "claude-sonnet-4-5",
					"tokens": {
						"input": 50000,
						"output": 12000,
						"cache_read": 8000,
						"cache_creation": 3000
					},
					"estimated_cost": {
						"currency": "USD",
						"amount": 412
					}
				}]
			}],
			"has_more": false,
			"next_page": null
		}`))
	}))
	defer server.Close()

	a := New("sk-ant-admin01-test-key").WithBaseURL(server.URL).WithNowFn(func() time.Time { return fixed })
	records, err := a.pollClaudeCode(context.Background(), time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("got %d records, want 1", len(records))
	}
	r := records[0]
	if r.Model != "claude-code/claude-sonnet-4-5" {
		t.Errorf("model = %q, want claude-code/claude-sonnet-4-5", r.Model)
	}
	if r.InputTokens != 50000 || r.CachedInputTokens != 8000 || r.CacheWriteTokens != 3000 || r.OutputTokens != 12000 {
		t.Errorf("tokens mismatch: input=%d cached=%d write=%d output=%d",
			r.InputTokens, r.CachedInputTokens, r.CacheWriteTokens, r.OutputTokens)
	}
}

func TestPollClaudeCodeMultiDay(t *testing.T) {
	call := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call++
		date := r.URL.Query().Get("starting_at")
		if call == 1 && date != "2026-07-17" {
			t.Errorf("expected day 2026-07-17, got %s", date)
		}
		w.Write([]byte(`{
			"data": [{
				"date": "` + date + `T00:00:00Z",
				"model_breakdown": [{
					"model": "claude-sonnet-4-5",
					"tokens": {"input":100,"output":50,"cache_read":10,"cache_creation":5},
					"estimated_cost": {"currency":"USD","amount":10}
				}]
			}],
			"has_more": false,
			"next_page": null
		}`))
	}))
	defer server.Close()

	a := New("sk-ant-admin01-test-key").WithBaseURL(server.URL)
	// Should poll 2 days: 2026-07-17 and 2026-07-18
	records, err := a.pollClaudeCode(context.Background(), time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 {
		t.Fatalf("got %d records, want 2 (across 2 days)", len(records))
	}
}

func TestPollUsageCombined(t *testing.T) {
	fixed := time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC) // same day as since, so only 1 day of Claude Code
	call := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call++
		if strings.Contains(r.URL.Path, "usage_report/messages") {
			w.Write([]byte(`{
				"data": [{"starting_at":"2026-07-17T00:00:00Z","ending_at":"2026-07-18T00:00:00Z","results":[{"model":"claude-sonnet-4-5","uncached_input_tokens":100,"cache_read_input_tokens":0,"cache_creation":{"ephemeral_1h_input_tokens":0,"ephemeral_5m_input_tokens":0},"output_tokens":50}]}],
				"has_more": false,"next_page": null
			}`))
		} else if strings.Contains(r.URL.Path, "claude_code") {
			w.Write([]byte(`{
				"data": [{"date":"2026-07-17T00:00:00Z","model_breakdown":[{"model":"claude-sonnet-4-5","tokens":{"input":200,"output":100,"cache_read":20,"cache_creation":10},"estimated_cost":{"currency":"USD","amount":25}}]}],
				"has_more": false,"next_page": null
			}`))
		}
	}))
	defer server.Close()

	a := New("sk-ant-admin01-test-key").WithBaseURL(server.URL).WithNowFn(func() time.Time { return fixed })
	records, err := a.PollUsage(context.Background(), time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 {
		t.Fatalf("got %d records, want 2 (1 API + 1 Claude Code)", len(records))
	}
	models := map[string]bool{}
	for _, r := range records {
		models[r.Model] = true
	}
	if !models["claude-sonnet-4-5"] || !models["claude-code/claude-sonnet-4-5"] {
		t.Errorf("expected both claude-sonnet-4-5 and claude-code/claude-sonnet-4-5, got %v", models)
	}
}

func TestPollUsageAuthError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":{"type":"authentication_error","message":"invalid x-api-key"}}`))
	}))
	defer server.Close()

	a := New("sk-ant-admin01-bad-key").WithBaseURL(server.URL)
	_, err := a.PollUsage(context.Background(), time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC))
	if err == nil {
		t.Fatal("expected auth error, got nil")
	}
}
