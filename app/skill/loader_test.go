package skill

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Ivantseng123/agentdock/shared/queue"
)

type mockFetcher struct {
	mu      sync.Mutex
	calls   int
	results map[string][]*SkillFiles
	errors  map[string]error
}

func (m *mockFetcher) fetch(ctx context.Context, pkg, version string) ([]*SkillFiles, error) {
	m.mu.Lock()
	m.calls++
	m.mu.Unlock()

	key := pkg + "@" + version
	if err, ok := m.errors[key]; ok {
		return nil, err
	}
	if skills, ok := m.results[key]; ok {
		return skills, nil
	}
	return nil, fmt.Errorf("package not found: %s", key)
}

func newTestLoader(cfg *SkillsFileConfig, fetcher fetchFunc, bakedIn map[string]*SkillFiles) *Loader {
	if cfg == nil {
		cfg = &SkillsFileConfig{
			Skills: map[string]*SkillConfig{},
			Cache:  CacheConfig{TTL: 5 * time.Minute},
		}
	}
	l := &Loader{
		config:  cfg,
		cache:   make(map[string]*cacheEntry),
		bakedIn: bakedIn,
		fetcher: fetcher,
		logger:  slog.Default(),
	}
	return l
}

func TestLoader_LoadAll_LocalSkill(t *testing.T) {
	bakedIn := map[string]*SkillFiles{
		"triage": {Name: "triage", Files: map[string][]byte{"SKILL.md": []byte("# Triage")}},
	}
	cfg := &SkillsFileConfig{
		Skills: map[string]*SkillConfig{
			"triage": {Type: "local", Path: "app/agents/skills/triage"},
		},
		Cache: CacheConfig{TTL: 5 * time.Minute},
	}
	fetcher := &mockFetcher{}
	loader := newTestLoader(cfg, fetcher.fetch, bakedIn)

	result, err := loader.LoadAll(context.Background())
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if _, ok := result["triage"]; !ok {
		t.Fatal("missing triage skill")
	}
	if string(result["triage"].Files["SKILL.md"]) != "# Triage" {
		t.Errorf("content = %q", string(result["triage"].Files["SKILL.md"]))
	}
	if fetcher.calls != 0 {
		t.Errorf("fetcher should not be called for local skills, got %d calls", fetcher.calls)
	}
}

func TestLoader_LoadAll_NpxSkill_CacheMiss(t *testing.T) {
	cfg := &SkillsFileConfig{
		Skills: map[string]*SkillConfig{
			"review": {Type: "remote", Package: "@team/review", Version: "latest", Timeout: 30 * time.Second},
		},
		Cache: CacheConfig{TTL: 5 * time.Minute},
	}
	fetcher := &mockFetcher{
		results: map[string][]*SkillFiles{
			"@team/review@latest": {{Name: "code-review", Files: map[string][]byte{"SKILL.md": []byte("# Review")}}},
		},
	}
	loader := newTestLoader(cfg, fetcher.fetch, nil)

	result, err := loader.LoadAll(context.Background())
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if _, ok := result["code-review"]; !ok {
		t.Fatal("missing code-review skill")
	}
	if fetcher.calls != 1 {
		t.Errorf("fetcher calls = %d, want 1", fetcher.calls)
	}
}

func TestLoader_LoadAll_NpxSkill_CacheHit(t *testing.T) {
	cfg := &SkillsFileConfig{
		Skills: map[string]*SkillConfig{
			"review": {Type: "remote", Package: "@team/review", Version: "latest", Timeout: 30 * time.Second},
		},
		Cache: CacheConfig{TTL: 5 * time.Minute},
	}
	fetcher := &mockFetcher{
		results: map[string][]*SkillFiles{
			"@team/review@latest": {{Name: "code-review", Files: map[string][]byte{"SKILL.md": []byte("# Review")}}},
		},
	}
	loader := newTestLoader(cfg, fetcher.fetch, nil)

	loader.LoadAll(context.Background())
	result, err := loader.LoadAll(context.Background())
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if _, ok := result["code-review"]; !ok {
		t.Fatal("missing code-review on cache hit")
	}
	if fetcher.calls != 1 {
		t.Errorf("fetcher calls = %d, want 1 (cache hit)", fetcher.calls)
	}
}

func TestLoader_LoadAll_NpxSkill_CacheExpired(t *testing.T) {
	cfg := &SkillsFileConfig{
		Skills: map[string]*SkillConfig{
			"review": {Type: "remote", Package: "@team/review", Version: "latest", Timeout: 30 * time.Second},
		},
		Cache: CacheConfig{TTL: 1 * time.Millisecond},
	}
	fetcher := &mockFetcher{
		results: map[string][]*SkillFiles{
			"@team/review@latest": {{Name: "code-review", Files: map[string][]byte{"SKILL.md": []byte("# Review")}}},
		},
	}
	loader := newTestLoader(cfg, fetcher.fetch, nil)

	loader.LoadAll(context.Background())
	time.Sleep(5 * time.Millisecond)
	loader.LoadAll(context.Background())

	if fetcher.calls != 2 {
		t.Errorf("fetcher calls = %d, want 2 (cache expired)", fetcher.calls)
	}
}

func TestLoader_LoadAll_NpxFail_FallbackBakedIn(t *testing.T) {
	bakedIn := map[string]*SkillFiles{
		"code-review": {Name: "code-review", Files: map[string][]byte{"SKILL.md": []byte("# Baked-in Review")}},
	}
	cfg := &SkillsFileConfig{
		Skills: map[string]*SkillConfig{
			"review": {Type: "remote", Package: "@team/review", Version: "latest", Timeout: 30 * time.Second},
		},
		Cache: CacheConfig{TTL: 5 * time.Minute},
	}
	fetcher := &mockFetcher{errors: map[string]error{"@team/review@latest": fmt.Errorf("network error")}}
	loader := newTestLoader(cfg, fetcher.fetch, bakedIn)

	result, err := loader.LoadAll(context.Background())
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if _, ok := result["code-review"]; !ok {
		t.Fatal("should fallback to baked-in skill")
	}
	if string(result["code-review"].Files["SKILL.md"]) != "# Baked-in Review" {
		t.Errorf("content = %q, want baked-in", string(result["code-review"].Files["SKILL.md"]))
	}
}

func TestLoader_LoadAll_NpxFail_NoBakedIn_Skip(t *testing.T) {
	cfg := &SkillsFileConfig{
		Skills: map[string]*SkillConfig{
			"review": {Type: "remote", Package: "@team/review", Version: "latest", Timeout: 30 * time.Second},
		},
		Cache: CacheConfig{TTL: 5 * time.Minute},
	}
	fetcher := &mockFetcher{errors: map[string]error{"@team/review@latest": fmt.Errorf("network error")}}
	loader := newTestLoader(cfg, fetcher.fetch, nil)

	result, err := loader.LoadAll(context.Background())
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("expected empty result, got %d", len(result))
	}
}

func TestLoader_LoadAll_NpxFail_NegativeCache(t *testing.T) {
	cfg := &SkillsFileConfig{
		Skills: map[string]*SkillConfig{
			"review": {Type: "remote", Package: "@team/review", Version: "latest", Timeout: 30 * time.Second},
		},
		Cache: CacheConfig{TTL: 5 * time.Minute},
	}
	fetcher := &mockFetcher{errors: map[string]error{"@team/review@latest": fmt.Errorf("network error")}}
	loader := newTestLoader(cfg, fetcher.fetch, nil)

	loader.LoadAll(context.Background())
	loader.LoadAll(context.Background())

	if fetcher.calls != 1 {
		t.Errorf("fetcher calls = %d, want 1 (negative cache)", fetcher.calls)
	}
}

func TestLoader_LoadAll_SamePackageDedup(t *testing.T) {
	cfg := &SkillsFileConfig{
		Skills: map[string]*SkillConfig{
			"entry-a": {Type: "remote", Package: "@team/multi", Version: "latest", Timeout: 30 * time.Second},
			"entry-b": {Type: "remote", Package: "@team/multi", Version: "latest", Timeout: 30 * time.Second},
		},
		Cache: CacheConfig{TTL: 5 * time.Minute},
	}
	fetcher := &mockFetcher{
		results: map[string][]*SkillFiles{
			"@team/multi@latest": {
				{Name: "skill-a", Files: map[string][]byte{"SKILL.md": []byte("# A")}},
				{Name: "skill-b", Files: map[string][]byte{"SKILL.md": []byte("# B")}},
			},
		},
	}
	loader := newTestLoader(cfg, fetcher.fetch, nil)

	result, err := loader.LoadAll(context.Background())
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if len(result) != 2 {
		t.Errorf("got %d skills, want 2", len(result))
	}
	if fetcher.calls != 1 {
		t.Errorf("fetcher calls = %d, want 1 (same package fetched once)", fetcher.calls)
	}
}

func TestLoader_LoadAll_ConflictFailFast(t *testing.T) {
	bakedIn := map[string]*SkillFiles{
		"code-review": {Name: "code-review", Files: map[string][]byte{"SKILL.md": []byte("# Local")}},
	}
	cfg := &SkillsFileConfig{
		Skills: map[string]*SkillConfig{
			"local-review": {Type: "local", Path: "app/agents/skills/code-review"},
			"remote-review": {Type: "remote", Package: "@team/review", Version: "latest", Timeout: 30 * time.Second},
		},
		Cache: CacheConfig{TTL: 5 * time.Minute},
	}
	fetcher := &mockFetcher{
		results: map[string][]*SkillFiles{
			// Remote package produces a skill with same name as local
			"@team/review@latest": {{Name: "code-review", Files: map[string][]byte{"SKILL.md": []byte("# Remote")}}},
		},
	}
	loader := newTestLoader(cfg, fetcher.fetch, bakedIn)

	_, err := loader.LoadAll(context.Background())
	if err == nil {
		t.Fatal("expected error for same-name conflict")
	}
	if !strings.Contains(err.Error(), "conflict") {
		t.Errorf("error should mention conflict: %v", err)
	}
}

// Ensure LoadAll returns map[string]*queue.SkillPayload — compile-time type check.
var _ func(context.Context) (map[string]*queue.SkillPayload, error) = (*Loader)(nil).LoadAll
