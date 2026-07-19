// Package pricing loads model pricing dynamically from OpenRouter's public
// models catalog (no auth), caches it on disk, and falls back to the bundled
// defaults offline — so prices stay fresh without manual tracking or recompiles.
package pricing

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/auswm85/candor/internal/cost"
)

// datedSnapshot matches a trailing model snapshot date. OpenRouter lists both a
// base ID ("openai/gpt-4o", current pricing) and pinned snapshots
// ("openai/gpt-4o-2024-05-13", historical pricing). Since our proxied model
// names normalize to the base ID, we take the base entry and skip the snapshots
// — otherwise an old snapshot's price would clobber the current one.
var datedSnapshot = regexp.MustCompile(`-(\d{8}|\d{4}-\d{2}-\d{2})$`)

// DefaultSource is OpenRouter's public model catalog (returns per-token pricing,
// including cache read/write, for models across providers — no API key needed).
const DefaultSource = "https://openrouter.ai/api/v1/models"

// cacheTTL is how long a cached fetch is considered fresh.
const cacheTTL = 24 * time.Hour

// providerFromPrefix maps an OpenRouter ID prefix to our provider name. Only
// providers we price via the engine need entries — OpenRouter-proxied traffic
// already carries its cost in the response.
var providerFromPrefix = map[string]string{
	"openai":    "openai",
	"anthropic": "anthropic",
}

type modelsResponse struct {
	Data []struct {
		ID      string `json:"id"`
		Pricing struct {
			Prompt          string `json:"prompt"`
			Completion      string `json:"completion"`
			InputCacheRead  string `json:"input_cache_read"`
			InputCacheWrite string `json:"input_cache_write"`
		} `json:"pricing"`
	} `json:"data"`
}

// Fetch pulls the catalog and builds a price table keyed by our canonical model
// IDs. Only providers in providerFromPrefix are included.
func Fetch(ctx context.Context, client *http.Client, url string) (cost.Prices, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("pricing source %s: HTTP %d", url, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, err
	}

	var mr modelsResponse
	if err := json.Unmarshal(body, &mr); err != nil {
		return nil, fmt.Errorf("decode pricing: %w", err)
	}

	prices := cost.Prices{}
	for _, m := range mr.Data {
		i := strings.IndexByte(m.ID, '/')
		if i < 0 {
			continue
		}
		prov, ok := providerFromPrefix[m.ID[:i]]
		if !ok {
			continue
		}
		model := m.ID[i+1:]
		if datedSnapshot.MatchString(model) {
			continue // historical snapshot; the base ID carries current pricing
		}
		mp := cost.ModelPrice{
			InputPer1M:       perMillion(m.Pricing.Prompt),
			OutputPer1M:      perMillion(m.Pricing.Completion),
			CachedInputPer1M: perMillion(m.Pricing.InputCacheRead),
			CacheWritePer1M:  perMillion(m.Pricing.InputCacheWrite),
		}
		if mp.InputPer1M == 0 && mp.OutputPer1M == 0 {
			continue // free/unpriced entry — skip so it doesn't shadow a default
		}
		if prices[prov] == nil {
			prices[prov] = map[string]cost.ModelPrice{}
		}
		prices[prov][cost.NormalizeModel(m.ID[i+1:])] = mp
	}
	return prices, nil
}

// perMillion converts an OpenRouter per-token price string to USD per 1M tokens.
func perMillion(s string) float64 {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return f * 1_000_000
}

type cacheEnvelope struct {
	FetchedAt time.Time   `json:"fetched_at"`
	Source    string      `json:"source"`
	Prices    cost.Prices `json:"prices"`
}

// Load returns a price table: the dynamic source layered over the bundled
// defaults (dynamic wins per model, defaults fill gaps). It uses a fresh cache
// if present, otherwise fetches and caches; on any failure it falls back to a
// stale cache, then to the bundled defaults alone. Never fails.
func Load(cacheDir, source string) cost.Prices {
	merged := cost.DefaultPrices()
	if source == "" {
		return merged // dynamic pricing disabled
	}
	cachePath := filepath.Join(cacheDir, "prices.json")

	if env, ok := readCache(cachePath); ok && time.Since(env.FetchedAt) < cacheTTL {
		return overlay(merged, env.Prices)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	fetched, err := Fetch(ctx, &http.Client{}, source)
	if err == nil && len(fetched) > 0 {
		writeCache(cachePath, cacheEnvelope{FetchedAt: time.Now().UTC(), Source: source, Prices: fetched})
		return overlay(merged, fetched)
	}
	// Fetch failed — use a stale cache if we have one, else bundled defaults.
	if env, ok := readCache(cachePath); ok {
		return overlay(merged, env.Prices)
	}
	return merged
}

// overlay writes src entries over dst (per provider/model), returning dst.
func overlay(dst, src cost.Prices) cost.Prices {
	for prov, models := range src {
		if dst[prov] == nil {
			dst[prov] = map[string]cost.ModelPrice{}
		}
		for model, mp := range models {
			dst[prov][model] = mp
		}
	}
	return dst
}

func readCache(path string) (cacheEnvelope, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return cacheEnvelope{}, false
	}
	var env cacheEnvelope
	if err := json.Unmarshal(b, &env); err != nil || len(env.Prices) == 0 {
		return cacheEnvelope{}, false
	}
	return env, true
}

func writeCache(path string, env cacheEnvelope) {
	b, err := json.MarshalIndent(env, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(path, b, 0o600)
}
