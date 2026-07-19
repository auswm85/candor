package alert

import (
	"strings"
	"testing"
	"time"

	"github.com/auswm85/candor/internal/config"
	"github.com/auswm85/candor/internal/store"
)

func newTestChecker(t *testing.T, budget float64, thresholds []int) *Checker {
	t.Helper()
	st, err := store.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{}
	cfg.Defaults.MonthlyBudgetUSD = budget
	cfg.Defaults.AlertThresholds = thresholds
	c := New(cfg, st)
	c.notify = func(string) error { return nil } // don't fire real OS notifications
	return c
}

func TestChecker_FiresOncePerThreshold(t *testing.T) {
	c := newTestChecker(t, 100, []int{50, 75, 90, 100})

	// Projected $80 → 80% crosses the 75 threshold (and 50).
	msg, err := c.Check(80, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if msg == "" {
		t.Fatal("expected a notification at 80% of budget")
	}

	// Same projection again must not re-notify.
	msg, err = c.Check(80, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if msg != "" {
		t.Fatalf("expected no re-notification, got %q", msg)
	}

	// Rising to 95% crosses the higher 90 threshold → notify again.
	msg, err = c.Check(95, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if msg == "" {
		t.Fatal("expected a notification when crossing the 90% threshold")
	}
}

func TestChecker_LogsHistory(t *testing.T) {
	c := newTestChecker(t, 100, []int{50, 75, 90, 100})

	if _, err := c.Check(80, time.Now()); err != nil { // crosses 75
		t.Fatal(err)
	}
	if _, err := c.Check(95, time.Now()); err != nil { // crosses 90
		t.Fatal(err)
	}

	events, err := c.store.RecentAlerts(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("got %d history events, want 2", len(events))
	}
	if events[0].ThresholdPct != 90 || events[1].ThresholdPct != 75 {
		t.Errorf("history thresholds = %d, %d; want 90, 75", events[0].ThresholdPct, events[1].ThresholdPct)
	}
}

func TestChecker_DailyDigest(t *testing.T) {
	c := newTestChecker(t, 100, []int{50})
	var msgs []string
	c.notify = func(m string) error { msgs = append(msgs, m); return nil }

	now := time.Date(2026, 7, 20, 10, 0, 0, 0, time.UTC) // 10:00 local

	// Disabled (-1) → nothing.
	c.cfg.Defaults.DailyDigestHour = -1
	if m, err := c.DailyDigest(now); err != nil || m != "" {
		t.Fatalf("disabled digest fired: %q (err %v)", m, err)
	}

	// Enabled at 09:00, now is 10:00 → fires once.
	c.cfg.Defaults.DailyDigestHour = 9
	m, err := c.DailyDigest(now)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(m, "Daily digest") || !strings.Contains(m, "month-to-date") {
		t.Fatalf("unexpected digest message: %q", m)
	}

	// Same day again → deduped, no second notification.
	if m2, _ := c.DailyDigest(now); m2 != "" {
		t.Fatalf("digest re-fired same day: %q", m2)
	}

	// Next day but before the hour (08:00 < 09:00) → nothing.
	early := time.Date(2026, 7, 21, 8, 0, 0, 0, time.UTC)
	if m3, _ := c.DailyDigest(early); m3 != "" {
		t.Fatalf("digest fired before the hour: %q", m3)
	}

	if len(msgs) != 1 {
		t.Fatalf("sent %d notifications, want exactly 1", len(msgs))
	}
}

func TestChecker_NoBudgetNoAlert(t *testing.T) {
	c := newTestChecker(t, 0, []int{50})
	msg, err := c.Check(1000, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if msg != "" {
		t.Fatalf("expected no alert with zero budget, got %q", msg)
	}
}

func TestChecker_BelowAllThresholds(t *testing.T) {
	c := newTestChecker(t, 100, []int{50, 75})
	msg, err := c.Check(40, time.Now()) // 40% < lowest threshold
	if err != nil {
		t.Fatal(err)
	}
	if msg != "" {
		t.Fatalf("expected no alert below thresholds, got %q", msg)
	}
}
