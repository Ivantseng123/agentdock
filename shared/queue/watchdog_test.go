package queue_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/Ivantseng123/agentdock/shared/queue"
	"github.com/Ivantseng123/agentdock/shared/queue/queuetest"
)

func TestWatchdog_PublishesFailedResultOnTimeout(t *testing.T) {
	store := queue.NewMemJobStore()
	store.Put(&queue.Job{ID: "j1", SubmittedAt: time.Now().Add(-10 * time.Minute)})
	store.UpdateStatus("j1", queue.JobRunning)

	results := queuetest.NewResultBus(10)
	defer results.Close()

	commands := queuetest.NewCommandBus(10)
	defer commands.Close()

	wd := queue.NewWatchdog(store, commands, results, queue.WatchdogConfig{
		JobTimeout:     1 * time.Minute,
		IdleTimeout:    0,
		PrepareTimeout: 0,
	}, slog.Default())

	wd.Check()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	ch, _ := results.Subscribe(ctx)
	select {
	case result := <-ch:
		if result.JobID != "j1" {
			t.Errorf("jobID = %q, want j1", result.JobID)
		}
		if result.Status != "failed" {
			t.Errorf("status = %q, want failed", result.Status)
		}
		if result.Error == "" {
			t.Error("error should contain timeout reason")
		}
	case <-ctx.Done():
		t.Fatal("timeout waiting for result on ResultBus")
	}

	state, _ := store.Get("j1")
	if state.Status != queue.JobFailed {
		t.Errorf("store status = %q, want failed", state.Status)
	}
}

func TestWatchdog_CancelFallbackAfterTimeout(t *testing.T) {
	store := queue.NewMemJobStore()
	store.Put(&queue.Job{ID: "j1", SubmittedAt: time.Now().Add(-5 * time.Minute)})
	store.UpdateStatus("j1", queue.JobCancelled)
	// Force CancelledAt older than cancelTimeout
	state, _ := store.Get("j1")
	state.CancelledAt = time.Now().Add(-2 * time.Minute)

	results := queuetest.NewResultBus(10)
	defer results.Close()
	commands := queuetest.NewCommandBus(10)
	defer commands.Close()

	var onKillReasons []string
	wd := queue.NewWatchdog(store, commands, results, queue.WatchdogConfig{
		JobTimeout:    10 * time.Minute,
		CancelTimeout: 60 * time.Second,
	}, slog.Default(), queue.WithWatchdogKillHook(func(r string) { onKillReasons = append(onKillReasons, r) }))

	wd.Check()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	ch, _ := results.Subscribe(ctx)

	select {
	case r := <-ch:
		if r.Status != "cancelled" {
			t.Errorf("status = %q, want cancelled", r.Status)
		}
	case <-ctx.Done():
		t.Fatal("no result published")
	}
	if len(onKillReasons) != 1 || onKillReasons[0] != "cancel fallback" {
		t.Errorf("onKill reasons = %v, want [cancel fallback]", onKillReasons)
	}

	// Store status should remain JobCancelled (not flipped to JobFailed)
	state, _ = store.Get("j1")
	if state.Status != queue.JobCancelled {
		t.Errorf("store status = %q, want JobCancelled", state.Status)
	}
}

func TestWatchdog_CancelWithinTimeoutDoesNotFire(t *testing.T) {
	store := queue.NewMemJobStore()
	store.Put(&queue.Job{ID: "j1", SubmittedAt: time.Now().Add(-5 * time.Minute)})
	store.UpdateStatus("j1", queue.JobCancelled) // CancelledAt is now()

	results := queuetest.NewResultBus(10)
	defer results.Close()
	commands := queuetest.NewCommandBus(10)
	defer commands.Close()

	wd := queue.NewWatchdog(store, commands, results, queue.WatchdogConfig{
		JobTimeout:    10 * time.Minute,
		CancelTimeout: 60 * time.Second,
	}, slog.Default())

	wd.Check()

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	ch, _ := results.Subscribe(ctx)
	select {
	case r := <-ch:
		t.Fatalf("unexpected publish: %+v", r)
	case <-ctx.Done():
		// ok
	}
}

func TestWatchdog_CancelStatePreemptsJobTimeout(t *testing.T) {
	store := queue.NewMemJobStore()
	store.Put(&queue.Job{ID: "j1", SubmittedAt: time.Now().Add(-30 * time.Minute)})
	store.UpdateStatus("j1", queue.JobCancelled)

	results := queuetest.NewResultBus(10)
	defer results.Close()
	commands := queuetest.NewCommandBus(10)
	defer commands.Close()

	wd := queue.NewWatchdog(store, commands, results, queue.WatchdogConfig{
		JobTimeout:    1 * time.Minute, // already exceeded
		CancelTimeout: 0,               // disable fallback
	}, slog.Default())

	wd.Check()

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	ch, _ := results.Subscribe(ctx)
	select {
	case r := <-ch:
		t.Fatalf("expected no publish, got %+v", r)
	case <-ctx.Done():
		// ok — cancelled state must not trigger jobTimeout killAndPublish
	}
	state, _ := store.Get("j1")
	if state.Status != queue.JobCancelled {
		t.Errorf("store flipped to %q, should stay JobCancelled", state.Status)
	}
}

func TestWatchdog_KillAndPublishBackOffOnRaceCancel(t *testing.T) {
	store := queue.NewMemJobStore()
	store.Put(&queue.Job{ID: "j1", SubmittedAt: time.Now().Add(-5 * time.Minute)})
	store.UpdateStatus("j1", queue.JobRunning)

	results := queuetest.NewResultBus(10)
	defer results.Close()
	commands := queuetest.NewCommandBus(10)
	defer commands.Close()

	wd := queue.NewWatchdog(store, commands, results, queue.WatchdogConfig{}, slog.Default())

	// Simulate race: caller flips to JobCancelled between check() and killAndPublish.
	store.UpdateStatus("j1", queue.JobCancelled)

	state, _ := store.Get("j1")
	wd.KillAndPublish(state, "job timeout")

	// Store must remain JobCancelled (back-off kicked in).
	state, _ = store.Get("j1")
	if state.Status != queue.JobCancelled {
		t.Errorf("store flipped to %q, back-off failed", state.Status)
	}
}
