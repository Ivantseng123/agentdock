// Package worker is the module entry point for the agentdock worker process.
// cmd/agentdock/worker.go wraps Run with cobra.
package worker

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/Ivantseng123/agentdock/shared/crypto"
	ghclient "github.com/Ivantseng123/agentdock/shared/github"
	"github.com/Ivantseng123/agentdock/shared/logging"
	"github.com/Ivantseng123/agentdock/shared/queue"
	"github.com/Ivantseng123/agentdock/worker/agent"
	"github.com/Ivantseng123/agentdock/worker/config"
	"github.com/Ivantseng123/agentdock/worker/pool"
)

// Run starts the worker process: initializes logging, connects Redis, builds
// the pool, and waits for SIGTERM/SIGINT. Returns on clean shutdown or error.
func Run(cfg *config.Config) error {
	stderrHandler := logging.NewStyledTextHandler(os.Stderr, &slog.HandlerOptions{Level: logging.ParseLevel(cfg.LogLevel)})

	rotator, err := logging.NewRotator(cfg.Logging.Dir)
	if err != nil {
		return fmt.Errorf("log rotator: %w", err)
	}
	rotator.StartCleanup(cfg.Logging.RetentionDays)

	fileHandler := slog.NewJSONHandler(rotator, &slog.HandlerOptions{Level: logging.ParseLevel(cfg.Logging.Level)})
	slog.SetDefault(slog.New(logging.NewMultiHandler(stderrHandler, fileHandler)))
	appLogger := logging.ComponentLogger(slog.Default(), logging.CompApp)

	// Transport selection. Kept in sync with app/app.go so a future backend
	// (e.g. github-runner) only needs a new case on both sides.
	jobStore := queue.NewMemJobStore()
	var bundle *queue.Bundle
	switch cfg.Queue.Transport {
	case "redis":
		rdb, err := queue.NewRedisClient(queue.RedisConfig{
			Addr:     cfg.Redis.Addr,
			Password: cfg.Redis.Password,
			DB:       cfg.Redis.DB,
			TLS:      cfg.Redis.TLS,
		})
		if err != nil {
			return fmt.Errorf("failed to connect to Redis: %w", err)
		}
		appLogger.Info("已連線至 Redis", "phase", "處理中", "addr", cfg.Redis.Addr)
		bundle = queue.NewRedisBundle(rdb, jobStore, "triage")
	default:
		return fmt.Errorf("unsupported queue.transport %q (supported: redis)", cfg.Queue.Transport)
	}

	agentRunner := agent.NewRunnerFromConfig(cfg)

	secretKey, err := crypto.DecodeSecretKey(cfg.SecretKey)
	if err != nil {
		return fmt.Errorf("invalid secret_key: %w", err)
	}

	githubLogger := logging.ComponentLogger(slog.Default(), logging.CompGitHub)
	repoCache := ghclient.NewRepoCache(cfg.RepoCache.Dir, cfg.RepoCache.MaxAge, cfg.GitHub.Token, githubLogger)

	repoAdapter := &pool.RepoCacheAdapter{Cache: repoCache}
	if err := repoAdapter.PurgeStale(); err != nil {
		appLogger.Warn("啟動時清理舊 repo 快取失敗", "phase", "處理中", "error", err)
	}

	var skillDirs []string
	seen := make(map[string]bool)
	for _, name := range cfg.Providers {
		if ag, ok := cfg.Agents[name]; ok && ag.SkillDir != "" && !seen[ag.SkillDir] {
			skillDirs = append(skillDirs, ag.SkillDir)
			seen[ag.SkillDir] = true
		}
	}

	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "unknown"
	}

	workerLogger := logging.ComponentLogger(slog.Default(), logging.CompWorker)
	workerPool := pool.NewPool(pool.Config{
		Queue:          bundle.Queue,
		Attachments:    bundle.Attachments,
		Results:        bundle.Results,
		Store:          jobStore,
		Runner:         &pool.AgentRunnerAdapter{Runner: agentRunner},
		RepoCache:      repoAdapter,
		WorkerCount:    cfg.Count,
		Hostname:       hostname,
		SkillDirs:      skillDirs,
		Commands:       bundle.Commands,
		Status:         bundle.Status,
		StatusInterval: cfg.Queue.StatusInterval,
		Logger:         workerLogger,
		SecretKey:      secretKey,
		WorkerSecrets:  cfg.Secrets,
		ExtraRules:     cfg.Prompt.ExtraRules,
	})

	ctx, cancel := context.WithCancel(context.Background())
	workerPool.Start(ctx)
	appLogger.Info("Worker 已啟動", "phase", "完成", "workers", cfg.Count)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	sig := <-sigCh
	appLogger.Info("正在關閉", "phase", "完成", "signal", sig)
	cancel()
	bundle.Close()
	return nil
}
