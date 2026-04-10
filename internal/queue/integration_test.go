package queue_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"slack-issue-bot/internal/queue"
	"slack-issue-bot/internal/worker"
)

type fakeRunner struct{}

func (f *fakeRunner) Run(ctx context.Context, workDir, prompt string) (string, error) {
	result := map[string]any{
		"status":         "CREATED",
		"title":          "Test issue",
		"body":           "## Problem\nTest",
		"labels":         []string{"bug"},
		"confidence":     "high",
		"files_found":    3,
		"open_questions": 0,
	}
	b, _ := json.Marshal(result)
	return fmt.Sprintf("Analysis done.\n\n===TRIAGE_RESULT===\n%s", string(b)), nil
}

type fakeRepo struct{}

func (f *fakeRepo) Prepare(cloneURL, branch string) (string, error) {
	return "/tmp/fake-repo", nil
}

func TestFullFlow_SubmitToResult(t *testing.T) {
	store := queue.NewMemJobStore()
	transport := queue.NewInMemTransport(10, store)
	defer transport.Close()

	pool := worker.NewPool(worker.Config{
		Queue:       transport,
		Attachments: transport,
		Results:     transport,
		Store:       store,
		Runner:      &fakeRunner{},
		RepoCache:   &fakeRepo{},
		WorkerCount: 1,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pool.Start(ctx)

	// Pre-signal attachments ready.
	transport.Prepare(ctx, "j1", nil)

	// Submit job.
	err := transport.Submit(ctx, &queue.Job{
		ID:       "j1",
		Priority: 50,
		Repo:     "owner/repo",
		Prompt:   "test prompt",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Wait for result.
	ch, _ := transport.Subscribe(ctx)
	select {
	case result := <-ch:
		if result.Status != "completed" {
			t.Errorf("status = %q, want completed", result.Status)
		}
		if result.Title != "Test issue" {
			t.Errorf("title = %q", result.Title)
		}
		if result.Confidence != "high" {
			t.Errorf("confidence = %q", result.Confidence)
		}
	case <-ctx.Done():
		t.Fatal("timeout waiting for result")
	}
}

func TestFullFlow_PriorityOrdering(t *testing.T) {
	store := queue.NewMemJobStore()
	transport := queue.NewInMemTransport(10, store)
	defer transport.Close()

	ctx := context.Background()
	transport.Prepare(ctx, "low", nil)
	transport.Prepare(ctx, "high", nil)
	transport.Prepare(ctx, "mid", nil)

	transport.Submit(ctx, &queue.Job{ID: "low", Priority: 10, Prompt: "low"})
	transport.Submit(ctx, &queue.Job{ID: "high", Priority: 100, Prompt: "high"})
	transport.Submit(ctx, &queue.Job{ID: "mid", Priority: 50, Prompt: "mid"})

	var mu sync.Mutex
	var order []string
	runner := &orderRunner{mu: &mu, order: &order}

	pool := worker.NewPool(worker.Config{
		Queue:       transport,
		Attachments: transport,
		Results:     transport,
		Store:       store,
		Runner:      runner,
		RepoCache:   &fakeRepo{},
		WorkerCount: 1,
	})

	ctx2, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool.Start(ctx2)

	// Collect 3 results.
	ch, _ := transport.Subscribe(ctx2)
	for i := 0; i < 3; i++ {
		select {
		case <-ch:
		case <-ctx2.Done():
			t.Fatalf("timeout after %d results", i)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if len(order) != 3 {
		t.Fatalf("expected 3 executions, got %d", len(order))
	}
	// First should be "high" (priority 100)
	if order[0] != "high" {
		t.Errorf("first execution = %q, want high", order[0])
	}
}

type orderRunner struct {
	mu    *sync.Mutex
	order *[]string
}

func (r *orderRunner) Run(ctx context.Context, workDir, prompt string) (string, error) {
	r.mu.Lock()
	*r.order = append(*r.order, prompt)
	r.mu.Unlock()

	result := `{"status":"CREATED","title":"t","body":"b","labels":[],"confidence":"high","files_found":1,"open_questions":0}`
	return fmt.Sprintf("===TRIAGE_RESULT===\n%s", result), nil
}
