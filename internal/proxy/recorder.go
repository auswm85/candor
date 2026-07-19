// Package proxy implements a local transparent reverse proxy that forwards LLM
// requests to their real provider and records per-request token usage in real
// time — the ingestion path for "live spend as you work".
package proxy

import (
	"sort"
	"sync"
	"time"

	"github.com/auswm85/candor/internal/cost"
	"github.com/auswm85/candor/internal/store"
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

// Event is a single recorded request, kept in an in-memory ring for the live
// TUI. JSON-tagged so it can be served over /stats to a detached viewer.
type Event struct {
	At         time.Time `json:"at"`
	Provider   string    `json:"provider"`
	Model      string    `json:"model"`
	Input      int64     `json:"input"`
	Cached     int64     `json:"cached"`
	CacheWrite int64     `json:"cache_write"`
	Output     int64     `json:"output"`
	CostUSD    float64   `json:"cost_usd"`
}

// Stats is a point-in-time snapshot of live session activity. The proxy serves
// it over /stats so a TUI running in a separate process (attached to a
// background proxy) can show the feed and burn rate without in-process access.
type Stats struct {
	Requests    int       `json:"requests"`
	SessionCost float64   `json:"session_cost"`
	Started     time.Time `json:"started"`
	Recent      []Event   `json:"recent"`           // newest first
	Limits      []Limits  `json:"limits,omitempty"` // latest per provider, by name
}

const ringSize = 256

// statsFeedSize is how many recent events the /stats endpoint returns.
const statsFeedSize = 32

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
	sessionCost float64           // cumulative USD recorded this session
	limits      map[string]Limits // latest rate-limit state per provider
	started     time.Time
}

func NewRecorder(st *store.Store, engine *cost.Engine) *Recorder {
	return &Recorder{store: st, engine: engine, nowFn: time.Now, started: time.Now()}
}

// SetLimits records the latest rate-limit window state for a provider. A
// snapshot with no windows is ignored so a response lacking the headers doesn't
// wipe a previously-seen state.
func (r *Recorder) SetLimits(l Limits) {
	if len(l.Windows) == 0 {
		return
	}
	r.mu.Lock()
	if r.limits == nil {
		r.limits = make(map[string]Limits)
	}
	r.limits[l.Provider] = l
	r.mu.Unlock()
}

// Snapshot returns the current session stats plus up to n most-recent events
// (newest first). Used both by the in-process TUI and the /stats endpoint.
func (r *Recorder) Snapshot(n int) Stats {
	r.mu.Lock()
	defer r.mu.Unlock()
	if n > len(r.ring) {
		n = len(r.ring)
	}
	recent := make([]Event, n)
	for i := 0; i < n; i++ {
		recent[i] = r.ring[len(r.ring)-1-i]
	}
	var limits []Limits
	if len(r.limits) > 0 {
		provs := make([]string, 0, len(r.limits))
		for p := range r.limits {
			provs = append(provs, p)
		}
		sort.Strings(provs)
		for _, p := range provs {
			limits = append(limits, r.limits[p])
		}
	}
	return Stats{
		Requests:    r.requests,
		SessionCost: r.sessionCost,
		Started:     r.started,
		Recent:      recent,
		Limits:      limits,
	}
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
