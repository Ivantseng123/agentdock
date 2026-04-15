package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"agentdock/internal/bot"
	"agentdock/internal/config"
	ghclient "agentdock/internal/github"
	"agentdock/internal/queue"
	"agentdock/internal/worker"

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
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))

	rdb, err := queue.NewRedisClient(queue.RedisConfig{
		Addr:     cfg.Redis.Addr,
		Password: cfg.Redis.Password,
		DB:       cfg.Redis.DB,
		TLS:      cfg.Redis.TLS,
	})
	if err != nil {
		return fmt.Errorf("failed to connect to Redis: %w", err)
	}
	slog.Info("connected to Redis", "addr", cfg.Redis.Addr)

	jobStore := queue.NewMemJobStore() // local ephemeral store for in-flight jobs

	bundle := queue.NewRedisBundle(rdb, jobStore, "triage")

	agentRunner := bot.NewAgentRunnerFromConfig(cfg)
	repoCache := ghclient.NewRepoCache(cfg.RepoCache.Dir, cfg.RepoCache.MaxAge, cfg.GitHub.Token)

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

	pool := worker.NewPool(worker.Config{
		Queue:          bundle.Queue,
		Attachments:    bundle.Attachments,
		Results:        bundle.Results,
		Store:          jobStore,
		Runner:         &agentRunnerAdapter{runner: agentRunner},
		RepoCache:      &repoCacheAdapter{cache: repoCache},
		WorkerCount:    cfg.Workers.Count,
		Hostname:       hostname,
		SkillDirs:      skillDirs,
		Commands:       bundle.Commands,
		Status:         bundle.Status,
		StatusInterval: cfg.Queue.StatusInterval,
	})

	ctx, cancel := context.WithCancel(context.Background())
	pool.Start(ctx)
	slog.Info("worker started", "workers", cfg.Workers.Count)

	// Wait for SIGTERM/SIGINT.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	sig := <-sigCh
	slog.Info("shutting down", "signal", sig)
	cancel()
	bundle.Close()
	return nil
}
