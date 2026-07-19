package alert

import (
	"fmt"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/auswm85/candor/internal/config"
	"github.com/auswm85/candor/internal/store"
)

type Checker struct {
	cfg    *config.Config
	store  *store.Store
	notify func(string) error // overridable in tests; defaults to OS notifier
}

func New(cfg *config.Config, st *store.Store) *Checker {
	return &Checker{cfg: cfg, store: st, notify: notify}
}

// Check compares projected monthly spend against the configured budget
// thresholds and sends an OS notification the first time each threshold is
// crossed within a calendar month. It returns the message sent, or "" if no
// new threshold was crossed. Repeated calls within the same month do not
// re-notify for a threshold already alerted.
func (c *Checker) Check(projectedMonth float64, now time.Time) (string, error) {
	budget := c.cfg.Defaults.MonthlyBudgetUSD
	if budget <= 0 || len(c.cfg.Defaults.AlertThresholds) == 0 {
		return "", nil
	}
	pct := projectedMonth / budget * 100

	// Highest threshold currently crossed.
	crossed := 0
	for _, t := range c.cfg.Defaults.AlertThresholds {
		if int(pct) >= t && t > crossed {
			crossed = t
		}
	}
	if crossed == 0 {
		return "", nil
	}

	// Dedup: only fire when crossing a higher threshold than already alerted
	// this month. The month is part of the key, so it resets automatically.
	monthKey := "alert_notified_" + now.Format("2006-01")
	prev := 0
	if c.store != nil {
		if v, err := c.store.GetConfigState(monthKey); err == nil && v != "" {
			prev, _ = strconv.Atoi(v)
		}
	}
	if crossed <= prev {
		return "", nil
	}

	msg := fmt.Sprintf("%d%% of monthly budget — projected $%.0f / $%.0f",
		crossed, projectedMonth, budget)
	_ = c.notify(msg)
	if c.store != nil {
		if err := c.store.SetConfigState(monthKey, strconv.Itoa(crossed)); err != nil {
			return msg, err
		}
		// Append to the alert history log (best-effort; a failed log write
		// shouldn't mask a delivered notification).
		_ = c.store.RecordAlert(crossed, projectedMonth, budget)
	}
	return msg, nil
}

// DailyDigest sends one summary notification per day, at or after the configured
// `daily_digest_hour`: yesterday's spend, month-to-date, and remaining budget.
// No-op when the digest hour is unset (-1) or already sent today. Returns the
// message sent, or "" if nothing was sent.
func (c *Checker) DailyDigest(now time.Time) (string, error) {
	hour := c.cfg.Defaults.DailyDigestHour
	if hour < 0 || hour > 23 || c.store == nil {
		return "", nil
	}
	if now.Hour() < hour {
		return "", nil // not yet time today
	}
	key := "digest_" + now.Format("2006-01-02")
	if v, err := c.store.GetConfigState(key); err == nil && v != "" {
		return "", nil // already sent today
	}

	loc := now.Location()
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	yesterdayStart := todayStart.AddDate(0, 0, -1)
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, loc)

	// yesterday = (cost since yesterday-start) − (cost since today-start).
	sinceYesterday, err := c.store.TotalCostSince(yesterdayStart)
	if err != nil {
		return "", err
	}
	today, err := c.store.TotalCostSince(todayStart)
	if err != nil {
		return "", err
	}
	mtd, err := c.store.TotalCostSince(monthStart)
	if err != nil {
		return "", err
	}
	yesterday := sinceYesterday - today

	msg := fmt.Sprintf("Daily digest — yesterday $%.2f · month-to-date $%.2f", yesterday, mtd)
	if budget := c.cfg.Defaults.MonthlyBudgetUSD; budget > 0 {
		msg += fmt.Sprintf(" · $%.2f of $%.0f left", budget-mtd, budget)
	}
	_ = c.notify(msg)
	if err := c.store.SetConfigState(key, "1"); err != nil {
		return msg, err
	}
	return msg, nil
}

func notify(msg string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("osascript", "-e",
			fmt.Sprintf(`display notification %q with title "candor"`, msg)).Run()
	case "linux":
		return exec.Command("notify-send", "candor", msg).Run()
	case "windows":
		// Single-quote the message: PowerShell single-quoted strings don't
		// interpolate $vars / $(...), so amounts like "$92" render literally and
		// nothing in the message can be evaluated. Embedded single quotes are
		// escaped by doubling them.
		ps := "'" + strings.ReplaceAll(msg, "'", "''") + "'"
		script := "New-BurntToastNotification -Text 'candor', " + ps
		return exec.Command("powershell", "-NoProfile", "-Command", script).Run()
	}
	return nil
}
