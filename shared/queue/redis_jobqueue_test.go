package queue

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func TestRedisJobQueue_SubmitAndReceive(t *testing.T) {
	client := testRedisClient(t)
	store := NewMemJobStore()
	q := NewRedisJobQueue(client, store, "triage")
	defer q.Close()

	ctx := context.Background()

	ch, err := q.Receive(ctx)
	if err != nil {
		t.Fatalf("Receive failed: %v", err)
	}

	// Give consumer group reader time to start.
	time.Sleep(100 * time.Millisecond)

	job := &Job{
		ID:          "job-redis-001",
		Priority:    2,
		TaskType:    "triage",
		ChannelID:   "C123",
		ThreadTS:    "1234567890.000001",
		UserID:      "U456",
		Repo:        "org/repo",
		Branch:      "main",
		SubmittedAt: time.Now(),
	}

	if err := q.Submit(ctx, job); err != nil {
		t.Fatalf("Submit failed: %v", err)
	}

	select {
	case got := <-ch:
		if got.ID != job.ID {
			t.Errorf("ID = %q, want %q", got.ID, job.ID)
		}
		if got.Priority != job.Priority {
			t.Errorf("Priority = %d, want %d", got.Priority, job.Priority)
		}
		if got.TaskType != job.TaskType {
			t.Errorf("TaskType = %q, want %q", got.TaskType, job.TaskType)
		}
		if got.ChannelID != job.ChannelID {
			t.Errorf("ChannelID = %q, want %q", got.ChannelID, job.ChannelID)
		}
		if got.Repo != job.Repo {
			t.Errorf("Repo = %q, want %q", got.Repo, job.Repo)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for job")
	}
}

func TestRedisJobQueue_AckAndDepth(t *testing.T) {
	client := testRedisClient(t)
	store := NewMemJobStore()
	q := NewRedisJobQueue(client, store, "triage")
	defer q.Close()

	ctx := context.Background()

	job1 := &Job{ID: "job-depth-001", Priority: 1, TaskType: "triage", SubmittedAt: time.Now()}
	job2 := &Job{ID: "job-depth-002", Priority: 1, TaskType: "triage", SubmittedAt: time.Now()}

	if err := q.Submit(ctx, job1); err != nil {
		t.Fatalf("Submit job1 failed: %v", err)
	}
	if err := q.Submit(ctx, job2); err != nil {
		t.Fatalf("Submit job2 failed: %v", err)
	}

	depth := q.QueueDepth()
	if depth != 2 {
		t.Errorf("QueueDepth = %d, want 2", depth)
	}

	// Consume one job.
	ch, err := q.Receive(ctx)
	if err != nil {
		t.Fatalf("Receive failed: %v", err)
	}

	select {
	case got := <-ch:
		if err := q.Ack(ctx, got.ID); err != nil {
			t.Fatalf("Ack failed: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for job")
	}

	// job-depth-001 is the first delivery (Streams preserve submit order); after
	// Ack moved it to JobPreparing, QueuePosition reports 0 — it is no longer
	// queued. job-depth-002 is now alone at the head, position 1.
	pos, err := q.QueuePosition("job-depth-001")
	if err != nil {
		t.Fatalf("QueuePosition(acked) failed: %v", err)
	}
	if pos != 0 {
		t.Errorf("QueuePosition(acked) = %d, want 0", pos)
	}
	pos, err = q.QueuePosition("job-depth-002")
	if err != nil {
		t.Fatalf("QueuePosition(pending) failed: %v", err)
	}
	if pos != 1 {
		t.Errorf("QueuePosition(pending) = %d, want 1", pos)
	}
}

func TestRedisJobQueue_QueuePositionTracksSubmissionOrder(t *testing.T) {
	client := testRedisClient(t)
	store := NewMemJobStore()
	q := NewRedisJobQueue(client, store, "triage")
	defer q.Close()

	ctx := context.Background()

	jobs := []*Job{
		{ID: "qp-001", TaskType: "triage", SubmittedAt: time.Now()},
		{ID: "qp-002", TaskType: "triage", SubmittedAt: time.Now()},
		{ID: "qp-003", TaskType: "triage", SubmittedAt: time.Now()},
	}
	for _, j := range jobs {
		if err := q.Submit(ctx, j); err != nil {
			t.Fatalf("Submit %s: %v", j.ID, err)
		}
	}

	cases := []struct {
		id   string
		want int
	}{
		{"qp-001", 1},
		{"qp-002", 2},
		{"qp-003", 3},
	}
	for _, c := range cases {
		pos, err := q.QueuePosition(c.id)
		if err != nil {
			t.Fatalf("QueuePosition(%s): %v", c.id, err)
		}
		if pos != c.want {
			t.Errorf("QueuePosition(%s) = %d, want %d", c.id, pos, c.want)
		}
	}
}

func TestRedisJobQueue_QueuePositionRecedesAfterAck(t *testing.T) {
	client := testRedisClient(t)
	store := NewMemJobStore()
	q := NewRedisJobQueue(client, store, "triage")
	defer q.Close()

	ctx := context.Background()

	jobs := []*Job{
		{ID: "qp-recede-001", TaskType: "triage", SubmittedAt: time.Now()},
		{ID: "qp-recede-002", TaskType: "triage", SubmittedAt: time.Now()},
		{ID: "qp-recede-003", TaskType: "triage", SubmittedAt: time.Now()},
	}
	for _, j := range jobs {
		if err := q.Submit(ctx, j); err != nil {
			t.Fatalf("Submit %s: %v", j.ID, err)
		}
	}

	if err := q.Ack(ctx, jobs[0].ID); err != nil {
		t.Fatalf("Ack: %v", err)
	}

	cases := []struct {
		id   string
		want int
	}{
		{jobs[0].ID, 0}, // acked → no longer pending
		{jobs[1].ID, 1}, // promoted to head
		{jobs[2].ID, 2},
	}
	for _, c := range cases {
		pos, err := q.QueuePosition(c.id)
		if err != nil {
			t.Fatalf("QueuePosition(%s): %v", c.id, err)
		}
		if pos != c.want {
			t.Errorf("QueuePosition(%s) = %d, want %d", c.id, pos, c.want)
		}
	}
}

func TestRedisJobQueue_QueueDepth_BeforeAnyConsumer(t *testing.T) {
	client := testRedisClient(t)
	store := NewMemJobStore()
	q := NewRedisJobQueue(client, store, "triage")
	defer q.Close()

	ctx := context.Background()

	// App pod submits before any worker connects — the consumer group does
	// not exist yet. /jobs must still report an accurate pending count,
	// otherwise operators can't tell if a submit landed.
	for i := 1; i <= 2; i++ {
		job := &Job{ID: fmt.Sprintf("pre-%d", i), Priority: 1, TaskType: "triage", SubmittedAt: time.Now()}
		if err := q.Submit(ctx, job); err != nil {
			t.Fatalf("Submit: %v", err)
		}
	}

	if depth := q.QueueDepth(); depth != 2 {
		t.Errorf("QueueDepth = %d before any consumer group, want 2", depth)
	}
}

func TestRedisJobQueue_QueueDepth_ExcludesAckedEntries(t *testing.T) {
	client := testRedisClient(t)
	store := NewMemJobStore()
	q := NewRedisJobQueue(client, store, "triage")
	defer q.Close()

	ctx := context.Background()

	for i := 1; i <= 3; i++ {
		job := &Job{ID: fmt.Sprintf("job-depth-%03d", i), Priority: 1, TaskType: "triage", SubmittedAt: time.Now()}
		if err := q.Submit(ctx, job); err != nil {
			t.Fatalf("Submit %s: %v", job.ID, err)
		}
	}

	// Simulate two workers consuming + acking one job each via the raw Redis
	// commands the pool uses internally. Bypassing q.Receive() here keeps the
	// test insensitive to the eager-pre-read of its dispatch goroutine.
	if err := client.XGroupCreateMkStream(ctx, q.stream, q.group, "0").Err(); err != nil && !isRedisError(err, "BUSYGROUP") {
		t.Fatalf("XGroupCreateMkStream: %v", err)
	}
	for i := 0; i < 2; i++ {
		res, err := client.XReadGroup(ctx, &redis.XReadGroupArgs{
			Group:    q.group,
			Consumer: fmt.Sprintf("test-%d", i),
			Streams:  []string{q.stream, ">"},
			Count:    1,
			Block:    2 * time.Second,
		}).Result()
		if err != nil {
			t.Fatalf("XReadGroup #%d: %v", i, err)
		}
		for _, s := range res {
			for _, m := range s.Messages {
				if err := client.XAck(ctx, q.stream, q.group, m.ID).Err(); err != nil {
					t.Fatalf("XAck %s: %v", m.ID, err)
				}
			}
		}
	}

	// XLEN stays at 3 (Redis Streams retain acked entries); QueueDepth must
	// report only the un-dispatched remainder — otherwise an external monitor
	// sees phantom backlog forever.
	if depth := q.QueueDepth(); depth != 1 {
		t.Errorf("QueueDepth = %d after 2/3 acked, want 1", depth)
	}
}

func TestRedisJobQueue_WorkerRegistration(t *testing.T) {
	client := testRedisClient(t)
	store := NewMemJobStore()
	q := NewRedisJobQueue(client, store, "triage")
	defer q.Close()

	ctx := context.Background()

	worker := WorkerInfo{
		WorkerID:    "worker-test-001",
		Name:        "test-worker",
		Nickname:    "Alice",
		Agents:      []string{"claude", "codex"},
		Tags:        []string{"gpu", "fast"},
		ConnectedAt: time.Now(),
	}

	if err := q.Register(ctx, worker); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	workers, err := q.ListWorkers(ctx)
	if err != nil {
		t.Fatalf("ListWorkers failed: %v", err)
	}
	if len(workers) != 1 {
		t.Fatalf("ListWorkers count = %d, want 1", len(workers))
	}
	if workers[0].WorkerID != worker.WorkerID {
		t.Errorf("WorkerID = %q, want %q", workers[0].WorkerID, worker.WorkerID)
	}
	if workers[0].Name != worker.Name {
		t.Errorf("Name = %q, want %q", workers[0].Name, worker.Name)
	}
	if workers[0].Nickname != worker.Nickname {
		t.Errorf("Nickname = %q, want %q", workers[0].Nickname, worker.Nickname)
	}
	if len(workers[0].Agents) != 2 {
		t.Errorf("Agents len = %d, want 2", len(workers[0].Agents))
	}

	// Unregister and verify empty.
	if err := q.Unregister(ctx, worker.WorkerID); err != nil {
		t.Fatalf("Unregister failed: %v", err)
	}

	workers, err = q.ListWorkers(ctx)
	if err != nil {
		t.Fatalf("ListWorkers after unregister failed: %v", err)
	}
	if len(workers) != 0 {
		t.Errorf("ListWorkers count after unregister = %d, want 0", len(workers))
	}
}
