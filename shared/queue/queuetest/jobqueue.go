package queuetest

import (
	"container/heap"
	"context"
	"sync"
	"sync/atomic"

	"github.com/Ivantseng123/agentdock/shared/queue"
)

type JobQueue struct {
	mu         sync.Mutex
	cond       *sync.Cond
	pq         priorityQueue
	capacity   int
	seqCounter atomic.Uint64
	store      queue.JobStore
	jobCh      chan *queue.Job
	closed     chan struct{}

	workerMu sync.Mutex
	workers  map[string]queue.WorkerInfo
}

func NewJobQueue(capacity int, store queue.JobStore) *JobQueue {
	q := &JobQueue{
		capacity: capacity,
		store:    store,
		jobCh:    make(chan *queue.Job, capacity),
		closed:   make(chan struct{}),
		workers:  make(map[string]queue.WorkerInfo),
	}
	q.cond = sync.NewCond(&q.mu)
	heap.Init(&q.pq)
	go q.dispatchLoop()
	return q
}

func (q *JobQueue) dispatchLoop() {
	for {
		q.mu.Lock()
		for q.pq.Len() == 0 {
			select {
			case <-q.closed:
				q.mu.Unlock()
				return
			default:
			}
			q.cond.Wait()
		}
		entry := heap.Pop(&q.pq).(*queueEntry)
		q.mu.Unlock()

		select {
		case q.jobCh <- entry.job:
		case <-q.closed:
			return
		}
	}
}

func (q *JobQueue) Submit(ctx context.Context, job *queue.Job) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.pq.Len() >= q.capacity {
		return queue.ErrQueueFull
	}
	job.Seq = q.seqCounter.Add(1)
	heap.Push(&q.pq, &queueEntry{job: job})
	q.store.Put(job)
	q.cond.Signal()
	return nil
}

func (q *JobQueue) QueuePosition(jobID string) (int, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	pos := q.pq.position(jobID)
	return pos, nil
}

func (q *JobQueue) QueueDepth() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.pq.Len()
}

func (q *JobQueue) Receive(ctx context.Context) (<-chan *queue.Job, error) {
	return q.jobCh, nil
}

func (q *JobQueue) Ack(ctx context.Context, jobID string) error {
	q.store.UpdateStatus(jobID, queue.JobPreparing)
	return nil
}

func (q *JobQueue) Register(ctx context.Context, info queue.WorkerInfo) error {
	q.workerMu.Lock()
	defer q.workerMu.Unlock()
	q.workers[info.WorkerID] = info
	return nil
}

func (q *JobQueue) Unregister(ctx context.Context, workerID string) error {
	q.workerMu.Lock()
	defer q.workerMu.Unlock()
	delete(q.workers, workerID)
	return nil
}

func (q *JobQueue) ListWorkers(ctx context.Context) ([]queue.WorkerInfo, error) {
	q.workerMu.Lock()
	defer q.workerMu.Unlock()
	result := make([]queue.WorkerInfo, 0, len(q.workers))
	for _, w := range q.workers {
		result = append(result, w)
	}
	return result, nil
}

func (q *JobQueue) Close() error {
	select {
	case <-q.closed:
	default:
		close(q.closed)
		q.cond.Broadcast()
	}
	return nil
}
