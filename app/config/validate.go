package config

import (
	"fmt"
	"path/filepath"
	"strings"
)

// Validate runs cross-field range checks on the merged Config and returns a
// single error listing every problem found (not fail-fast).
func Validate(cfg *Config) error {
	var errs []string

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
	if cfg.RateLimit.PerUser < 0 {
		errs = append(errs, "rate_limit.per_user must be >= 0")
	}
	if cfg.RateLimit.PerChannel < 0 {
		errs = append(errs, "rate_limit.per_channel must be >= 0")
	}
	if cfg.RateLimit.Window <= 0 {
		errs = append(errs, "rate_limit.window must be > 0")
	}
	if cfg.MaxThreadMessages < 1 {
		errs = append(errs, "max_thread_messages must be >= 1")
	}
	if cfg.SemaphoreTimeout <= 0 {
		errs = append(errs, "semaphore_timeout must be > 0")
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
	if (cfg.Mantis.BaseURL != "") != (cfg.Mantis.APIToken != "") {
		errs = append(errs, "mantis.base_url and mantis.api_token must both be set or both be empty")
	}

	if len(errs) > 0 {
		return fmt.Errorf("config validation failed:\n  %s", strings.Join(errs, "\n  "))
	}
	return nil
}
