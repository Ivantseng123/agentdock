package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisJobQueue implements JobQueue using Redis Streams with consumer groups
// for reliable job dispatch and worker registration via Redis keys.
type RedisJobQueue struct {
	rdb        *redis.Client
	store      JobStore
	taskType   string
	stream     string
	group      string
	consumerID string
	stopCh     chan struct{}
	seqCounter atomic.Uint64
}

// NewRedisJobQueue creates a new Redis-backed job queue for the given task type.
func NewRedisJobQueue(rdb *redis.Client, store JobStore, taskType string) *RedisJobQueue {
	return &RedisJobQueue{
		rdb:        rdb,
		store:      store,
		taskType:   taskType,
		stream:     keyPrefix + "jobs:" + taskType,
		group:      "workers",
		consumerID: fmt.Sprintf("worker-%d", time.Now().UnixNano()),
		stopCh:     make(chan struct{}),
	}
}

// Submit adds a job to the Redis stream and stores it in the job store.
//
// The Seq field is assigned from a process-local monotonic counter so that
// QueuePosition can compute submission order without an extra Redis round-trip.
// Only this app instance's QueuePosition consults the value — across-instance
// ordering is irrelevant because each app's MemJobStore only sees its own
// submissions.
func (q *RedisJobQueue) Submit(ctx context.Context, job *Job) error {
	job.Seq = q.seqCounter.Add(1)

	data, err := json.Marshal(job)
	if err != nil {
		return fmt.Errorf("marshal job: %w", err)
	}

	if err := q.store.Put(job); err != nil {
		return fmt.Errorf("store put: %w", err)
	}

	return q.rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: q.stream,
		Values: map[string]interface{}{"payload": string(data)},
	}).Err()
}

// QueuePosition returns the 1-based position of a still-pending job in this
// app instance's submission queue. Returns 0 once the job has been picked up
// by a worker (status != JobPending) — it is no longer "queued".
//
// Counting is local to this app instance's JobStore; in a multi-app deployment
// each instance reports its own backlog. That is the same fidelity the
// in-memory queue gives, and matches how callers use the value (UX hint, not
// distributed consensus).
func (q *RedisJobQueue) QueuePosition(jobID string) (int, error) {
	state, err := q.store.Get(jobID)
	if err != nil {
		return 0, err
	}
	if state.Status != JobPending {
		return 0, nil
	}
	pending, err := q.store.ListPending()
	if err != nil {
		return 0, err
	}
	pos := 0
	for _, p := range pending {
		if p.Job.Seq <= state.Job.Seq {
			pos++
		}
	}
	return pos, nil
}

// QueueDepth returns the number of jobs still awaiting dispatch to any worker
// — Redis' `lag` for the consumer group.
//
// Raw XLEN is intentionally avoided: Redis Streams keep entries after XACK,
// so XLEN grows monotonically and drifts further from reality over time; an
// external monitor polling /jobs would otherwise see phantom backlog forever.
//
// Prefers `lag` (Redis 7.0+). Falls back to `XLEN - entries-read` when lag is
// unavailable (-1). When the consumer group has not been created yet (fresh
// stream, no Receive call), XLEN is accurate — nothing has been consumed.
func (q *RedisJobQueue) QueueDepth() int {
	ctx := context.Background()
	groups, err := q.rdb.XInfoGroups(ctx, q.stream).Result()
	if err != nil {
		if n, xErr := q.rdb.XLen(ctx, q.stream).Result(); xErr == nil {
			return int(n)
		}
		return 0
	}
	for _, g := range groups {
		if g.Name != q.group {
			continue
		}
		if g.Lag >= 0 {
			return int(g.Lag)
		}
		total, err := q.rdb.XLen(ctx, q.stream).Result()
		if err != nil {
			return 0
		}
		depth := total - g.EntriesRead
		if depth < 0 {
			depth = 0
		}
		return int(depth)
	}
	// Group not yet created — nothing has been consumed, XLEN is accurate.
	n, err := q.rdb.XLen(ctx, q.stream).Result()
	if err != nil {
		return 0
	}
	return int(n)
}

// Receive creates a consumer group and returns a channel that delivers jobs.
func (q *RedisJobQueue) Receive(ctx context.Context) (<-chan *Job, error) {
	// Create consumer group; ignore BUSYGROUP error if it already exists.
	err := q.rdb.XGroupCreateMkStream(ctx, q.stream, q.group, "0").Err()
	if err != nil && !isRedisError(err, "BUSYGROUP") {
		return nil, fmt.Errorf("create consumer group: %w", err)
	}

	ch := make(chan *Job, 64)
	go func() {
		defer close(ch)
		for {
			select {
			case <-q.stopCh:
				return
			default:
			}

			streams, err := q.rdb.XReadGroup(ctx, &redis.XReadGroupArgs{
				Group:    q.group,
				Consumer: q.consumerID,
				Streams:  []string{q.stream, ">"},
				Count:    1,
				Block:    5 * time.Second,
			}).Result()
			if err != nil {
				if err == redis.Nil {
					continue
				}
				select {
				case <-q.stopCh:
					return
				case <-ctx.Done():
					return
				default:
					continue
				}
			}

			for _, stream := range streams {
				for _, msg := range stream.Messages {
					payload, ok := msg.Values["payload"].(string)
					if !ok {
						q.rdb.XAck(ctx, q.stream, q.group, msg.ID)
						continue
					}

					var job Job
					if err := json.Unmarshal([]byte(payload), &job); err != nil {
						q.rdb.XAck(ctx, q.stream, q.group, msg.ID)
						continue
					}

					// Store in local JobStore so worker pool can find it.
					q.store.Put(&job)

					select {
					case ch <- &job:
					case <-q.stopCh:
						return
					case <-ctx.Done():
						return
					}
				}
			}
		}
	}()

	return ch, nil
}

// Ack acknowledges a job by updating its status and acking all pending
// messages from this consumer.
func (q *RedisJobQueue) Ack(ctx context.Context, jobID string) error {
	if err := q.store.UpdateStatus(jobID, JobPreparing); err != nil {
		return fmt.Errorf("update status: %w", err)
	}

	// Ack all pending messages from this consumer.
	pending, err := q.rdb.XPendingExt(ctx, &redis.XPendingExtArgs{
		Stream:   q.stream,
		Group:    q.group,
		Start:    "-",
		End:      "+",
		Count:    100,
		Consumer: q.consumerID,
	}).Result()
	if err != nil {
		return nil // non-fatal: message was logically processed
	}

	for _, p := range pending {
		q.rdb.XAck(ctx, q.stream, q.group, p.ID)
	}

	return nil
}

// Register stores worker info in Redis with a 30-second TTL.
func (q *RedisJobQueue) Register(ctx context.Context, info WorkerInfo) error {
	data, err := json.Marshal(info)
	if err != nil {
		return fmt.Errorf("marshal worker info: %w", err)
	}

	key := keyPrefix + "workers:" + info.WorkerID
	return q.rdb.Set(ctx, key, string(data), 30*time.Second).Err()
}

// Unregister removes a worker's registration from Redis.
func (q *RedisJobQueue) Unregister(ctx context.Context, workerID string) error {
	key := keyPrefix + "workers:" + workerID
	return q.rdb.Del(ctx, key).Err()
}

// ListWorkers scans for all registered workers and returns their info.
func (q *RedisJobQueue) ListWorkers(ctx context.Context) ([]WorkerInfo, error) {
	pattern := keyPrefix + "workers:*"
	var workers []WorkerInfo

	iter := q.rdb.Scan(ctx, 0, pattern, 100).Iterator()
	for iter.Next(ctx) {
		val, err := q.rdb.Get(ctx, iter.Val()).Result()
		if err != nil {
			continue // key may have expired
		}

		var info WorkerInfo
		if err := json.Unmarshal([]byte(val), &info); err != nil {
			continue
		}
		workers = append(workers, info)
	}
	if err := iter.Err(); err != nil {
		return nil, fmt.Errorf("scan workers: %w", err)
	}

	return workers, nil
}

// Close signals the background goroutine to stop.
func (q *RedisJobQueue) Close() error {
	close(q.stopCh)
	return nil
}
