package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/Ivantseng123/agentdock/internal/bot"
	"github.com/Ivantseng123/agentdock/internal/config"
	ghclient "github.com/Ivantseng123/agentdock/shared/github"
	"github.com/Ivantseng123/agentdock/shared/logging"
	"github.com/Ivantseng123/agentdock/shared/queue"
	"github.com/Ivantseng123/agentdock/internal/worker"

	"github.com/spf13/cobra"
)

var workerConfigPath string

var workerCmd = &cobra.Command{
	Use:          "worker",
	Short:        "Run a worker process (Redis mode)",
	SilenceUsage: true,
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		return loadAndStash(cmd, workerConfigPath, ScopeWorker)
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		return runWorker(cfgFromCtx(cmd.Context()))
	},
}

func init() {
	workerCmd.Flags().StringVarP(&workerConfigPath, "config", "c", "", "path to worker config file (default ~/.config/agentdock/config.yaml)")
}

func runWorker(cfg *config.Config) error {
	// Preflight runs in PersistentPreRunE. slog initialized AFTER preflight
	// to keep interactive output clean.
	stderrHandler := logging.NewStyledTextHandler(os.Stderr, &slog.HandlerOptions{Level: parseLogLevel(cfg.LogLevel)})

	rotator, err := logging.NewRotator(cfg.Logging.Dir)
	if err != nil {
		return fmt.Errorf("log rotator: %w", err)
	}
	rotator.StartCleanup(cfg.Logging.RetentionDays)

	fileHandler := slog.NewJSONHandler(rotator, &slog.HandlerOptions{Level: parseLogLevel(cfg.Logging.Level)})
	slog.SetDefault(slog.New(logging.NewMultiHandler(stderrHandler, fileHandler)))
	appLogger := logging.ComponentLogger(slog.Default(), logging.CompApp)

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

	jobStore := queue.NewMemJobStore() // local ephemeral store for in-flight jobs

	bundle := queue.NewRedisBundle(rdb, jobStore, "triage")

	agentRunner := bot.NewAgentRunnerFromConfig(cfg)

	// secret_key is validated and beacon-verified during preflight.
	secretKey, err := config.DecodeSecretKey(cfg.SecretKey)
	if err != nil {
		return fmt.Errorf("invalid secret_key: %w", err)
	}

	githubLogger := logging.ComponentLogger(slog.Default(), logging.CompGitHub)
	repoCache := ghclient.NewRepoCache(cfg.RepoCache.Dir, cfg.RepoCache.MaxAge, cfg.GitHub.Token, githubLogger)

	// Purge stale repos from previous unclean shutdown.
	repoAdapter := &repoCacheAdapter{cache: repoCache}
	if err := repoAdapter.PurgeStale(); err != nil {
		appLogger.Warn("啟動時清理舊 repo 快取失敗", "phase", "處理中", "error", err)
	}

	// Collect skill dirs.
	var skillDirs []string
	seen := make(map[string]bool)
	for _, name := range cfg.Providers {
		if agent, ok := cfg.Agents[name]; ok && agent.SkillDir != "" && !seen[agent.SkillDir] {
			skillDirs = append(skillDirs, agent.SkillDir)
			seen[agent.SkillDir] = true
		}
	}

	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "unknown"
	}

	workerLogger := logging.ComponentLogger(slog.Default(), logging.CompWorker)
	pool := worker.NewPool(worker.Config{
		Queue:          bundle.Queue,
		Attachments:    bundle.Attachments,
		Results:        bundle.Results,
		Store:          jobStore,
		Runner:         &agentRunnerAdapter{runner: agentRunner},
		RepoCache:      repoAdapter,
		WorkerCount:    cfg.Worker.Count,
		Hostname:       hostname,
		SkillDirs:      skillDirs,
		Commands:       bundle.Commands,
		Status:         bundle.Status,
		StatusInterval: cfg.Queue.StatusInterval,
		Logger:         workerLogger,
		SecretKey:      secretKey,
		WorkerSecrets:  cfg.Secrets,
		ExtraRules:     cfg.Worker.Prompt.ExtraRules,
	})

	ctx, cancel := context.WithCancel(context.Background())
	pool.Start(ctx)
	appLogger.Info("Worker 已啟動", "phase", "完成", "workers", cfg.Worker.Count)

	// Wait for SIGTERM/SIGINT.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	sig := <-sigCh
	appLogger.Info("正在關閉", "phase", "完成", "signal", sig)
	cancel()
	bundle.Close()
	return nil
}
