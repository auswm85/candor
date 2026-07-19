package auth

import (
	"os"
	"testing"
)

func TestFileBackend(t *testing.T) {
	os.Setenv("CANDOR_KEYCHAIN", "file")
	defer os.Unsetenv("CANDOR_KEYCHAIN")

	// Clear any leftover state
	os.Remove(os.TempDir() + "/candor-keys.json")

	// Should start empty
	if configured := ListConfiguredProviders(); len(configured) != 0 {
		t.Fatalf("expected 0, got %d: %v", len(configured), configured)
	}

	// Set key
	if err := SetProviderKey("openai", "sk-test-123"); err != nil {
		t.Fatal(err)
	}

	// Get key
	got, err := GetProviderKey("openai")
	if err != nil {
		t.Fatal(err)
	}
	if got != "sk-test-123" {
		t.Errorf("got %q, want sk-test-123", got)
	}

	// HasProviderKey
	if !HasProviderKey("openai") {
		t.Error("expected HasProviderKey true")
	}

	// List
	configured := ListConfiguredProviders()
	if len(configured) != 1 || configured[0] != "openai" {
		t.Errorf("got %v, expected [openai]", configured)
	}

	// Clear
	if err := ClearProviderKey("openai"); err != nil {
		t.Fatal(err)
	}
	if HasProviderKey("openai") {
		t.Error("expected false after clear")
	}

	// Get nonexistent
	_, err = GetProviderKey("openai")
	if err == nil {
		t.Error("expected error for missing key")
	}
}

func TestMultipleProviders(t *testing.T) {
	os.Setenv("CANDOR_KEYCHAIN", "file")
	defer os.Unsetenv("CANDOR_KEYCHAIN")
	defer os.Remove(os.TempDir() + "/candor-keys.json")

	for _, p := range []string{"openai", "anthropic", "openrouter"} {
		if err := SetProviderKey(p, "key-"+p); err != nil {
			t.Fatal(err)
		}
	}

	configured := ListConfiguredProviders()
	if len(configured) != 3 {
		t.Errorf("got %d, want 3: %v", len(configured), configured)
	}

	// Overwrite
	if err := SetProviderKey("openai", "key-new"); err != nil {
		t.Fatal(err)
	}
	got, _ := GetProviderKey("openai")
	if got != "key-new" {
		t.Errorf("got %q, want key-new", got)
	}
}
