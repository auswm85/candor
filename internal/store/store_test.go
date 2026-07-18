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

	// Migrate manually (no golang-migrate wired yet)
	schema, err := os.ReadFile("../../db/migrations/001_init.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(string(schema)); err != nil {
		t.Fatal(err)
	}

	r := UsageRow{
		ProviderID:       1,
		ModelID:          1,
		BucketStart:      time.Date(2026, 7, 18, 0, 0, 0, 0, time.UTC),
		BucketEnd:        time.Date(2026, 7, 18, 0, 5, 0, 0, time.UTC),
		InputTokens:      1000,
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

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}