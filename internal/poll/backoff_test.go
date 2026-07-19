package poll

import (
	"errors"
	"testing"
	"time"

	"github.com/auswm85/token-tracker/internal/provider"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		err  error
		want errKind
	}{
		{&provider.APIError{Provider: "anthropic", Status: 401}, kindPermanent},
		{&provider.APIError{Provider: "openrouter", Status: 403}, kindPermanent},
		{&provider.APIError{Provider: "openai", Status: 429}, kindRateLimited},
		{&provider.APIError{Provider: "openai", Status: 500}, kindTransient},
		{errors.New("dial tcp: connection refused"), kindTransient},
		// wrapped API error still classifies (adapters wrap with %w)
		{errWrap(&provider.APIError{Status: 401}), kindPermanent},
	}
	for _, c := range cases {
		if got := classify(c.err); got != c.want {
			t.Errorf("classify(%v) = %d, want %d", c.err, got, c.want)
		}
	}
}

func errWrap(err error) error { return errorsJoinLike{err} }

type errorsJoinLike struct{ e error }

func (w errorsJoinLike) Error() string { return "wrapped: " + w.e.Error() }
func (w errorsJoinLike) Unwrap() error { return w.e }

func TestStateStore_PermanentSkipAndBackoffReset(t *testing.T) {
	s := newStateStore()
	now := time.Now()

	// Permanent (401): skipped forever after one failure.
	perm := s.recordFailure("anthropic", &provider.APIError{Status: 401}, now)
	if !perm {
		t.Fatal("401 should be reported permanent")
	}
	if !s.shouldSkip("anthropic", now.Add(time.Hour)) {
		t.Fatal("permanent provider should stay skipped")
	}

	// Transient: backoff window, then eligible again.
	s.recordFailure("openai", &provider.APIError{Status: 500}, now)
	if !s.shouldSkip("openai", now.Add(30*time.Second)) {
		t.Fatal("should skip inside the first backoff (1m) window")
	}
	if s.shouldSkip("openai", now.Add(2*time.Minute)) {
		t.Fatal("should be eligible after the backoff window")
	}

	// Success clears state.
	s.recordSuccess("openai")
	if s.shouldSkip("openai", now) {
		t.Fatal("success should reset backoff")
	}
}

func TestBackoff_Curve(t *testing.T) {
	want := []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 16 * time.Minute, 30 * time.Minute, 30 * time.Minute}
	for i, w := range want {
		if got := backoff(i + 1); got != w {
			t.Errorf("backoff(%d) = %s, want %s", i+1, got, w)
		}
	}
}
