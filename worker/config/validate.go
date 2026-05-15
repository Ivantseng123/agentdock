package config

import (
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// Validate runs cross-field range checks on the merged Config and returns a
// single error listing every problem found.
func Validate(cfg *Config) error {
	var errs []string

	if cfg.Count < 1 {
		errs = append(errs, "count must be >= 1")
	}
	if cfg.Queue.Capacity < 1 {
		errs = append(errs, "queue.capacity must be >= 1")
	}
	if cfg.Queue.JobTimeout <= 0 {
		errs = append(errs, "queue.job_timeout must be > 0")
	}
	if cfg.Queue.AgentIdleTimeout <= 0 {
		errs = append(errs, "queue.agent_idle_timeout must be > 0")
	}
	if cfg.Queue.PrepareTimeout <= 0 {
		errs = append(errs, "queue.prepare_timeout must be > 0")
	}
	if cfg.Queue.StatusInterval <= 0 {
		errs = append(errs, "queue.status_interval must be > 0")
	}
	if cfg.Logging.RetentionDays < 1 {
		errs = append(errs, "logging.retention_days must be >= 1")
	}
	if cfg.RepoCache.Dir == "" {
		errs = append(errs, "repo_cache.dir must not be empty; delete the line to use the default, or set an absolute path")
	} else if !filepath.IsAbs(cfg.RepoCache.Dir) {
		errs = append(errs, fmt.Sprintf("repo_cache.dir must be an absolute path, got %q", cfg.RepoCache.Dir))
	}
	if cfg.RepoCache.MaxAge <= 0 {
		errs = append(errs, "repo_cache.max_age must be > 0")
	}

	// NicknamePool: trim-normalise in place, then check length.
	for i, raw := range cfg.NicknamePool {
		trimmed := strings.TrimSpace(raw)
		cfg.NicknamePool[i] = trimmed
		if trimmed == "" {
			errs = append(errs, fmt.Sprintf("nickname_pool[%d] is empty or whitespace", i))
			continue
		}
		if n := utf8.RuneCountInString(trimmed); n > 32 {
			errs = append(errs, fmt.Sprintf("nickname_pool[%d] length %d exceeds 32 runes", i, n))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("config validation failed:\n  %s", strings.Join(errs, "\n  "))
	}
	return nil
}
