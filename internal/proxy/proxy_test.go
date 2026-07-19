package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/auswm85/token-tracker/internal/cost"
	"github.com/auswm85/token-tracker/internal/store"
)

func newProxy(t *testing.T, upstream string) (*Proxy, *store.Store) {
	t.Helper()
	st, err := store.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	prices := cost.Prices{"openai": {"gpt-4o": {InputPer1M: 2.50, CachedInputPer1M: 0.3125, OutputPer1M: 10.00}}}
	rec := NewRecorder(st, cost.New(prices))
	return NewProxy(map[string]string{"openai": upstream}, rec), st
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

	p, st := newProxy(t, upstream.URL)
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

	p, st := newProxy(t, upstream.URL)
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
