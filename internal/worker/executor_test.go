package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"agentdock/internal/bot"
	"agentdock/internal/queue"
)

func TestClassifyResult_UserCancel(t *testing.T) {
	store := queue.NewMemJobStore()
	store.Put(&queue.Job{ID: "j1"})
	store.UpdateStatus("j1", queue.JobCancelled)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	job := &queue.Job{ID: "j1"}
	result := classifyResult(job, time.Now(), fmt.Errorf("killed"), "/tmp/repo", ctx, store)

	if result.Status != "cancelled" {
		t.Errorf("status = %q, want cancelled", result.Status)
	}
	if result.RepoPath != "/tmp/repo" {
		t.Errorf("RepoPath = %q, want /tmp/repo", result.RepoPath)
	}
	if result.Error != "" {
		t.Errorf("Error should be empty for cancelled, got %q", result.Error)
	}
}

func TestClassifyResult_WatchdogKillFallsThroughToFailed(t *testing.T) {
	store := queue.NewMemJobStore()
	store.Put(&queue.Job{ID: "j1"})
	store.UpdateStatus("j1", queue.JobFailed)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result := classifyResult(&queue.Job{ID: "j1"}, time.Now(),
		fmt.Errorf("killed"), "/tmp/repo", ctx, store)

	if result.Status != "failed" {
		t.Errorf("status = %q, want failed", result.Status)
	}
	if result.Error == "" {
		t.Error("Error should be populated for failed")
	}
}

func TestClassifyResult_RunningStoreIsFailed(t *testing.T) {
	store := queue.NewMemJobStore()
	store.Put(&queue.Job{ID: "j1"})
	store.UpdateStatus("j1", queue.JobRunning)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result := classifyResult(&queue.Job{ID: "j1"}, time.Now(),
		errors.New("exit 143"), "/tmp/repo", ctx, store)

	if result.Status != "failed" {
		t.Errorf("status = %q, want failed (store not yet JobCancelled)", result.Status)
	}
}

func TestClassifyResult_DeadlineExceededIsFailed(t *testing.T) {
	store := queue.NewMemJobStore()
	store.Put(&queue.Job{ID: "j1"})
	store.UpdateStatus("j1", queue.JobCancelled) // even with cancelled store…

	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	result := classifyResult(&queue.Job{ID: "j1"}, time.Now(),
		fmt.Errorf("timeout"), "/tmp/repo", ctx, store)

	if result.Status != "failed" {
		t.Errorf("DeadlineExceeded must yield failed, got %q", result.Status)
	}
}

func TestClassifyResult_NoErrorRoutesToFailed(t *testing.T) {
	store := queue.NewMemJobStore()
	store.Put(&queue.Job{ID: "j1"})

	ctx := context.Background()
	result := classifyResult(&queue.Job{ID: "j1"}, time.Now(),
		errors.New("parse failed"), "/tmp/repo", ctx, store)

	if result.Status != "failed" {
		t.Errorf("status = %q, want failed", result.Status)
	}
}

// Scenario 6 — Admin kill path: store=JobFailed + ctx cancel yields "failed", not "cancelled".
// Covered by TestClassifyResult_WatchdogKillFallsThroughToFailed above, which also
// asserts result.Error is non-empty.

// Agent emitted a JSON-format REJECTED ("not our bug") — worker must stamp
// Confidence=low and carry the message so the listener's existing "skipped"
// lane fires instead of trying to create an issue with an empty title.
func TestExecuteJob_AgentRejectedRoutesToLowConfidence(t *testing.T) {
	store := queue.NewMemJobStore()
	job := &queue.Job{
		ID:   "jrej",
		Repo: "o/r",
		PromptContext: &queue.PromptContext{
			ThreadMessages: []queue.ThreadMessage{{User: "T", Timestamp: "1", Text: "test"}},
			Channel:        "test",
			Reporter:       "tester",
			Goal:           "test goal",
		},
	}
	store.Put(job)

	output := "done.\n\n===TRIAGE_RESULT===\n" + `{"status":"REJECTED","message":"not our repo"}`

	deps := executionDeps{
		attachments: queue.NewInMemAttachmentStore(),
		repoCache:   &mockRepo{path: "/tmp/r"},
		runner:      &mockRunner{output: output},
		store:       store,
	}

	result := executeJob(context.Background(), job, deps, bot.RunOptions{}, slog.Default())

	if result.Status != "completed" {
		t.Errorf("status = %q, want completed", result.Status)
	}
	if result.Confidence != "low" {
		t.Errorf("confidence = %q, want low (so listener skips issue creation)", result.Confidence)
	}
	if result.Title != "" {
		t.Errorf("title must stay empty for REJECTED, got %q", result.Title)
	}
	if result.Message != "not our repo" {
		t.Errorf("message = %q, want 'not our repo'", result.Message)
	}
}

// Agent emitted a JSON-format ERROR — worker must route to failed so the
// user sees the reason and gets a retry button, not a 422 "title blank".
func TestExecuteJob_AgentErrorRoutesToFailed(t *testing.T) {
	store := queue.NewMemJobStore()
	job := &queue.Job{
		ID:   "jerr",
		Repo: "o/r",
		PromptContext: &queue.PromptContext{
			ThreadMessages: []queue.ThreadMessage{{User: "T", Timestamp: "1", Text: "test"}},
			Channel:        "test",
			Reporter:       "tester",
			Goal:           "test goal",
		},
	}
	store.Put(job)

	output := "done.\n\n===TRIAGE_RESULT===\n" + `{"status":"ERROR","message":"gh exploded"}`

	deps := executionDeps{
		attachments: queue.NewInMemAttachmentStore(),
		repoCache:   &mockRepo{path: "/tmp/r"},
		runner:      &mockRunner{output: output},
		store:       store,
	}

	result := executeJob(context.Background(), job, deps, bot.RunOptions{}, slog.Default())

	if result.Status != "failed" {
		t.Errorf("status = %q, want failed", result.Status)
	}
	if !strings.Contains(result.Error, "gh exploded") {
		t.Errorf("error = %q, want to mention 'gh exploded'", result.Error)
	}
}

// Spec §9: Job with nil PromptContext must fail loudly with a clear error,
// not silently render an empty prompt. Drain-and-cut makes this path
// unreachable in production, but the defense is worth verifying.
func TestExecuteJob_NilPromptContextFailsMalformed(t *testing.T) {
	store := queue.NewMemJobStore()
	job := &queue.Job{ID: "jnil", Repo: "o/r"} // no PromptContext
	store.Put(job)

	deps := executionDeps{
		attachments: queue.NewInMemAttachmentStore(),
		repoCache:   &mockRepo{path: "/tmp/r"},
		runner:      &mockRunner{},
		store:       store,
	}

	result := executeJob(context.Background(), job, deps, bot.RunOptions{}, slog.Default())

	if result.Status != "failed" {
		t.Errorf("status = %q, want failed", result.Status)
	}
	if !strings.Contains(result.Error, "missing prompt_context") {
		t.Errorf("error = %q, want substring 'missing prompt_context'", result.Error)
	}
}

// Scenario B-race — Pre-Prepare ctx guard: store set to JobCancelled before Prepare runs
// → Prepare is not invoked and the result is cancelled.
func TestExecuteJob_PrePrepareGuardSkipsClone(t *testing.T) {
	store := queue.NewMemJobStore()
	job := &queue.Job{ID: "jguard", Repo: "o/r"}
	store.Put(job)
	store.UpdateStatus("jguard", queue.JobCancelled)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	prepareCalled := false
	deps := executionDeps{
		attachments: queue.NewInMemAttachmentStore(),
		repoCache: &mockRepo{
			path:        "/tmp/r",
			prepareHook: func() { prepareCalled = true },
		},
		runner: &mockRunner{},
		store:  store,
	}

	result := executeJob(ctx, job, deps, bot.RunOptions{}, slog.Default())

	if prepareCalled {
		t.Error("Prepare must not be invoked when ctx is cancelled before prep")
	}
	if result.Status != "cancelled" {
		t.Errorf("status = %q, want cancelled", result.Status)
	}
}
