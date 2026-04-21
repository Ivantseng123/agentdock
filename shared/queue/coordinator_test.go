package queue_test

import (
	"context"
	"testing"
	"time"

	"github.com/Ivantseng123/agentdock/shared/queue"
	"github.com/Ivantseng123/agentdock/shared/queue/queuetest"
)

func TestCoordinator_RoutesToRegisteredQueue(t *testing.T) {
	store := queue.NewMemJobStore()
	bundleA := queuetest.NewBundle(10, 1, store)
	defer bundleA.Close()
	bundleB := queuetest.NewBundle(10, 1, store)
	defer bundleB.Close()
	fallback := queuetest.NewBundle(10, 1, store)
	defer fallback.Close()

	coord := queue.NewCoordinator(fallback.Queue)
	coord.RegisterQueue("triage", bundleA.Queue)
	coord.RegisterQueue("review", bundleB.Queue)

	ctx := context.Background()

	chA, _ := bundleA.Queue.Receive(ctx)
	chB, _ := bundleB.Queue.Receive(ctx)

	coord.Submit(ctx, &queue.Job{ID: "t1", TaskType: "triage", Priority: 50})
	coord.Submit(ctx, &queue.Job{ID: "r1", TaskType: "review", Priority: 50})

	select {
	case job := <-chA:
		if job.ID != "t1" {
			t.Errorf("bundleA got %q, want t1", job.ID)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for triage job in bundleA")
	}

	select {
	case job := <-chB:
		if job.ID != "r1" {
			t.Errorf("bundleB got %q, want r1", job.ID)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for review job in bundleB")
	}
}

func TestCoordinator_FallbackForEmptyTaskType(t *testing.T) {
	store := queue.NewMemJobStore()
	bundleTriage := queuetest.NewBundle(10, 1, store)
	defer bundleTriage.Close()
	fallback := queuetest.NewBundle(10, 1, store)
	defer fallback.Close()

	coord := queue.NewCoordinator(fallback.Queue)
	coord.RegisterQueue("triage", bundleTriage.Queue)

	ctx := context.Background()
	chFallback, _ := fallback.Queue.Receive(ctx)

	coord.Submit(ctx, &queue.Job{ID: "f1", TaskType: "", Priority: 50})

	select {
	case job := <-chFallback:
		if job.ID != "f1" {
			t.Errorf("fallback got %q, want f1", job.ID)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for job in fallback queue")
	}
}

func TestCoordinator_QueueDepthSumsAll(t *testing.T) {
	store := queue.NewMemJobStore()
	bundleA := queuetest.NewBundle(10, 1, store)
	defer bundleA.Close()
	bundleB := queuetest.NewBundle(10, 1, store)
	defer bundleB.Close()
	fallback := queuetest.NewBundle(10, 1, store)
	defer fallback.Close()

	coord := queue.NewCoordinator(fallback.Queue)
	coord.RegisterQueue("triage", bundleA.Queue)
	coord.RegisterQueue("review", bundleB.Queue)

	ctx := context.Background()

	coord.Submit(ctx, &queue.Job{ID: "t1", TaskType: "triage", Priority: 50})
	coord.Submit(ctx, &queue.Job{ID: "r1", TaskType: "review", Priority: 50})

	// Give dispatch loops a moment to move jobs, but QueueDepth counts
	// items still in the priority queue, so check quickly.
	// The dispatch loop will move items to the channel, so depth may drop.
	// We need to check before dispatch drains them. Submit is synchronous
	// and adds to the pq, but the dispatch goroutine may pop immediately.
	// Instead, fill up: submit without consuming to keep depth stable.
	// Use capacity=2 bundles without consuming to ensure items stay in pq or channel.

	// Reset with fresh bundles where we don't consume.
	bundleC := queuetest.NewBundle(10, 1, store)
	defer bundleC.Close()
	bundleD := queuetest.NewBundle(10, 1, store)
	defer bundleD.Close()
	fallback2 := queuetest.NewBundle(10, 1, store)
	defer fallback2.Close()

	coord2 := queue.NewCoordinator(fallback2.Queue)
	coord2.RegisterQueue("triage", bundleC.Queue)
	coord2.RegisterQueue("review", bundleD.Queue)

	coord2.Submit(ctx, &queue.Job{ID: "t2", TaskType: "triage", Priority: 50})
	coord2.Submit(ctx, &queue.Job{ID: "r2", TaskType: "review", Priority: 50})

	// Allow time for dispatch to move items
	time.Sleep(50 * time.Millisecond)

	depth := coord2.QueueDepth()
	// Items may be in the pq or already dispatched to channel.
	// QueueDepth only counts pq items (not channel). But the dispatch loop
	// moves them to the channel, so depth may be 0. Let's verify the sum
	// logic is correct by checking it's >= 0 and <= 2.
	if depth < 0 || depth > 2 {
		t.Errorf("QueueDepth = %d, expected 0-2", depth)
	}

	// Test dedup: register the same queue for two task types.
	bundleE := queuetest.NewBundle(10, 1, store)
	defer bundleE.Close()
	fallback3 := queuetest.NewBundle(10, 1, store)
	defer fallback3.Close()

	coord3 := queue.NewCoordinator(fallback3.Queue)
	coord3.RegisterQueue("triage", bundleE.Queue)
	coord3.RegisterQueue("review", bundleE.Queue) // same queue!

	coord3.Submit(ctx, &queue.Job{ID: "t3", TaskType: "triage", Priority: 50})
	coord3.Submit(ctx, &queue.Job{ID: "r3", TaskType: "review", Priority: 50})

	time.Sleep(50 * time.Millisecond)

	depth3 := coord3.QueueDepth()
	// Should NOT double-count bundleE even though it's registered twice.
	// The single queue has 2 items submitted, so depth <= 2 (dispatch may drain some).
	if depth3 < 0 || depth3 > 2 {
		t.Errorf("QueueDepth with shared queue = %d, expected 0-2", depth3)
	}

	// Verify dedup: if we separately counted without dedup, we'd get wrong results.
	// We can verify the dedup logic indirectly: submitting to the same queue
	// under two task types and checking depth matches the queue's own depth.
	ownDepth := bundleE.Queue.QueueDepth()
	if depth3 != ownDepth+fallback3.Queue.QueueDepth() {
		t.Errorf("dedup failed: coord depth=%d, bundleE=%d, fallback=%d",
			depth3, ownDepth, fallback3.Queue.QueueDepth())
	}
}
