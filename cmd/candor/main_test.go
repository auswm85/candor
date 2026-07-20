package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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
