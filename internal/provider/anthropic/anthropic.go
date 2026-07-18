// Package anthropic implements the Provider interface for Anthropic's Usage & Cost API.
// Requires an Admin API key (sk-ant-admin01-...), not a standard API key.
// Endpoints:
//
//	Usage: GET /v1/organizations/usage_report/messages?starting_at=...&ending_at=...&group_by[]=model&bucket_width=1d
//	Cost:  GET /v1/organizations/cost_report?starting_at=...&ending_at=...&group_by[]=model
package anthropic

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/auswm85/token-tracker/internal/provider"
)

type Adapter struct {
	apiKey string
	client *http.Client
}

func New(apiKey string) *Adapter {
	return &Adapter{
		apiKey: apiKey,
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

func (a *Adapter) Name() string { return "anthropic" }

func (a *Adapter) PollUsage(ctx context.Context, since time.Time) ([]provider.UsageRecord, error) {
	return nil, fmt.Errorf("not yet implemented")
}