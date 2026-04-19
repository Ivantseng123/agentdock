package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/Ivantseng123/agentdock/shared/queue"
	workerpool "github.com/Ivantseng123/agentdock/worker/pool"
)

// LocalAdapterConfig holds agent-specific configuration for the local adapter.
type LocalAdapterConfig struct {
	Runner         workerpool.Runner
	RepoCache      workerpool.RepoProvider
	SkillDirs      []string
	WorkerCount    int
	StatusInterval time.Duration
	Capabilities   []string
	Store          queue.JobStore
	Logger         *slog.Logger
	ExtraRules     []string
}

// LocalAdapter runs agents locally via workerpool.Pool.
// It implements queue.Adapter.
type LocalAdapter struct {
	cfg  LocalAdapterConfig
	pool *workerpool.Pool
}

func NewLocalAdapter(cfg LocalAdapterConfig) *LocalAdapter {
	return &LocalAdapter{cfg: cfg}
}

func (a *LocalAdapter) Name() string           { return "local" }
func (a *LocalAdapter) Capabilities() []string { return a.cfg.Capabilities }

func (a *LocalAdapter) Start(deps queue.AdapterDeps) error {
	a.pool = workerpool.NewPool(workerpool.Config{
		Queue:          deps.Jobs,
		Attachments:    deps.Attachments,
		Results:        deps.Results,
		Store:          a.cfg.Store,
		Runner:         a.cfg.Runner,
		RepoCache:      a.cfg.RepoCache,
		WorkerCount:    a.cfg.WorkerCount,
		SkillDirs:      a.cfg.SkillDirs,
		Commands:       deps.Commands,
		Status:         deps.Status,
		StatusInterval: a.cfg.StatusInterval,
		Logger:         a.cfg.Logger,
		ExtraRules:     a.cfg.ExtraRules,
	})
	a.pool.Start(context.Background())
	return nil
}

func (a *LocalAdapter) Stop() error {
	return nil
}
