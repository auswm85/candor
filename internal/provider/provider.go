package provider

import (
	"context"
	"time"
)

type UsageRecord struct {
	Provider          string
	Model             string
	BucketStart       time.Time
	BucketEnd         time.Time
	InputTokens       int64
	CachedInputTokens int64
	CacheWriteTokens  int64
	OutputTokens      int64
	CostUSD           float64
	RawPayload        string
}

type Provider interface {
	Name() string
	PollUsage(ctx context.Context, since time.Time) ([]UsageRecord, error)
}
