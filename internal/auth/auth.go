package auth

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/zalando/go-keyring"
)

const serviceName = "token-tracker"
const envKeychain = "TOKEN_TRACKER_KEYCHAIN"

func keyName(provider string) string { return fmt.Sprintf("token-tracker/%s-api-key", provider) }

var (
	ErrNotFound = keyring.ErrNotFound
	mu          sync.Mutex
)

type fileStore map[string]string

func fileBackend() string {
	v := os.Getenv(envKeychain)
	if v == "" {
		v = "native"
	}
	return v
}

func loadFile() fileStore {
	path := filepath.Join(os.TempDir(), "token-tracker-keys.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return fileStore{}
	}
	var s fileStore
	json.Unmarshal(data, &s)
	return s
}

func saveFile(s fileStore) {
	path := filepath.Join(os.TempDir(), "token-tracker-keys.json")
	data, _ := json.Marshal(s)
	os.WriteFile(path, data, 0600)
}

func SetProviderKey(provider, key string) error {
	if fileBackend() == "file" {
		mu.Lock()
		defer mu.Unlock()
		s := loadFile()
		s[keyName(provider)] = key
		saveFile(s)
		return nil
	}
	return keyring.Set(serviceName, keyName(provider), key)
}

func GetProviderKey(provider string) (string, error) {
	if fileBackend() == "file" {
		mu.Lock()
		defer mu.Unlock()
		s := loadFile()
		k, ok := s[keyName(provider)]
		if !ok {
			return "", keyring.ErrNotFound
		}
		return k, nil
	}
	return keyring.Get(serviceName, keyName(provider))
}

func HasProviderKey(provider string) bool {
	_, err := GetProviderKey(provider)
	return err == nil
}

func ClearProviderKey(provider string) error {
	if fileBackend() == "file" {
		mu.Lock()
		defer mu.Unlock()
		s := loadFile()
		delete(s, keyName(provider))
		saveFile(s)
		return nil
	}
	return keyring.Delete(serviceName, keyName(provider))
}

func ListConfiguredProviders() []string {
	out := make([]string, 0, 3)
	for _, p := range []string{"openai", "anthropic", "openrouter"} {
		if HasProviderKey(p) {
			out = append(out, p)
		}
	}
	return out
}