package anthropic

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/auswm85/token-tracker/internal/provider"
)

const anthropicVersion = "2023-06-01"

type Adapter struct {
	apiKey  string
	baseURL string
	client  *http.Client
	nowFn   func() time.Time // for testing; defaults to time.Now
}

func New(apiKey string) *Adapter {
	return &Adapter{
		apiKey:  apiKey,
		baseURL: "https://api.anthropic.com",
		client:  &http.Client{Timeout: 30 * time.Second},
		nowFn:   time.Now,
	}
}

func (a *Adapter) Name() string { return "anthropic" }

func (a *Adapter) WithBaseURL(baseURL string) *Adapter {
	return &Adapter{apiKey: a.apiKey, baseURL: baseURL, client: a.client, nowFn: a.nowFn}
}

func (a *Adapter) WithNowFn(fn func() time.Time) *Adapter {
	return &Adapter{apiKey: a.apiKey, baseURL: a.baseURL, client: a.client, nowFn: fn}
}

// --- Messages (API) usage ---

type apiUsageResponse struct {
	Data []struct {
		StartingAt string `json:"starting_at"`
		EndingAt   string `json:"ending_at"`
		Results    []struct {
			Model         string `json:"model"`
			UncachedInput int64  `json:"uncached_input_tokens"`
			CacheRead     int64  `json:"cache_read_input_tokens"`
			CacheCreation struct {
				Ephemeral1H int64 `json:"ephemeral_1h_input_tokens"`
				Ephemeral5M int64 `json:"ephemeral_5m_input_tokens"`
			} `json:"cache_creation"`
			Output int64 `json:"output_tokens"`
		} `json:"results"`
	} `json:"data"`
	HasMore  bool   `json:"has_more"`
	NextPage string `json:"next_page"`
}

func (a *Adapter) pollMessages(ctx context.Context, since time.Time) ([]provider.UsageRecord, error) {
	var records []provider.UsageRecord
	page := ""
	for {
		u, _ := url.Parse(a.baseURL + "/v1/organizations/usage_report/messages")
		q := u.Query()
		q.Set("starting_at", since.Format(time.RFC3339))
		q.Set("ending_at", time.Now().Format(time.RFC3339))
		q.Set("group_by[]", "model")
		q.Set("bucket_width", "1d")
		q.Set("limit", "31")
		if page != "" {
			q.Set("page", page)
		}
		u.RawQuery = q.Encode()

		var report apiUsageResponse
		if err := a.get(ctx, u.String(), &report); err != nil {
			return nil, fmt.Errorf("messages: %w", err)
		}
		for _, bucket := range report.Data {
			start, _ := time.Parse(time.RFC3339, bucket.StartingAt)
			end, _ := time.Parse(time.RFC3339, bucket.EndingAt)
			for _, r := range bucket.Results {
				records = append(records, provider.UsageRecord{
					Provider:          "anthropic",
					Model:             r.Model,
					BucketStart:       start,
					BucketEnd:         end,
					InputTokens:       r.UncachedInput,
					CachedInputTokens: r.CacheRead,
					CacheWriteTokens:  r.CacheCreation.Ephemeral1H + r.CacheCreation.Ephemeral5M,
					OutputTokens:      r.Output,
				})
			}
		}
		if !report.HasMore {
			break
		}
		page = report.NextPage
	}
	return records, nil
}

// --- Claude Code usage ---

type claudeCodeUserMetrics struct {
	InputTokens     int64
	CacheReadTokens int64
	CacheCreate     int64
	OutputTokens    int64
	EstimatedCost   float64
}

type claudeCodeResponse struct {
	Data []struct {
		Date string `json:"date"`
		ModelBreakdown []struct {
			Model  string `json:"model"`
			Tokens struct {
				Input         int64 `json:"input"`
				Output        int64 `json:"output"`
				CacheRead     int64 `json:"cache_read"`
				CacheCreation int64 `json:"cache_creation"`
			} `json:"tokens"`
			EstimatedCost struct {
				Currency string `json:"currency"`
				Amount   int64  `json:"amount"`
			} `json:"estimated_cost"`
		} `json:"model_breakdown"`
	} `json:"data"`
	HasMore  bool   `json:"has_more"`
	NextPage string `json:"next_page"`
}

func (a *Adapter) pollClaudeCode(ctx context.Context, since time.Time) ([]provider.UsageRecord, error) {
	now := a.nowFn().Add(-2 * time.Hour)
	day := since.Truncate(24 * time.Hour)
	recordsByDayModel := make(map[string]*provider.UsageRecord)

	for day.Before(now) {
		page := ""
		dateStr := day.Format("2006-01-02")

		for {
			u, _ := url.Parse(a.baseURL + "/v1/organizations/usage_report/claude_code")
			q := u.Query()
			q.Set("starting_at", dateStr)
			q.Set("limit", "1000")
			if page != "" {
				q.Set("page", page)
			}
			u.RawQuery = q.Encode()

			var resp claudeCodeResponse
			if err := a.get(ctx, u.String(), &resp); err != nil {
				return nil, fmt.Errorf("claude_code %s: %w", dateStr, err)
			}

			for _, entry := range resp.Data {
				for _, mb := range entry.ModelBreakdown {
					cacheWrite := mb.Tokens.CacheCreation
					key := dateStr + "|" + mb.Model
					if existing, ok := recordsByDayModel[key]; ok {
						existing.InputTokens += mb.Tokens.Input
						existing.CachedInputTokens += mb.Tokens.CacheRead
						existing.CacheWriteTokens += cacheWrite
						existing.OutputTokens += mb.Tokens.Output
					} else {
						recordsByDayModel[key] = &provider.UsageRecord{
							Provider:          "anthropic",
							Model:             "claude-code/" + mb.Model,
							BucketStart:       day,
							BucketEnd:         day.Add(24 * time.Hour),
							InputTokens:       mb.Tokens.Input,
							CachedInputTokens: mb.Tokens.CacheRead,
							CacheWriteTokens:  cacheWrite,
							OutputTokens:      mb.Tokens.Output,
						}
					}
				}
			}
			if !resp.HasMore {
				break
			}
			page = resp.NextPage
		}
		day = day.Add(24 * time.Hour)
	}

	records := make([]provider.UsageRecord, 0, len(recordsByDayModel))
	for _, r := range recordsByDayModel {
		records = append(records, *r)
	}
	return records, nil
}

// --- Combined ---

func (a *Adapter) PollUsage(ctx context.Context, since time.Time) ([]provider.UsageRecord, error) {
	apiRecords, err := a.pollMessages(ctx, since)
	if err != nil {
		return nil, err
	}
	codeRecords, err := a.pollClaudeCode(ctx, since)
	if err != nil {
		return nil, err
	}
	return append(apiRecords, codeRecords...), nil
}

// --- HTTP helper ---

func (a *Adapter) get(ctx context.Context, u string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return fmt.Errorf("request: %w", err)
	}
	req.Header.Set("x-api-key", a.apiKey)
	req.Header.Set("anthropic-version", anthropicVersion)

	resp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("anthropic %s: %s", resp.Status, string(body[:min(len(body), 500)]))
	}
	if err := json.Unmarshal(body, dst); err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	return nil
}