// Package proxy implements a local transparent reverse proxy that forwards LLM
// requests to their real provider and records per-request token usage in real
// time — the ingestion path for "live spend as you work".
package proxy

import (
	"time"

	"github.com/auswm85/token-tracker/internal/cost"
	"github.com/auswm85/token-tracker/internal/store"
)

// Usage is the token accounting extracted from a single LLM response.
type Usage struct {
	Model             string
	InputTokens       int64
	CachedInputTokens int64
	CacheWriteTokens  int64
	OutputTokens      int64
	// CostUSD, when > 0, is a provider-supplied cost (e.g. OpenRouter includes
	// one). When 0, the recorder prices the tokens with the cost engine.
	CostUSD float64
}

// Recorder turns extracted usage into a stored, costed usage row.
type Recorder struct {
	store  *store.Store
	engine *cost.Engine
	nowFn  func() time.Time
}

func NewRecorder(st *store.Store, engine *cost.Engine) *Recorder {
	return &Recorder{store: st, engine: engine, nowFn: time.Now}
}

// Record prices (if needed) and additively stores one request's usage into the
// current minute bucket, so the TUI/dashboard reflect it within a refresh tick.
func (r *Recorder) Record(providerName string, u Usage) error {
	model := u.Model
	if model == "" {
		model = "unknown"
	}
	providerID, err := r.store.ProviderID(providerName)
	if err != nil {
		return err
	}
	modelID, err := r.store.ModelID(providerID, model)
	if err != nil {
		return err
	}

	costUSD := u.CostUSD
	if costUSD == 0 {
		costUSD = r.engine.Compute(providerName, model,
			u.InputTokens, u.CachedInputTokens, u.CacheWriteTokens, u.OutputTokens)
	}

	bucket := r.nowFn().UTC().Truncate(time.Minute)
	return r.store.AddUsage(store.UsageRow{
		ProviderID:        providerID,
		ModelID:           modelID,
		BucketStart:       bucket,
		BucketEnd:         bucket.Add(time.Minute),
		InputTokens:       u.InputTokens,
		CachedInputTokens: u.CachedInputTokens,
		CacheWriteTokens:  u.CacheWriteTokens,
		OutputTokens:      u.OutputTokens,
		CostUSD:           costUSD,
	})
}
