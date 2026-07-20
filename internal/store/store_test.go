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

	if _, err := s.Migrate(); err != nil {
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

	if _, err := s.Migrate(); err != nil {
		t.Fatalf("first migrate: %v", err)
	}
	// Running again must not fail (schema already applied).
	if _, err := s.Migrate(); err != nil {
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
	if _, err := s.Migrate(); err != nil {
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
	if _, err := s.Migrate(); err != nil {
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

	// Days are grouped in the machine's local zone (buckets stored UTC). Derive
	// the expectation the same way so this stays correct in any test-runner tz.
	want := map[string]float64{}
	for _, r := range rows {
		want[r.BucketStart.In(time.Local).Format("2006-01-02")] += r.CostUSD
	}
	if len(got) != len(want) {
		t.Fatalf("got %d days, want %d: %+v", len(got), len(want), got)
	}
	for i, d := range got {
		if abs(d.CostUSD-want[d.Day]) > 0.0001 {
			t.Errorf("day %q cost = %v, want %v", d.Day, d.CostUSD, want[d.Day])
		}
		if i > 0 && got[i-1].Day > d.Day {
			t.Errorf("days not ascending: %q before %q", got[i-1].Day, d.Day)
		}
	}
}

// TestStore_TotalCostSince_LocalBoundary guards the cross-offset comparison bug:
// buckets are stored as UTC RFC3339 ("…Z"), so a `since` carrying a non-UTC zone
// offset must be normalized to UTC before the (string) comparison — otherwise a
// bucket one minute in the future would be wrongly counted as "since". Uses a
// fixed zone so it fails on the bug regardless of the runner's own timezone.
func TestStore_TotalCostSince_LocalBoundary(t *testing.T) {
	s, err := Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Migrate(); err != nil {
		t.Fatal(err)
	}

	pid, _ := s.ProviderID("anthropic")
	mid, _ := s.ModelID(pid, "claude-sonnet-4-5")
	t0 := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	if err := s.AddUsage(UsageRow{ProviderID: pid, ModelID: mid, BucketStart: t0, BucketEnd: t0.Add(time.Minute), CostUSD: 3.00}); err != nil {
		t.Fatal(err)
	}

	west := time.FixedZone("west", -7*3600) // same instants, non-UTC offset

	// since == t0 → bucket is included.
	if got, _ := s.TotalCostSince(t0.In(west)); abs(got-3.00) > 0.0001 {
		t.Errorf("TotalCostSince(t0) = %v, want 3.00", got)
	}
	// since one minute after t0 → bucket is in the past, must be excluded.
	if got, _ := s.TotalCostSince(t0.Add(time.Minute).In(west)); got != 0 {
		t.Errorf("TotalCostSince(t0+1m) = %v, want 0 (bucket precedes the bound)", got)
	}
}

func TestStore_ModelUsageSince(t *testing.T) {
	s, err := Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Migrate(); err != nil {
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

func TestStore_ExportRows(t *testing.T) {
	s, err := Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Migrate(); err != nil {
		t.Fatal(err)
	}
	pid, _ := s.ProviderID("openrouter")
	mid, _ := s.ModelID(pid, "deepseek-chat")

	jan01 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	jan15 := time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC)
	feb01 := time.Date(2026, 2, 1, 10, 0, 0, 0, time.UTC)
	for _, bs := range []time.Time{jan01, jan15, feb01} {
		if err := s.AddUsage(UsageRow{ProviderID: pid, ModelID: mid, BucketStart: bs, BucketEnd: bs.Add(time.Minute),
			InputTokens: 100, CachedInputTokens: 20, CacheWriteTokens: 5, OutputTokens: 50, CostUSD: 0.01}); err != nil {
			t.Fatal(err)
		}
	}

	// January only: [Jan 1, Feb 1) → 2 rows, oldest first, Feb excluded.
	rows, err := s.ExportRows(jan01, feb01)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2 (Feb excluded): %+v", len(rows), rows)
	}
	if !rows[0].BucketStart.Equal(jan01) || rows[1].BucketStart.Before(rows[0].BucketStart) {
		t.Errorf("rows not oldest-first: %+v", rows)
	}
	r := rows[0]
	if r.Provider != "openrouter" || r.Model != "deepseek-chat" ||
		r.Input != 100 || r.CacheRead != 20 || r.CacheWrite != 5 || r.Output != 50 {
		t.Errorf("row fields = %+v", r)
	}

	// Zero until → no upper bound → all 3.
	all, err := s.ExportRows(jan01, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("open-ended export got %d rows, want 3", len(all))
	}
}

func TestStore_TotalTokensSince(t *testing.T) {
	s, err := Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Migrate(); err != nil {
		t.Fatal(err)
	}
	pid, _ := s.ProviderID("openai")
	mid, _ := s.ModelID(pid, "gpt-4o")
	base := time.Date(2026, 7, 18, 0, 0, 0, 0, time.UTC)

	_ = s.AddUsage(UsageRow{ProviderID: pid, ModelID: mid, BucketStart: base, BucketEnd: base.Add(time.Hour),
		InputTokens: 1000, CachedInputTokens: 500, CacheWriteTokens: 100, OutputTokens: 200})
	_ = s.AddUsage(UsageRow{ProviderID: pid, ModelID: mid, BucketStart: base.Add(time.Minute), BucketEnd: base.Add(time.Hour),
		InputTokens: 300, OutputTokens: 50})

	// 1800 (first row: 1000+500+100+200) + 350 (second) = 2150.
	got, err := s.TotalTokensSince(base.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if got != 2150 {
		t.Fatalf("total tokens = %d, want 2150", got)
	}
}

func TestStore_AlertEvents(t *testing.T) {
	s, err := Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Migrate(); err != nil {
		t.Fatal(err)
	}

	// Empty to start.
	if got, err := s.RecentAlerts(5); err != nil || len(got) != 0 {
		t.Fatalf("empty RecentAlerts = %v, %v", got, err)
	}

	if err := s.RecordAlert(75, 77.5, 100); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordAlert(90, 92.0, 100); err != nil {
		t.Fatal(err)
	}

	got, err := s.RecentAlerts(5)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d events, want 2", len(got))
	}
	// Newest first; the 90% event was recorded last.
	if got[0].ThresholdPct != 90 || abs(got[0].ProjectedUSD-92.0) > 0.001 || abs(got[0].BudgetUSD-100) > 0.001 {
		t.Errorf("newest event = %+v", got[0])
	}
	if got[1].ThresholdPct != 75 {
		t.Errorf("second event = %+v", got[1])
	}
	if got[0].FiredAt.IsZero() {
		t.Error("FiredAt not parsed")
	}
}

// TestStore_ConfigState covers the alert-dedup state the budget-alert loop
// depends on: a fired threshold is recorded per month and must survive
// restarts, so set→get round-trip and upsert-overwrite semantics matter.
func TestStore_ConfigState(t *testing.T) {
	s, err := Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Migrate(); err != nil {
		t.Fatal(err)
	}

	// Missing key → zero value, no error (ErrNoRows is swallowed by design).
	if v, err := s.GetConfigState("alert.fired.2026-07.50"); err != nil || v != "" {
		t.Fatalf("GetConfigState(missing) = %q, %v; want \"\", nil", v, err)
	}

	// set → get round-trip.
	if err := s.SetConfigState("alert.fired.2026-07.50", "1"); err != nil {
		t.Fatal(err)
	}
	if v, err := s.GetConfigState("alert.fired.2026-07.50"); err != nil || v != "1" {
		t.Fatalf("GetConfigState = %q, %v; want \"1\", nil", v, err)
	}

	// Upsert overwrites.
	if err := s.SetConfigState("alert.fired.2026-07.50", "2"); err != nil {
		t.Fatal(err)
	}
	if v, err := s.GetConfigState("alert.fired.2026-07.50"); err != nil || v != "2" {
		t.Fatalf("GetConfigState(after upsert) = %q, %v; want \"2\", nil", v, err)
	}

	// Other keys are unaffected.
	if v, err := s.GetConfigState("alert.fired.2026-07.75"); err != nil || v != "" {
		t.Fatalf("GetConfigState(other key) = %q, %v; want \"\", nil", v, err)
	}

	// An empty value round-trips without error.
	if err := s.SetConfigState("empty", ""); err != nil {
		t.Fatal(err)
	}
	if v, err := s.GetConfigState("empty"); err != nil || v != "" {
		t.Fatalf("GetConfigState(empty value) = %q, %v; want \"\", nil", v, err)
	}
}

// TestStore_ConfigState_NoTable documents the pre-migration contract: both
// accessors fail loudly when config_state doesn't exist yet.
func TestStore_ConfigState_NoTable(t *testing.T) {
	s, err := Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if _, err := s.GetConfigState("k"); err == nil {
		t.Error("GetConfigState before Migrate: expected error, got nil")
	}
	if err := s.SetConfigState("k", "v"); err == nil {
		t.Error("SetConfigState before Migrate: expected error, got nil")
	}
}

func TestStore_HourlyCostSince(t *testing.T) {
	s, err := Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Migrate(); err != nil {
		t.Fatal(err)
	}

	pid, _ := s.ProviderID("anthropic")
	sonnet, _ := s.ModelID(pid, "claude-sonnet-4-5")
	haiku, _ := s.ModelID(pid, "claude-haiku-4-5")

	hourA := time.Date(2026, 7, 18, 10, 5, 0, 0, time.UTC)
	hourAb := time.Date(2026, 7, 18, 10, 40, 0, 0, time.UTC) // same UTC hour, other model
	hourB := time.Date(2026, 7, 18, 13, 15, 0, 0, time.UTC)

	rows := []UsageRow{
		{ProviderID: pid, ModelID: sonnet, BucketStart: hourA, BucketEnd: hourA.Add(time.Minute), CostUSD: 1.00},
		{ProviderID: pid, ModelID: haiku, BucketStart: hourAb, BucketEnd: hourAb.Add(time.Minute), CostUSD: 0.50},
		{ProviderID: pid, ModelID: sonnet, BucketStart: hourB, BucketEnd: hourB.Add(time.Minute), CostUSD: 2.00},
	}
	for _, r := range rows {
		if err := s.AddUsage(r); err != nil {
			t.Fatal(err)
		}
	}

	got, err := s.HourlyCostSince(time.Date(2026, 7, 18, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}

	// Hours group in the machine's local zone (buckets stored UTC). Derive the
	// expectation the same way so this stays correct in any test-runner tz.
	want := map[string]float64{}
	for _, r := range rows {
		want[r.BucketStart.In(time.Local).Format("2006-01-02T15")] += r.CostUSD
	}
	if len(got) != len(want) {
		t.Fatalf("got %d hours, want %d: %+v", len(got), len(want), got)
	}
	for i, h := range got {
		if abs(h.CostUSD-want[h.Hour]) > 0.0001 {
			t.Errorf("hour %q cost = %v, want %v", h.Hour, h.CostUSD, want[h.Hour])
		}
		if i > 0 && got[i-1].Hour > h.Hour {
			t.Errorf("hours not ascending: %q before %q", got[i-1].Hour, h.Hour)
		}
	}

	// Rows before the since bound are excluded, and an empty range yields an
	// empty result (not an error).
	got, err = s.HourlyCostSince(time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("HourlyCostSince(after all rows) = %+v, want empty", got)
	}
}

// TestStore_MigrateCount pins the applied-count return: first call applies the
// pending files, subsequent calls are no-ops.
func TestStore_MigrateCount(t *testing.T) {
	s, err := Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	n, err := s.Migrate()
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Error("first Migrate applied 0 migrations, want > 0")
	}
	if n, err := s.Migrate(); err != nil || n != 0 {
		t.Errorf("second Migrate = %d, %v; want 0, nil", n, err)
	}
}

// TestStore_MigrateCorruptFile exercises the "ensure schema_migrations" error
// path: a file that isn't a SQLite database fails the first statement.
func TestStore_MigrateCorruptFile(t *testing.T) {
	path := t.TempDir() + "/test.db"
	if err := os.WriteFile(path, []byte("this is not a sqlite database"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if _, err := s.Migrate(); err == nil {
		t.Fatal("Migrate on a corrupt file: expected error, got nil")
	}
}

// TestStore_MigrateBadSchemaMigrationsTable exercises the "check migration"
// error path: schema_migrations exists but lacks the version column, so the
// per-file existence query fails.
func TestStore_MigrateBadSchemaMigrationsTable(t *testing.T) {
	s, err := Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if _, err := s.db.Exec(`CREATE TABLE schema_migrations (foo TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Migrate(); err == nil {
		t.Fatal("Migrate with a malformed schema_migrations table: expected error, got nil")
	}
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}
