package worker

import (
	"context"
	"fmt"
	"testing"
	"time"

	"agentdock/internal/bot"
	"agentdock/internal/queue"
)

type mockRunner struct {
	output string
	err    error
}

func (m *mockRunner) Run(ctx context.Context, workDir, prompt string, opts bot.RunOptions) (string, error) {
	return m.output, m.err
}

type mockRepo struct {
	path string
	err  error
}

func (m *mockRepo) Prepare(cloneURL, branch string) (string, error) {
	return m.path, m.err
}

func TestPool_ExecutesJobAndPublishesResult(t *testing.T) {
	store := queue.NewMemJobStore()
	bundle := queue.NewInMemBundle(10, 3, store)
	defer bundle.Close()

	agentOutput := "Analysis done.\n\n===TRIAGE_RESULT===\n" + `{
  "status": "CREATED",
  "title": "Bug fix",
  "body": "## Problem\nSomething broke",
  "labels": ["bug"],
  "confidence": "high",
  "files_found": 3,
  "open_questions": 0
}`

	pool := NewPool(Config{
		Queue:       bundle.Queue,
		Attachments: bundle.Attachments,
		Results:     bundle.Results,
		Store:       store,
		Runner:      &mockRunner{output: agentOutput},
		RepoCache:   &mockRepo{path: "/tmp/test-repo"},
		WorkerCount: 1,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pool.Start(ctx)

	// Signal attachments ready before submitting.
	bundle.Attachments.Prepare(ctx, "j1", nil)

	bundle.Queue.Submit(ctx, &queue.Job{
		ID:       "j1",
		Priority: 50,
		Repo:     "owner/repo",
		Prompt:   "test prompt",
	})

	ch, _ := bundle.Results.Subscribe(ctx)
	select {
	case result := <-ch:
		if result.JobID != "j1" {
			t.Errorf("jobID = %q, want j1", result.JobID)
		}
		if result.Status != "completed" {
			t.Errorf("status = %q, want completed", result.Status)
		}
		if result.Title != "Bug fix" {
			t.Errorf("title = %q", result.Title)
		}
	case <-ctx.Done():
		t.Fatal("timeout waiting for result")
	}
}

func TestPool_AgentFailurePublishesFailedResult(t *testing.T) {
	store := queue.NewMemJobStore()
	bundle := queue.NewInMemBundle(10, 3, store)
	defer bundle.Close()

	pool := NewPool(Config{
		Queue:       bundle.Queue,
		Attachments: bundle.Attachments,
		Results:     bundle.Results,
		Store:       store,
		Runner:      &mockRunner{err: fmt.Errorf("agent crashed")},
		RepoCache:   &mockRepo{path: "/tmp/test-repo"},
		WorkerCount: 1,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pool.Start(ctx)
	bundle.Attachments.Prepare(ctx, "j1", nil)
	bundle.Queue.Submit(ctx, &queue.Job{ID: "j1", Priority: 50, Prompt: "test"})

	ch, _ := bundle.Results.Subscribe(ctx)
	select {
	case result := <-ch:
		if result.Status != "failed" {
			t.Errorf("status = %q, want failed", result.Status)
		}
		if result.Error == "" {
			t.Error("error should not be empty")
		}
	case <-ctx.Done():
		t.Fatal("timeout")
	}
}
