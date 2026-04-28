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
	CancelTimeout  time.Duration
}

type Watchdog struct {
	store          JobStore
	commands       CommandBus
	results        ResultBus
	jobTimeout     time.Duration
	idleTimeout    time.Duration
	prepareTimeout time.Duration
	cancelTimeout  time.Duration
	interval       time.Duration
	logger         *slog.Logger
	onKill         func(reason string) // optional metric hook
}

// WatchdogOption is a functional option for Watchdog.
type WatchdogOption func(*Watchdog)

// WithWatchdogKillHook sets a callback invoked on every killAndPublish call
// with the kill reason. Use this to record metrics without creating an import
// cycle.
func WithWatchdogKillHook(fn func(reason string)) WatchdogOption {
	return func(w *Watchdog) { w.onKill = fn }
}

func NewWatchdog(store JobStore, commands CommandBus, results ResultBus, cfg WatchdogConfig, logger *slog.Logger, opts ...WatchdogOption) *Watchdog {
	interval := cfg.JobTimeout / 3
	if interval < 30*time.Second {
		interval = 30 * time.Second
	}
	w := &Watchdog{
		store:          store,
		commands:       commands,
		results:        results,
		jobTimeout:     cfg.JobTimeout,
		idleTimeout:    cfg.IdleTimeout,
		prepareTimeout: cfg.PrepareTimeout,
		cancelTimeout:  cfg.CancelTimeout,
		interval:       interval,
		logger:         logger,
	}
	for _, o := range opts {
		o(w)
	}
	return w
}

func (w *Watchdog) Start(stop <-chan struct{}) {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	w.logger.Info("Watchdog 已啟動", "phase", "處理中",
		"job_timeout", w.jobTimeout,
		"idle_timeout", w.idleTimeout,
		"prepare_timeout", w.prepareTimeout,
		"cancel_timeout", w.cancelTimeout,
		"check_interval", w.interval,
	)

	for {
		select {
		case <-ticker.C:
			w.check()
		case <-stop:
			w.logger.Info("Watchdog 已停止", "phase", "完成")
			return
		}
	}
}

// DefaultStoreOpTimeout bounds each JobStore / CommandBus call that lacks a
// caller-provided deadline so a degraded backend (Redis hang) cannot stall the
// caller indefinitely. 5s is comfortably above a healthy Redis round-trip yet
// surfaces problems well before the next tick / user retry.
const DefaultStoreOpTimeout = 5 * time.Second

func (w *Watchdog) check() {
	ctx, cancel := context.WithTimeout(context.Background(), DefaultStoreOpTimeout)
	defer cancel()
	all, err := w.store.ListAll(ctx)
	if err != nil {
		w.logger.Warn("Watchdog 列舉工作失敗", "phase", "失敗", "error", err)
		return
	}

	now := time.Now()
	for _, state := range all {
		// Terminal states (no action needed).
		if state.Status == JobCompleted || state.Status == JobFailed {
			continue
		}

		// Cancelled: wait for worker, fall back after cancelTimeout.
		if state.Status == JobCancelled {
			if w.cancelTimeout > 0 && !state.CancelledAt.IsZero() &&
				now.Sub(state.CancelledAt) > w.cancelTimeout {
				w.publishCancelledFallback(state)
			}
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

func (w *Watchdog) publishCancelledFallback(state *JobState) {
	if w.onKill != nil {
		w.onKill("cancel fallback")
	}
	w.logger.Warn("取消狀態逾時，補發 cancelled result", "phase", "完成",
		"job_id", state.Job.ID,
		"cancelled_age", time.Since(state.CancelledAt))
	if w.results != nil {
		ctx, cancel := context.WithTimeout(context.Background(), DefaultStoreOpTimeout)
		defer cancel()
		w.results.Publish(ctx, &JobResult{
			JobID:      state.Job.ID,
			Status:     "cancelled",
			FinishedAt: time.Now(),
		})
	}
}

func (w *Watchdog) killAndPublish(state *JobState, reason string) {
	ctx, cancel := context.WithTimeout(context.Background(), DefaultStoreOpTimeout)
	defer cancel()
	// Back off if the job was cancelled in the race window.
	if fresh, _ := w.store.Get(ctx, state.Job.ID); fresh != nil && fresh.Status == JobCancelled {
		return
	}

	if w.onKill != nil {
		w.onKill(reason)
	}
	w.logger.Warn("強制終止逾時工作", "phase", "失敗",
		"job_id", state.Job.ID, "status", state.Status, "reason", reason)

	if w.commands != nil {
		w.commands.Send(ctx, Command{JobID: state.Job.ID, Action: "kill"})
	}

	w.store.UpdateStatus(ctx, state.Job.ID, JobFailed)

	if w.results != nil {
		w.results.Publish(ctx, &JobResult{
			JobID:      state.Job.ID,
			Status:     "failed",
			Error:      fmt.Sprintf("job terminated: %s", reason),
			FinishedAt: time.Now(),
		})
	}
}
