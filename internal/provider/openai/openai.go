package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
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
		baseURL: "https://api.openai.com",
		client:  &http.Client{Timeout: 30 * time.Second},
	}
}

func (a *Adapter) Name() string { return "openai" }

// WithBaseURL returns a copy pointed at a different base URL (for testing).
func (a *Adapter) WithBaseURL(baseURL string) *Adapter {
	return &Adapter{apiKey: a.apiKey, baseURL: baseURL, client: a.client}
}

// costsResponse mirrors GET /v1/organization/costs. Note `amount.value` is a
// string (arbitrary precision), and each result's `line_item` encodes the model
// and token tier, e.g. "gpt-4o-mini-2024-07-18, input".
type costsResponse struct {
	Data []struct {
		StartTime int64 `json:"start_time"`
		Results   []struct {
			Amount struct {
				Value string `json:"value"`
			} `json:"amount"`
			Quantity float64 `json:"quantity"`
			LineItem string  `json:"line_item"`
		} `json:"results"`
	} `json:"data"`
	HasMore  bool   `json:"has_more"`
	NextPage string `json:"next_page"`
}

func (a *Adapter) PollUsage(ctx context.Context, since time.Time) ([]provider.UsageRecord, error) {
	byDayModel := make(map[string]*provider.UsageRecord)

	page := ""
	for {
		q := url.Values{}
		q.Set("start_time", strconv.FormatInt(since.Unix(), 10))
		q.Set("bucket_width", "1d")
		q.Add("group_by[]", "line_item")
		q.Set("limit", "180")
		if page != "" {
			q.Set("page", page)
		}

		var resp costsResponse
		if err := a.get(ctx, a.baseURL+"/v1/organization/costs?"+q.Encode(), &resp); err != nil {
			return nil, err
		}

		for _, bucket := range resp.Data {
			day := time.Unix(bucket.StartTime, 0).UTC()
			key := day.Format("2006-01-02")
			for _, r := range bucket.Results {
				model, tier := parseLineItem(r.LineItem)
				if model == "" {
					continue
				}
				cost, _ := strconv.ParseFloat(r.Amount.Value, 64)
				qty := int64(r.Quantity)

				rec := byDayModel[key+"|"+model]
				if rec == nil {
					rec = &provider.UsageRecord{
						Provider:    "openai",
						Model:       model,
						BucketStart: day,
						BucketEnd:   day.Add(24 * time.Hour),
					}
					byDayModel[key+"|"+model] = rec
				}
				rec.CostUSD += cost
				switch {
				case strings.Contains(tier, "output"):
					rec.OutputTokens += qty
				case strings.Contains(tier, "cach"):
					rec.CachedInputTokens += qty
				case strings.Contains(tier, "input"):
					rec.InputTokens += qty
				}
			}
		}

		if !resp.HasMore || resp.NextPage == "" {
			break
		}
		page = resp.NextPage
	}

	records := make([]provider.UsageRecord, 0, len(byDayModel))
	for _, r := range byDayModel {
		records = append(records, *r)
	}
	return records, nil
}

// parseLineItem splits "gpt-4o-mini-2024-07-18, input" into ("gpt-4o-mini-2024-07-18", "input").
// The tier is lower-cased; model names contain no ", " so the first separator wins.
func parseLineItem(li string) (model, tier string) {
	if i := strings.Index(li, ", "); i >= 0 {
		return li[:i], strings.ToLower(li[i+2:])
	}
	return li, ""
}

func (a *Adapter) get(ctx context.Context, u string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
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
	if resp.StatusCode == http.StatusUnauthorized {
		return &provider.APIError{Provider: "openai", Status: resp.StatusCode, Message: "openai 401: the costs API requires an Admin key " +
			"(create one at https://platform.openai.com/settings/organization/admin-keys and store it with `tt auth openai`); " +
			"a standard project/inference key cannot read organization costs"}
	}
	if resp.StatusCode != http.StatusOK {
		return &provider.APIError{Provider: "openai", Status: resp.StatusCode,
			Message: fmt.Sprintf("openai %s: %s", resp.Status, string(body[:min(len(body), 300)]))}
	}
	if err := json.Unmarshal(body, dst); err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	return nil
}
