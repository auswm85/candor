package cost

import (
	"strings"
	"time"
)

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
		// Claude Code usage is reported as "claude-code/<model>"; fall back to
		// the underlying model's pricing so those rows still get costed.
		if trimmed := strings.TrimPrefix(model, "claude-code/"); trimmed != model {
			p, ok = modelPrices[trimmed]
		}
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
// config file does not override a provider/model. Values reflect published
// list prices and can drift; `tt prices diff` is the intended reconciliation
// path once implemented.
func DefaultPrices() Prices {
	return Prices{
		"anthropic": {
			"claude-opus-4-1":   {InputPer1M: 15.00, CachedInputPer1M: 1.50, CacheWritePer1M: 18.75, OutputPer1M: 75.00},
			"claude-opus-4-5":   {InputPer1M: 5.00, CachedInputPer1M: 0.50, CacheWritePer1M: 6.25, OutputPer1M: 25.00},
			"claude-sonnet-4-5": {InputPer1M: 3.00, CachedInputPer1M: 0.30, CacheWritePer1M: 3.75, OutputPer1M: 15.00},
			"claude-haiku-4-5":  {InputPer1M: 1.00, CachedInputPer1M: 0.10, CacheWritePer1M: 1.25, OutputPer1M: 5.00},
		},
		"openai": {
			"gpt-4o":      {InputPer1M: 2.50, CachedInputPer1M: 0.3125, CacheWritePer1M: 3.125, OutputPer1M: 10.00},
			"gpt-4o-mini": {InputPer1M: 0.15, CachedInputPer1M: 0.01875, CacheWritePer1M: 0.1875, OutputPer1M: 0.60},
		},
	}
}
