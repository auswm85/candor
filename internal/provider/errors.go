package provider

import "fmt"

// APIError is a non-2xx response from a provider's usage API. Carrying the HTTP
// status lets the poll loop classify failures (auth vs rate-limit vs transient)
// without brittle string matching. Message may include an actionable hint.
type APIError struct {
	Provider string
	Status   int
	Message  string
}

func (e *APIError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return fmt.Sprintf("%s: HTTP %d", e.Provider, e.Status)
}
