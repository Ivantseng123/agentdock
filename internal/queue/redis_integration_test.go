package queue_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"agentdock/internal/queue"
	"agentdock/internal/worker"
)

func redisClient(t *testing.T) *redis.Client {
	t.Helper()
	addr := os.Getenv("TEST_REDIS_ADDR")
	if addr == "" {
		addr = "localhost:6379"
	}
	client := redis.NewClient(&redis.Options{Addr: addr, DB: 15})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		t.Skipf("skipping Redis integration test: %v", err)
	}
	client.FlushDB(ctx)
	t.Cleanup(func() {
		client.FlushDB(context.Background())
		client.Close()
	})
	return client
}

func TestRedisFullFlow_SubmitToResult(t *testing.T) {
	rdb := redisClient(t)
	store := queue.NewMemJobStore()
	bundle := queue.NewRedisBundle(rdb, store, "triage")
	defer bundle.Close()

	pool := worker.NewPool(worker.Config{
		Queue:       bundle.Queue,
		Attachments: bundle.Attachments,
		Results:     bundle.Results,
		Store:       store,
		Runner:      &fakeRunner{},
		RepoCache:   &fakeRepo{},
		WorkerCount: 1,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool.Start(ctx)

	// Submit job.
	bundle.Queue.Submit(ctx, &queue.Job{
		ID:       "j1",
		Priority: 50,
		Repo:     "owner/repo",
		Prompt:   "test prompt",
		TaskType: "triage",
	})

	// Wait for result.
	ch, _ := bundle.Results.Subscribe(ctx)
	select {
	case result := <-ch:
		if result.Status != "completed" {
			t.Errorf("status = %q, want completed", result.Status)
		}
		if result.Title != "Test issue" {
			t.Errorf("title = %q, want 'Test issue'", result.Title)
		}
	case <-ctx.Done():
		t.Fatal("timeout waiting for result")
	}
}
