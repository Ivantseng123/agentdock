package queue

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

type WatchdogConfig struct {
	JobTimeout     time.Duration
	IdleTimeout    time.Duration
	PrepareTimeout time.Duration
}

type Watchdog struct {
	store          JobStore
	commands       CommandBus
	results        ResultBus
	jobTimeout     time.Duration
	idleTimeout    time.Duration
	prepareTimeout time.Duration
	interval       time.Duration
}

func NewWatchdog(store JobStore, commands CommandBus, results ResultBus, cfg WatchdogConfig) *Watchdog {
	interval := cfg.JobTimeout / 3
	if interval < 30*time.Second {
		interval = 30 * time.Second
	}
	return &Watchdog{
		store:          store,
		commands:       commands,
		results:        results,
		jobTimeout:     cfg.JobTimeout,
		idleTimeout:    cfg.IdleTimeout,
		prepareTimeout: cfg.PrepareTimeout,
		interval:       interval,
	}
}

func (w *Watchdog) Start(stop <-chan struct{}) {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	slog.Info("job watchdog started",
		"job_timeout", w.jobTimeout,
		"idle_timeout", w.idleTimeout,
		"prepare_timeout", w.prepareTimeout,
		"check_interval", w.interval,
	)

	for {
		select {
		case <-ticker.C:
			w.check()
		case <-stop:
			slog.Info("job watchdog stopped")
			return
		}
	}
}

func (w *Watchdog) check() {
	all, err := w.store.ListAll()
	if err != nil {
		slog.Warn("watchdog: failed to list jobs", "error", err)
		return
	}

	now := time.Now()
	for _, state := range all {
		if state.Status == JobCompleted || state.Status == JobFailed {
			continue
		}

		if now.Sub(state.Job.SubmittedAt) > w.jobTimeout {
			w.killAndPublish(state, "job timeout")
			continue
		}

		if state.Status == JobPreparing && w.prepareTimeout > 0 {
			if state.AgentStatus == nil || state.AgentStatus.LastEventAt.IsZero() {
				if !state.StartedAt.IsZero() && now.Sub(state.StartedAt) > w.prepareTimeout {
					w.killAndPublish(state, "prepare timeout")
					continue
				}
			}
		}

		if w.idleTimeout > 0 && state.AgentStatus != nil && !state.AgentStatus.LastEventAt.IsZero() {
			if now.Sub(state.AgentStatus.LastEventAt) > w.idleTimeout {
				w.killAndPublish(state, "agent idle timeout")
				continue
			}
		}
	}
}

func (w *Watchdog) killAndPublish(state *JobState, reason string) {
	slog.Warn("watchdog: killing stuck job",
		"job_id", state.Job.ID, "status", state.Status, "reason", reason)

	if w.commands != nil {
		w.commands.Send(context.Background(), Command{JobID: state.Job.ID, Action: "kill"})
	}

	w.store.UpdateStatus(state.Job.ID, JobFailed)

	if w.results != nil {
		w.results.Publish(context.Background(), &JobResult{
			JobID:      state.Job.ID,
			Status:     "failed",
			Error:      fmt.Sprintf("job terminated: %s", reason),
			FinishedAt: time.Now(),
		})
	}
}
