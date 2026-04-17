package queue_test

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"agentdock/internal/bot"
	"agentdock/internal/queue"
	"agentdock/internal/worker"
)

type fakeRunner struct{}

func (f *fakeRunner) Run(ctx context.Context, workDir, prompt string, opts bot.RunOptions) (string, error) {
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

func (f *fakeRepo) Prepare(cloneURL, branch, token string) (string, error) {
	return "/tmp/fake-repo", nil
}

func (f *fakeRepo) RemoveWorktree(path string) error { return nil }
func (f *fakeRepo) CleanAll() error                  { return nil }
func (f *fakeRepo) PurgeStale() error                { return nil }

func TestFullFlow_SubmitToResult(t *testing.T) {
	store := queue.NewMemJobStore()
	bundle := queue.NewInMemBundle(10, 3, store)
	defer bundle.Close()

	pool := worker.NewPool(worker.Config{
		Queue:       bundle.Queue,
		Attachments: bundle.Attachments,
		Results:     bundle.Results,
		Store:       store,
		Runner:      &fakeRunner{},
		RepoCache:   &fakeRepo{},
		WorkerCount: 1,
		Logger:      slog.Default(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pool.Start(ctx)

	// Pre-signal attachments ready.
	bundle.Attachments.Prepare(ctx, "j1", nil)

	// Submit job.
	err := bundle.Queue.Submit(ctx, &queue.Job{
		ID:       "j1",
		Priority: 50,
		Repo:     "owner/repo",
		PromptContext: &queue.PromptContext{
			ThreadMessages: []queue.ThreadMessage{{User: "T", Timestamp: "1", Text: "test prompt"}},
			Channel:        "test",
			Reporter:       "tester",
			Goal:           "test goal",
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Wait for result.
	ch, _ := bundle.Results.Subscribe(ctx)
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
	bundle := queue.NewInMemBundle(10, 3, store)
	defer bundle.Close()

	ctx := context.Background()
	bundle.Attachments.Prepare(ctx, "low", nil)
	bundle.Attachments.Prepare(ctx, "high", nil)
	bundle.Attachments.Prepare(ctx, "mid", nil)

	bundle.Queue.Submit(ctx, &queue.Job{ID: "low", Priority: 10, PromptContext: &queue.PromptContext{ThreadMessages: []queue.ThreadMessage{{User: "T", Timestamp: "1", Text: "low"}}, Channel: "test", Reporter: "tester", Goal: "test goal"}})
	bundle.Queue.Submit(ctx, &queue.Job{ID: "high", Priority: 100, PromptContext: &queue.PromptContext{ThreadMessages: []queue.ThreadMessage{{User: "T", Timestamp: "1", Text: "high"}}, Channel: "test", Reporter: "tester", Goal: "test goal"}})
	bundle.Queue.Submit(ctx, &queue.Job{ID: "mid", Priority: 50, PromptContext: &queue.PromptContext{ThreadMessages: []queue.ThreadMessage{{User: "T", Timestamp: "1", Text: "mid"}}, Channel: "test", Reporter: "tester", Goal: "test goal"}})

	var mu sync.Mutex
	var order []string
	runner := &orderRunner{mu: &mu, order: &order}

	pool := worker.NewPool(worker.Config{
		Queue:       bundle.Queue,
		Attachments: bundle.Attachments,
		Results:     bundle.Results,
		Store:       store,
		Runner:      runner,
		RepoCache:   &fakeRepo{},
		WorkerCount: 1,
		Logger:      slog.Default(),
	})

	ctx2, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool.Start(ctx2)

	// Collect 3 results.
	ch, _ := bundle.Results.Subscribe(ctx2)
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
	// First should be "high" (priority 100) — prompt is now XML-wrapped so check substring
	if !strings.Contains(order[0], "high") {
		t.Errorf("first execution prompt = %q, want to contain 'high'", order[0])
	}
}

type orderRunner struct {
	mu    *sync.Mutex
	order *[]string
}

func (r *orderRunner) Run(ctx context.Context, workDir, prompt string, opts bot.RunOptions) (string, error) {
	r.mu.Lock()
	*r.order = append(*r.order, prompt)
	r.mu.Unlock()

	result := `{"status":"CREATED","title":"t","body":"b","labels":[],"confidence":"high","files_found":1,"open_questions":0}`
	return fmt.Sprintf("===TRIAGE_RESULT===\n%s", result), nil
}
