package poll

import (
	"context"
	"testing"
	"time"

	"github.com/auswm85/candor/internal/cost"
	"github.com/auswm85/candor/internal/provider"
	"github.com/auswm85/candor/internal/store"
)

type fakeProvider struct {
	name    string
	records []provider.UsageRecord
}

func (f *fakeProvider) Name() string { return f.name }
func (f *fakeProvider) PollUsage(_ context.Context, _ time.Time) ([]provider.UsageRecord, error) {
	return f.records, nil
}

func TestScheduler_PersistCostsAndStores(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}

	base := time.Date(2026, 7, 18, 0, 0, 0, 0, time.UTC)
	fp := &fakeProvider{
		name: "anthropic",
		records: []provider.UsageRecord{
			// Tokens only → cost computed by the engine.
			// 1M input @ $3 + 1M output @ $15 = $18.00
			{
				Provider:     "anthropic",
				Model:        "claude-sonnet-4-5",
				BucketStart:  base,
				BucketEnd:    base.Add(24 * time.Hour),
				InputTokens:  1_000_000,
				OutputTokens: 1_000_000,
			},
			// Provider-supplied cost → passed through unchanged.
			{
				Provider:    "openrouter",
				Model:       "openai/gpt-4o",
				BucketStart: base,
				BucketEnd:   base.Add(24 * time.Hour),
				CostUSD:     4.20,
			},
		},
	}

	engine := cost.New(cost.DefaultPrices())
	// nil alerter: alert checking is exercised separately in the alert package.
	s := New([]provider.Provider{fp}, st, engine, nil, time.Minute)
	s.pollAll(context.Background())

	total, err := st.TotalCostSince(base.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if want := 18.00 + 4.20; abs(total-want) > 0.0001 {
		t.Fatalf("total = %v, want %v", total, want)
	}

	// Re-polling the same window must be idempotent (upsert, not double-count).
	s.pollAll(context.Background())
	total2, err := st.TotalCostSince(base.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if abs(total2-total) > 0.0001 {
		t.Fatalf("re-poll changed total: %v != %v", total2, total)
	}
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}
