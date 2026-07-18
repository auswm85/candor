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

	if err := s.InsertUsage(r); err != nil {
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
		if err := s.InsertUsage(r); err != nil {
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

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}
