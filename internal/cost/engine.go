package cost

import "time"

type ModelPrice struct {
	InputPer1M        float64
	CachedInputPer1M  float64
	CacheWritePer1M   float64
	OutputPer1M       float64
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