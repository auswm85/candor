package app

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/auswm85/candor/internal/config"
	"github.com/auswm85/candor/internal/cost"
	"github.com/auswm85/candor/internal/store"
)

func TestValidateProxyListen(t *testing.T) {
	cases := []struct {
		addr    string
		allow   bool
		wantErr bool
	}{
		{"127.0.0.1:7879", false, false},
		{"localhost:7879", false, false}, // not an IP; not rejected
		{"[::1]:7879", false, false},
		{"0.0.0.0:7879", false, true},
		{":7879", false, true},
		{"192.168.1.5:7879", false, true},
		{"0.0.0.0:7879", true, false}, // override allows it
		{"192.168.1.5:7879", true, false},
		{"not-an-addr", false, true}, // unparseable address
		{"not-an-addr", true, false}, // override skips parsing entirely
	}
	for _, c := range cases {
		err := ValidateProxyListen(c.addr, c.allow)
		if (err != nil) != c.wantErr {
			t.Errorf("ValidateProxyListen(%q, allow=%v) err=%v, wantErr=%v", c.addr, c.allow, err, c.wantErr)
		}
	}
}

func TestProxyChildEnv(t *testing.T) {
	cfg := &config.Config{}
	cfg.Proxy.Upstreams = map[string]string{
		"anthropic":  "https://api.anthropic.com",
		"openai":     "https://api.openai.com",
		"openrouter": "https://openrouter.ai",
	}
	listen := "127.0.0.1:7879"

	// Scoped to one provider → only that provider's env var, with correct suffix.
	got := ProxyChildEnv(cfg, listen, []string{"anthropic"})
	want := []string{"ANTHROPIC_BASE_URL=http://127.0.0.1:7879/anthropic"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("anthropic-only = %v, want %v", got, want)
	}

	// openai carries the /v1 suffix and both env-var aliases.
	got = ProxyChildEnv(cfg, listen, []string{"openai"})
	for _, w := range []string{
		"OPENAI_BASE_URL=http://127.0.0.1:7879/openai/v1",
		"OPENAI_API_BASE=http://127.0.0.1:7879/openai/v1",
	} {
		if !contains(got, w) {
			t.Errorf("openai env missing %q, got %v", w, got)
		}
	}

	// Empty providers → all configured upstreams covered.
	got = ProxyChildEnv(cfg, listen, nil)
	for _, w := range []string{"ANTHROPIC_BASE_URL", "OPENAI_BASE_URL", "OPENROUTER_BASE_URL"} {
		found := false
		for _, g := range got {
			if strings.HasPrefix(g, w+"=") {
				found = true
			}
		}
		if !found {
			t.Errorf("all-providers env missing %s, got %v", w, got)
		}
	}
}

func TestProjectMonthValue(t *testing.T) {
	// July has 31 days. 10 days elapsed, $50 spent → 50/10*31 = $155.
	jul10 := time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC) // 10 full days into July
	if got := ProjectMonthValue(50, jul10); abs(got-155) > 0.001 {
		t.Errorf("July projection = %.2f, want 155.00 (uses 31 days, not 30)", got)
	}

	// February 2026 has 28 days. 14 days in, $28 → 28/14*28 = $56.
	feb15 := time.Date(2026, 2, 15, 0, 0, 0, 0, time.UTC)
	if got := ProjectMonthValue(28, feb15); abs(got-56) > 0.001 {
		t.Errorf("Feb projection = %.2f, want 56.00", got)
	}

	// December rollover must not panic and uses 31 days.
	dec10 := time.Date(2026, 12, 11, 0, 0, 0, 0, time.UTC)
	if got := ProjectMonthValue(100, dec10); abs(got-310) > 0.001 {
		t.Errorf("Dec projection = %.2f, want 310.00", got)
	}
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

func TestOpenCodeConfigContent(t *testing.T) {
	cfg := &config.Config{}
	cfg.Proxy.Upstreams = map[string]string{
		"openai": "x", "anthropic": "x", "openrouter": "x",
	}
	listen := "127.0.0.1:7879"

	// Scoped to openrouter → only that provider's baseURL, with the /api/v1 path.
	got, err := OpenCodeConfigContent(cfg, listen, []string{"openrouter"})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"provider":{"openrouter":{"options":{"baseURL":"http://127.0.0.1:7879/openrouter/api/v1"}}}}`
	if got != want {
		t.Errorf("scoped =\n %s\nwant\n %s", got, want)
	}

	// All providers → valid JSON containing each proxy baseURL.
	all, err := OpenCodeConfigContent(cfg, listen, nil)
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Provider map[string]struct {
			Options struct {
				BaseURL string `json:"baseURL"`
			} `json:"options"`
		} `json:"provider"`
	}
	if err := json.Unmarshal([]byte(all), &parsed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	cases := map[string]string{
		"anthropic":  "http://127.0.0.1:7879/anthropic",
		"openai":     "http://127.0.0.1:7879/openai/v1",
		"openrouter": "http://127.0.0.1:7879/openrouter/api/v1",
	}
	for p, url := range cases {
		if parsed.Provider[p].Options.BaseURL != url {
			t.Errorf("%s baseURL = %q, want %q", p, parsed.Provider[p].Options.BaseURL, url)
		}
	}
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// openTestStore opens a migrated SQLite store in a temp dir, matching the
// convention in internal/store tests.
func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Migrate(); err != nil {
		_ = st.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestProxyUpstreams(t *testing.T) {
	// No configured upstreams → built-in defaults for all three providers.
	cfg := &config.Config{}
	def := ProxyUpstreams(cfg)
	want := map[string]string{
		"openai":     "https://api.openai.com",
		"openrouter": "https://openrouter.ai",
		"anthropic":  "https://api.anthropic.com",
	}
	if len(def) != len(want) {
		t.Fatalf("defaults = %v, want %d entries", def, len(want))
	}
	for p, url := range want {
		if def[p] != url {
			t.Errorf("default upstream %s = %q, want %q", p, def[p], url)
		}
	}

	// Configured upstreams are returned as-is (defaults not merged in).
	cfg.Proxy.Upstreams = map[string]string{"anthropic": "https://example.com"}
	got := ProxyUpstreams(cfg)
	if len(got) != 1 || got["anthropic"] != "https://example.com" {
		t.Errorf("configured = %v, want exactly the configured map", got)
	}
}

func TestProxyListen(t *testing.T) {
	cfg := &config.Config{}
	if got := ProxyListen(cfg); got != "127.0.0.1:7879" {
		t.Errorf("default listen = %q, want 127.0.0.1:7879", got)
	}
	cfg.Proxy.Listen = "127.0.0.1:9999"
	if got := ProxyListen(cfg); got != "127.0.0.1:9999" {
		t.Errorf("configured listen = %q, want 127.0.0.1:9999", got)
	}
}

func TestProxyChildEnv_UnknownProviderSkipped(t *testing.T) {
	cfg := &config.Config{}
	if got := ProxyChildEnv(cfg, "127.0.0.1:7879", []string{"bogus"}); len(got) != 0 {
		t.Errorf("unknown provider = %v, want no env vars", got)
	}
}

func TestOpenCodeConfigContent_UnknownProviderSkipped(t *testing.T) {
	cfg := &config.Config{}
	got, err := OpenCodeConfigContent(cfg, "127.0.0.1:7879", []string{"bogus"})
	if err != nil {
		t.Fatal(err)
	}
	if got != `{"provider":{}}` {
		t.Errorf("unknown provider = %s, want empty provider map", got)
	}
}

func TestProxyHealthy(t *testing.T) {
	hostPort := func(srv *httptest.Server) string {
		return strings.TrimPrefix(srv.URL, "http://")
	}

	// 200 from /healthz → healthy.
	okSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(w, r)
	}))
	defer okSrv.Close()
	if !ProxyHealthy(hostPort(okSrv), time.Second) {
		t.Error("200 /healthz: got unhealthy, want healthy")
	}

	// 500 from /healthz → unhealthy (server answers, but not OK).
	badSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer badSrv.Close()
	if ProxyHealthy(hostPort(badSrv), time.Second) {
		t.Error("500 /healthz: got healthy, want unhealthy")
	}

	// Unreachable (connection refused) → unhealthy.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	dead := l.Addr().String()
	_ = l.Close()
	if ProxyHealthy(dead, 200*time.Millisecond) {
		t.Error("connection refused: got healthy, want unhealthy")
	}

	// Server that never answers within the timeout → unhealthy.
	slowSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(150 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer slowSrv.Close()
	if ProxyHealthy(hostPort(slowSrv), 20*time.Millisecond) {
		t.Error("slow server past client timeout: got healthy, want unhealthy")
	}
}

func TestProjectMonth(t *testing.T) {
	st := openTestStore(t)
	pid, err := st.ProviderID("anthropic")
	if err != nil {
		t.Fatal(err)
	}
	mid, err := st.ModelID(pid, "claude-sonnet-4-5")
	if err != nil {
		t.Fatal(err)
	}
	seed := func(start time.Time, costUSD float64) {
		t.Helper()
		err := st.AddUsage(store.UsageRow{
			ProviderID:  pid,
			ModelID:     mid,
			BucketStart: start,
			BucketEnd:   start.Add(time.Minute),
			CostUSD:     costUSD,
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	now := time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC) // 10 full days into July
	seed(time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC), 30)
	seed(time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC), 20)     // exactly at month start → included
	seed(time.Date(2026, 6, 30, 23, 59, 0, 0, time.UTC), 999) // previous month → excluded

	got, err := ProjectMonth(st, now)
	if err != nil {
		t.Fatal(err)
	}
	// $50 month-to-date, 10 days elapsed, 31 days in July → 50/10*31 = 155.
	if abs(got-155) > 0.001 {
		t.Errorf("ProjectMonth = %.2f, want 155.00 (previous-month rows excluded)", got)
	}

	// Empty store → $0 projection, no error.
	empty := openTestStore(t)
	got, err = ProjectMonth(empty, now)
	if err != nil {
		t.Fatal(err)
	}
	if got != 0 {
		t.Errorf("empty store ProjectMonth = %.2f, want 0", got)
	}
}

func TestProjectMonth_StoreError(t *testing.T) {
	st := openTestStore(t)
	_ = st.Close()
	if _, err := ProjectMonth(st, time.Now()); err == nil {
		t.Error("closed store: got nil error, want error")
	}
}

func TestProjectMonthValue_EdgeCases(t *testing.T) {
	// First hours of the month: elapsed clamps to 1 day so the projection
	// doesn't explode toward infinity.
	jul1 := time.Date(2026, 7, 1, 6, 0, 0, 0, time.UTC) // 6h elapsed → clamp to 1 day
	if got := ProjectMonthValue(10, jul1); abs(got-310) > 0.001 {
		t.Errorf("first-day projection = %.2f, want 310.00 (10 * 31 days)", got)
	}

	// Zero spend → zero projection.
	if got := ProjectMonthValue(0, time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)); got != 0 {
		t.Errorf("zero spend projection = %.2f, want 0", got)
	}
}

func TestBuildEngine(t *testing.T) {
	cfg := &config.Config{}
	cfg.Database = filepath.Join(t.TempDir(), "tokens.db")
	cfg.Pricing.Source = "" // disable dynamic pricing → bundled defaults, no network

	eng := BuildEngine(cfg)
	if eng == nil {
		t.Fatal("BuildEngine returned nil")
	}
	// Bundled defaults: claude-sonnet-4-5 input is $3 / 1M tokens.
	if got := eng.Compute("anthropic", "claude-sonnet-4-5", 1_000_000, 0, 0, 0); abs(got-3) > 1e-9 {
		t.Errorf("Compute(sonnet, 1M input) = %v, want 3.00 (bundled default prices)", got)
	}
	if got := eng.Compute("unknown", "unknown", 1_000_000, 0, 0, 0); got != 0 {
		t.Errorf("Compute(unknown) = %v, want 0", got)
	}
}

func TestBuildRecorder(t *testing.T) {
	st := openTestStore(t)
	rec := BuildRecorder(st, cost.New(cost.DefaultPrices()))
	if rec == nil {
		t.Fatal("BuildRecorder returned nil")
	}
	snap := rec.Snapshot(10)
	if snap.Requests != 0 || snap.SessionCost != 0 || len(snap.Recent) != 0 {
		t.Errorf("fresh recorder snapshot = %+v, want zeroed", snap)
	}
}

func TestBuildProxy(t *testing.T) {
	st := openTestStore(t)
	cfg := &config.Config{} // default upstreams, MaxBodyBytes 0 → 16 MiB default
	p := BuildProxy(cfg, BuildRecorder(st, cost.New(cost.DefaultPrices())))
	if p == nil {
		t.Fatal("BuildProxy returned nil")
	}

	// /healthz answers 200.
	rr := httptest.NewRecorder()
	p.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rr.Code != http.StatusOK {
		t.Errorf("/healthz = %d, want 200", rr.Code)
	}

	// /stats answers 200 with a JSON body.
	rr = httptest.NewRecorder()
	p.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/stats", nil))
	if rr.Code != http.StatusOK {
		t.Errorf("/stats = %d, want 200", rr.Code)
	}
	var stats map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &stats); err != nil {
		t.Errorf("/stats body is not valid JSON: %v", err)
	}

	// Unknown provider prefix → 404 (no upstream contacted).
	rr = httptest.NewRecorder()
	p.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/bogus/v1/messages", nil))
	if rr.Code != http.StatusNotFound {
		t.Errorf("unknown provider = %d, want 404", rr.Code)
	}
}

func TestStartAlertLoop_NoOp(t *testing.T) {
	cases := []struct {
		name   string
		budget float64
		digest int
	}{
		{"nothing configured", 0, -1},
		{"budget without thresholds is still off when digest disabled", 100, -1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st := openTestStore(t)
			cfg := &config.Config{}
			cfg.Defaults.MonthlyBudgetUSD = c.budget
			if c.budget > 0 {
				cfg.Defaults.AlertThresholds = nil // no thresholds → alerts off
			}
			cfg.Defaults.DailyDigestHour = c.digest

			done := make(chan struct{})
			go func() {
				StartAlertLoop(context.Background(), cfg, st, time.Millisecond)
				close(done)
			}()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("StartAlertLoop did not return promptly for a no-op config")
			}
		})
	}
}

func TestStartAlertLoop_TickUntilCancel(t *testing.T) {
	st := openTestStore(t)
	cfg := &config.Config{}
	// Alerts enabled, but the store is empty → projected spend $0 → no
	// threshold is ever crossed, so no OS notification can fire.
	cfg.Defaults.MonthlyBudgetUSD = 1000
	cfg.Defaults.AlertThresholds = []int{50, 75, 100}
	cfg.Defaults.DailyDigestHour = -1

	ctx, cancel := context.WithCancel(context.Background())
	StartAlertLoop(ctx, cfg, st, 10*time.Millisecond)

	// Let it tick several times, then cancel; must not hang or fire anything.
	time.Sleep(50 * time.Millisecond)
	cancel()
	time.Sleep(30 * time.Millisecond) // let the goroutine observe cancellation

	monthKey := "alert_notified_" + time.Now().Format("2006-01")
	if v, err := st.GetConfigState(monthKey); err != nil {
		t.Fatal(err)
	} else if v != "" {
		t.Errorf("dedup key %s = %q, want unset (no threshold crossed)", monthKey, v)
	}
	events, err := st.RecentAlerts(5)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Errorf("recorded %d alert events, want 0", len(events))
	}
}
