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

// Event is a single recorded request, kept in an in-memory ring for the live TUI.
type Event struct {
	At         time.Time
	Provider   string
	Model      string
	Input      int64
	Cached     int64
	CacheWrite int64
	Output     int64
	CostUSD    float64
}

const ringSize = 256

// Recorder turns extracted usage into a stored, costed usage row, and keeps a
// ring of recent events + a session counter for the live dashboard.
type Recorder struct {
	store  *store.Store
	engine *cost.Engine
	nowFn  func() time.Time
	ids    sync.Map // "provider" and "provider\x00model" -> int64 row id

	mu          sync.Mutex
	ring        []Event // oldest-first, capped at ringSize
	requests    int
	sessionCost float64 // cumulative USD recorded this session
	started     time.Time
}

func NewRecorder(st *store.Store, engine *cost.Engine) *Recorder {
	return &Recorder{store: st, engine: engine, nowFn: time.Now, started: time.Now()}
}

// Recent returns up to n most-recent events, newest first.
func (r *Recorder) Recent(n int) []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	if n > len(r.ring) {
		n = len(r.ring)
	}
	out := make([]Event, n)
	for i := 0; i < n; i++ {
		out[i] = r.ring[len(r.ring)-1-i]
	}
	return out
}

// Requests is the number of requests recorded since the process started.
func (r *Recorder) Requests() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.requests
}

// SessionCost is the cumulative USD recorded since the process started.
func (r *Recorder) SessionCost() float64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sessionCost
}

// Started reports when this recorder (this daemon session) began.
func (r *Recorder) Started() time.Time { return r.started }

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

	now := r.nowFn()
	r.mu.Lock()
	r.ring = append(r.ring, Event{
		At: now, Provider: providerName, Model: model,
		Input: u.InputTokens, Cached: u.CachedInputTokens,
		CacheWrite: u.CacheWriteTokens, Output: u.OutputTokens, CostUSD: costUSD,
	})
	if len(r.ring) > ringSize {
		r.ring = r.ring[len(r.ring)-ringSize:]
	}
	r.requests++
	r.sessionCost += costUSD
	r.mu.Unlock()

	bucket := now.UTC().Truncate(time.Minute)
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
