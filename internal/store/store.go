package store

import (
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
}

type UsageRow struct {
	ID               int64
	ProviderID       int64
	ModelID          int64
	BucketStart      time.Time
	BucketEnd        time.Time
	InputTokens      int64
	CachedInputTokens int64
	CacheWriteTokens int64
	OutputTokens     int64
	CostUSD          float64
	RawPayload       string
	FetchedAt        time.Time
}

func Open(path string) (*Store, error) {
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

func (s *Store) Migrate(migrationsDir string) error {
	return nil
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
	defer rows.Close()

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