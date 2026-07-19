package cost

import (
	"regexp"
	"strings"
	"time"
)

// modelDateSuffix matches a trailing model snapshot date, e.g. the "-20250929"
// in "claude-sonnet-4-5-20250929" or the "-2024-08-06" in "gpt-4o-2024-08-06".
var modelDateSuffix = regexp.MustCompile(`-(\d{8}|\d{4}-\d{2}-\d{2})$`)

// NormalizeModel maps the various model-ID spellings to a single canonical key
// so proxied traffic, the bundled table, and the dynamic (OpenRouter) table all
// line up. It strips the "claude-code/" prefix and any dated snapshot suffix,
// and folds version dots to dashes — Anthropic reports "claude-sonnet-4-5" while
// OpenRouter lists "claude-sonnet-4.5"; both normalize to "claude-sonnet-4-5".
func NormalizeModel(model string) string {
	model = strings.TrimPrefix(model, "claude-code/")
	model = modelDateSuffix.ReplaceAllString(model, "")
	return strings.ReplaceAll(model, ".", "-")
}

type ModelPrice struct {
	InputPer1M       float64
	CachedInputPer1M float64
	CacheWritePer1M  float64
	OutputPer1M      float64
}

type Prices map[string]map[string]ModelPrice

type Engine struct {
	prices Prices
}

func New(prices Prices) *Engine {
	return &Engine{prices: prices}
}

func (e *Engine) Compute(provider, model string, inputTokens, cachedInput, cacheWrite, outputTokens int64) float64 {
	modelPrices, ok := e.prices[provider]
	if !ok {
		return 0
	}
	p, ok := modelPrices[model]
	if !ok {
		// Fall back to the canonical ID so proxied harness traffic still costs.
		p, ok = modelPrices[NormalizeModel(model)]
	}
	if !ok {
		return 0
	}
	baseInput := float64(inputTokens) / 1_000_000 * p.InputPer1M
	cachedCost := float64(cachedInput) / 1_000_000 * p.CachedInputPer1M
	writeCost := float64(cacheWrite) / 1_000_000 * p.CacheWritePer1M
	outputCost := float64(outputTokens) / 1_000_000 * p.OutputPer1M
	return baseInput + cachedCost + writeCost + outputCost
}

func (e *Engine) ProjectMonthly(provider string, since time.Time, currentCost float64) float64 {
	daysElapsed := time.Since(since).Hours() / 24
	if daysElapsed < 1 {
		daysElapsed = 1
	}
	daysInMonth := 30.0
	return currentCost / daysElapsed * daysInMonth
}

func (e *Engine) Breakdown(prices Prices) map[string]map[string]float64 {
	_ = prices
	return nil
}

// DefaultPrices returns built-in pricing (USD per 1M tokens) used when the
// config file does not override a provider/model. Cache-read is ~0.1x input and
// 5-minute cache-write is ~1.25x input. Values reflect published list prices as
// of mid-2026 and can drift; `tt prices diff` is the intended reconciliation
// path once implemented. Dated snapshot IDs (e.g. claude-sonnet-4-5-20250929)
// resolve to these base entries via model normalization.
func DefaultPrices() Prices {
	return Prices{
		"anthropic": {
			// Opus tier ($5 / $25)
			"claude-opus-4-8": {InputPer1M: 5.00, CachedInputPer1M: 0.50, CacheWritePer1M: 6.25, OutputPer1M: 25.00},
			"claude-opus-4-7": {InputPer1M: 5.00, CachedInputPer1M: 0.50, CacheWritePer1M: 6.25, OutputPer1M: 25.00},
			"claude-opus-4-6": {InputPer1M: 5.00, CachedInputPer1M: 0.50, CacheWritePer1M: 6.25, OutputPer1M: 25.00},
			"claude-opus-4-5": {InputPer1M: 5.00, CachedInputPer1M: 0.50, CacheWritePer1M: 6.25, OutputPer1M: 25.00},
			"claude-opus-4-1": {InputPer1M: 15.00, CachedInputPer1M: 1.50, CacheWritePer1M: 18.75, OutputPer1M: 75.00},
			// Sonnet tier ($3 / $15)
			"claude-sonnet-5":   {InputPer1M: 3.00, CachedInputPer1M: 0.30, CacheWritePer1M: 3.75, OutputPer1M: 15.00},
			"claude-sonnet-4-6": {InputPer1M: 3.00, CachedInputPer1M: 0.30, CacheWritePer1M: 3.75, OutputPer1M: 15.00},
			"claude-sonnet-4-5": {InputPer1M: 3.00, CachedInputPer1M: 0.30, CacheWritePer1M: 3.75, OutputPer1M: 15.00},
			// Haiku tier ($1 / $5)
			"claude-haiku-4-5": {InputPer1M: 1.00, CachedInputPer1M: 0.10, CacheWritePer1M: 1.25, OutputPer1M: 5.00},
			// Fable tier ($10 / $50)
			"claude-fable-5": {InputPer1M: 10.00, CachedInputPer1M: 1.00, CacheWritePer1M: 12.50, OutputPer1M: 50.00},
		},
		"openai": {
			"gpt-4o":      {InputPer1M: 2.50, CachedInputPer1M: 0.3125, CacheWritePer1M: 3.125, OutputPer1M: 10.00},
			"gpt-4o-mini": {InputPer1M: 0.15, CachedInputPer1M: 0.01875, CacheWritePer1M: 0.1875, OutputPer1M: 0.60},
		},
	}
}
