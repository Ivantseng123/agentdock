package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"agentdock/internal/bot"
	"agentdock/internal/config"
	ghclient "agentdock/internal/github"
	"agentdock/internal/queue"
	"agentdock/internal/worker"
)

func runWorker() {
	fs := flag.NewFlagSet("worker", flag.ExitOnError)
	configPath := fs.String("config", "", "path to worker config file (optional, can use env vars only)")
	fs.Parse(os.Args[2:])

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))

	var cfg *config.Config
	var err error
	if *configPath != "" {
		cfg, err = config.Load(*configPath)
		if err != nil {
			slog.Error("failed to load config", "error", err)
			os.Exit(1)
		}
	} else {
		cfg, err = config.LoadDefaults()
		if err != nil {
			slog.Error("failed to load defaults", "error", err)
			os.Exit(1)
		}
	}

	rdb, err := queue.NewRedisClient(queue.RedisConfig{
		Addr:     cfg.Redis.Addr,
		Password: cfg.Redis.Password,
		DB:       cfg.Redis.DB,
		TLS:      cfg.Redis.TLS,
	})
	if err != nil {
		slog.Error("failed to connect to Redis", "error", err)
		os.Exit(1)
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
}
