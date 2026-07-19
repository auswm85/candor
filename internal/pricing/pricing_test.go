package pricing

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/auswm85/candor/internal/cost"
)

func TestFetch_MapsAndNormalizes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[
			{"id":"anthropic/claude-sonnet-4.5","pricing":{"prompt":"0.000003","completion":"0.000015","input_cache_read":"0.0000003","input_cache_write":"0.00000375"}},
			{"id":"openai/gpt-4o","pricing":{"prompt":"0.0000025","completion":"0.00001","input_cache_read":"0.00000125"}},
			{"id":"meta-llama/llama-3","pricing":{"prompt":"0.0000001","completion":"0.0000001"}},
			{"id":"openai/free-model","pricing":{"prompt":"0","completion":"0"}}
		]}`))
	}))
	defer srv.Close()

	prices, err := Fetch(context.Background(), &http.Client{}, srv.URL)
	if err != nil {
		t.Fatal(err)
	}

	// meta-llama isn't a priced provider; free-model is skipped (0/0).
	if _, ok := prices["meta-llama"]; ok {
		t.Error("non-priced provider should be excluded")
	}
	if _, ok := prices["openai"]["free-model"]; ok {
		t.Error("zero-priced model should be skipped")
	}

	// Anthropic: dotted OpenRouter ID normalizes to dashed canonical key, per-1M.
	sonnet, ok := prices["anthropic"]["claude-sonnet-4-5"]
	if !ok {
		t.Fatalf("missing anthropic/claude-sonnet-4-5; got %+v", prices["anthropic"])
	}
	if sonnet.InputPer1M != 3.0 || sonnet.OutputPer1M != 15.0 || sonnet.CachedInputPer1M != 0.30 || sonnet.CacheWritePer1M != 3.75 {
		t.Errorf("sonnet prices = %+v", sonnet)
	}

	gpt4o := prices["openai"]["gpt-4o"]
	if gpt4o.InputPer1M != 2.50 || gpt4o.OutputPer1M != 10.0 || gpt4o.CachedInputPer1M != 1.25 {
		t.Errorf("gpt-4o prices = %+v", gpt4o)
	}

	// End to end: a dated Claude Code model resolves via the engine.
	eng := cost.New(prices)
	got := eng.Compute("anthropic", "claude-sonnet-4-5-20250929", 1_000_000, 0, 0, 1_000_000)
	if math.Abs(got-18.0) > 1e-9 { // 1M input @3 + 1M output @15
		t.Errorf("engine cost = %v, want 18.0", got)
	}
}

func TestLoad_FallsBackToDefaultsWhenSourceEmpty(t *testing.T) {
	p := Load(t.TempDir(), "") // dynamic disabled
	if _, ok := p["anthropic"]["claude-opus-4-8"]; !ok {
		t.Error("expected bundled defaults when source is empty")
	}
}

func TestLoad_FetchFailureFallsBack(t *testing.T) {
	// Unreachable source → must not fail, returns bundled defaults.
	p := Load(t.TempDir(), "http://127.0.0.1:1/nope")
	if _, ok := p["anthropic"]["claude-opus-4-8"]; !ok {
		t.Error("expected fallback to bundled defaults on fetch failure")
	}
}
