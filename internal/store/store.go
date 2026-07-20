package store

import (
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/auswm85/candor/db"
	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
}

type UsageRow struct {
	ID                int64
	ProviderID        int64
	ModelID           int64
	BucketStart       time.Time
	BucketEnd         time.Time
	InputTokens       int64
	CachedInputTokens int64
	CacheWriteTokens  int64
	OutputTokens      int64
	CostUSD           float64
	FetchedAt         time.Time // when the row was last written
}

func Open(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("create db dir: %w", err)
		}
	}
	// Pre-create the DB file with strict perms (also fixes an existing 0644 file).
	// The DB holds usage data, not secrets, but it's still private.
	if path != ":memory:" && !strings.HasPrefix(path, "file::memory:") {
		if f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600); err == nil {
			_ = f.Close()
			_ = os.Chmod(path, 0o600)
		}
	}
	// NOTE: modernc.org/sqlite uses `_pragma=...` DSN params — the mattn-style
	// `_journal_mode=WAL` / `_busy_timeout=N` forms are silently ignored.
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	db.SetMaxOpenConns(1)
	return &Store{db: db}, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

// Migrate applies any embedded SQL migrations that have not yet run. Each file
// is executed once, inside a transaction, and recorded in schema_migrations so
// subsequent calls are idempotent.
// Migrate returns the number of migrations newly applied by this call (0 when
// already up to date).
func (s *Store) Migrate() (int, error) {
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version    TEXT PRIMARY KEY,
		applied_at TEXT NOT NULL DEFAULT (datetime('now'))
	)`); err != nil {
		return 0, fmt.Errorf("ensure schema_migrations: %w", err)
	}

	entries, err := fs.ReadDir(db.MigrationFiles, "migrations")
	if err != nil {
		return 0, fmt.Errorf("read migrations: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	count := 0
	for _, name := range names {
		var exists int
		if err := s.db.QueryRow(
			`SELECT count(*) FROM schema_migrations WHERE version = ?`, name,
		).Scan(&exists); err != nil {
			return count, fmt.Errorf("check migration %s: %w", name, err)
		}
		if exists > 0 {
			continue
		}

		sqlBytes, err := fs.ReadFile(db.MigrationFiles, "migrations/"+name)
		if err != nil {
			return count, fmt.Errorf("read migration %s: %w", name, err)
		}

		tx, err := s.db.Begin()
		if err != nil {
			return count, fmt.Errorf("begin %s: %w", name, err)
		}
		if _, err := tx.Exec(string(sqlBytes)); err != nil {
			_ = tx.Rollback()
			return count, fmt.Errorf("apply %s: %w", name, err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations (version) VALUES (?)`, name); err != nil {
			_ = tx.Rollback()
			return count, fmt.Errorf("record %s: %w", name, err)
		}
		if err := tx.Commit(); err != nil {
			return count, fmt.Errorf("commit %s: %w", name, err)
		}
		count++
	}
	return count, nil
}

// ProviderID returns the row id for a provider name, inserting it if absent.
func (s *Store) ProviderID(name string) (int64, error) {
	if _, err := s.db.Exec(
		`INSERT INTO providers (name) VALUES (?) ON CONFLICT(name) DO NOTHING`, name,
	); err != nil {
		return 0, fmt.Errorf("upsert provider %s: %w", name, err)
	}
	var id int64
	if err := s.db.QueryRow(`SELECT id FROM providers WHERE name = ?`, name).Scan(&id); err != nil {
		return 0, fmt.Errorf("lookup provider %s: %w", name, err)
	}
	return id, nil
}

// ModelID returns the row id for a (provider, model) pair, inserting it if absent.
func (s *Store) ModelID(providerID int64, name string) (int64, error) {
	if _, err := s.db.Exec(
		`INSERT INTO models (provider_id, name) VALUES (?, ?) ON CONFLICT(provider_id, name) DO NOTHING`,
		providerID, name,
	); err != nil {
		return 0, fmt.Errorf("upsert model %s: %w", name, err)
	}
	var id int64
	if err := s.db.QueryRow(
		`SELECT id FROM models WHERE provider_id = ? AND name = ?`, providerID, name,
	).Scan(&id); err != nil {
		return 0, fmt.Errorf("lookup model %s: %w", name, err)
	}
	return id, nil
}

// AddUsage additively records usage into a bucket, summing tokens and cost onto
// any existing row for the same (provider, model, bucket_start). This is the
// proxy write path, where each request contributes incrementally.
func (s *Store) AddUsage(r UsageRow) error {
	_, err := s.db.Exec(`
		INSERT INTO usage_records
			(provider_id, model_id, bucket_start, bucket_end,
			 input_tokens, cached_input_tokens, cache_write_tokens,
			 output_tokens, cost_usd)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(provider_id, model_id, bucket_start) DO UPDATE SET
			input_tokens        = input_tokens + excluded.input_tokens,
			cached_input_tokens = cached_input_tokens + excluded.cached_input_tokens,
			cache_write_tokens  = cache_write_tokens + excluded.cache_write_tokens,
			output_tokens       = output_tokens + excluded.output_tokens,
			cost_usd            = cost_usd + excluded.cost_usd,
			fetched_at          = datetime('now')
	`, r.ProviderID, r.ModelID, r.BucketStart.Format(time.RFC3339),
		r.BucketEnd.Format(time.RFC3339), r.InputTokens, r.CachedInputTokens,
		r.CacheWriteTokens, r.OutputTokens, r.CostUSD)
	return err
}

func (s *Store) UsageSince(since time.Time) ([]UsageRow, error) {
	rows, err := s.db.Query(`
		SELECT id, provider_id, model_id, bucket_start, bucket_end,
		       input_tokens, cached_input_tokens, cache_write_tokens,
		       output_tokens, cost_usd, fetched_at
		FROM usage_records
		WHERE bucket_start >= ?
		ORDER BY bucket_start DESC
	`, since.UTC().Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var results []UsageRow
	for rows.Next() {
		var r UsageRow
		var start, end, fetched string
		if err := rows.Scan(&r.ID, &r.ProviderID, &r.ModelID, &start, &end,
			&r.InputTokens, &r.CachedInputTokens, &r.CacheWriteTokens,
			&r.OutputTokens, &r.CostUSD, &fetched); err != nil {
			return nil, err
		}
		r.BucketStart, _ = time.Parse(time.RFC3339, start)
		r.BucketEnd, _ = time.Parse(time.RFC3339, end)
		r.FetchedAt, _ = time.Parse(time.RFC3339, fetched)
		results = append(results, r)
	}
	return results, rows.Err()
}

func (s *Store) TotalCostSince(since time.Time) (float64, error) {
	var total sql.NullFloat64
	err := s.db.QueryRow(`
		SELECT SUM(cost_usd) FROM usage_records WHERE bucket_start >= ?
	`, since.UTC().Format(time.RFC3339)).Scan(&total)
	if err != nil {
		return 0, err
	}
	return total.Float64, nil
}

// TotalTokensSince returns the total tokens (input + cache-read + cache-write +
// output) recorded since the given time.
func (s *Store) TotalTokensSince(since time.Time) (int64, error) {
	var total sql.NullInt64
	err := s.db.QueryRow(`
		SELECT SUM(input_tokens + cached_input_tokens + cache_write_tokens + output_tokens)
		FROM usage_records WHERE bucket_start >= ?
	`, since.UTC().Format(time.RFC3339)).Scan(&total)
	if err != nil {
		return 0, err
	}
	return total.Int64, nil
}

// DayCost is a single day's total cost. Day is "YYYY-MM-DD" in the machine's
// local time zone (buckets are stored in UTC and converted for grouping).
type DayCost struct {
	Day     string
	CostUSD float64
}

// DailyCostSince returns total cost grouped by local calendar day since the
// given time, oldest day first. Buckets are stored in UTC and converted to
// local time (DST-aware, per timestamp) so a day reflects the user's clock.
func (s *Store) DailyCostSince(since time.Time) ([]DayCost, error) {
	rows, err := s.db.Query(`
		SELECT strftime('%Y-%m-%d', bucket_start, 'localtime') AS day, SUM(cost_usd) AS cost
		FROM usage_records
		WHERE bucket_start >= ?
		GROUP BY day
		ORDER BY day ASC
	`, since.UTC().Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []DayCost
	for rows.Next() {
		var d DayCost
		if err := rows.Scan(&d.Day, &d.CostUSD); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// HourCost is total cost within a single clock hour, keyed by the local-time
// hour prefix "2006-01-02T15".
type HourCost struct {
	Hour    string
	CostUSD float64
}

// HourlyCostSince returns total cost grouped by local clock hour since the given
// time, oldest hour first. Used by the TUI's 24-hour trend sparkline. Buckets
// are stored in UTC and converted to local time for grouping.
func (s *Store) HourlyCostSince(since time.Time) ([]HourCost, error) {
	rows, err := s.db.Query(`
		SELECT strftime('%Y-%m-%dT%H', bucket_start, 'localtime') AS hour, SUM(cost_usd) AS cost
		FROM usage_records
		WHERE bucket_start >= ?
		GROUP BY hour
		ORDER BY hour ASC
	`, since.UTC().Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []HourCost
	for rows.Next() {
		var h HourCost
		if err := rows.Scan(&h.Hour, &h.CostUSD); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// GetConfigState returns the stored value for a key, or "" if unset.
func (s *Store) GetConfigState(key string) (string, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM config_state WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return v, nil
}

// SetConfigState upserts a key/value pair in config_state.
func (s *Store) SetConfigState(key, value string) error {
	_, err := s.db.Exec(
		`INSERT INTO config_state (key, value) VALUES (?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		key, value,
	)
	return err
}

// AlertEvent is one budget-threshold notification that was fired.
type AlertEvent struct {
	FiredAt      time.Time
	ThresholdPct int
	ProjectedUSD float64
	BudgetUSD    float64
}

// RecordAlert appends a fired budget alert to the history log.
func (s *Store) RecordAlert(thresholdPct int, projectedUSD, budgetUSD float64) error {
	_, err := s.db.Exec(
		`INSERT INTO alert_events (fired_at, threshold_pct, projected_usd, budget_usd)
		 VALUES (?, ?, ?, ?)`,
		time.Now().UTC().Format(time.RFC3339), thresholdPct, projectedUSD, budgetUSD,
	)
	return err
}

// RecentAlerts returns up to n most-recently fired alerts, newest first.
func (s *Store) RecentAlerts(n int) ([]AlertEvent, error) {
	rows, err := s.db.Query(
		`SELECT fired_at, threshold_pct, projected_usd, budget_usd
		 FROM alert_events ORDER BY fired_at DESC, id DESC LIMIT ?`, n,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []AlertEvent
	for rows.Next() {
		var e AlertEvent
		var fired string
		if err := rows.Scan(&fired, &e.ThresholdPct, &e.ProjectedUSD, &e.BudgetUSD); err != nil {
			return nil, err
		}
		e.FiredAt, _ = time.Parse(time.RFC3339, fired)
		out = append(out, e)
	}
	return out, rows.Err()
}

// ModelUsage is a per-model token + cost aggregate, ordered by spend descending.
type ModelUsage struct {
	Provider   string
	Model      string
	Input      int64
	Cached     int64
	CacheWrite int64
	Output     int64
	CostUSD    float64
}

// ModelUsageSince returns per-model token sums and cost since the given time,
// most expensive first — backing the TUI's top-models and cache-impact views.
func (s *Store) ModelUsageSince(since time.Time) ([]ModelUsage, error) {
	rows, err := s.db.Query(`
		SELECT p.name, m.name,
		       SUM(u.input_tokens), SUM(u.cached_input_tokens),
		       SUM(u.cache_write_tokens), SUM(u.output_tokens), SUM(u.cost_usd)
		FROM usage_records u
		JOIN providers p ON p.id = u.provider_id
		JOIN models    m ON m.id = u.model_id
		WHERE u.bucket_start >= ?
		GROUP BY p.name, m.name
		ORDER BY SUM(u.cost_usd) DESC
	`, since.UTC().Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []ModelUsage
	for rows.Next() {
		var mu ModelUsage
		if err := rows.Scan(&mu.Provider, &mu.Model, &mu.Input, &mu.Cached, &mu.CacheWrite, &mu.Output, &mu.CostUSD); err != nil {
			return nil, err
		}
		out = append(out, mu)
	}
	return out, rows.Err()
}

// ModelCost is a per-model cost aggregate, ordered by spend descending.
type ModelCost struct {
	Provider string
	Model    string
	CostUSD  float64
}

// ExportRow is one raw per-minute usage bucket, joined with provider/model
// names, for data export.
type ExportRow struct {
	BucketStart time.Time
	Provider    string
	Model       string
	Input       int64
	CacheRead   int64
	CacheWrite  int64
	Output      int64
	CostUSD     float64
}

// ExportRows returns raw usage rows in [since, until), oldest first. A zero
// until means "no upper bound".
func (s *Store) ExportRows(since, until time.Time) ([]ExportRow, error) {
	if until.IsZero() {
		until = time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	rows, err := s.db.Query(`
		SELECT u.bucket_start, p.name, m.name,
		       u.input_tokens, u.cached_input_tokens, u.cache_write_tokens,
		       u.output_tokens, u.cost_usd
		FROM usage_records u
		JOIN providers p ON p.id = u.provider_id
		JOIN models    m ON m.id = u.model_id
		WHERE u.bucket_start >= ? AND u.bucket_start < ?
		ORDER BY u.bucket_start ASC
	`, since.UTC().Format(time.RFC3339), until.UTC().Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []ExportRow
	for rows.Next() {
		var r ExportRow
		var start string
		if err := rows.Scan(&start, &r.Provider, &r.Model,
			&r.Input, &r.CacheRead, &r.CacheWrite, &r.Output, &r.CostUSD); err != nil {
			return nil, err
		}
		r.BucketStart, _ = time.Parse(time.RFC3339, start)
		out = append(out, r)
	}
	return out, rows.Err()
}

// CostByModelSince returns total cost grouped by model since the given time,
// most expensive first.
func (s *Store) CostByModelSince(since time.Time) ([]ModelCost, error) {
	rows, err := s.db.Query(`
		SELECT p.name, m.name, SUM(u.cost_usd) AS cost
		FROM usage_records u
		JOIN providers p ON p.id = u.provider_id
		JOIN models    m ON m.id = u.model_id
		WHERE u.bucket_start >= ?
		GROUP BY p.name, m.name
		ORDER BY cost DESC
	`, since.UTC().Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []ModelCost
	for rows.Next() {
		var mc ModelCost
		if err := rows.Scan(&mc.Provider, &mc.Model, &mc.CostUSD); err != nil {
			return nil, err
		}
		out = append(out, mc)
	}
	return out, rows.Err()
}
