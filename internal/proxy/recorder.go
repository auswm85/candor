// Package proxy implements a local transparent reverse proxy that forwards LLM
// requests to their real provider and records per-request token usage in real
// time — the ingestion path for "live spend as you work".
package proxy

import (
	"sync"
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
	ids    sync.Map // "provider" and "provider\x00model" -> int64 row id
}

func NewRecorder(st *store.Store, engine *cost.Engine) *Recorder {
	return &Recorder{store: st, engine: engine, nowFn: time.Now}
}

// providerID / modelID memoize the upsert+lookup so a busy proxy doesn't hit the
// DB for IDs on every request (they never change once created).
func (r *Recorder) providerID(name string) (int64, error) {
	if v, ok := r.ids.Load(name); ok {
		return v.(int64), nil
	}
	id, err := r.store.ProviderID(name)
	if err != nil {
		return 0, err
	}
	r.ids.Store(name, id)
	return id, nil
}

func (r *Recorder) modelID(providerID int64, provider, model string) (int64, error) {
	key := provider + "\x00" + model
	if v, ok := r.ids.Load(key); ok {
		return v.(int64), nil
	}
	id, err := r.store.ModelID(providerID, model)
	if err != nil {
		return 0, err
	}
	r.ids.Store(key, id)
	return id, nil
}

// Record prices (if needed) and additively stores one request's usage into the
// current minute bucket, so the TUI/dashboard reflect it within a refresh tick.
func (r *Recorder) Record(providerName string, u Usage) error {
	model := u.Model
	if model == "" {
		model = "unknown"
	}
	providerID, err := r.providerID(providerName)
	if err != nil {
		return err
	}
	modelID, err := r.modelID(providerID, providerName, model)
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
