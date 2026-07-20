package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/auswm85/candor/internal/store"
)

func TestHumanBytes(t *testing.T) {
	cases := []struct {
		n    int64
		want string
	}{
		{0, "0 B"},
		{1, "1 B"},
		{1023, "1023 B"},
		{1024, "1.0 KB"},
		{1536, "1.5 KB"},
		{1048576, "1.0 MB"},
		{1073741824, "1.0 GB"},
		{2147483648, "2.0 GB"},
	}
	for _, c := range cases {
		t.Run(c.want, func(t *testing.T) {
			if got := humanBytes(c.n); got != c.want {
				t.Errorf("humanBytes(%d) = %q, want %q", c.n, got, c.want)
			}
		})
	}
}

func TestDbSize(t *testing.T) {
	// Non-existent file → 0.
	if got := dbSize("/nonexistent/path/to/db"); got != 0 {
		t.Errorf("non-existent file = %d, want 0", got)
	}

	// Existing file returns its size.
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")
	if err := os.WriteFile(path, []byte("hello world"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := dbSize(path); got != 11 {
		t.Errorf("dbSize = %d, want 11", got)
	}
}

func TestNewProxyServer(t *testing.T) {
	h := http.NotFoundHandler()
	srv := newProxyServer("127.0.0.1:9999", h)

	if srv.Addr != "127.0.0.1:9999" {
		t.Errorf("Addr = %q, want 127.0.0.1:9999", srv.Addr)
	}
	if srv.ReadHeaderTimeout != 10*time.Second {
		t.Errorf("ReadHeaderTimeout = %v", srv.ReadHeaderTimeout)
	}
	if srv.IdleTimeout != 120*time.Second {
		t.Errorf("IdleTimeout = %v", srv.IdleTimeout)
	}
	if srv.MaxHeaderBytes != 1<<20 {
		t.Errorf("MaxHeaderBytes = %d", srv.MaxHeaderBytes)
	}
	if srv.WriteTimeout != 0 {
		t.Errorf("WriteTimeout = %v, want 0 (no WriteTimeout for streaming)", srv.WriteTimeout)
	}
}

func TestShutdownOnDone(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer ts.Close()

	// Build a server on a random port for shutdown testing.
	h := http.NotFoundHandler()
	srv := &http.Server{Addr: "127.0.0.1:0", Handler: h}
	ctx, cancel := context.WithCancel(context.Background())

	// Cancel the context, which should trigger shutdown.
	cancel()
	shutdownOnDone(ctx, srv) // blocks until shutdown completes or times out
	// If we reach here without hanging forever, shutdown completed.
}

func TestRotateLog(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "daemon.log")

	// Under threshold → no rotation.
	if err := os.WriteFile(log, []byte("small"), 0o600); err != nil {
		t.Fatal(err)
	}
	rotateLogAt(log, 100)
	if _, err := os.Stat(log + ".1"); err == nil {
		t.Fatal("rotated a small file")
	}

	// Over threshold → current becomes .1, and prior generations shift down.
	_ = os.WriteFile(log, []byte("current-generation-that-is-over-threshold"), 0o600)
	_ = os.WriteFile(log+".1", []byte("gen1"), 0o600)
	_ = os.WriteFile(log+".2", []byte("gen2"), 0o600)
	_ = os.WriteFile(log+".3", []byte("gen3-should-be-dropped"), 0o600)

	rotateLogAt(log, 10)

	if b, _ := os.ReadFile(log + ".1"); !strings.HasPrefix(string(b), "current") {
		t.Errorf(".1 = %q, want the rotated current log", b)
	}
	if b, _ := os.ReadFile(log + ".2"); string(b) != "gen1" {
		t.Errorf(".2 = %q, want gen1", b)
	}
	if b, _ := os.ReadFile(log + ".3"); string(b) != "gen2" {
		t.Errorf(".3 = %q, want gen2", b)
	}
	if _, err := os.Stat(log + ".4"); err == nil {
		t.Error("kept a .4; should cap at .3")
	}
	if _, err := os.Stat(log); err == nil {
		t.Error("original log still present after rotation")
	}
}

// rotateLog (the wrapper used at dashboard startup) rotates with the package
// defaults, same as rotateLogAt.
func TestRotateLog_Wrapper(t *testing.T) {
	log := filepath.Join(t.TempDir(), "daemon.log")
	if err := os.WriteFile(log, make([]byte, logMaxBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	rotateLog(log)
	if _, err := os.Stat(log + ".1"); err != nil {
		t.Error("expected rotation to daemon.log.1")
	}
	if _, err := os.Stat(log); err == nil {
		t.Error("original log still present after rotation")
	}
}

func sampleRows() []store.ExportRow {
	return []store.ExportRow{
		{
			BucketStart: time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC),
			Provider:    "openrouter", Model: "deepseek-chat",
			Input: 100, CacheRead: 20, CacheWrite: 5, Output: 50, CostUSD: 0.012345,
		},
	}
}

func TestWriteExportCSV(t *testing.T) {
	var b bytes.Buffer
	if err := writeExportCSV(&b, sampleRows()); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want header + 1 row:\n%s", len(lines), out)
	}
	if lines[0] != "bucket_start,provider,model,input,cache_read,cache_write,output,cost_usd" {
		t.Errorf("header = %q", lines[0])
	}
	want := "2026-01-01T10:00:00Z,openrouter,deepseek-chat,100,20,5,50,0.012345"
	if lines[1] != want {
		t.Errorf("row =\n %q\nwant\n %q", lines[1], want)
	}
}

func TestWriteExportJSON(t *testing.T) {
	var b bytes.Buffer
	if err := writeExportJSON(&b, sampleRows()); err != nil {
		t.Fatal(err)
	}
	var got []struct {
		BucketStart string `json:"bucket_start"`
		Provider    string `json:"provider"`
		Model       string `json:"model"`
		Tokens      struct {
			Input      int64 `json:"input"`
			CacheRead  int64 `json:"cache_read"`
			CacheWrite int64 `json:"cache_write"`
			Output     int64 `json:"output"`
		} `json:"tokens"`
		CostUSD float64 `json:"cost_usd"`
	}
	if err := json.Unmarshal(b.Bytes(), &got); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, b.String())
	}
	if len(got) != 1 {
		t.Fatalf("got %d records, want 1", len(got))
	}
	r := got[0]
	if r.BucketStart != "2026-01-01T10:00:00Z" || r.Provider != "openrouter" ||
		r.Tokens.CacheRead != 20 || r.Tokens.CacheWrite != 5 || r.CostUSD != 0.012345 {
		t.Errorf("record = %+v", r)
	}
}

// Empty result sets must still produce valid output (header-only CSV, [] JSON).
func TestWriteExport_Empty(t *testing.T) {
	var csvBuf, jsonBuf bytes.Buffer
	if err := writeExportCSV(&csvBuf, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(csvBuf.String(), "bucket_start,") {
		t.Errorf("empty CSV missing header: %q", csvBuf.String())
	}
	if err := writeExportJSON(&jsonBuf, nil); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(jsonBuf.String()) != "[]" {
		t.Errorf("empty JSON = %q, want []", jsonBuf.String())
	}
}

// ---------------------------------------------------------------------------
// Cobra subcommand tests
//
// The command tree is wired in main(), so tests replicate that wiring once
// (setupCommands) and drive commands via rootCmd.SetArgs + Execute with HOME
// pointed at a temp dir — config then resolves the database under
// $HOME/.local/share/candor/tokens.db. Commands write to os.Stdout directly,
// so output is captured through a pipe rather than cobra's OutOrStdout.
// ---------------------------------------------------------------------------

var cmdSetupOnce sync.Once

// setupCommands replicates the flag registration and command wiring main()
// performs, so tests can execute the cobra command tree in-process.
func setupCommands() {
	cmdSetupOnce.Do(func() {
		runCmd.Flags().SetInterspersed(false)
		runCmd.Flags().StringSlice("provider", nil, "Provider base URLs to route (default: all configured); repeatable")
		spendCmd.Flags().Bool("by-model", false, "Break spend down by model")
		exportCmd.Flags().String("since", "", "Start date, inclusive (YYYY-MM-DD) — required")
		exportCmd.Flags().String("until", "", "End date, inclusive (YYYY-MM-DD); default: now")
		exportCmd.Flags().String("format", "csv", "Output format: csv or json")
		statusCmd.Flags().Bool("json", false, "Output status as JSON")
		rootCmd.AddCommand(proxyCmd, runCmd, tuiCmd, spendCmd, exportCmd, statusCmd, migrateCmd, serviceCmd)
		// Tests assert on the returned error; keep cobra from printing usage.
		rootCmd.SilenceUsage = true
		rootCmd.SilenceErrors = true
	})
}

// resetFlags restores every command flag to its default. The cobra commands
// are package-level singletons, so a flag set in one test would otherwise leak
// into the next execution.
func resetFlags() {
	_ = spendCmd.Flags().Set("by-model", "false")
	_ = exportCmd.Flags().Set("since", "")
	_ = exportCmd.Flags().Set("until", "")
	_ = exportCmd.Flags().Set("format", "csv")
	_ = statusCmd.Flags().Set("json", "false")
}

// captureStdout runs fn with os.Stdout redirected to a pipe and returns what
// was written. Command output under test is always small (well under the pipe
// buffer), so this never blocks.
func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = old }()
	runErr := fn()
	_ = w.Close()
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(out), runErr
}

// executeCommand runs the root cobra command with args under an isolated HOME
// and returns captured stdout plus the command's error.
func executeCommand(t *testing.T, home string, args ...string) (string, error) {
	t.Helper()
	setupCommands()
	resetFlags()
	t.Setenv("HOME", home)
	rootCmd.SetArgs(args)
	return captureStdout(t, rootCmd.Execute)
}

// dbPath mirrors config's default database location for a given HOME.
func dbPath(home string) string {
	return filepath.Join(home, ".local", "share", "candor", "tokens.db")
}

// writeConfig writes a config.yaml into home's candor config dir.
func writeConfig(t *testing.T, home, content string) {
	t.Helper()
	dir := filepath.Join(home, ".config", "candor")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

type seedRow struct {
	provider, model string
	bucket          time.Time
	cost            float64
	input, output   int64
}

// seedUsage records usage rows directly into home's database.
func seedUsage(t *testing.T, home string, rows ...seedRow) {
	t.Helper()
	st, err := store.Open(dbPath(home))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if _, err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		pid, err := st.ProviderID(r.provider)
		if err != nil {
			t.Fatal(err)
		}
		mid, err := st.ModelID(pid, r.model)
		if err != nil {
			t.Fatal(err)
		}
		if err := st.AddUsage(store.UsageRow{
			ProviderID:   pid,
			ModelID:      mid,
			BucketStart:  r.bucket,
			BucketEnd:    r.bucket.Add(time.Minute),
			InputTokens:  r.input,
			OutputTokens: r.output,
			CostUSD:      r.cost,
		}); err != nil {
			t.Fatal(err)
		}
	}
}

// --- spend -----------------------------------------------------------------

func TestSpendCmd_Today(t *testing.T) {
	home := t.TempDir()
	now := time.Now()
	seedUsage(t, home,
		seedRow{provider: "openai", model: "gpt-4o", bucket: now, cost: 2.00},
		// 45 days back is always outside both today and the current month.
		seedRow{provider: "openai", model: "gpt-4o", bucket: now.AddDate(0, 0, -45), cost: 99.00},
	)
	out, err := executeCommand(t, home, "spend", "today")
	if err != nil {
		t.Fatal(err)
	}
	if want := "Today spend: $2.00\n"; out != want {
		t.Errorf("out = %q, want %q", out, want)
	}
}

func TestSpendCmd_Month(t *testing.T) {
	home := t.TempDir()
	now := time.Now()
	seedUsage(t, home,
		seedRow{provider: "openai", model: "gpt-4o", bucket: now, cost: 2.00},
		seedRow{provider: "openai", model: "gpt-4o", bucket: now.AddDate(0, 0, -45), cost: 99.00},
	)
	out, err := executeCommand(t, home, "spend", "month")
	if err != nil {
		t.Fatal(err)
	}
	if want := "Month spend: $2.00\n"; out != want {
		t.Errorf("out = %q, want %q", out, want)
	}
}

// No period argument defaults to today.
func TestSpendCmd_DefaultPeriod(t *testing.T) {
	home := t.TempDir()
	out, err := executeCommand(t, home, "spend")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "Today spend: $") {
		t.Errorf("out = %q, want a Today spend line", out)
	}
}

func TestSpendCmd_EmptyDB(t *testing.T) {
	home := t.TempDir()
	out, err := executeCommand(t, home, "spend", "today")
	if err != nil {
		t.Fatal(err)
	}
	if want := "Today spend: $0.00\n"; out != want {
		t.Errorf("out = %q, want %q", out, want)
	}
}

func TestSpendCmd_UnknownPeriod(t *testing.T) {
	home := t.TempDir()
	_, err := executeCommand(t, home, "spend", "banana")
	if err == nil || !strings.Contains(err.Error(), `unknown period "banana"`) {
		t.Errorf("err = %v, want unknown period error", err)
	}
}

func TestSpendCmd_TooManyArgs(t *testing.T) {
	home := t.TempDir()
	if _, err := executeCommand(t, home, "spend", "today", "month"); err == nil {
		t.Fatal("want an arg-count error")
	}
}

func TestSpendCmd_ByModel(t *testing.T) {
	home := t.TempDir()
	now := time.Now()
	seedUsage(t, home,
		seedRow{provider: "openai", model: "gpt-4o", bucket: now, cost: 3.00},
		// Same model in a second bucket — must aggregate to 3.50.
		seedRow{provider: "openai", model: "gpt-4o", bucket: now.Add(-time.Minute), cost: 0.50},
		seedRow{provider: "anthropic", model: "claude-sonnet", bucket: now, cost: 1.00},
	)
	out, err := executeCommand(t, home, "spend", "month", "--by-model")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		fmt.Sprintf("  %-32s $%8.2f\n", "openai/gpt-4o", 3.50),
		fmt.Sprintf("  %-32s $%8.2f\n", "anthropic/claude-sonnet", 1.00),
		fmt.Sprintf("  %-32s $%8.2f\n", "TOTAL", 4.50),
	} {
		if !strings.Contains(out, want) {
			t.Errorf("out missing %q:\n%s", want, out)
		}
	}
	// Most expensive first.
	if strings.Index(out, "openai/gpt-4o") > strings.Index(out, "anthropic/claude-sonnet") {
		t.Errorf("models not ordered by spend desc:\n%s", out)
	}
}

func TestSpendCmd_ByModelEmpty(t *testing.T) {
	home := t.TempDir()
	out, err := executeCommand(t, home, "spend", "today", "--by-model")
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("No usage recorded since %s.\n", time.Now().Format("2006-01-02"))
	if out != want {
		t.Errorf("out = %q, want %q", out, want)
	}
}

// --- status ----------------------------------------------------------------

func TestStatusCmd_Text(t *testing.T) {
	home := t.TempDir()
	// Point the proxy probe at a certainly-closed port so the result is
	// deterministic even if a real candor proxy is running on this machine.
	writeConfig(t, home, "proxy:\n  listen: 127.0.0.1:1\n")
	seedUsage(t, home, seedRow{provider: "openai", model: "gpt-4o", bucket: time.Now(), cost: 2.00})

	out, err := executeCommand(t, home, "status")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Database:    " + dbPath(home),
		"Proxy:       not running",
		"Today:       $2.00",
		"This month:  $2.00",
		"Projected:",
		"of $100 budget", // default monthly_budget_usd
	} {
		if !strings.Contains(out, want) {
			t.Errorf("out missing %q:\n%s", want, out)
		}
	}
}

func TestStatusCmd_JSON(t *testing.T) {
	home := t.TempDir()
	writeConfig(t, home, "proxy:\n  listen: 127.0.0.1:1\n")
	seedUsage(t, home, seedRow{provider: "openai", model: "gpt-4o", bucket: time.Now(), cost: 2.00})

	out, err := executeCommand(t, home, "status", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var rep map[string]any
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	if rep["database"] != dbPath(home) {
		t.Errorf("database = %v, want %q", rep["database"], dbPath(home))
	}
	if rep["proxy_up"] != false {
		t.Errorf("proxy_up = %v, want false", rep["proxy_up"])
	}
	if _, ok := rep["proxy_url"]; ok {
		t.Error("proxy_url present while proxy is down")
	}
	if rep["today_usd"] != 2.0 || rep["month_usd"] != 2.0 {
		t.Errorf("today/month = %v/%v, want 2/2", rep["today_usd"], rep["month_usd"])
	}
	if rep["budget_usd"] != 100.0 {
		t.Errorf("budget_usd = %v, want 100", rep["budget_usd"])
	}
	if size, ok := rep["db_size_bytes"].(float64); !ok || size <= 0 {
		t.Errorf("db_size_bytes = %v, want > 0", rep["db_size_bytes"])
	}
	if proj, ok := rep["projected_usd"].(float64); !ok || proj <= 0 {
		t.Errorf("projected_usd = %v, want > 0", rep["projected_usd"])
	}
}

func TestStatusCmd_ProxyRunning(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()
	listen := strings.TrimPrefix(ts.URL, "http://")

	home := t.TempDir()
	writeConfig(t, home, "proxy:\n  listen: "+listen+"\n")

	out, err := executeCommand(t, home, "status")
	if err != nil {
		t.Fatal(err)
	}
	if want := "Proxy:       running at " + ts.URL; !strings.Contains(out, want) {
		t.Errorf("out missing %q:\n%s", want, out)
	}
}

// monthly_budget_usd: 0 drops the budget suffix from the Projected line.
func TestStatusCmd_NoBudget(t *testing.T) {
	home := t.TempDir()
	writeConfig(t, home, "proxy:\n  listen: 127.0.0.1:1\ndefaults:\n  monthly_budget_usd: 0\n")
	out, err := executeCommand(t, home, "status")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Projected:") {
		t.Errorf("out missing Projected line:\n%s", out)
	}
	if strings.Contains(out, "budget") {
		t.Errorf("out mentions a budget with monthly_budget_usd: 0:\n%s", out)
	}
}

// --- migrate ---------------------------------------------------------------

func TestMigrateCmd(t *testing.T) {
	home := t.TempDir()

	out, err := executeCommand(t, home, "migrate")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "migration(s) applied. Database: "+dbPath(home)) {
		t.Errorf("first migrate out = %q", out)
	}

	// Second run is idempotent.
	out, err = executeCommand(t, home, "migrate")
	if err != nil {
		t.Fatal(err)
	}
	if want := "Already up to date. Database: " + dbPath(home) + "\n"; out != want {
		t.Errorf("second migrate out = %q, want %q", out, want)
	}
}

// --- service ---------------------------------------------------------------

func TestServiceCmd(t *testing.T) {
	out, err := executeCommand(t, t.TempDir(), "service")
	exe, exeErr := os.Executable()
	if exeErr != nil {
		t.Fatal(exeErr)
	}
	switch runtime.GOOS {
	case "darwin":
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{
			`<plist version="1.0">`, "dev.candor",
			"<string>" + exe + "</string>", "<string>proxy</string>",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("plist missing %q:\n%s", want, out)
			}
		}
	case "linux":
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"[Unit]", "ExecStart=" + exe + " proxy", "WantedBy=default.target"} {
			if !strings.Contains(out, want) {
				t.Errorf("unit missing %q:\n%s", want, out)
			}
		}
	default:
		if err == nil || !strings.Contains(err.Error(), "not supported") {
			t.Errorf("err = %v, want unsupported-platform error", err)
		}
	}
}

// --- export ----------------------------------------------------------------

func TestExportCmd_ArgErrors(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{"missing since", []string{"export"}, "--since is required"},
		{"bad format", []string{"export", "--since", "2026-01-01", "--format", "xml"}, `unknown --format "xml"`},
		{"bad since", []string{"export", "--since", "01/02/2026"}, "parse --since"},
		{"bad until", []string{"export", "--since", "2026-01-01", "--until", "not-a-date"}, "parse --until"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := executeCommand(t, t.TempDir(), c.args...)
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("err = %v, want error containing %q", err, c.wantErr)
			}
		})
	}
}

// seedExportRows writes rows at mid-day UTC so --since/--until filtering is
// independent of the machine's time zone.
func seedExportRows(t *testing.T, home string) {
	t.Helper()
	seedUsage(t, home,
		seedRow{provider: "openai", model: "gpt-4o",
			bucket: time.Date(2026, 1, 5, 12, 0, 0, 0, time.UTC), cost: 0.50, input: 100, output: 50},
		seedRow{provider: "anthropic", model: "claude-sonnet",
			bucket: time.Date(2026, 1, 10, 12, 0, 0, 0, time.UTC), cost: 1.50, input: 200, output: 80},
	)
}

func TestExportCmd_CSV(t *testing.T) {
	home := t.TempDir()
	seedExportRows(t, home)
	out, err := executeCommand(t, home, "export", "--since", "2026-01-01")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	want := []string{
		"bucket_start,provider,model,input,cache_read,cache_write,output,cost_usd",
		"2026-01-05T12:00:00Z,openai,gpt-4o,100,0,0,50,0.500000",
		"2026-01-10T12:00:00Z,anthropic,claude-sonnet,200,0,0,80,1.500000",
	}
	if len(lines) != len(want) {
		t.Fatalf("got %d lines, want %d:\n%s", len(lines), len(want), out)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Errorf("line %d = %q, want %q", i, lines[i], want[i])
		}
	}
}

func TestExportCmd_JSON(t *testing.T) {
	home := t.TempDir()
	seedExportRows(t, home)
	out, err := executeCommand(t, home, "export", "--since", "2026-01-01", "--format", "json")
	if err != nil {
		t.Fatal(err)
	}
	var got []struct {
		BucketStart string `json:"bucket_start"`
		Provider    string `json:"provider"`
		Model       string `json:"model"`
		Tokens      struct {
			Input  int64 `json:"input"`
			Output int64 `json:"output"`
		} `json:"tokens"`
		CostUSD float64 `json:"cost_usd"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	if len(got) != 2 {
		t.Fatalf("got %d records, want 2", len(got))
	}
	if got[0].BucketStart != "2026-01-05T12:00:00Z" || got[0].Provider != "openai" ||
		got[0].Model != "gpt-4o" || got[0].Tokens.Input != 100 || got[0].CostUSD != 0.50 {
		t.Errorf("record 0 = %+v", got[0])
	}
	if got[1].Provider != "anthropic" || got[1].CostUSD != 1.50 {
		t.Errorf("record 1 = %+v", got[1])
	}
}

// --until is inclusive of the named day: --until 2026-01-06 must include the
// Jan 5 row and exclude the Jan 10 row, regardless of machine time zone.
func TestExportCmd_UntilInclusive(t *testing.T) {
	home := t.TempDir()
	seedExportRows(t, home)
	out, err := executeCommand(t, home, "export", "--since", "2026-01-01", "--until", "2026-01-06")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "gpt-4o") {
		t.Errorf("Jan 5 row missing:\n%s", out)
	}
	if strings.Contains(out, "claude-sonnet") {
		t.Errorf("Jan 10 row should be excluded by --until 2026-01-06:\n%s", out)
	}
}

func TestExportCmd_Empty(t *testing.T) {
	home := t.TempDir()
	seedExportRows(t, home)

	out, err := executeCommand(t, home, "export", "--since", "2026-02-01")
	if err != nil {
		t.Fatal(err)
	}
	if want := "bucket_start,provider,model,input,cache_read,cache_write,output,cost_usd\n"; out != want {
		t.Errorf("out = %q, want header only", out)
	}

	out, err = executeCommand(t, home, "export", "--since", "2026-02-01", "--format", "json")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != "[]" {
		t.Errorf("json out = %q, want []", out)
	}
}

// --- openStore -------------------------------------------------------------

func TestOpenStore_OK(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfg, st, err := openStore()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if cfg.Database != dbPath(home) {
		t.Errorf("Database = %q, want %q", cfg.Database, dbPath(home))
	}
	if _, err := os.Stat(cfg.Database); err != nil {
		t.Errorf("database file not created: %v", err)
	}
}

func TestOpenStore_ConfigError(t *testing.T) {
	home := t.TempDir()
	writeConfig(t, home, "defaults:\n  monthly_budget_usd: -5\n")
	t.Setenv("HOME", home)
	_, _, err := openStore()
	if err == nil || !strings.Contains(err.Error(), "config:") {
		t.Errorf("err = %v, want a config error", err)
	}
}

func TestOpenStore_BadDatabasePath(t *testing.T) {
	home := t.TempDir()
	// A regular file sits where the database's parent dir must be created.
	blocker := filepath.Join(home, "blocker")
	if err := os.WriteFile(blocker, []byte("not a dir"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeConfig(t, home, `database: "`+blocker+`/tokens.db"`+"\n")
	t.Setenv("HOME", home)
	_, _, err := openStore()
	if err == nil || !strings.Contains(err.Error(), "open store:") {
		t.Errorf("err = %v, want an open store error", err)
	}
}
