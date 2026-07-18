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