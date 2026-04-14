package queue

import (
	"context"
	"testing"
	"time"
)

func TestWatchdog_PublishesFailedResultOnTimeout(t *testing.T) {
	store := NewMemJobStore()
	store.Put(&Job{ID: "j1", SubmittedAt: time.Now().Add(-10 * time.Minute)})
	store.UpdateStatus("j1", JobRunning)

	results := NewInMemResultBus(10)
	defer results.Close()

	commands := NewInMemCommandBus(10)
	defer commands.Close()

	wd := NewWatchdog(store, commands, results, WatchdogConfig{
		JobTimeout:     1 * time.Minute,
		IdleTimeout:    0,
		PrepareTimeout: 0,
	})

	wd.check()

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
	if state.Status != JobFailed {
		t.Errorf("store status = %q, want failed", state.Status)
	}
}
