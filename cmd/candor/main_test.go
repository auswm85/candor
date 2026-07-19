package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/auswm85/candor/internal/store"
)

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
