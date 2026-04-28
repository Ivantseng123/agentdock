package queue

import (
	"context"
	"log/slog"
	"time"
)

type VerdictKind string

const (
	VerdictOK            VerdictKind = "ok"
	VerdictBusyEnqueueOK VerdictKind = "busy_enqueue"
	VerdictNoWorkers     VerdictKind = "no_workers"
)

type Verdict struct {
	Kind          VerdictKind
	WorkerCount   int
	ActiveJobs    int
	TotalSlots    int
	EstimatedWait time.Duration
}

type WorkerAvailability interface {
	CheckSoft(ctx context.Context) Verdict
	CheckHard(ctx context.Context) Verdict
}

type AvailabilityConfig struct {
	AvgJobDuration time.Duration
}

// AvailabilityOption configures observability hooks without creating an
// import cycle with shared/metrics.
type AvailabilityOption func(*availability)

// WithVerdictHook is invoked on every compute() call with kind, stage, and duration.
func WithVerdictHook(fn func(kind, stage string, d time.Duration)) AvailabilityOption {
	return func(a *availability) { a.verdictHook = fn }
}

// WithDepErrorHook is invoked when a dependency call fails, with the
// dependency name (e.g. "list_workers", "list_all").
func WithDepErrorHook(fn func(dep string)) AvailabilityOption {
	return func(a *availability) { a.depErrorHook = fn }
}

type availability struct {
	queue        JobQueue
	store        JobStore
	avgJob       time.Duration
	logger       *slog.Logger
	verdictHook  func(kind, stage string, d time.Duration)
	depErrorHook func(dep string)
}

func NewWorkerAvailability(q JobQueue, store JobStore, cfg AvailabilityConfig, opts ...AvailabilityOption) WorkerAvailability {
	avg := cfg.AvgJobDuration
	if avg <= 0 {
		avg = 3 * time.Minute
	}
	a := &availability{
		queue:  q,
		store:  store,
		avgJob: avg,
		logger: slog.Default(),
	}
	for _, o := range opts {
		o(a)
	}
	return a
}

func (a *availability) CheckSoft(ctx context.Context) Verdict {
	return a.observe(ctx, "soft")
}

func (a *availability) CheckHard(ctx context.Context) Verdict {
	return a.observe(ctx, "hard")
}

func (a *availability) observe(ctx context.Context, stage string) Verdict {
	start := time.Now()
	v := a.compute(ctx)
	if a.verdictHook != nil {
		a.verdictHook(string(v.Kind), stage, time.Since(start))
	}
	return v
}

func (a *availability) compute(ctx context.Context) Verdict {
	workers, err := a.queue.ListWorkers(ctx)
	if err != nil {
		a.logger.Warn("可用性檢查: 列舉 worker 失敗", "phase", "失敗", "error", err)
		if a.depErrorHook != nil {
			a.depErrorHook("list_workers")
		}
		return Verdict{Kind: VerdictOK}
	}
	totalSlots := 0
	for _, w := range workers {
		totalSlots += normaliseSlots(w.Slots)
	}
	if len(workers) == 0 {
		return Verdict{Kind: VerdictNoWorkers}
	}

	// QueueDepth() has no error return (interface signature in queue.JobQueue),
	// so it never contributes to depErrorHook. Spec test matrix lists it as a
	// fail-open dep, but the interface makes that case unreachable today.
	depth := a.queue.QueueDepth()
	states, err := a.store.ListAll(ctx)
	if err != nil {
		a.logger.Warn("可用性檢查: 列舉工作狀態失敗", "phase", "失敗", "error", err)
		if a.depErrorHook != nil {
			a.depErrorHook("list_all")
		}
		return Verdict{Kind: VerdictOK, WorkerCount: len(workers), TotalSlots: totalSlots}
	}
	running := 0
	for _, s := range states {
		if s.Status == JobPreparing || s.Status == JobRunning {
			running++
		}
	}
	active := depth + running

	if active >= totalSlots {
		overflow := active - totalSlots + 1
		return Verdict{
			Kind:          VerdictBusyEnqueueOK,
			WorkerCount:   len(workers),
			TotalSlots:    totalSlots,
			ActiveJobs:    active,
			EstimatedWait: time.Duration(overflow) * a.avgJob,
		}
	}
	return Verdict{
		Kind:        VerdictOK,
		WorkerCount: len(workers),
		TotalSlots:  totalSlots,
		ActiveJobs:  active,
	}
}

func normaliseSlots(s int) int {
	if s <= 0 {
		return 1
	}
	return s
}
