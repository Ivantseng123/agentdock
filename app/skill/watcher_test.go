package skill

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWatcher_ReloadsOnFileChange(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "skills.yaml")

	initialYAML := `
skills:
  triage:
    type: local
    path: agents/skills/triage
cache:
  ttl: 5m
`
	os.WriteFile(cfgPath, []byte(initialYAML), 0644)

	loader := &Loader{
		config:  &SkillsFileConfig{Skills: map[string]*SkillConfig{}, Cache: CacheConfig{TTL: 5 * time.Minute}},
		cache:   make(map[string]*cacheEntry),
		bakedIn: make(map[string]*SkillFiles),
		fetcher: func(ctx context.Context, pkg, version string) ([]*SkillFiles, error) {
			return nil, nil
		},
		logger: slog.Default(),
	}

	stop, err := loader.StartWatcher(cfgPath)
	if err != nil {
		t.Fatalf("StartWatcher: %v", err)
	}
	defer stop()

	time.Sleep(100 * time.Millisecond)

	updatedYAML := `
skills:
  triage:
    type: local
    path: agents/skills/triage
  review:
    type: remote
    package: "@team/review"
    version: "latest"
cache:
  ttl: 10m
`
	os.WriteFile(cfgPath, []byte(updatedYAML), 0644)

	time.Sleep(1 * time.Second)

	loader.mu.RLock()
	defer loader.mu.RUnlock()

	if len(loader.config.Skills) != 2 {
		t.Errorf("skills count = %d, want 2 after reload", len(loader.config.Skills))
	}
	if loader.config.Cache.TTL != 10*time.Minute {
		t.Errorf("cache TTL = %v, want 10m after reload", loader.config.Cache.TTL)
	}
}
