package poll

import (
	"errors"
	"math"
	"net/http"
	"sync"
	"time"

	"github.com/auswm85/token-tracker/internal/provider"
)

// providerState tracks per-provider poll health for backoff and skip decisions.
type providerState struct {
	consecutiveFails int
	nextPollAfter    time.Time
	permanent        bool // 401/403: don't retry until the daemon restarts
}

type stateStore struct {
	mu     sync.Mutex
	states map[string]*providerState
}

func newStateStore() *stateStore { return &stateStore{states: map[string]*providerState{}} }

// shouldSkip reports whether the provider should be skipped this cycle (a
// permanent auth failure, or still inside a backoff window).
func (s *stateStore) shouldSkip(name string, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.states[name]
	if st == nil {
		return false
	}
	return st.permanent || now.Before(st.nextPollAfter)
}

func (s *stateStore) recordSuccess(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.states[name] = &providerState{}
}

// recordFailure updates backoff state and reports whether this failure is
// permanent (so the caller can log an appropriate hint).
func (s *stateStore) recordFailure(name string, err error, now time.Time) (permanent bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.states[name]
	if st == nil {
		st = &providerState{}
		s.states[name] = st
	}
	st.consecutiveFails++
	if classify(err) == kindPermanent {
		st.permanent = true
		return true
	}
	st.nextPollAfter = now.Add(backoff(st.consecutiveFails))
	return false
}

type errKind int

const (
	kindTransient errKind = iota
	kindRateLimited
	kindPermanent
)

// classify uses the provider's typed API error status when available (auth vs
// rate-limit vs everything-else). Network/decode errors are transient.
func classify(err error) errKind {
	var apiErr *provider.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.Status {
		case http.StatusUnauthorized, http.StatusForbidden:
			return kindPermanent
		case http.StatusTooManyRequests:
			return kindRateLimited
		}
	}
	return kindTransient
}

// backoff is exponential: 1m, 2m, 4m, 8m, 16m, capped at 30m.
func backoff(fails int) time.Duration {
	if fails <= 0 {
		return 0
	}
	d := time.Duration(math.Pow(2, float64(fails-1))) * time.Minute
	if d > 30*time.Minute {
		d = 30 * time.Minute
	}
	return d
}
