package pool

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/Ivantseng123/agentdock/shared/queue"
	"github.com/Ivantseng123/agentdock/shared/queue/queuetest"
	"github.com/Ivantseng123/agentdock/worker/agent"
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

// Note: REJECTED/ERROR classification tests moved to internal/bot/result_listener_test.go
// once parsing became an app-side concern (refactor/parse-out-of-worker).

// Spec §9: Job with nil PromptContext must fail loudly with a clear error,
// not silently render an empty prompt. Drain-and-cut makes this path
// unreachable in production, but the defense is worth verifying.
func TestExecuteJob_NilPromptContextFailsMalformed(t *testing.T) {
	store := queue.NewMemJobStore()
	job := &queue.Job{ID: "jnil", Repo: "o/r"} // no PromptContext
	store.Put(job)

	deps := executionDeps{
		attachments: queuetest.NewAttachmentStore(),
		repoCache:   &mockRepo{path: "/tmp/r"},
		runner:      &mockRunner{},
		store:       store,
	}

	result := executeJob(context.Background(), job, deps, agent.RunOptions{}, slog.Default())

	if result.Status != "failed" {
		t.Errorf("status = %q, want failed", result.Status)
	}
	if !strings.Contains(result.Error, "missing prompt_context") {
		t.Errorf("error = %q, want substring 'missing prompt_context'", result.Error)
	}
}

// ── RED/GREEN #5: empty repo reference → fail before git clone. ────────────
//
// When the app-side state-machine race leaves Job.Repo="" and CloneURL as the
// placeholder "https://github.com/.git" (cleanCloneURL("") output), the
// worker must refuse before invoking Prepare rather than asking git to clone
// a nonsense URL.
func TestExecuteJob_EmptyRepoPlaceholderCloneURL_FailsBeforeClone(t *testing.T) {
	store := queue.NewMemJobStore()
	job := &queue.Job{
		ID:            "jempty1",
		Repo:          "",
		CloneURL:      "https://github.com/.git",
		Branch:        "master",
		PromptContext: &queue.PromptContext{},
	}
	store.Put(job)

	prepareCalled := false
	deps := executionDeps{
		attachments: queuetest.NewAttachmentStore(),
		repoCache: &mockRepo{
			path:        "/tmp/r",
			prepareHook: func() { prepareCalled = true },
		},
		runner: &mockRunner{},
		store:  store,
	}

	result := executeJob(context.Background(), job, deps, agent.RunOptions{}, slog.Default())

	if prepareCalled {
		t.Error("Prepare must not run for empty-repo placeholder CloneURL")
	}
	if result.Status != "failed" {
		t.Errorf("status = %q, want failed", result.Status)
	}
	if !strings.Contains(result.Error, "empty repo reference") {
		t.Errorf("error should mention empty repo reference, got %q", result.Error)
	}
}

func TestExecuteJob_EmptyRepoStringWithNonEmptyCloneURL_FailsBeforeClone(t *testing.T) {
	// Edge case: CloneURL is some URL but Repo is blank — still a race
	// symptom, still refuse.
	store := queue.NewMemJobStore()
	job := &queue.Job{
		ID:            "jempty2",
		Repo:          "",
		CloneURL:      "https://github.com/something.git",
		Branch:        "master",
		PromptContext: &queue.PromptContext{},
	}
	store.Put(job)

	prepareCalled := false
	deps := executionDeps{
		attachments: queuetest.NewAttachmentStore(),
		repoCache: &mockRepo{
			path:        "/tmp/r",
			prepareHook: func() { prepareCalled = true },
		},
		runner: &mockRunner{},
		store:  store,
	}

	result := executeJob(context.Background(), job, deps, agent.RunOptions{}, slog.Default())

	if prepareCalled {
		t.Error("Prepare must not run when Repo is empty but CloneURL is set")
	}
	if result.Status != "failed" {
		t.Errorf("status = %q, want failed", result.Status)
	}
	if !strings.Contains(result.Error, "empty repo reference") {
		t.Errorf("error should mention empty repo reference, got %q", result.Error)
	}
}

func TestExecuteJob_EmptyCloneURLIsAskPath_NotFlaggedEmpty(t *testing.T) {
	// Ask-with-no-repo: CloneURL is "" and EmptyDirProvider handles it. The
	// empty-repo guard must NOT fire here — otherwise every Ask job breaks.
	store := queue.NewMemJobStore()
	job := &queue.Job{
		ID:            "jask",
		Repo:          "",
		CloneURL:      "",
		PromptContext: &queue.PromptContext{},
	}
	store.Put(job)

	deps := executionDeps{
		attachments: queuetest.NewAttachmentStore(),
		repoCache:   &mockRepo{path: "/tmp/r"},
		runner:      &mockRunner{output: "ok"},
		store:       store,
	}

	result := executeJob(context.Background(), job, deps, agent.RunOptions{}, slog.Default())
	if result.Status == "failed" && strings.Contains(result.Error, "empty repo reference") {
		t.Error("Ask-style empty CloneURL must not be flagged as empty repo reference")
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
		attachments: queuetest.NewAttachmentStore(),
		repoCache: &mockRepo{
			path:        "/tmp/r",
			prepareHook: func() { prepareCalled = true },
		},
		runner: &mockRunner{},
		store:  store,
	}

	result := executeJob(ctx, job, deps, agent.RunOptions{}, slog.Default())

	if prepareCalled {
		t.Error("Prepare must not be invoked when ctx is cancelled before prep")
	}
	if result.Status != "cancelled" {
		t.Errorf("status = %q, want cancelled", result.Status)
	}
}
