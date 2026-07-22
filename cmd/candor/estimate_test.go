package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// estimateConfig writes a minimal config that disables dynamic pricing so
// estimate tests use the bundled price table and never hit the network.
func estimateConfig(t *testing.T, home string) {
	t.Helper()
	writeConfig(t, home, "pricing:\n  source: \"\"\nproxy:\n  listen: 127.0.0.1:1\n")
}

// writePromptFile writes prompt to a temporary file and returns its path.
func writePromptFile(t *testing.T, prompt string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "prompt.txt")
	if err := os.WriteFile(path, []byte(prompt), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestEstimateCmd_Happy(t *testing.T) {
	home := t.TempDir()
	estimateConfig(t, home)

	prompt := "The quick brown fox jumps over the lazy dog"
	out, err := executeCommand(t, home, "estimate", "--model", "gpt-4o", "--prompt", prompt)
	if err != nil {
		t.Fatalf("unexpected error: %v\n%s", err, out)
	}

	want := []string{
		"Provider:          openai",
		"Model:             gpt-4o",
		"Prompt chars:      43",
		"Estimated tokens:  ~10",
		"Input rate:         $2.50 / 1M tokens",
		"Estimated cost:    $0.000025",
	}
	for _, w := range want {
		if !strings.Contains(out, w) {
			t.Errorf("output missing %q:\n%s", w, out)
		}
	}
}

func TestEstimateCmd_MissingModel(t *testing.T) {
	home := t.TempDir()
	estimateConfig(t, home)

	_, err := executeCommand(t, home, "estimate", "--prompt", "hello")
	if err == nil || !strings.Contains(err.Error(), "--model is required") {
		t.Errorf("err = %v, want --model required error", err)
	}
}

func TestEstimateCmd_MissingPrompt(t *testing.T) {
	home := t.TempDir()
	estimateConfig(t, home)

	_, err := executeCommand(t, home, "estimate", "--model", "gpt-4o")
	if err == nil || !strings.Contains(err.Error(), "--prompt or --prompt-file is required") {
		t.Errorf("err = %v, want prompt/file required error", err)
	}
}

func TestEstimateCmd_BothPromptAndFile(t *testing.T) {
	home := t.TempDir()
	estimateConfig(t, home)
	path := writePromptFile(t, "hello")

	_, err := executeCommand(t, home, "estimate", "--model", "gpt-4o", "--prompt", "hello", "--prompt-file", path)
	if err == nil || !strings.Contains(err.Error(), "use --prompt or --prompt-file, not both") {
		t.Errorf("err = %v, want not-both error", err)
	}
}

func TestEstimateCmd_PromptFile(t *testing.T) {
	home := t.TempDir()
	estimateConfig(t, home)
	prompt := "The quick brown fox jumps over the lazy dog"
	path := writePromptFile(t, prompt)

	out, err := executeCommand(t, home, "estimate", "--model", "gpt-4o", "--prompt-file", path)
	if err != nil {
		t.Fatalf("unexpected error: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Prompt chars:      43") {
		t.Errorf("output missing char count:\n%s", out)
	}
	if !strings.Contains(out, "Estimated tokens:  ~10") {
		t.Errorf("output missing token estimate:\n%s", out)
	}
}

func TestEstimateCmd_PromptFileNotFound(t *testing.T) {
	home := t.TempDir()
	estimateConfig(t, home)

	_, err := executeCommand(t, home, "estimate", "--model", "gpt-4o", "--prompt-file", "/nonexistent/path/prompt.txt")
	if err == nil || !strings.Contains(err.Error(), "read prompt file:") {
		t.Errorf("err = %v, want read prompt file error", err)
	}
}

func TestEstimateCmd_EmptyPromptFile(t *testing.T) {
	home := t.TempDir()
	estimateConfig(t, home)
	path := writePromptFile(t, "")

	_, err := executeCommand(t, home, "estimate", "--model", "gpt-4o", "--prompt-file", path)
	if err == nil || !strings.Contains(err.Error(), "prompt is empty") {
		t.Errorf("err = %v, want empty-prompt error", err)
	}
}

func TestEstimateCmd_UnknownModel(t *testing.T) {
	home := t.TempDir()
	estimateConfig(t, home)

	_, err := executeCommand(t, home, "estimate", "--model", "not-a-model", "--prompt", "hello")
	if err == nil || !strings.Contains(err.Error(), "no pricing found for openai/not-a-model") {
		t.Errorf("err = %v, want no pricing error", err)
	}
}

func TestEstimateCmd_UnknownProvider(t *testing.T) {
	home := t.TempDir()
	estimateConfig(t, home)

	_, err := executeCommand(t, home, "estimate", "--provider", "unknown", "--model", "gpt-4o", "--prompt", "hello")
	if err == nil || !strings.Contains(err.Error(), "no pricing found for unknown/gpt-4o") {
		t.Errorf("err = %v, want no pricing error", err)
	}
}

func TestEstimateCmd_TokenMath(t *testing.T) {
	home := t.TempDir()
	estimateConfig(t, home)

	cases := []struct {
		repeat int
		want   int64
	}{
		{1, 1}, // below 4 runes, minimum 1
		{3, 1},
		{4, 1}, // exactly 4 runes -> 1 token
		{7, 1},
		{8, 2},
		{12, 3},
		{40, 10},
	}
	for _, c := range cases {
		t.Run(fmt.Sprintf("%drunes", c.repeat), func(t *testing.T) {
			prompt := strings.Repeat("a", c.repeat)
			out, err := executeCommand(t, home, "estimate", "--model", "gpt-4o-mini", "--prompt", prompt)
			if err != nil {
				t.Fatalf("unexpected error: %v\n%s", err, out)
			}
			want := fmt.Sprintf("Estimated tokens:  ~%d", c.want)
			if !strings.Contains(out, want) {
				t.Errorf("output missing %q:\n%s", want, out)
			}
		})
	}
}

func TestEstimateCmd_Unicode(t *testing.T) {
	home := t.TempDir()
	estimateConfig(t, home)

	// "こんにちは" is 15 bytes but exactly 5 runes.
	out, err := executeCommand(t, home, "estimate", "--model", "gpt-4o-mini", "--prompt", "こんにちは")
	if err != nil {
		t.Fatalf("unexpected error: %v\n%s", err, out)
	}
	want := []string{
		"Prompt chars:      5",
		"Estimated tokens:  ~1",
	}
	for _, w := range want {
		if !strings.Contains(out, w) {
			t.Errorf("output missing %q:\n%s", w, out)
		}
	}
}
