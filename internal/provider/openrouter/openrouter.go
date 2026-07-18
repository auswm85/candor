package openrouter

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/auswm85/token-tracker/internal/provider"
)

type Adapter struct {
	apiKey  string
	baseURL string
	client  *http.Client
}

func New(apiKey string) *Adapter {
	return &Adapter{
		apiKey:  apiKey,
		baseURL: "https://openrouter.ai",
		client:  &http.Client{Timeout: 30 * time.Second},
	}
}

func (a *Adapter) Name() string { return "openrouter" }

// WithBaseURL returns a copy pointed at a different base URL (for testing).
func (a *Adapter) WithBaseURL(baseURL string) *Adapter {
	return &Adapter{apiKey: a.apiKey, baseURL: baseURL, client: a.client}
}

// activityResponse mirrors OpenRouter's GET /api/v1/activity payload. Each item
// is a (date, model, endpoint) row; `usage` is the cost in USD.
type activityResponse struct {
	Data []struct {
		Date             string  `json:"date"` // YYYY-MM-DD (UTC)
		Model            string  `json:"model"`
		PromptTokens     int64   `json:"prompt_tokens"`
		CompletionTokens int64   `json:"completion_tokens"`
		ReasoningTokens  int64   `json:"reasoning_tokens"`
		Usage            float64 `json:"usage"` // total cost in USD
	} `json:"data"`
}

func (a *Adapter) PollUsage(ctx context.Context, _ time.Time) ([]provider.UsageRecord, error) {
	var resp activityResponse
	if err := a.get(ctx, a.baseURL+"/api/v1/activity", &resp); err != nil {
		return nil, err
	}

	// Multiple rows can share a (date, model) across endpoints/providers; sum
	// them into one record so it maps to a single usage_records bucket.
	byDayModel := make(map[string]*provider.UsageRecord)
	for _, it := range resp.Data {
		day, err := time.Parse("2006-01-02", it.Date)
		if err != nil {
			continue
		}
		key := it.Date + "|" + it.Model
		if r, ok := byDayModel[key]; ok {
			r.InputTokens += it.PromptTokens
			r.OutputTokens += it.CompletionTokens + it.ReasoningTokens
			r.CostUSD += it.Usage
		} else {
			byDayModel[key] = &provider.UsageRecord{
				Provider:     "openrouter",
				Model:        it.Model, // full prefixed slug, e.g. "openai/gpt-4o"
				BucketStart:  day,
				BucketEnd:    day.Add(24 * time.Hour),
				InputTokens:  it.PromptTokens,
				OutputTokens: it.CompletionTokens + it.ReasoningTokens,
				CostUSD:      it.Usage,
			}
		}
	}

	records := make([]provider.UsageRecord, 0, len(byDayModel))
	for _, r := range byDayModel {
		records = append(records, *r)
	}
	return records, nil
}

func (a *Adapter) get(ctx context.Context, url string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+a.apiKey)

	resp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("http: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read: %w", err)
	}
	if resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("openrouter activity requires a provisioning key — " +
			"create one at https://openrouter.ai/settings/provisioning-keys and store it with `tt auth openrouter` " +
			"(a standard inference key cannot read account activity)")
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("openrouter %s: %s", resp.Status, string(body[:min(len(body), 300)]))
	}
	if err := json.Unmarshal(body, dst); err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	return nil
}
