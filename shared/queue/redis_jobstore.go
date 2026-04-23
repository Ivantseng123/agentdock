package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisJobStore implements JobStore on Redis so job state survives app restarts.
//
// Keys (all TTL'd, refreshed on every write):
//
//	<prefix>:job:<jobID>                       → JSON(JobState)  — primary record
//	<prefix>:thread:<channelID>:<threadTS>     → jobID           — secondary index
//
// The secondary index lets GetByThread resolve in O(1) instead of scanning.
// Both keys are refreshed together on Put/Update*/SetWorker/SetAgentStatus so a
// busy job does not expire mid-run.
//
// Mutations (UpdateStatus, SetWorker, SetAgentStatus) use optimistic
// concurrency via WATCH/MULTI/EXEC (see txUpdate) so concurrent writers on the
// same jobID cannot lose updates. Lua was considered but rejected: we want the
// JSON (un)marshal to live in Go where JobState is defined, and go-redis'
// Watch helper already gives us the retry loop for free.
//
// This is part 1/2 of #123 — the type is not yet wired into app/ (see #146).
type RedisJobStore struct {
	rdb    *redis.Client
	prefix string
	ttl    time.Duration
}

// NewRedisJobStore constructs a Redis-backed JobStore.
//
// prefix is the namespace root (e.g. "ad:jobstore"). Do not include a trailing
// colon — one is inserted automatically. ttl is applied to every write; a job
// that never receives another update will be evicted by Redis after ttl. Pick
// something comfortably larger than the longest expected job runtime (the
// reference default is 1h, matching MemJobStore's cleanup cadence in #123).
func NewRedisJobStore(rdb *redis.Client, prefix string, ttl time.Duration) *RedisJobStore {
	return &RedisJobStore{
		rdb:    rdb,
		prefix: strings.TrimRight(prefix, ":"),
		ttl:    ttl,
	}
}

// jobKey returns the primary key for a job.
func (s *RedisJobStore) jobKey(jobID string) string {
	return s.prefix + ":job:" + jobID
}

// threadKey returns the secondary-index key for a (channel, thread) pair.
func (s *RedisJobStore) threadKey(channelID, threadTS string) string {
	return s.prefix + ":thread:" + channelID + ":" + threadTS
}

// jobKeyPattern is the SCAN match for primary records. Thread index keys sit
// under a different sub-prefix so they are naturally excluded.
func (s *RedisJobStore) jobKeyPattern() string {
	return s.prefix + ":job:*"
}

// maxTxRetries caps the WATCH retry loop. A healthy system finishes in 1–2
// attempts; the cap just stops a pathological contention scenario from
// spinning forever. Production sees per-job contention of at most 2 writers
// (worker status report + app status update), so 128 leaves huge headroom.
const maxTxRetries = 128

// Put stores a freshly-submitted job in Pending state and refreshes TTLs on
// both the primary record and the thread secondary index.
func (s *RedisJobStore) Put(job *Job) error {
	ctx := context.Background()
	state := &JobState{Job: job, Status: JobPending}

	data, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("marshal job state: %w", err)
	}

	pk := s.jobKey(job.ID)
	tk := s.threadKey(job.ChannelID, job.ThreadTS)

	// Both writes in one pipeline so we never leave the index orphaned on a
	// partial failure (either both succeed or the caller gets an error).
	_, err = s.rdb.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		pipe.Set(ctx, pk, data, s.ttl)
		pipe.Set(ctx, tk, job.ID, s.ttl)
		return nil
	})
	if err != nil {
		return fmt.Errorf("redis put: %w", err)
	}
	return nil
}

// Get loads a job by ID. Returns an error if the key is missing (expired or
// never written).
func (s *RedisJobStore) Get(jobID string) (*JobState, error) {
	ctx := context.Background()
	data, err := s.rdb.Get(ctx, s.jobKey(jobID)).Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, fmt.Errorf("job %q not found", jobID)
		}
		return nil, fmt.Errorf("redis get: %w", err)
	}
	var state JobState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("unmarshal job state: %w", err)
	}
	return &state, nil
}

// GetByThread resolves the thread secondary index to a jobID and then loads
// that job's state. Two round-trips in the steady-state happy path; a SCAN
// fallback would be O(N) and drift under load, so the extra key is worth it.
func (s *RedisJobStore) GetByThread(channelID, threadTS string) (*JobState, error) {
	ctx := context.Background()
	jobID, err := s.rdb.Get(ctx, s.threadKey(channelID, threadTS)).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, fmt.Errorf("no job found for thread %s:%s", channelID, threadTS)
		}
		return nil, fmt.Errorf("redis get thread index: %w", err)
	}
	return s.Get(jobID)
}

// ListPending scans primary keys and returns states whose Status is Pending.
// SCAN (not KEYS) is used so the call is non-blocking against Redis even when
// the keyspace grows.
func (s *RedisJobStore) ListPending() ([]*JobState, error) {
	states, err := s.listAllStates()
	if err != nil {
		return nil, err
	}
	out := states[:0]
	for _, st := range states {
		if st.Status == JobPending {
			out = append(out, st)
		}
	}
	return out, nil
}

// ListAll scans primary keys and returns every live job state.
func (s *RedisJobStore) ListAll() ([]*JobState, error) {
	return s.listAllStates()
}

// listAllStates is the shared SCAN body for ListPending / ListAll. Keys that
// expired between SCAN and GET are skipped silently — that is the expected
// race window for a TTL'd store.
func (s *RedisJobStore) listAllStates() ([]*JobState, error) {
	ctx := context.Background()
	// scanBatch: SCAN page size; mgetBatch: MGET chunk size. Chunking keeps
	// MGET payload bounded on deep rehydrate (thousands of jobs). Using SCAN
	// + MGET avoids the N+1 round-trip of per-key GETs.
	const scanBatch = 100
	const mgetBatch = 100
	var result []*JobState
	iter := s.rdb.Scan(ctx, 0, s.jobKeyPattern(), scanBatch).Iterator()
	keys := make([]string, 0, mgetBatch)
	flush := func() error {
		if len(keys) == 0 {
			return nil
		}
		values, err := s.rdb.MGet(ctx, keys...).Result()
		if err != nil {
			return fmt.Errorf("redis mget during scan: %w", err)
		}
		for i, v := range values {
			if v == nil {
				// Expired between SCAN and MGET; skip silently.
				continue
			}
			raw, ok := v.(string)
			if !ok {
				return fmt.Errorf("redis mget at %s: unexpected type %T", keys[i], v)
			}
			var state JobState
			if err := json.Unmarshal([]byte(raw), &state); err != nil {
				return fmt.Errorf("unmarshal job state at %s: %w", keys[i], err)
			}
			result = append(result, &state)
		}
		keys = keys[:0]
		return nil
	}
	for iter.Next(ctx) {
		keys = append(keys, iter.Val())
		if len(keys) >= mgetBatch {
			if err := flush(); err != nil {
				return nil, err
			}
		}
	}
	if err := iter.Err(); err != nil {
		return nil, fmt.Errorf("scan jobs: %w", err)
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return result, nil
}

// UpdateStatus mutates a job's status with the same side-effects MemJobStore
// applies (StartedAt/WaitTime on first Running, CancelledAt on first
// Cancelled).
func (s *RedisJobStore) UpdateStatus(jobID string, status JobStatus) error {
	err := s.txUpdate(jobID, func(state *JobState) error {
		state.Status = status
		if status == JobRunning && state.StartedAt.IsZero() {
			state.StartedAt = time.Now()
			state.WaitTime = state.StartedAt.Sub(state.Job.SubmittedAt)
		}
		if status == JobCancelled && state.CancelledAt.IsZero() {
			state.CancelledAt = time.Now()
		}
		return nil
	})
	if errors.Is(err, errJobMissing) {
		// Match MemJobStore's error shape so callers can diff/grep log output
		// without caring which backend is wired.
		return fmt.Errorf("job %q not found", jobID)
	}
	return err
}

// SetWorker tags a job with the worker ID that claimed it.
func (s *RedisJobStore) SetWorker(jobID, workerID string) error {
	err := s.txUpdate(jobID, func(state *JobState) error {
		state.WorkerID = workerID
		return nil
	})
	if errors.Is(err, errJobMissing) {
		return fmt.Errorf("job %q not found", jobID)
	}
	return err
}

// SetAgentStatus stores the most recent StatusReport from the worker. Matches
// MemJobStore semantics: missing job is a silent no-op (the job may have been
// deleted while the report was in flight).
func (s *RedisJobStore) SetAgentStatus(jobID string, report StatusReport) error {
	err := s.txUpdate(jobID, func(state *JobState) error {
		r := report
		state.AgentStatus = &r
		return nil
	})
	if err != nil && errors.Is(err, errJobMissing) {
		return nil
	}
	return err
}

// Delete removes both the primary record and its thread secondary index.
// Missing keys are not an error — Delete is idempotent.
func (s *RedisJobStore) Delete(jobID string) error {
	ctx := context.Background()
	pk := s.jobKey(jobID)

	// Read first so we know which thread index to clear. Missing primary is
	// still a successful delete.
	data, err := s.rdb.Get(ctx, pk).Bytes()
	if err != nil && !errors.Is(err, redis.Nil) {
		return fmt.Errorf("redis get for delete: %w", err)
	}

	var tk string
	if err == nil {
		var state JobState
		if uErr := json.Unmarshal(data, &state); uErr == nil && state.Job != nil {
			tk = s.threadKey(state.Job.ChannelID, state.Job.ThreadTS)
		}
	}

	_, err = s.rdb.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		pipe.Del(ctx, pk)
		if tk != "" {
			pipe.Del(ctx, tk)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("redis delete: %w", err)
	}
	return nil
}

// errJobMissing is returned by txUpdate when the primary key is absent. It is
// deliberately a sentinel so SetAgentStatus can match MemJobStore's silent
// no-op behaviour without string-matching.
var errJobMissing = errors.New("job not found")

// txUpdate runs a read-modify-write against the primary job record under
// WATCH/MULTI/EXEC. On key modification by another client between WATCH and
// EXEC, go-redis returns redis.TxFailedErr and we retry the whole body.
//
// Why WATCH over Lua: JobState lives in Go (json tags, time.Time, pointers),
// so parsing it in Lua would mean duplicating layout knowledge in a second
// language. WATCH lets us keep the entire state model in Go and only pay the
// cost on contention (retry), which is rare for per-job writes.
func (s *RedisJobStore) txUpdate(jobID string, mutate func(*JobState) error) error {
	ctx := context.Background()
	pk := s.jobKey(jobID)

	for attempt := 0; attempt < maxTxRetries; attempt++ {
		err := s.rdb.Watch(ctx, func(tx *redis.Tx) error {
			data, err := tx.Get(ctx, pk).Bytes()
			if err != nil {
				if errors.Is(err, redis.Nil) {
					return errJobMissing
				}
				return err
			}

			var state JobState
			if err := json.Unmarshal(data, &state); err != nil {
				return fmt.Errorf("unmarshal job state: %w", err)
			}

			if err := mutate(&state); err != nil {
				return err
			}

			out, err := json.Marshal(&state)
			if err != nil {
				return fmt.Errorf("marshal job state: %w", err)
			}

			// Refresh thread secondary index TTL alongside primary so neither
			// key goes stale while the job is actively updating.
			var tk string
			if state.Job != nil {
				tk = s.threadKey(state.Job.ChannelID, state.Job.ThreadTS)
			}

			_, err = tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
				pipe.Set(ctx, pk, out, s.ttl)
				if tk != "" {
					pipe.Set(ctx, tk, state.Job.ID, s.ttl)
				}
				return nil
			})
			return err
		}, pk)

		if err == nil {
			return nil
		}
		if errors.Is(err, redis.TxFailedErr) {
			// Optimistic conflict — another writer got there first. Back off a
			// tiny randomised amount so contending writers desynchronise
			// instead of all re-entering WATCH at the same millisecond.
			jitter := time.Duration(rand.Intn(500)) * time.Microsecond
			time.Sleep(jitter)
			continue
		}
		return err
	}
	return fmt.Errorf("redis tx update job %q: exceeded %d retries", jobID, maxTxRetries)
}
