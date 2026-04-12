package queue

import (
	"context"
	"testing"
	"time"
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
		Prompt:      "Triage this thread",
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
		if got.Prompt != job.Prompt {
			t.Errorf("Prompt = %q, want %q", got.Prompt, job.Prompt)
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

	// QueuePosition always returns 0 in Redis mode.
	pos, err := q.QueuePosition("job-depth-001")
	if err != nil {
		t.Fatalf("QueuePosition failed: %v", err)
	}
	if pos != 0 {
		t.Errorf("QueuePosition = %d, want 0", pos)
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
