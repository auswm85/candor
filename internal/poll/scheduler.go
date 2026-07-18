package poll

import (
	"context"
	"log"
	"time"

	"github.com/auswm85/token-tracker/internal/provider"
	"github.com/auswm85/token-tracker/internal/store"
)

type Scheduler struct {
	providers []provider.Provider
	store     *store.Store
	interval  time.Duration
}

func New(providers []provider.Provider, s *store.Store, interval time.Duration) *Scheduler {
	return &Scheduler{
		providers: providers,
		store:     s,
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
	for _, p := range s.providers {
		records, err := p.PollUsage(ctx, time.Now().Add(-48*time.Hour))
		if err != nil {
			log.Printf("poll %s: %v", p.Name(), err)
			continue
		}
		log.Printf("poll %s: %d records", p.Name(), len(records))
		_ = records
	}
}