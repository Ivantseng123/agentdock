package queue_test

import (
	"context"
	"testing"
	"time"

	"github.com/Ivantseng123/agentdock/shared/queue"
	"github.com/Ivantseng123/agentdock/shared/queue/queuetest"
)

func newAvail(t *testing.T) (*queuetest.JobQueue, queue.JobStore, queue.WorkerAvailability) {
	t.Helper()
	store := queue.NewMemJobStore()
	q := queuetest.NewJobQueue(50, store)
	a := queue.NewWorkerAvailability(q, store, queue.AvailabilityConfig{
		AvgJobDuration: 3 * time.Minute,
	})
	return q, store, a
}

func TestAvailability_HealthyOK(t *testing.T) {
	q, _, a := newAvail(t)

	q.Register(context.Background(), queue.WorkerInfo{WorkerID: "w1", Slots: 1})
	q.Register(context.Background(), queue.WorkerInfo{WorkerID: "w2", Slots: 1})

	v := a.CheckHard(context.Background())
	if v.Kind != queue.VerdictOK {
		t.Errorf("Kind = %q, want %q", v.Kind, queue.VerdictOK)
	}
	if v.WorkerCount != 2 {
		t.Errorf("WorkerCount = %d, want 2", v.WorkerCount)
	}
	if v.TotalSlots != 2 {
		t.Errorf("TotalSlots = %d, want 2", v.TotalSlots)
	}
}

func TestAvailability_NoWorkers(t *testing.T) {
	_, _, a := newAvail(t)

	v := a.CheckHard(context.Background())
	if v.Kind != queue.VerdictNoWorkers {
		t.Errorf("Kind = %q, want %q", v.Kind, queue.VerdictNoWorkers)
	}
	if v.WorkerCount != 0 {
		t.Errorf("WorkerCount = %d, want 0", v.WorkerCount)
	}
}

func TestAvailability_BusyEnqueueOK_FullySaturated(t *testing.T) {
	ctx := context.Background()
	q, store, a := newAvail(t)

	q.Register(ctx, queue.WorkerInfo{WorkerID: "w1", Slots: 1})
	q.Register(ctx, queue.WorkerInfo{WorkerID: "w2", Slots: 1})

	// Two jobs in JobRunning consume both slots; queue depth = 0.
	store.Put(ctx, &queue.Job{ID: "j1"})
	store.UpdateStatus(ctx, "j1", queue.JobRunning)
	store.Put(ctx, &queue.Job{ID: "j2"})
	store.UpdateStatus(ctx, "j2", queue.JobRunning)

	v := a.CheckHard(ctx)
	if v.Kind != queue.VerdictBusyEnqueueOK {
		t.Errorf("Kind = %q, want %q", v.Kind, queue.VerdictBusyEnqueueOK)
	}
	if v.ActiveJobs != 2 {
		t.Errorf("ActiveJobs = %d, want 2", v.ActiveJobs)
	}
	wantETA := 1 * 3 * time.Minute // overflow=1
	if v.EstimatedWait != wantETA {
		t.Errorf("EstimatedWait = %v, want %v", v.EstimatedWait, wantETA)
	}
}

func TestAvailability_BusyEnqueueOK_WithQueueDepth(t *testing.T) {
	ctx := context.Background()
	// queuetest.JobQueue.Submit pushes onto a heap that a background dispatch
	// goroutine drains into a channel; QueueDepth races against that drain
	// and is unreliable for "I just submitted N, expect depth=N" assertions.
	// Use a deterministic stub instead so the math under test isn't flaky.
	store := queue.NewMemJobStore()
	q := &fakeDepthQueue{
		workerListingQueue: workerListingQueue{
			workers: []queue.WorkerInfo{{WorkerID: "w1", Slots: 1}},
		},
		depth: 4,
	}
	a := queue.NewWorkerAvailability(q, store, queue.AvailabilityConfig{
		AvgJobDuration: 3 * time.Minute,
	})

	// 1 running in store + 4 from queue depth = 5 active, slots = 1, overflow = 5
	store.Put(ctx, &queue.Job{ID: "j1"})
	store.UpdateStatus(ctx, "j1", queue.JobRunning)

	v := a.CheckHard(context.Background())
	if v.Kind != queue.VerdictBusyEnqueueOK {
		t.Errorf("Kind = %q, want %q", v.Kind, queue.VerdictBusyEnqueueOK)
	}
	wantETA := time.Duration(5) * 3 * time.Minute
	if v.EstimatedWait != wantETA {
		t.Errorf("EstimatedWait = %v, want %v", v.EstimatedWait, wantETA)
	}
}

func TestAvailability_MultiSlotWorker(t *testing.T) {
	ctx := context.Background()
	q, store, a := newAvail(t)

	// One worker with 3 slots, two running jobs → 1 spare slot → OK.
	q.Register(ctx, queue.WorkerInfo{WorkerID: "w1", Slots: 3})
	store.Put(ctx, &queue.Job{ID: "j1"})
	store.UpdateStatus(ctx, "j1", queue.JobRunning)
	store.Put(ctx, &queue.Job{ID: "j2"})
	store.UpdateStatus(ctx, "j2", queue.JobRunning)

	v := a.CheckHard(ctx)
	if v.Kind != queue.VerdictOK {
		t.Errorf("Kind = %q, want %q (3 slots, 2 active)", v.Kind, queue.VerdictOK)
	}
	if v.TotalSlots != 3 {
		t.Errorf("TotalSlots = %d, want 3", v.TotalSlots)
	}
}

func TestAvailability_ZeroSlotsNormalisedToOne(t *testing.T) {
	ctx := context.Background()
	q, _, a := newAvail(t)

	// Slots=0 (e.g. older worker that didn't set the field) → treated as 1.
	q.Register(ctx, queue.WorkerInfo{WorkerID: "old", Slots: 0})

	v := a.CheckHard(ctx)
	if v.TotalSlots != 1 {
		t.Errorf("TotalSlots = %d, want 1 (normalised)", v.TotalSlots)
	}
	if v.Kind != queue.VerdictOK {
		t.Errorf("Kind = %q, want %q", v.Kind, queue.VerdictOK)
	}
}
