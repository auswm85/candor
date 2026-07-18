package poll

import (
	"context"
	"log"
	"time"

	"github.com/auswm85/token-tracker/internal/alert"
	"github.com/auswm85/token-tracker/internal/cost"
	"github.com/auswm85/token-tracker/internal/provider"
	"github.com/auswm85/token-tracker/internal/store"
)

// lookback is how far back each poll re-fetches. Records are upserted by
// (provider, model, bucket_start), so overlapping windows are idempotent.
const lookback = 31 * 24 * time.Hour

type Scheduler struct {
	providers []provider.Provider
	store     *store.Store
	engine    *cost.Engine
	alerter   *alert.Checker
	interval  time.Duration
}

func New(providers []provider.Provider, s *store.Store, engine *cost.Engine, alerter *alert.Checker, interval time.Duration) *Scheduler {
	return &Scheduler{
		providers: providers,
		store:     s,
		engine:    engine,
		alerter:   alerter,
		interval:  interval,
	}
}

func (s *Scheduler) Start(ctx context.Context) {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	// Run immediately on start
	s.pollAll(ctx)

	for {
		select {
		case <-ticker.C:
			s.pollAll(ctx)
		case <-ctx.Done():
			return
		}
	}
}

func (s *Scheduler) pollAll(ctx context.Context) {
	since := time.Now().Add(-lookback)
	for _, p := range s.providers {
		records, err := p.PollUsage(ctx, since)
		if err != nil {
			log.Printf("poll %s: %v", p.Name(), err)
			continue
		}
		stored, err := s.persist(records)
		if err != nil {
			log.Printf("persist %s: %v", p.Name(), err)
			continue
		}
		log.Printf("poll %s: %d records stored", p.Name(), stored)
	}

	if err := s.store.SetConfigState("last_poll", time.Now().Format(time.RFC3339)); err != nil {
		log.Printf("record last_poll: %v", err)
	}
	s.checkAlerts()
}

// checkAlerts projects month-to-date spend forward and lets the alert checker
// decide whether a threshold notification should fire.
func (s *Scheduler) checkAlerts() {
	if s.alerter == nil {
		return
	}
	now := time.Now()
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	spend, err := s.store.TotalCostSince(monthStart)
	if err != nil {
		log.Printf("alert: month spend: %v", err)
		return
	}
	projected := s.engine.ProjectMonthly("", monthStart, spend)
	msg, err := s.alerter.Check(projected)
	if err != nil {
		log.Printf("alert: %v", err)
		return
	}
	if msg != "" {
		log.Printf("alert fired: %s", msg)
	}
}

// persist costs each record (using the provider-supplied cost when present,
// otherwise the pricing engine) and writes it to the store.
func (s *Scheduler) persist(records []provider.UsageRecord) (int, error) {
	count := 0
	for _, r := range records {
		providerID, err := s.store.ProviderID(r.Provider)
		if err != nil {
			return count, err
		}
		modelID, err := s.store.ModelID(providerID, r.Model)
		if err != nil {
			return count, err
		}

		costUSD := r.CostUSD
		if costUSD == 0 {
			costUSD = s.engine.Compute(r.Provider, r.Model,
				r.InputTokens, r.CachedInputTokens, r.CacheWriteTokens, r.OutputTokens)
		}

		if err := s.store.InsertUsage(store.UsageRow{
			ProviderID:        providerID,
			ModelID:           modelID,
			BucketStart:       r.BucketStart,
			BucketEnd:         r.BucketEnd,
			InputTokens:       r.InputTokens,
			CachedInputTokens: r.CachedInputTokens,
			CacheWriteTokens:  r.CacheWriteTokens,
			OutputTokens:      r.OutputTokens,
			CostUSD:           costUSD,
			RawPayload:        r.RawPayload,
		}); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}
