package alert

import (
	"testing"

	"github.com/auswm85/token-tracker/internal/config"
	"github.com/auswm85/token-tracker/internal/store"
)

func newTestChecker(t *testing.T, budget float64, thresholds []int) *Checker {
	t.Helper()
	st, err := store.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Migrate(); err != nil {
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
	msg, err := c.Check(80)
	if err != nil {
		t.Fatal(err)
	}
	if msg == "" {
		t.Fatal("expected a notification at 80% of budget")
	}

	// Same projection again must not re-notify.
	msg, err = c.Check(80)
	if err != nil {
		t.Fatal(err)
	}
	if msg != "" {
		t.Fatalf("expected no re-notification, got %q", msg)
	}

	// Rising to 95% crosses the higher 90 threshold → notify again.
	msg, err = c.Check(95)
	if err != nil {
		t.Fatal(err)
	}
	if msg == "" {
		t.Fatal("expected a notification when crossing the 90% threshold")
	}
}

func TestChecker_NoBudgetNoAlert(t *testing.T) {
	c := newTestChecker(t, 0, []int{50})
	msg, err := c.Check(1000)
	if err != nil {
		t.Fatal(err)
	}
	if msg != "" {
		t.Fatalf("expected no alert with zero budget, got %q", msg)
	}
}

func TestChecker_BelowAllThresholds(t *testing.T) {
	c := newTestChecker(t, 100, []int{50, 75})
	msg, err := c.Check(40) // 40% < lowest threshold
	if err != nil {
		t.Fatal(err)
	}
	if msg != "" {
		t.Fatalf("expected no alert below thresholds, got %q", msg)
	}
}
