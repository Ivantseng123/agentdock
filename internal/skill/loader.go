package skill

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"agentdock/internal/queue"
	"golang.org/x/sync/singleflight"
)

// fetchFunc is the signature for fetching a package's skills. Swappable for tests.
type fetchFunc func(ctx context.Context, pkg, version string) ([]*SkillFiles, error)

// cacheStatus tracks whether a cache entry represents a successful fetch or a failure.
type cacheStatus int

const (
	cacheOK      cacheStatus = iota
	cacheFailed              // fetch failed; held to avoid hammering the registry
	cacheInvalid             // validation failed
)

// cacheEntry holds one fetched result (or recorded failure) for a "pkg@version" key.
type cacheEntry struct {
	status    cacheStatus
	skills    []*SkillFiles
	reason    string
	fetchedAt time.Time
}

// Loader is the central component for skill loading. It manages an in-memory
// TTL cache, singleflight dedup, two-layer fallback (baked-in → skip), and
// startup warm-up.
type Loader struct {
	mu      sync.RWMutex
	config  *SkillsFileConfig
	cache   map[string]*cacheEntry // keyed by "pkg@version"
	bakedIn map[string]*SkillFiles // keyed by skill name
	fetcher fetchFunc
	group   singleflight.Group
}

// NewLoader builds a Loader from a config file path and an optional baked-in
// skill directory. Pass an empty bakedInDir to skip baked-in loading.
func NewLoader(configPath, bakedInDir string) (*Loader, error) {
	var cfg *SkillsFileConfig
	if configPath == "" {
		cfg = &SkillsFileConfig{}
		applySkillsDefaults(cfg)
	} else {
		var err error
		cfg, err = LoadSkillsConfig(configPath)
		if err != nil {
			return nil, fmt.Errorf("load skills config: %w", err)
		}
	}

	bakedIn := make(map[string]*SkillFiles)
	if bakedInDir != "" {
		bakedIn = loadBakedInSkills(bakedInDir)
	}

	return &Loader{
		config:  cfg,
		cache:   make(map[string]*cacheEntry),
		bakedIn: bakedIn,
		fetcher: FetchPackage,
	}, nil
}

// Warmup pre-fetches all remote packages defined in config so that the first
// real job doesn't pay the npm install latency. Errors are logged but not returned.
func (l *Loader) Warmup(ctx context.Context) {
	l.mu.RLock()
	cfg := l.config
	l.mu.RUnlock()

	seen := make(map[string]bool)
	for _, sc := range cfg.Skills {
		if sc.Type != "remote" {
			continue
		}
		cacheKey := sc.Package + "@" + sc.Version
		if seen[cacheKey] {
			continue
		}
		seen[cacheKey] = true

		result := make(map[string]*queue.SkillPayload)
		sources := make(map[string]string)
		l.loadRemote(ctx, sc, cacheKey, result, sources)
	}
}

// LoadAll iterates every configured skill, resolves it (from cache or by
// fetching), applies fallbacks, and returns a map of skill name → SkillPayload
// ready to embed in a Job. The result map is always non-nil.
func (l *Loader) LoadAll(ctx context.Context) (map[string]*queue.SkillPayload, error) {
	l.mu.RLock()
	cfg := l.config
	l.mu.RUnlock()

	result := make(map[string]*queue.SkillPayload)
	sources := make(map[string]string) // skill name → source (for conflict detection)

	// Deduplicate: same package@version should only be fetched once per call.
	seenRemote := make(map[string]bool)

	for _, sc := range cfg.Skills {
		switch sc.Type {
		case "local":
			if err := l.loadLocal(sc, result, sources); err != nil {
				return nil, err
			}

		case "remote":
			cacheKey := sc.Package + "@" + sc.Version
			if seenRemote[cacheKey] {
				continue
			}
			seenRemote[cacheKey] = true
			if err := l.loadRemote(ctx, sc, cacheKey, result, sources); err != nil {
				return nil, err
			}

		default:
			slog.Warn("skill: unknown type", "type", sc.Type)
		}
	}

	if err := ValidateJobSize(result); err != nil {
		return nil, fmt.Errorf("skill job size exceeded: %w", err)
	}

	return result, nil
}

// ReloadConfig re-reads the skills YAML and clears cache entries for packages
// that were added, changed, or removed.
func (l *Loader) ReloadConfig(path string) error {
	newCfg, err := LoadSkillsConfig(path)
	if err != nil {
		return fmt.Errorf("reload skills config: %w", err)
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	oldCfg := l.config

	// Build set of old package keys.
	oldKeys := make(map[string]bool)
	for _, sc := range oldCfg.Skills {
		if sc.Type == "remote" {
			oldKeys[sc.Package+"@"+sc.Version] = true
		}
	}

	// Build set of new package keys and detect changes.
	newKeys := make(map[string]bool)
	for _, sc := range newCfg.Skills {
		if sc.Type == "remote" {
			newKeys[sc.Package+"@"+sc.Version] = true
		}
	}

	// Evict cache for any key that was removed or not present in new config.
	for key := range l.cache {
		if !newKeys[key] {
			delete(l.cache, key)
		}
	}

	// Also evict entries for brand-new keys (they weren't cached yet, but be explicit).
	for key := range newKeys {
		if !oldKeys[key] {
			delete(l.cache, key)
		}
	}

	l.config = newCfg
	return nil
}

// loadLocal resolves a "local" skill from the baked-in map using the config key
// as the skill name lookup.
func (l *Loader) loadLocal(sc *SkillConfig, result map[string]*queue.SkillPayload, sources map[string]string) error {
	skillName := filepath.Base(sc.Path)

	sf, ok := l.bakedIn[skillName]
	if !ok {
		slog.Warn("skill: local skill not found in baked-in map", "name", skillName)
		return nil
	}
	return addSkill(result, sf.Name, "local:"+sc.Path, sf, sources)
}

// addSkill adds a skill to the result map. Returns an error if the name
// already exists from a different source (fail fast on conflict).
func addSkill(result map[string]*queue.SkillPayload, name, source string, sf *SkillFiles, sources map[string]string) error {
	if prev, exists := sources[name]; exists && prev != source {
		return fmt.Errorf("skill name conflict: %q provided by both %q and %q", name, prev, source)
	}
	sources[name] = source
	result[name] = &queue.SkillPayload{Files: sf.Files}
	return nil
}

// loadRemote resolves a remote skill: checks TTL cache, uses singleflight to
// avoid duplicate fetches, validates files, and falls back to baked-in on failure.
func (l *Loader) loadRemote(ctx context.Context, sc *SkillConfig, cacheKey string, result map[string]*queue.SkillPayload, sources map[string]string) error {
	l.mu.RLock()
	entry, cached := l.cache[cacheKey]
	ttl := l.config.Cache.TTL
	l.mu.RUnlock()

	source := "remote:" + sc.Package

	// Cache hit and not yet expired — use it directly.
	if cached && time.Since(entry.fetchedAt) < ttl {
		if entry.status == cacheOK {
			for _, sf := range entry.skills {
				if err := addSkill(result, sf.Name, source, sf, sources); err != nil {
					return err
				}
			}
		} else {
			// Negative cache: fetch previously failed; fall back without retrying.
			l.fallbackBakedIn(cacheKey, result)
		}
		return nil
	}

	// Cache miss or expired: use singleflight to prevent thundering herd.
	type sfResult struct {
		skills []*SkillFiles
	}
	raw, fetchErr, _ := l.group.Do(cacheKey, func() (interface{}, error) {
		fetchCtx := ctx
		if sc.Timeout > 0 {
			var cancel context.CancelFunc
			fetchCtx, cancel = context.WithTimeout(ctx, sc.Timeout)
			defer cancel()
		}
		skills, err := l.fetcher(fetchCtx, sc.Package, sc.Version)
		return &sfResult{skills: skills}, err
	})

	if fetchErr != nil {
		slog.Warn("skill: fetch failed, recording negative cache", "pkg", cacheKey, "err", fetchErr)
		l.setCacheEntry(cacheKey, &cacheEntry{
			status:    cacheFailed,
			reason:    fetchErr.Error(),
			fetchedAt: time.Now(),
		})
		l.fallbackBakedIn(cacheKey, result)
		return nil
	}

	sfr := raw.(*sfResult)

	// Validate each skill's files.
	if valErr := l.validateSkillsBatch(sfr.skills); valErr != nil {
		slog.Warn("skill: validation failed, recording negative cache", "pkg", cacheKey, "err", valErr)
		l.setCacheEntry(cacheKey, &cacheEntry{
			status:    cacheInvalid,
			reason:    valErr.Error(),
			fetchedAt: time.Now(),
		})
		l.fallbackBakedIn(cacheKey, result)
		return nil
	}

	l.setCacheEntry(cacheKey, &cacheEntry{
		status:    cacheOK,
		skills:    sfr.skills,
		fetchedAt: time.Now(),
	})

	for _, sf := range sfr.skills {
		if err := addSkill(result, sf.Name, source, sf, sources); err != nil {
			return err
		}
	}
	return nil
}

// fallbackBakedIn adds all baked-in skills to result that aren't already
// present, as a best-effort fallback when remote fetch fails.
func (l *Loader) fallbackBakedIn(pkg string, result map[string]*queue.SkillPayload) {
	if len(l.bakedIn) == 0 {
		return
	}
	for name, sf := range l.bakedIn {
		if _, exists := result[name]; !exists {
			result[name] = &queue.SkillPayload{Files: sf.Files}
		}
	}
}

// setCacheEntry writes a cache entry under the write lock.
func (l *Loader) setCacheEntry(key string, entry *cacheEntry) {
	l.mu.Lock()
	l.cache[key] = entry
	l.mu.Unlock()
}

// validateSkillsBatch calls ValidateSkillFiles for each skill in the slice.
func (l *Loader) validateSkillsBatch(skills []*SkillFiles) error {
	for _, sf := range skills {
		if err := ValidateSkillFiles(sf.Files); err != nil {
			return fmt.Errorf("skill %q: %w", sf.Name, err)
		}
	}
	return nil
}

// loadBakedInSkills scans dir for subdirectories, each expected to be one
// skill (must contain SKILL.md). Returns a name → SkillFiles map.
func loadBakedInSkills(dir string) map[string]*SkillFiles {
	result := make(map[string]*SkillFiles)

	entries, err := os.ReadDir(dir)
	if err != nil {
		slog.Warn("skill: cannot read baked-in dir", "dir", dir, "err", err)
		return result
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		skillDir := filepath.Join(dir, entry.Name())
		sf, err := loadSingleBakedIn(skillDir)
		if err != nil {
			slog.Warn("skill: skip baked-in skill", "dir", skillDir, "err", err)
			continue
		}
		result[sf.Name] = sf
	}
	return result
}

// loadSingleBakedIn reads one baked-in skill directory.  It requires SKILL.md
// to be present and uses readDirRecursive (from npx.go) to collect all files.
func loadSingleBakedIn(skillDir string) (*SkillFiles, error) {
	if _, err := os.Stat(filepath.Join(skillDir, "SKILL.md")); err != nil {
		return nil, fmt.Errorf("no SKILL.md in %s", skillDir)
	}

	files, err := readDirRecursive(skillDir, "")
	if err != nil {
		return nil, fmt.Errorf("read baked-in skill dir %s: %w", skillDir, err)
	}

	return &SkillFiles{
		Name:  filepath.Base(skillDir),
		Files: files,
	}, nil
}
