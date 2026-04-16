package main

import (
	"strings"
	"testing"
	"time"

	"agentdock/internal/config"
)

func TestValidate_OK(t *testing.T) {
	cfg := goodConfig()
	if err := validate(cfg); err != nil {
		t.Errorf("validate(goodConfig) returned %v, want nil", err)
	}
}

func TestValidate_WorkersZero(t *testing.T) {
	cfg := goodConfig()
	cfg.Workers.Count = 0
	err := validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "workers.count must be >= 1") {
		t.Errorf("expected workers.count error, got %v", err)
	}
}

func TestValidate_WorkersZero_RedisTransport(t *testing.T) {
	cfg := goodConfig()
	cfg.Workers.Count = 0
	cfg.Queue.Transport = "redis"
	if err := validate(cfg); err != nil {
		t.Errorf("redis transport should allow workers.count=0, got %v", err)
	}
}

func TestValidate_MultipleErrors_ListedAtOnce(t *testing.T) {
	cfg := goodConfig()
	cfg.Workers.Count = 0
	cfg.Queue.Capacity = -5
	cfg.Queue.JobTimeout = 0
	err := validate(cfg)
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	for _, want := range []string{
		"workers.count must be >= 1",
		"queue.capacity must be >= 1",
		"queue.job_timeout must be > 0",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("expected %q in error, got: %s", want, msg)
		}
	}
}

func goodConfig() *config.Config {
	return &config.Config{
		Workers: config.WorkersConfig{Count: 3},
		Queue: config.QueueConfig{
			Capacity:         50,
			JobTimeout:       20 * time.Minute,
			AgentIdleTimeout: 5 * time.Minute,
			PrepareTimeout:   3 * time.Minute,
			StatusInterval:   5 * time.Second,
		},
		RateLimit:         config.RateLimitConfig{PerUser: 0, PerChannel: 0, Window: time.Minute},
		MaxConcurrent:     3,
		MaxThreadMessages: 50,
		SemaphoreTimeout:  30 * time.Second,
		Logging:           config.LoggingConfig{RetentionDays: 30},
	}
}
