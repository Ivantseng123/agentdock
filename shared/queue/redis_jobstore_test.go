package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// makeJob builds a Job with just enough fields populated for JobStore tests.
func makeJob(id, channelID, threadTS string) *Job {
	return &Job{
		ID:          id,
		ChannelID:   channelID,
		ThreadTS:    threadTS,
		SubmittedAt: time.Now(),
		TaskType:    "ask",
	}
}

func newTestStore(t *testing.T, ttl time.Duration) *RedisJobStore {
	t.Helper()
	client := testRedisClient(t)
	return NewRedisJobStore(client, "ad:jobstore:test", ttl)
}

// TestRedisJobStore_Put_RespectsCancelledContext verifies #194: every method
// on RedisJobStore must honour the caller's context so a degraded Redis
// backend cannot stall the app indefinitely. A cancelled ctx should surface
// quickly instead of blocking on a network round-trip.
func TestRedisJobStore_Put_RespectsCancelledContext(t *testing.T) {
	store := newTestStore(t, time.Minute)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled before the call

	err := store.Put(ctx, makeJob("j-cancelled", "C1", "100.001"))
	if err == nil {
		t.Fatal("Put with cancelled ctx should have errored, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Put error = %v, want errors.Is(err, context.Canceled)", err)
	}
}

// TestRedisJobStore_AllMethods_RespectCancelledContext extends the #194
// contract to every JobStore method, not just Put. The four txUpdate-backed
// methods (UpdateStatus / SetWorker / SetAgentStatus, plus the read-only
// GetByThread / ListPending / ListAll / Get / Delete paths) each rely on
// honouring caller ctx for the latent-hang fix to hold. A future regression
// that re-introduces context.Background() inside any one of them would slip
// past a Put-only test.
func TestRedisJobStore_AllMethods_RespectCancelledContext(t *testing.T) {
	store := newTestStore(t, time.Minute)

	// Seed one job under a healthy ctx so read paths have a real key to
	// resolve; without this, "missing job" could shadow the ctx-cancelled
	// assertion on Get / GetByThread / UpdateStatus.
	seedCtx := context.Background()
	if err := store.Put(seedCtx, makeJob("j-seed", "C1", "100.001")); err != nil {
		t.Fatalf("seed Put: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	cases := []struct {
		name string
		op   func() error
	}{
		{"Put", func() error { return store.Put(ctx, makeJob("j-x", "C2", "200.001")) }},
		{"Get", func() error { _, err := store.Get(ctx, "j-seed"); return err }},
		{"GetByThread", func() error { _, err := store.GetByThread(ctx, "C1", "100.001"); return err }},
		{"ListPending", func() error { _, err := store.ListPending(ctx); return err }},
		{"ListAll", func() error { _, err := store.ListAll(ctx); return err }},
		{"UpdateStatus", func() error { return store.UpdateStatus(ctx, "j-seed", JobRunning) }},
		{"SetWorker", func() error { return store.SetWorker(ctx, "j-seed", "worker-a") }},
		{"SetAgentStatus", func() error { return store.SetAgentStatus(ctx, "j-seed", StatusReport{JobID: "j-seed"}) }},
		{"Delete", func() error { return store.Delete(ctx, "j-seed") }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.op()
			if err == nil {
				t.Fatalf("%s with cancelled ctx should have errored, got nil", tc.name)
			}
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("%s error = %v, want errors.Is(err, context.Canceled)", tc.name, err)
			}
		})
	}
}

func TestRedisJobStore_PutAndGet(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, time.Minute)

	job := makeJob("j1", "C1", "100.001")
	if err := store.Put(ctx, job); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, err := store.Get(ctx, "j1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Job.ID != "j1" {
		t.Errorf("Job.ID = %q, want j1", got.Job.ID)
	}
	if got.Status != JobPending {
		t.Errorf("Status = %q, want %q", got.Status, JobPending)
	}
}

func TestRedisJobStore_Get_Missing(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, time.Minute)
	if _, err := store.Get(ctx, "nope"); err == nil {
		t.Fatal("expected error for missing jobID")
	}
}

func TestRedisJobStore_GetByThread(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, time.Minute)

	job := makeJob("j1", "C1", "100.001")
	if err := store.Put(ctx, job); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, err := store.GetByThread(ctx, "C1", "100.001")
	if err != nil {
		t.Fatalf("GetByThread: %v", err)
	}
	if got.Job.ID != "j1" {
		t.Errorf("Job.ID = %q, want j1", got.Job.ID)
	}

	if _, err := store.GetByThread(ctx, "C1", "missing"); err == nil {
		t.Fatal("expected error for unknown thread")
	}
}

func TestRedisJobStore_ListPending(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, time.Minute)

	for i := 0; i < 3; i++ {
		job := makeJob(fmt.Sprintf("j%d", i), "C1", fmt.Sprintf("ts%d", i))
		if err := store.Put(ctx, job); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	// Advance one out of Pending.
	if err := store.UpdateStatus(ctx, "j1", JobRunning); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}

	pending, err := store.ListPending(ctx)
	if err != nil {
		t.Fatalf("ListPending: %v", err)
	}
	if len(pending) != 2 {
		t.Fatalf("len(pending) = %d, want 2", len(pending))
	}
	for _, st := range pending {
		if st.Status != JobPending {
			t.Errorf("Status = %q, want pending", st.Status)
		}
	}
}

func TestRedisJobStore_ListAll(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, time.Minute)

	for i := 0; i < 3; i++ {
		job := makeJob(fmt.Sprintf("j%d", i), "C1", fmt.Sprintf("ts%d", i))
		if err := store.Put(ctx, job); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	all, err := store.ListAll(ctx)
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("len(all) = %d, want 3", len(all))
	}
}

func TestRedisJobStore_UpdateStatus_RunningSideEffects(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, time.Minute)

	job := makeJob("j1", "C1", "100.001")
	if err := store.Put(ctx, job); err != nil {
		t.Fatalf("Put: %v", err)
	}

	if err := store.UpdateStatus(ctx, "j1", JobRunning); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}
	got, err := store.Get(ctx, "j1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != JobRunning {
		t.Errorf("Status = %q, want running", got.Status)
	}
	if got.StartedAt.IsZero() {
		t.Error("StartedAt should be set after running")
	}
	if got.WaitTime <= 0 {
		t.Errorf("WaitTime = %v, want > 0", got.WaitTime)
	}

	if err := store.UpdateStatus(ctx, "j1", JobCancelled); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}
	got, _ = store.Get(ctx, "j1")
	if got.CancelledAt.IsZero() {
		t.Error("CancelledAt should be set after cancellation")
	}
}

func TestRedisJobStore_UpdateStatus_Missing(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, time.Minute)
	if err := store.UpdateStatus(ctx, "nope", JobRunning); err == nil {
		t.Fatal("expected error updating missing job")
	}
}

func TestRedisJobStore_SetWorker(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, time.Minute)

	job := makeJob("j1", "C1", "100.001")
	if err := store.Put(ctx, job); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := store.SetWorker(ctx, "j1", "worker-a"); err != nil {
		t.Fatalf("SetWorker: %v", err)
	}
	got, _ := store.Get(ctx, "j1")
	if got.WorkerID != "worker-a" {
		t.Errorf("WorkerID = %q, want worker-a", got.WorkerID)
	}
}

func TestRedisJobStore_SetAgentStatus(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, time.Minute)

	job := makeJob("j1", "C1", "100.001")
	if err := store.Put(ctx, job); err != nil {
		t.Fatalf("Put: %v", err)
	}

	report := StatusReport{
		JobID:       "j1",
		WorkerID:    "worker-a",
		PID:         12345,
		AgentCmd:    "claude",
		Alive:       true,
		LastEvent:   "tool_call",
		LastEventAt: time.Now(),
		ToolCalls:   5,
	}
	if err := store.SetAgentStatus(ctx, "j1", report); err != nil {
		t.Fatalf("SetAgentStatus: %v", err)
	}

	got, _ := store.Get(ctx, "j1")
	if got.AgentStatus == nil {
		t.Fatal("AgentStatus is nil, want populated")
	}
	if got.AgentStatus.PID != 12345 {
		t.Errorf("PID = %d, want 12345", got.AgentStatus.PID)
	}
	if got.AgentStatus.ToolCalls != 5 {
		t.Errorf("ToolCalls = %d, want 5", got.AgentStatus.ToolCalls)
	}

	// Silent no-op when job is missing (matches MemJobStore semantics).
	if err := store.SetAgentStatus(ctx, "nope", report); err != nil {
		t.Errorf("SetAgentStatus on missing job should no-op, got %v", err)
	}
}

func TestRedisJobStore_Delete(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, time.Minute)

	job := makeJob("j1", "C1", "100.001")
	if err := store.Put(ctx, job); err != nil {
		t.Fatalf("Put: %v", err)
	}

	if err := store.Delete(ctx, "j1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := store.Get(ctx, "j1"); err == nil {
		t.Fatal("expected Get to fail after Delete")
	}
	if _, err := store.GetByThread(ctx, "C1", "100.001"); err == nil {
		t.Fatal("expected GetByThread to fail after Delete (secondary index must be cleared)")
	}

	// Idempotent: second delete is not an error.
	if err := store.Delete(ctx, "j1"); err != nil {
		t.Errorf("Delete on missing job should be no-op, got %v", err)
	}
}

func TestRedisJobStore_TTLExpiry(t *testing.T) {
	ctx := context.Background()
	// Short TTL + short wait. Using 1s so the test stays quick.
	store := newTestStore(t, 1*time.Second)

	job := makeJob("j1", "C1", "100.001")
	if err := store.Put(ctx, job); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Confirm it's there.
	if _, err := store.Get(ctx, "j1"); err != nil {
		t.Fatalf("Get right after Put: %v", err)
	}

	// Wait for expiry.
	time.Sleep(1500 * time.Millisecond)

	if _, err := store.Get(ctx, "j1"); err == nil {
		t.Fatal("expected Get to fail after TTL expiry")
	}
	if _, err := store.GetByThread(ctx, "C1", "100.001"); err == nil {
		t.Fatal("expected GetByThread to fail after TTL expiry")
	}
}

func TestRedisJobStore_TTLRefreshedOnUpdate(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, 2*time.Second)

	job := makeJob("j1", "C1", "100.001")
	if err := store.Put(ctx, job); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Sleep part-way, then refresh via update.
	time.Sleep(1 * time.Second)
	if err := store.UpdateStatus(ctx, "j1", JobRunning); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}

	// Original TTL would have expired by now (1.5s total since Put, original
	// TTL 2s) — but refresh should have bumped TTL back to 2s, so still alive.
	time.Sleep(1500 * time.Millisecond)
	if _, err := store.Get(ctx, "j1"); err != nil {
		t.Fatalf("Get after TTL refresh: %v — TTL was not refreshed on UpdateStatus", err)
	}
}

// TestRedisJobStore_ConcurrentUpdates_NoLostUpdate exercises the WATCH/MULTI/EXEC
// path: many goroutines alternate UpdateStatus and SetAgentStatus on the same
// job; all writes must land. If WATCH were missing, a SetAgentStatus
// interleaved with UpdateStatus could read a stale JobState and clobber the
// status change.
func TestRedisJobStore_ConcurrentUpdates_NoLostUpdate(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, time.Minute)

	job := makeJob("j1", "C1", "100.001")
	if err := store.Put(ctx, job); err != nil {
		t.Fatalf("Put: %v", err)
	}

	const writers = 4
	const iters = 20

	var wg sync.WaitGroup
	wg.Add(writers * 2)

	errs := make(chan error, writers*2*iters)

	for w := 0; w < writers; w++ {
		w := w
		go func() {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				if err := store.UpdateStatus(ctx, "j1", JobRunning); err != nil {
					errs <- fmt.Errorf("UpdateStatus(w=%d,i=%d): %w", w, i, err)
					return
				}
			}
		}()
		go func() {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				rep := StatusReport{
					JobID:       "j1",
					WorkerID:    fmt.Sprintf("w-%d", w),
					PID:         w*1000 + i,
					LastEventAt: time.Now(),
					ToolCalls:   i,
				}
				if err := store.SetAgentStatus(ctx, "j1", rep); err != nil {
					errs <- fmt.Errorf("SetAgentStatus(w=%d,i=%d): %w", w, i, err)
					return
				}
			}
		}()
	}

	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent write failed: %v", err)
	}

	got, err := store.Get(ctx, "j1")
	if err != nil {
		t.Fatalf("Get after contention: %v", err)
	}
	// After the storm, the job must still have a status transition AND an
	// agent report — neither side got wiped by the other.
	if got.Status != JobRunning {
		t.Errorf("Status = %q, want running (UpdateStatus clobbered)", got.Status)
	}
	if got.AgentStatus == nil {
		t.Fatal("AgentStatus is nil — SetAgentStatus was clobbered by UpdateStatus")
	}
	if got.StartedAt.IsZero() {
		t.Error("StartedAt is zero — StartedAt side-effect was clobbered")
	}
}

// TestRedisJobStore_JSONRoundTrip verifies that the JobState shape (pointer
// AgentStatus, zero time.Time fields, nested Job struct) survives a Redis
// round-trip — this is what lets a restarted app read back state written by a
// previous process.
func TestRedisJobStore_JSONRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, time.Minute)

	job := &Job{
		ID:           "j1",
		ChannelID:    "C1",
		ThreadTS:     "100.001",
		Priority:     5,
		Seq:          42,
		UserID:       "U1",
		Repo:         "org/repo",
		Branch:       "main",
		CloneURL:     "https://github.com/org/repo.git",
		TaskType:     "issue",
		SubmittedAt:  time.Now().UTC().Truncate(time.Millisecond),
		RetryCount:   1,
		RetryOfJobID: "j0",
	}

	// --- Primary put: AgentStatus nil, StartedAt/CancelledAt zero. ---
	if err := store.Put(ctx, job); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := store.Get(ctx, "j1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.AgentStatus != nil {
		t.Errorf("AgentStatus = %+v, want nil on fresh Put", got.AgentStatus)
	}
	if !got.StartedAt.IsZero() {
		t.Errorf("StartedAt = %v, want zero", got.StartedAt)
	}
	if !got.CancelledAt.IsZero() {
		t.Errorf("CancelledAt = %v, want zero", got.CancelledAt)
	}
	if got.Job.SubmittedAt.UnixNano() != job.SubmittedAt.UnixNano() {
		t.Errorf("SubmittedAt round-trip: got %v, want %v", got.Job.SubmittedAt, job.SubmittedAt)
	}
	if got.Job.RetryOfJobID != "j0" {
		t.Errorf("RetryOfJobID lost in round-trip: %q", got.Job.RetryOfJobID)
	}

	// --- Now populate the pointer AgentStatus and re-read. ---
	rep := StatusReport{
		JobID:        "j1",
		WorkerID:     "worker-a",
		PID:          999,
		AgentCmd:     "claude",
		Alive:        true,
		LastEventAt:  time.Now().UTC().Truncate(time.Millisecond),
		ToolCalls:    3,
		OutputBytes:  1024,
		CostUSD:      0.02,
		InputTokens:  100,
		OutputTokens: 200,
		JobStatus:    JobRunning,
	}
	if err := store.SetAgentStatus(ctx, "j1", rep); err != nil {
		t.Fatalf("SetAgentStatus: %v", err)
	}
	got, err = store.Get(ctx, "j1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.AgentStatus == nil {
		t.Fatal("AgentStatus is nil after SetAgentStatus")
	}
	if got.AgentStatus.PID != 999 {
		t.Errorf("PID = %d, want 999", got.AgentStatus.PID)
	}
	if got.AgentStatus.CostUSD != 0.02 {
		t.Errorf("CostUSD = %f, want 0.02", got.AgentStatus.CostUSD)
	}
	if got.AgentStatus.JobStatus != JobRunning {
		t.Errorf("JobStatus = %q, want running", got.AgentStatus.JobStatus)
	}

	// Defensive: re-marshal back to JSON to confirm nothing unexpected leaked in.
	if _, err := json.Marshal(got); err != nil {
		t.Errorf("re-marshal round-tripped state: %v", err)
	}
}

// TestRedisJobStore_SCANNotKEYS is a smoke test that sanity-checks ListAll
// works with many keys in the namespace — the real enforcement (SCAN vs KEYS)
// is a review-time concern since both return the same data.
func TestRedisJobStore_SCANNotKEYS(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, time.Minute)

	for i := 0; i < 50; i++ {
		job := makeJob(fmt.Sprintf("job-%d", i), "C1", fmt.Sprintf("ts-%d", i))
		if err := store.Put(ctx, job); err != nil {
			t.Fatalf("Put[%d]: %v", i, err)
		}
	}
	all, err := store.ListAll(ctx)
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(all) != 50 {
		t.Errorf("len(all) = %d, want 50", len(all))
	}
}

// compile-time interface check — fails the test build if RedisJobStore ever
// falls out of conformance with JobStore.
var _ JobStore = (*RedisJobStore)(nil)
