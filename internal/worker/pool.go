package worker

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"agentdock/internal/bot"
	"agentdock/internal/queue"
)

type Config struct {
	Queue          queue.JobQueue
	Attachments    queue.AttachmentStore
	Results        queue.ResultBus
	Store          queue.JobStore
	Runner         Runner
	RepoCache      RepoProvider
	WorkerCount    int
	Hostname       string
	SkillDirs      []string
	Commands       queue.CommandBus
	Status         queue.StatusBus
	StatusInterval time.Duration
	Logger         *slog.Logger
	SecretKey      []byte
	WorkerSecrets  map[string]string
}

type Pool struct {
	cfg      Config
	registry *queue.ProcessRegistry
}

func NewPool(cfg Config) *Pool {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
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

	// Register workers with the queue and maintain heartbeat.
	go p.workerHeartbeat(ctx)

	p.cfg.Logger.Info("Worker pool 已啟動", "phase", "處理中", "count", p.cfg.WorkerCount)
}

func (p *Pool) commandListener(ctx context.Context) {
	commands, err := p.cfg.Commands.Receive(ctx)
	if err != nil {
		p.cfg.Logger.Error("接收指令失敗", "phase", "失敗", "error", err)
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
					p.cfg.Logger.Warn("終止指令失敗", "phase", "失敗", "job_id", cmd.JobID, "error", err)
				}
			}
		case <-ctx.Done():
			return
		}
	}
}

func (p *Pool) runWorker(ctx context.Context, id int) {
	logger := p.cfg.Logger.With("worker_id", id)
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
			if err != nil {
				p.cfg.Results.Publish(ctx, &queue.JobResult{
					JobID: job.ID, Status: "failed", Error: "state lookup failed",
				})
				continue
			}
			switch state.Status {
			case queue.JobCancelled:
				p.cfg.Results.Publish(ctx, &queue.JobResult{
					JobID: job.ID, Status: "cancelled",
				})
				continue
			case queue.JobFailed:
				p.cfg.Results.Publish(ctx, &queue.JobResult{
					JobID: job.ID, Status: "failed", Error: "terminated before execution",
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

func (p *Pool) executeWithTracking(ctx context.Context, workerIndex int, job *queue.Job) {
	logger := p.cfg.Logger.With("worker_id", workerIndex, "job_id", job.ID)
	jobCtx, jobCancel := context.WithCancel(ctx)
	defer jobCancel()

	p.registry.RegisterPending(job.ID, jobCancel)
	defer p.registry.Remove(job.ID)

	// Race mitigation: close the gap between queue-check and RegisterPending
	// for both cancel and admin-failed states.
	if s, _ := p.cfg.Store.Get(job.ID); s != nil &&
		(s.Status == queue.JobCancelled || s.Status == queue.JobFailed) {
		jobCancel()
	}

	wID := fmt.Sprintf("%s/worker-%d", p.cfg.Hostname, workerIndex)

	status := &statusAccumulator{
		jobID:    job.ID,
		workerID: wID,
		alive:    true,
	}

	// Status reporting — starts AFTER agent process launches (OnStarted).
	var stopReporter chan struct{}

	opts := bot.RunOptions{
		OnStarted: func(pid int, command string) {
			status.setPID(pid, command)
			p.registry.SetStarted(job.ID, pid, command)
			logger.Info("Agent 已註冊", "phase", "處理中", "pid", pid, "command", command)

			// Now that we have a PID, send first report immediately + start periodic.
			if p.cfg.Status != nil {
				p.publishStatus(jobCtx, job.ID, status)
				stopReporter = make(chan struct{})
				interval := p.cfg.StatusInterval
				if interval <= 0 {
					interval = 5 * time.Second
				}
				go p.reportStatus(jobCtx, job.ID, status, interval, stopReporter)
			}
		},
		OnEvent: func(event queue.StreamEvent) {
			status.recordEvent(event)
		},
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

	p.cfg.Store.SetWorker(job.ID, wID)

	// Prep-phase status signal — PID=0, AgentCmd="" lets StatusListener render
	// the "準備中" template before the agent process starts.
	if p.cfg.Status != nil {
		p.publishStatus(jobCtx, job.ID, status)
	}

	deps := executionDeps{
		attachments:   p.cfg.Attachments,
		repoCache:     p.cfg.RepoCache,
		runner:        p.cfg.Runner,
		store:         p.cfg.Store,
		skillDirs:     p.cfg.SkillDirs,
		secretKey:     p.cfg.SecretKey,
		workerSecrets: p.cfg.WorkerSecrets,
	}

	result := executeJob(jobCtx, job, deps, opts, logger)
	status.setPrepareSeconds(result.PrepareSeconds)

	// Send final status report (captures cost/tokens from result event).
	status.alive = false
	if p.cfg.Status != nil {
		p.publishStatus(ctx, job.ID, status)
	}

	if stopReporter != nil {
		close(stopReporter)
	}

	// Clean up this job's worktree.
	if result.RepoPath != "" {
		if err := p.cfg.RepoCache.RemoveWorktree(result.RepoPath); err != nil {
			logger.Warn("Worktree 清理失敗", "phase", "失敗", "path", result.RepoPath, "error", err)
		}
	}

	p.cfg.Store.UpdateStatus(job.ID, queue.JobStatus(result.Status))
	if err := p.cfg.Results.Publish(ctx, result); err != nil {
		logger.Error("failed to publish result", "error", err)
	}

	if result.Status == "cancelled" {
		logger.Info("工作已取消", "phase", "完成")
	} else {
		logger.Info("工作完成", "phase", "完成", "status", result.Status)
	}
}

func (p *Pool) workerHeartbeat(ctx context.Context) {
	now := time.Now()
	for i := 0; i < p.cfg.WorkerCount; i++ {
		info := queue.WorkerInfo{
			WorkerID:    fmt.Sprintf("%s/worker-%d", p.cfg.Hostname, i),
			Name:        p.cfg.Hostname,
			ConnectedAt: now,
		}
		if err := p.cfg.Queue.Register(ctx, info); err != nil {
			p.cfg.Logger.Warn("Worker 註冊失敗", "phase", "失敗", "worker_id", info.WorkerID, "error", err)
		}
	}

	// Re-register every 20s to keep the 30s TTL alive.
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			for i := 0; i < p.cfg.WorkerCount; i++ {
				info := queue.WorkerInfo{
					WorkerID:    fmt.Sprintf("%s/worker-%d", p.cfg.Hostname, i),
					Name:        p.cfg.Hostname,
					ConnectedAt: now,
				}
				p.cfg.Queue.Register(ctx, info)
			}
		case <-ctx.Done():
			// Best-effort unregister with short timeout; TTL expires in 30s anyway.
			unregCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			for i := 0; i < p.cfg.WorkerCount; i++ {
				wID := fmt.Sprintf("%s/worker-%d", p.cfg.Hostname, i)
				p.cfg.Queue.Unregister(unregCtx, wID)
			}
			cancel()
			// Clean up all cached repos and worktrees.
			if err := p.cfg.RepoCache.CleanAll(); err != nil {
				p.cfg.Logger.Warn("關機時 repo 清理失敗", "phase", "失敗", "error", err)
			}
			return
		}
	}
}

func (p *Pool) reportStatus(ctx context.Context, jobID string, status *statusAccumulator, interval time.Duration, stop <-chan struct{}) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			p.publishStatus(ctx, jobID, status)
		case <-stop:
			return
		case <-ctx.Done():
			return
		}
	}
}

// publishStatus stamps the current worker-side JobStatus onto the report before
// emission so the app pod's JobStore can track the lifecycle across transports.
// A miss on store.Get (job already cleaned up) drops the JobStatus tag but still
// ships the runtime telemetry so late reports don't get lost.
func (p *Pool) publishStatus(ctx context.Context, jobID string, status *statusAccumulator) {
	if p.cfg.Status == nil {
		return
	}
	report := status.toReport()
	if p.cfg.Store != nil {
		if state, err := p.cfg.Store.Get(jobID); err == nil && state != nil {
			report.JobStatus = state.Status
		}
	}
	p.cfg.Status.Report(ctx, report)
}
