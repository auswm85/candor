package alert

import (
	"fmt"
	"os/exec"
	"runtime"
	"time"

	"github.com/auswm85/token-tracker/internal/config"
)

type Checker struct {
	cfg *config.Config
}

func New(cfg *config.Config) *Checker {
	return &Checker{cfg: cfg}
}

func (c *Checker) Check(projectedMonth float64, currentCost float64, period string, since time.Time) ([]string, error) {
	budget := c.cfg.Defaults.MonthlyBudgetUSD
	if budget <= 0 {
		return nil, nil
	}

	var triggers []string
	pct := (projectedMonth / budget) * 100

	for _, threshold := range c.cfg.Defaults.AlertThresholds {
		if int(pct) >= threshold {
			msg := fmt.Sprintf("tracker: %.0f%% of monthly budget (%.0f%% = $%.0f / $%.0f)",
				pct, pct, projectedMonth, budget)
			triggers = append(triggers, msg)
			_ = notify(msg)
		}
	}
	return triggers, nil
}

func notify(msg string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("osascript", "-e",
			fmt.Sprintf(`display notification "%s" with title "token-tracker"`, msg)).Run()
	case "linux":
		return exec.Command("notify-send", "token-tracker", msg).Run()
	}
	return nil
}

func (c *Checker) Run() {
	for {
		time.Sleep(5 * time.Minute)
	}
}