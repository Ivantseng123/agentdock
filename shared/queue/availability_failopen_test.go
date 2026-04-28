package queue_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Ivantseng123/agentdock/shared/queue"
)

// erroringQueue satisfies queue.JobQueue but returns errors on the methods
// availability depends on. Other methods panic — they should never be called
// by the availability service.
type erroringQueue struct {
	listWorkersErr error
}

func (e *erroringQueue) Submit(context.Context, *queue.Job) error { panic("unused") }
func (e *erroringQueue) QueuePosition(string) (int, error)        { panic("unused") }
func (e *erroringQueue) QueueDepth() int                          { return 0 }
func (e *erroringQueue) Receive(context.Context) (<-chan *queue.Job, error) {
	panic("unused")
}
func (e *erroringQueue) Ack(context.Context, string) error { panic("unused") }
func (e *erroringQueue) Register(context.Context, queue.WorkerInfo) error {
	panic("unused")
}
func (e *erroringQueue) Unregister(context.Context, string) error { panic("unused") }
func (e *erroringQueue) ListWorkers(context.Context) ([]queue.WorkerInfo, error) {
	if e.listWorkersErr != nil {
		return nil, e.listWorkersErr
	}
	return nil, nil
}
func (e *erroringQueue) Close() error { return nil }

// erroringStore satisfies queue.JobStore but errors on ListAll.
type erroringStore struct {
	listAllErr error
}

func (s *erroringStore) Put(context.Context, *queue.Job) error { panic("unused") }
func (s *erroringStore) Get(context.Context, string) (*queue.JobState, error) {
	panic("unused")
}
func (s *erroringStore) GetByThread(context.Context, string, string) (*queue.JobState, error) {
	panic("unused")
}
func (s *erroringStore) ListPending(context.Context) ([]*queue.JobState, error) {
	panic("unused")
}
func (s *erroringStore) UpdateStatus(context.Context, string, queue.JobStatus) error {
	panic("unused")
}
func (s *erroringStore) SetWorker(context.Context, string, string) error { panic("unused") }
func (s *erroringStore) SetAgentStatus(context.Context, string, queue.StatusReport) error {
	panic("unused")
}
func (s *erroringStore) Delete(context.Context, string) error { panic("unused") }
func (s *erroringStore) ListAll(context.Context) ([]*queue.JobState, error) {
	if s.listAllErr != nil {
		return nil, s.listAllErr
	}
	return nil, nil
}

func TestAvailability_FailOpen_ListWorkersError(t *testing.T) {
	q := &erroringQueue{listWorkersErr: errors.New("redis down")}
	store := queue.NewMemJobStore()
	a := queue.NewWorkerAvailability(q, store, queue.AvailabilityConfig{
		AvgJobDuration: 3 * time.Minute,
	})

	v := a.CheckHard(context.Background())
	if v.Kind != queue.VerdictOK {
		t.Errorf("Kind = %q, want %q (fail-open)", v.Kind, queue.VerdictOK)
	}
}

func TestAvailability_FailOpen_ListAllError(t *testing.T) {
	// Need a queue that returns at least one worker (so we pass the NoWorkers gate)
	// AND a store that errors on ListAll.
	q := &workerListingQueue{erroringQueue: erroringQueue{}, workers: []queue.WorkerInfo{{WorkerID: "w1"}}}
	store := &erroringStore{listAllErr: errors.New("store down")}
	a := queue.NewWorkerAvailability(q, store, queue.AvailabilityConfig{
		AvgJobDuration: 3 * time.Minute,
	})

	v := a.CheckHard(context.Background())
	if v.Kind != queue.VerdictOK {
		t.Errorf("Kind = %q, want %q (fail-open on store error)", v.Kind, queue.VerdictOK)
	}
}

// workerListingQueue is erroringQueue but with a configurable workers list.
type workerListingQueue struct {
	erroringQueue
	workers []queue.WorkerInfo
}

func (w *workerListingQueue) ListWorkers(context.Context) ([]queue.WorkerInfo, error) {
	return w.workers, nil
}

// fakeDepthQueue is workerListingQueue with a fixed QueueDepth — used by
// availability tests that need a deterministic depth value without the
// race-prone async drain in queuetest.JobQueue.
type fakeDepthQueue struct {
	workerListingQueue
	depth int
}

func (f *fakeDepthQueue) QueueDepth() int { return f.depth }
