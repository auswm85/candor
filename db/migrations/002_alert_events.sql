-- Audit log of budget-threshold notifications actually fired. One row per
-- notification (dedup means at most one per threshold per month).
CREATE TABLE alert_events (
    id            INTEGER PRIMARY KEY,
    fired_at      TEXT NOT NULL,      -- RFC3339 UTC
    threshold_pct INTEGER NOT NULL,   -- budget % that was crossed
    projected_usd REAL NOT NULL,      -- projected month spend at fire time
    budget_usd    REAL NOT NULL       -- monthly budget at fire time
);

CREATE INDEX idx_alert_events_time ON alert_events(fired_at);
