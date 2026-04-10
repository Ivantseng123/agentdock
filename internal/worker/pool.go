package worker

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"time"

	"slack-issue-bot/internal/bot"
	"slack-issue-bot/internal/queue"
)

type Config struct {
	Queue          queue.JobQueue
	Attachments    queue.AttachmentStore
	Results        queue.ResultBus
	Store          queue.JobStore
	Runner         Runner
	RepoCache      RepoProvider
	WorkerCount    int
	SkillDir       string
	Commands       queue.CommandBus
	Status         queue.StatusBus
	StatusInterval time.Duration
}

type Pool struct {
	cfg      Config
	registry *queue.ProcessRegistry
}

func NewPool(cfg Config) *Pool {
	return &Pool{
		cfg:      cfg,
		registry: queue.NewProcessRegistry(),
	}
}

func (p *Pool) Start(ctx context.Context) {
	if p.cfg.Commands != nil {
		go p.commandListener(ctx)
	}
	for i := 0; i < p.cfg.WorkerCount; i++ {
		go p.runWorker(ctx, i)
	}
	slog.Info("worker pool started", "count", p.cfg.WorkerCount)
}

func (p *Pool) commandListener(ctx context.Context) {
	commands, err := p.cfg.Commands.Receive(ctx)
	if err != nil {
		slog.Error("failed to receive commands", "error", err)
		return
	}
	for {
		select {
		case cmd, ok := <-commands:
			if !ok {
				return
			}
			if cmd.Action == "kill" {
				if err := p.registry.Kill(cmd.JobID); err != nil {
					slog.Warn("kill command failed", "job_id", cmd.JobID, "error", err)
				}
			}
		case <-ctx.Done():
			return
		}
	}
}

func (p *Pool) runWorker(ctx context.Context, id int) {
	logger := slog.With("worker_id", id)
	jobs, err := p.cfg.Queue.Receive(ctx)
	if err != nil {
		logger.Error("failed to receive jobs", "error", err)
		return
	}
	for {
		select {
		case job, ok := <-jobs:
			if !ok {
				logger.Info("job channel closed")
				return
			}
			// Check if cancelled while pending.
			state, err := p.cfg.Store.Get(job.ID)
			if err != nil || state.Status == queue.JobFailed {
				p.cfg.Results.Publish(ctx, &queue.JobResult{
					JobID: job.ID, Status: "failed", Error: "cancelled before execution",
				})
				continue
			}
			p.executeWithTracking(ctx, id, job)
		case <-ctx.Done():
			logger.Info("worker shutting down")
			return
		}
	}
}

func (p *Pool) executeWithTracking(ctx context.Context, workerID int, job *queue.Job) {
	logger := slog.With("worker_id", workerID, "job_id", job.ID)
	jobCtx, jobCancel := context.WithCancel(ctx)
	defer jobCancel()

	status := &statusAccumulator{
		jobID:    job.ID,
		workerID: fmt.Sprintf("worker-%d", workerID),
		alive:    true,
	}

	opts := bot.RunOptions{
		OnStarted: func(pid int, command string) {
			status.setPID(pid, command)
			p.registry.Register(job.ID, pid, command, jobCancel)
			logger.Info("agent registered", "pid", pid, "command", command)
		},
		OnEvent: func(event queue.StreamEvent) {
			status.recordEvent(event)
		},
	}

	// Status reporting.
	var stopReporter chan struct{}
	if p.cfg.Status != nil {
		stopReporter = make(chan struct{})
		interval := p.cfg.StatusInterval
		if interval <= 0 {
			interval = 5 * time.Second
		}
		go p.reportStatus(jobCtx, status, interval, stopReporter)
	}

	// Ack.
	if err := p.cfg.Queue.Ack(jobCtx, job.ID); err != nil {
		logger.Error("ack failed", "error", err)
		p.cfg.Results.Publish(ctx, &queue.JobResult{
			JobID: job.ID, Status: "failed", Error: fmt.Sprintf("ack failed: %v", err),
		})
		if stopReporter != nil {
			close(stopReporter)
		}
		return
	}

	deps := executionDeps{
		attachments: p.cfg.Attachments,
		repoCache:   p.cfg.RepoCache,
		runner:      p.cfg.Runner,
		store:       p.cfg.Store,
		skillDir:    p.cfg.SkillDir,
	}

	result := executeJob(jobCtx, job, deps, opts)

	if stopReporter != nil {
		close(stopReporter)
	}
	p.registry.Remove(job.ID)

	// Post-kill cleanup.
	if result.Status == "failed" {
		if repoPath, err := p.cfg.RepoCache.Prepare(job.CloneURL, job.Branch); err == nil {
			exec.Command("git", "-C", repoPath, "checkout", ".").Run()
			exec.Command("git", "-C", repoPath, "clean", "-fd").Run()
		}
	}

	p.cfg.Store.UpdateStatus(job.ID, queue.JobStatus(result.Status))
	if err := p.cfg.Results.Publish(ctx, result); err != nil {
		logger.Error("failed to publish result", "error", err)
	}
	logger.Info("job completed", "status", result.Status)
}

func (p *Pool) reportStatus(ctx context.Context, status *statusAccumulator, interval time.Duration, stop <-chan struct{}) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			p.cfg.Status.Report(ctx, status.toReport())
		case <-stop:
			return
		case <-ctx.Done():
			return
		}
	}
}
