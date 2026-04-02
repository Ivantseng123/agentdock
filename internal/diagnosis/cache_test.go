package diagnosis

import (
	"testing"
	"time"

	"slack-issue-bot/internal/llm"
)

func TestCacheSetGet(t *testing.T) {
	c := NewCache(5 * time.Minute)
	defer c.Stop()

	key := c.Key("org/repo", "main", "login broken", "en", nil)
	resp := llm.DiagnoseResponse{
		Summary:    "login bug",
		Confidence: "high",
		Files:      []llm.FileRef{{Path: "auth/login.go", Description: "auth handler"}},
	}

	c.Set(key, resp)

	got, ok := c.Get(key)
	if !ok {
		t.Fatal("expected cache hit")
	}
	if got.Summary != "login bug" {
		t.Errorf("expected summary 'login bug', got %q", got.Summary)
	}
	if got.Confidence != "high" {
		t.Errorf("expected confidence 'high', got %q", got.Confidence)
	}
	if len(got.Files) != 1 || got.Files[0].Path != "auth/login.go" {
		t.Errorf("unexpected files: %v", got.Files)
	}
}

func TestCacheMiss(t *testing.T) {
	c := NewCache(5 * time.Minute)
	defer c.Stop()

	_, ok := c.Get("nonexistent-key")
	if ok {
		t.Fatal("expected cache miss for unknown key")
	}
}

func TestCacheExpiry(t *testing.T) {
	c := NewCache(50 * time.Millisecond)
	defer c.Stop()

	key := c.Key("org/repo", "main", "test", "", nil)
	c.Set(key, llm.DiagnoseResponse{Summary: "cached"})

	// Should hit immediately.
	if _, ok := c.Get(key); !ok {
		t.Fatal("expected cache hit before expiry")
	}

	// Wait for expiry.
	time.Sleep(100 * time.Millisecond)

	if _, ok := c.Get(key); ok {
		t.Fatal("expected cache miss after expiry")
	}
}

func TestCacheDifferentKeys(t *testing.T) {
	c := NewCache(5 * time.Minute)
	defer c.Stop()

	k1 := c.Key("org/repo", "main", "bug in login", "en", nil)
	k2 := c.Key("org/repo", "main", "bug in logout", "en", nil)
	k3 := c.Key("org/repo", "dev", "bug in login", "en", nil)
	k4 := c.Key("org/repo", "main", "bug in login", "zh-TW", nil)
	k5 := c.Key("org/repo", "main", "bug in login", "en", []string{"rule1"})

	keys := []string{k1, k2, k3, k4, k5}
	seen := make(map[string]bool)
	for _, k := range keys {
		if seen[k] {
			t.Fatalf("duplicate key produced: %s", k)
		}
		seen[k] = true
	}

	// Verify different keys store different values.
	c.Set(k1, llm.DiagnoseResponse{Summary: "resp1"})
	c.Set(k2, llm.DiagnoseResponse{Summary: "resp2"})

	got1, ok1 := c.Get(k1)
	got2, ok2 := c.Get(k2)
	if !ok1 || !ok2 {
		t.Fatal("expected both keys to hit")
	}
	if got1.Summary != "resp1" || got2.Summary != "resp2" {
		t.Errorf("values mixed up: got1=%q got2=%q", got1.Summary, got2.Summary)
	}
}
