package integration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/Ivantseng123/agentdock/shared/queue"
	"github.com/Ivantseng123/agentdock/shared/queue/queuetest"
	"github.com/Ivantseng123/agentdock/worker/agent"
	"github.com/Ivantseng123/agentdock/worker/pool"
)

type fakeRunner struct{}

func (f *fakeRunner) Run(ctx context.Context, workDir, prompt string, opts agent.RunOptions) (string, error) {
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

// PrepareAt satisfies RepoProvider for tests that don't exercise refs.
// Defaults to no-op success.
func (f *fakeRepo) PrepareAt(cloneURL, branch, token, targetPath string) error {
	return nil
}

func (f *fakeRepo) RemoveWorktree(path string) error { return nil }
func (f *fakeRepo) CleanAll() error                  { return nil }
func (f *fakeRepo) PurgeStale() error                { return nil }

func TestFullFlow_SubmitToResult(t *testing.T) {
	ctx := context.Background()
	store := queue.NewMemJobStore()
	bundle := queuetest.NewBundle(10, 3, store)
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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	p.Start(ctx)

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
		// Worker ships raw agent output; app-side ResultListener is now
		// responsible for parsing Title/Confidence out of RawOutput.
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

