package main

import (
	"fmt"
	"strings"

	"agentdock/internal/config"
)

// validate runs all cross-field range checks on the merged Config and returns
// a single error listing every problem found (not fail-fast). Per D15.
func validate(cfg *config.Config) error {
	var errs []string

	if cfg.Queue.Transport != "redis" && cfg.Workers.Count < 1 {
		errs = append(errs, "workers.count must be >= 1")
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
	if cfg.RateLimit.PerUser < 0 {
		errs = append(errs, "rate_limit.per_user must be >= 0")
	}
	if cfg.RateLimit.PerChannel < 0 {
		errs = append(errs, "rate_limit.per_channel must be >= 0")
	}
	if cfg.RateLimit.Window <= 0 {
		errs = append(errs, "rate_limit.window must be > 0")
	}
	if cfg.MaxConcurrent < 1 {
		errs = append(errs, "max_concurrent must be >= 1")
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

	if len(errs) > 0 {
		return fmt.Errorf("config validation failed:\n  %s", strings.Join(errs, "\n  "))
	}
	return nil
}
