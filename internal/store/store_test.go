package store

import (
	"os"
	"testing"
	"time"
)

func TestStore_InsertAndQuery(t *testing.T) {
	path := t.TempDir() + "/test.db"
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	defer os.Remove(path)

	if err := s.Migrate(); err != nil {
		t.Fatal(err)
	}

	r := UsageRow{
		ProviderID:        1,
		ModelID:           1,
		BucketStart:       time.Date(2026, 7, 18, 0, 0, 0, 0, time.UTC),
		BucketEnd:         time.Date(2026, 7, 18, 0, 5, 0, 0, time.UTC),
		InputTokens:       1000,
		CachedInputTokens: 500,
		CacheWriteTokens:  100,
		OutputTokens:      200,
		CostUSD:           0.025,
	}

	if err := s.AddUsage(r); err != nil {
		t.Fatal(err)
	}

	rows, err := s.UsageSince(time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}

	total, err := s.TotalCostSince(time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if abs(total-0.025) > 0.0001 {
		t.Fatalf("total = %v, want 0.025", total)
	}
}

func TestStore_MigrateIdempotent(t *testing.T) {
	path := t.TempDir() + "/test.db"
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := s.Migrate(); err != nil {
		t.Fatalf("first migrate: %v", err)
	}
	// Running again must not fail (schema already applied).
	if err := s.Migrate(); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
}

func TestStore_UpsertAndByModel(t *testing.T) {
	path := t.TempDir() + "/test.db"
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Migrate(); err != nil {
		t.Fatal(err)
	}

	pid, err := s.ProviderID("anthropic")
	if err != nil {
		t.Fatal(err)
	}
	// Idempotent: same name returns same id.
	if pid2, _ := s.ProviderID("anthropic"); pid2 != pid {
		t.Fatalf("provider id changed: %d != %d", pid2, pid)
	}

	sonnet, err := s.ModelID(pid, "claude-sonnet-4-5")
	if err != nil {
		t.Fatal(err)
	}
	haiku, err := s.ModelID(pid, "claude-haiku-4-5")
	if err != nil {
		t.Fatal(err)
	}
	if sonnet == haiku {
		t.Fatal("distinct models share an id")
	}

	base := time.Date(2026, 7, 18, 0, 0, 0, 0, time.UTC)
	must := func(r UsageRow) {
		if err := s.AddUsage(r); err != nil {
			t.Fatal(err)
		}
	}
	must(UsageRow{ProviderID: pid, ModelID: sonnet, BucketStart: base, BucketEnd: base.Add(time.Hour), CostUSD: 2.00})
	must(UsageRow{ProviderID: pid, ModelID: haiku, BucketStart: base, BucketEnd: base.Add(time.Hour), CostUSD: 0.50})

	rows, err := s.CostByModelSince(base.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d model rows, want 2", len(rows))
	}
	// Ordered by cost descending.
	if rows[0].Model != "claude-sonnet-4-5" || abs(rows[0].CostUSD-2.00) > 0.0001 {
		t.Fatalf("unexpected top row: %+v", rows[0])
	}
	if rows[1].Model != "claude-haiku-4-5" {
		t.Fatalf("unexpected second row: %+v", rows[1])
	}
}

func TestStore_DailyCostSince(t *testing.T) {
	path := t.TempDir() + "/test.db"
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Migrate(); err != nil {
		t.Fatal(err)
	}

	pid, _ := s.ProviderID("anthropic")
	mid, _ := s.ModelID(pid, "claude-sonnet-4-5")
	day1 := time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC)
	day2 := time.Date(2026, 7, 18, 9, 0, 0, 0, time.UTC)
	day2b := time.Date(2026, 7, 18, 14, 0, 0, 0, time.UTC)

	rows := []UsageRow{
		{ProviderID: pid, ModelID: mid, BucketStart: day1, BucketEnd: day1.Add(time.Hour), CostUSD: 1.00},
		{ProviderID: pid, ModelID: mid, BucketStart: day2, BucketEnd: day2.Add(time.Hour), CostUSD: 2.00},
		{ProviderID: pid, ModelID: mid, BucketStart: day2b, BucketEnd: day2b.Add(time.Hour), CostUSD: 0.50},
	}
	for _, r := range rows {
		if err := s.AddUsage(r); err != nil {
			t.Fatal(err)
		}
	}

	got, err := s.DailyCostSince(time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d days, want 2: %+v", len(got), got)
	}
	// Oldest first, and the two day-2 rows are summed.
	if got[0].Day != "2026-07-17" || abs(got[0].CostUSD-1.00) > 0.0001 {
		t.Fatalf("day 0 = %+v", got[0])
	}
	if got[1].Day != "2026-07-18" || abs(got[1].CostUSD-2.50) > 0.0001 {
		t.Fatalf("day 1 = %+v", got[1])
	}
}

func TestStore_ModelUsageSince(t *testing.T) {
	s, err := Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Migrate(); err != nil {
		t.Fatal(err)
	}
	pid, _ := s.ProviderID("anthropic")
	sonnet, _ := s.ModelID(pid, "claude-sonnet-4-5")
	haiku, _ := s.ModelID(pid, "claude-haiku-4-5")
	base := time.Date(2026, 7, 18, 0, 0, 0, 0, time.UTC)

	_ = s.AddUsage(UsageRow{ProviderID: pid, ModelID: sonnet, BucketStart: base, BucketEnd: base.Add(time.Hour),
		InputTokens: 1000, CachedInputTokens: 500, CacheWriteTokens: 100, OutputTokens: 200, CostUSD: 2.00})
	_ = s.AddUsage(UsageRow{ProviderID: pid, ModelID: haiku, BucketStart: base, BucketEnd: base.Add(time.Hour),
		InputTokens: 300, OutputTokens: 50, CostUSD: 0.50})

	rows, err := s.ModelUsageSince(base.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
	// Ordered by cost desc; token sums present.
	if rows[0].Model != "claude-sonnet-4-5" || rows[0].Input != 1000 || rows[0].Cached != 500 || rows[0].CacheWrite != 100 {
		t.Errorf("top row = %+v", rows[0])
	}
	if rows[1].Model != "claude-haiku-4-5" {
		t.Errorf("second row = %+v", rows[1])
	}
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}
