package integration_test

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Ivantseng123/agentdock/shared/queue"
	"github.com/Ivantseng123/agentdock/worker/pool"
	"github.com/redis/go-redis/v9"
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
	ctx := context.Background()
	rdb := redisClient(t)
	store := queue.NewMemJobStore()
	bundle := queue.NewRedisBundle(rdb, store, "triage")
	defer bundle.Close()

	p := pool.NewPool(pool.Config{
		Queue:       bundle.Queue,
		Attachments: bundle.Attachments,
		Results:     bundle.Results,
		Store:       store,
		Runner:      &fakeRunner{},
		RepoCache:   &fakeRepo{},
		WorkerCount: 1,
		Logger:      slog.Default(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	p.Start(ctx)

	// Submit job.
	bundle.Queue.Submit(ctx, &queue.Job{
		ID:       "j1",
		Priority: 50,
		Repo:     "owner/repo",
		TaskType: "triage",
		PromptContext: &queue.PromptContext{
			ThreadMessages: []queue.ThreadMessage{{User: "T", Timestamp: "1", Text: "test prompt"}},
			Channel:        "test",
			Reporter:       "tester",
			Goal:           "test goal",
		},
	})

	// Wait for result.
	ch, _ := bundle.Results.Subscribe(ctx)
	select {
	case result := <-ch:
		if result.Status != "completed" {
			t.Errorf("status = %q, want completed", result.Status)
		}
		// Worker no longer parses; it forwards RawOutput for the app to decode.
		if !strings.Contains(result.RawOutput, "===TRIAGE_RESULT===") {
			t.Errorf("RawOutput missing TRIAGE_RESULT marker: %q", result.RawOutput)
		}
		if !strings.Contains(result.RawOutput, "Test issue") {
			t.Errorf("RawOutput missing expected title fragment; got %q", result.RawOutput)
		}
		// Title is no longer a JobResult field — parsing is app-side.
	case <-ctx.Done():
		t.Fatal("timeout waiting for result")
	}
}
