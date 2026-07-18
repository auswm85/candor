package store

import (
	"database/sql"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/auswm85/token-tracker/db"
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
	RawPayload        string
	FetchedAt         time.Time
}

func Open(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create db dir: %w", err)
		}
	}
	db, err := sql.Open("sqlite", path+"?_journal_mode=WAL")
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
func (s *Store) Migrate() error {
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version    TEXT PRIMARY KEY,
		applied_at TEXT NOT NULL DEFAULT (datetime('now'))
	)`); err != nil {
		return fmt.Errorf("ensure schema_migrations: %w", err)
	}

	entries, err := fs.ReadDir(db.MigrationFiles, "migrations")
	if err != nil {
		return fmt.Errorf("read migrations: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	for _, name := range names {
		var applied int
		if err := s.db.QueryRow(
			`SELECT count(*) FROM schema_migrations WHERE version = ?`, name,
		).Scan(&applied); err != nil {
			return fmt.Errorf("check migration %s: %w", name, err)
		}
		if applied > 0 {
			continue
		}

		sqlBytes, err := fs.ReadFile(db.MigrationFiles, "migrations/"+name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}

		tx, err := s.db.Begin()
		if err != nil {
			return fmt.Errorf("begin %s: %w", name, err)
		}
		if _, err := tx.Exec(string(sqlBytes)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("apply %s: %w", name, err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations (version) VALUES (?)`, name); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("record %s: %w", name, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit %s: %w", name, err)
		}
	}
	return nil
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

func (s *Store) InsertUsage(r UsageRow) error {
	_, err := s.db.Exec(`
		INSERT INTO usage_records
			(provider_id, model_id, bucket_start, bucket_end,
			 input_tokens, cached_input_tokens, cache_write_tokens,
			 output_tokens, cost_usd, raw_payload)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(provider_id, model_id, bucket_start) DO UPDATE SET
			cost_usd = excluded.cost_usd,
			fetched_at = datetime('now')
	`, r.ProviderID, r.ModelID, r.BucketStart.Format(time.RFC3339),
		r.BucketEnd.Format(time.RFC3339), r.InputTokens, r.CachedInputTokens,
		r.CacheWriteTokens, r.OutputTokens, r.CostUSD, r.RawPayload)
	return err
}

func (s *Store) UsageSince(since time.Time) ([]UsageRow, error) {
	rows, err := s.db.Query(`
		SELECT id, provider_id, model_id, bucket_start, bucket_end,
		       input_tokens, cached_input_tokens, cache_write_tokens,
		       output_tokens, cost_usd, raw_payload, fetched_at
		FROM usage_records
		WHERE bucket_start >= ?
		ORDER BY bucket_start DESC
	`, since.Format(time.RFC3339))
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
			&r.OutputTokens, &r.CostUSD, &r.RawPayload, &fetched); err != nil {
			return nil, err
		}
		r.BucketStart, _ = time.Parse(time.RFC3339, start)
		r.BucketEnd, _ = time.Parse(time.RFC3339, end)
		r.FetchedAt, _ = time.Parse(time.RFC3339, fetched)
		results = append(results, r)
	}
	return results, nil
}

func (s *Store) TotalCostSince(since time.Time) (float64, error) {
	var total sql.NullFloat64
	err := s.db.QueryRow(`
		SELECT SUM(cost_usd) FROM usage_records WHERE bucket_start >= ?
	`, since.Format(time.RFC3339)).Scan(&total)
	if err != nil {
		return 0, err
	}
	return total.Float64, nil
}

// ModelCost is a per-model cost aggregate, ordered by spend descending.
type ModelCost struct {
	Provider string
	Model    string
	CostUSD  float64
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
	`, since.Format(time.RFC3339))
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
