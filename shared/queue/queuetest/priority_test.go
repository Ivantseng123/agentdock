package queuetest

import (
	"container/heap"
	"testing"

	"github.com/Ivantseng123/agentdock/shared/queue"
)

func TestPriorityQueue_HigherPriorityFirst(t *testing.T) {
	pq := &priorityQueue{}
	heap.Init(pq)

	heap.Push(pq, &queueEntry{job: &queue.Job{ID: "low", Priority: 10, Seq: 1}})
	heap.Push(pq, &queueEntry{job: &queue.Job{ID: "high", Priority: 100, Seq: 2}})
	heap.Push(pq, &queueEntry{job: &queue.Job{ID: "mid", Priority: 50, Seq: 3}})

	got := heap.Pop(pq).(*queueEntry).job.ID
	if got != "high" {
		t.Errorf("first pop = %q, want high", got)
	}
	got = heap.Pop(pq).(*queueEntry).job.ID
	if got != "mid" {
		t.Errorf("second pop = %q, want mid", got)
	}
	got = heap.Pop(pq).(*queueEntry).job.ID
	if got != "low" {
		t.Errorf("third pop = %q, want low", got)
	}
}

func TestPriorityQueue_FIFOWithinSamePriority(t *testing.T) {
	pq := &priorityQueue{}
	heap.Init(pq)

	heap.Push(pq, &queueEntry{job: &queue.Job{ID: "first", Priority: 50, Seq: 1}})
	heap.Push(pq, &queueEntry{job: &queue.Job{ID: "second", Priority: 50, Seq: 2}})
	heap.Push(pq, &queueEntry{job: &queue.Job{ID: "third", Priority: 50, Seq: 3}})

	got := heap.Pop(pq).(*queueEntry).job.ID
	if got != "first" {
		t.Errorf("first pop = %q, want first", got)
	}
	got = heap.Pop(pq).(*queueEntry).job.ID
	if got != "second" {
		t.Errorf("second pop = %q, want second", got)
	}
}

func TestPriorityQueue_LenAndEmpty(t *testing.T) {
	pq := &priorityQueue{}
	heap.Init(pq)

	if pq.Len() != 0 {
		t.Errorf("empty queue Len() = %d", pq.Len())
	}

	heap.Push(pq, &queueEntry{job: &queue.Job{ID: "a", Priority: 50, Seq: 1}})
	if pq.Len() != 1 {
		t.Errorf("after push Len() = %d, want 1", pq.Len())
	}

	heap.Pop(pq)
	if pq.Len() != 0 {
		t.Errorf("after pop Len() = %d, want 0", pq.Len())
	}
}
