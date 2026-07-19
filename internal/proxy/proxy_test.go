package proxy

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/auswm85/candor/internal/cost"
	"github.com/auswm85/candor/internal/store"
)

func newProxy(t *testing.T, provider, upstream string) (*Proxy, *store.Store) {
	t.Helper()
	st, err := store.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	prices := cost.Prices{
		"openai":    {"gpt-4o": {InputPer1M: 2.50, CachedInputPer1M: 0.3125, OutputPer1M: 10.00}},
		"anthropic": {"claude-sonnet-4-5": {InputPer1M: 3.00, CachedInputPer1M: 0.30, CacheWritePer1M: 3.75, OutputPer1M: 15.00}},
	}
	rec := NewRecorder(st, cost.New(prices))
	return NewProxy(map[string]string{provider: upstream}, rec, 16<<20), st
}

func TestProxy_Healthz(t *testing.T) {
	p, _ := newProxy(t, "openai", "http://unused.invalid")
	front := httptest.NewServer(p)
	defer front.Close()

	resp, err := http.Get(front.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "ok" {
		t.Fatalf("body = %q, want %q", body, "ok")
	}
}

func TestProxy_RequestBodyTooLarge(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("upstream should not be reached for an oversized body")
	}))
	defer upstream.Close()

	st, err := store.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	p := NewProxy(map[string]string{"openai": upstream.URL}, NewRecorder(st, cost.New(nil)), 64) // 64-byte cap
	front := httptest.NewServer(p)
	defer front.Close()

	req, _ := http.NewRequest("POST", front.URL+"/openai/v1/chat/completions",
		strings.NewReader(strings.Repeat("x", 200)))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", resp.StatusCode)
	}
}

func TestProxy_NonStreamingCapturesUsageAndForwards(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("upstream path = %q", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer sk-user-key" {
			t.Errorf("auth not forwarded: %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"gpt-4o","usage":{"prompt_tokens":1000,"completion_tokens":500,"prompt_tokens_details":{"cached_tokens":200}}}`))
	}))
	defer upstream.Close()

	p, st := newProxy(t, "openai", upstream.URL)
	front := httptest.NewServer(p)
	defer front.Close()

	req, _ := http.NewRequest("POST", front.URL+"/openai/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-4o","messages":[]}`))
	req.Header.Set("Authorization", "Bearer sk-user-key")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), `"gpt-4o"`) {
		t.Errorf("response not forwarded to client: %s", body)
	}

	// input = prompt_tokens - cached = 800; cached = 200; output = 500.
	// cost = 800/1e6*2.50 + 200/1e6*0.3125 + 500/1e6*10 = 0.0020 + 0.0000625 + 0.0050 = 0.0070625
	total, err := st.TotalCostSince(time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if diff := total - 0.0070625; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("stored cost = %v, want 0.0070625", total)
	}
	rows, _ := st.CostByModelSince(time.Now().Add(-time.Hour))
	if len(rows) != 1 || rows[0].Provider != "openai" || rows[0].Model != "gpt-4o" {
		t.Errorf("stored rows = %+v", rows)
	}
}

func TestProxy_StatsEndpointReflectsRecordedRequests(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"gpt-4o","usage":{"prompt_tokens":1000,"completion_tokens":500}}`))
	}))
	defer upstream.Close()

	p, _ := newProxy(t, "openai", upstream.URL)
	front := httptest.NewServer(p)
	defer front.Close()

	// Before any traffic, /stats reports an empty session.
	var s0 Stats
	getJSON(t, front.URL+"/stats", &s0)
	if s0.Requests != 0 || len(s0.Recent) != 0 {
		t.Fatalf("empty stats = %+v", s0)
	}

	req, _ := http.NewRequest("POST", front.URL+"/openai/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-4o","messages":[]}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(resp.Body)
	resp.Body.Close()

	var s1 Stats
	getJSON(t, front.URL+"/stats", &s1)
	if s1.Requests != 1 {
		t.Fatalf("requests = %d, want 1", s1.Requests)
	}
	if len(s1.Recent) != 1 || s1.Recent[0].Provider != "openai" || s1.Recent[0].Model != "gpt-4o" {
		t.Fatalf("recent = %+v", s1.Recent)
	}
	if s1.SessionCost <= 0 {
		t.Fatalf("session cost = %v, want > 0", s1.SessionCost)
	}
}

// TestProxy_AnthropicRequestBodyForwardedVerbatim guards prompt-cache continuity
// (and first-party fidelity): the Anthropic request body must reach the upstream
// byte-for-byte, with NO mutation — unlike the OpenAI/OpenRouter paths that inject
// usage flags. Mutating it would change the cache key and risk the traffic being
// classified as a non-first-party harness on subscription logins.
func TestProxy_AnthropicRequestBodyForwardedVerbatim(t *testing.T) {
	// Streaming request: the OpenAI path would inject stream_options here; the
	// Anthropic path must not touch it.
	reqBody := `{"model":"claude-sonnet-4-5","stream":true,"system":"be terse","messages":[{"role":"user","content":"hi"}]}`

	var got string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = string(b)
		if r.Header.Get("anthropic-beta") != "oauth-2025-04-20" {
			t.Errorf("anthropic-beta not forwarded: %q", r.Header.Get("anthropic-beta"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer upstream.Close()

	p, _ := newProxy(t, "anthropic", upstream.URL)
	front := httptest.NewServer(p)
	defer front.Close()

	req, _ := http.NewRequest("POST", front.URL+"/anthropic/v1/messages", strings.NewReader(reqBody))
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(resp.Body)
	resp.Body.Close()

	if got != reqBody {
		t.Fatalf("anthropic request body was mutated:\n got:  %s\n want: %s", got, reqBody)
	}
}

func TestProxy_StatsExposesRateLimits(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("anthropic-ratelimit-unified-5h-utilization", "62.5")
		w.Header().Set("anthropic-ratelimit-unified-5h-status", "allowed")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"usage":{"input_tokens":10,"output_tokens":5}}`))
	}))
	defer upstream.Close()

	p, _ := newProxy(t, "anthropic", upstream.URL)
	front := httptest.NewServer(p)
	defer front.Close()

	req, _ := http.NewRequest("POST", front.URL+"/anthropic/v1/messages",
		strings.NewReader(`{"model":"claude-sonnet-4-5","messages":[]}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(resp.Body)
	resp.Body.Close()

	var s Stats
	getJSON(t, front.URL+"/stats", &s)
	if len(s.Limits) != 1 || s.Limits[0].Provider != "anthropic" {
		t.Fatalf("limits = %+v", s.Limits)
	}
	w := s.Limits[0].Windows
	if len(w) != 1 || w[0].Label != "5h" || w[0].Utilization != 62.5 {
		t.Fatalf("windows = %+v", w)
	}
}

func getJSON(t *testing.T, url string, v any) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		t.Fatal(err)
	}
}

func TestProxy_StreamingCapturesFinalUsageChunk(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify include_usage was injected by the proxy.
		reqBody, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(reqBody), `"include_usage":true`) {
			t.Errorf("proxy did not inject include_usage: %s", reqBody)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		_, _ = w.Write([]byte("data: {\"model\":\"gpt-4o\",\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n"))
		if fl != nil {
			fl.Flush()
		}
		_, _ = w.Write([]byte("data: {\"model\":\"gpt-4o\",\"usage\":{\"prompt_tokens\":100,\"completion_tokens\":40}}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer upstream.Close()

	p, st := newProxy(t, "openai", upstream.URL)
	front := httptest.NewServer(p)
	defer front.Close()

	req, _ := http.NewRequest("POST", front.URL+"/openai/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-4o","stream":true,"messages":[]}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "[DONE]") {
		t.Errorf("stream not forwarded intact: %s", body)
	}

	total, err := st.TotalCostSince(time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	// cost = 100/1e6*2.50 + 40/1e6*10 = 0.00025 + 0.0004 = 0.00065
	if diff := total - 0.00065; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("stored cost = %v, want 0.00065", total)
	}
}

func TestProxy_AnthropicNonStreamingCachesTokens(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Errorf("upstream path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"claude-sonnet-4-5","usage":{"input_tokens":1000,"output_tokens":300,"cache_read_input_tokens":400,"cache_creation_input_tokens":200}}`))
	}))
	defer upstream.Close()

	p, st := newProxy(t, "anthropic", upstream.URL)
	front := httptest.NewServer(p)
	defer front.Close()

	req, _ := http.NewRequest("POST", front.URL+"/anthropic/v1/messages",
		strings.NewReader(`{"model":"claude-sonnet-4-5","messages":[]}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	rows, _ := st.UsageSince(time.Now().Add(-time.Hour))
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	r := rows[0]
	if r.InputTokens != 1000 || r.CachedInputTokens != 400 || r.CacheWriteTokens != 200 || r.OutputTokens != 300 {
		t.Errorf("tokens in=%d cached=%d write=%d out=%d", r.InputTokens, r.CachedInputTokens, r.CacheWriteTokens, r.OutputTokens)
	}
	// cost = 1000/1e6*3 + 400/1e6*0.30 + 200/1e6*3.75 + 300/1e6*15
	//      = 0.003 + 0.00012 + 0.00075 + 0.0045 = 0.00837
	if diff := r.CostUSD - 0.00837; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("cost = %v, want 0.00837", r.CostUSD)
	}
}

func TestProxy_AnthropicStreamingCombinesEvents(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		// input + cache tokens arrive in message_start; final output in message_delta.
		_, _ = w.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"model\":\"claude-sonnet-4-5\",\"usage\":{\"input_tokens\":1000,\"cache_read_input_tokens\":400,\"cache_creation_input_tokens\":200,\"output_tokens\":1}}}\n\n"))
		if fl != nil {
			fl.Flush()
		}
		_, _ = w.Write([]byte("event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":300}}\n\n"))
		_, _ = w.Write([]byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"))
	}))
	defer upstream.Close()

	p, st := newProxy(t, "anthropic", upstream.URL)
	front := httptest.NewServer(p)
	defer front.Close()

	req, _ := http.NewRequest("POST", front.URL+"/anthropic/v1/messages",
		strings.NewReader(`{"model":"claude-sonnet-4-5","stream":true,"messages":[]}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "message_stop") {
		t.Errorf("stream not forwarded intact")
	}

	rows, _ := st.UsageSince(time.Now().Add(-time.Hour))
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	r := rows[0]
	if r.InputTokens != 1000 || r.CachedInputTokens != 400 || r.CacheWriteTokens != 200 || r.OutputTokens != 300 {
		t.Errorf("combined tokens wrong: in=%d cached=%d write=%d out=%d", r.InputTokens, r.CachedInputTokens, r.CacheWriteTokens, r.OutputTokens)
	}
}

func TestProxy_OpenRouterUsesProviderCostAndInjectsAccounting(t *testing.T) {
	var gotBody []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		// OpenRouter includes a `cost` in usage when accounting is on.
		_, _ = w.Write([]byte(`{"model":"anthropic/claude-sonnet-4.5","usage":{"prompt_tokens":1000,"completion_tokens":200,"cost":0.0123}}`))
	}))
	defer upstream.Close()

	p, st := newProxy(t, "openrouter", upstream.URL)
	front := httptest.NewServer(p)
	defer front.Close()

	req, _ := http.NewRequest("POST", front.URL+"/openrouter/api/v1/chat/completions",
		strings.NewReader(`{"model":"anthropic/claude-sonnet-4.5","messages":[]}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	// Proxy must have injected usage accounting.
	if !strings.Contains(string(gotBody), `"include":true`) {
		t.Errorf("proxy did not inject usage accounting: %s", gotBody)
	}

	// Stored cost must be OpenRouter's provider cost, not an engine estimate
	// (there's no openrouter pricing in the test engine).
	total, err := st.TotalCostSince(time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if diff := total - 0.0123; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("stored cost = %v, want 0.0123 (provider-supplied)", total)
	}
}
