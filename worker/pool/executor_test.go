package pool

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Ivantseng123/agentdock/shared/queue"
	"github.com/Ivantseng123/agentdock/shared/queue/queuetest"
	"github.com/Ivantseng123/agentdock/worker/agent"
)

func TestClassifyResult_UserCancel(t *testing.T) {
	ctx := context.Background()
	store := queue.NewMemJobStore()
	store.Put(ctx, &queue.Job{ID: "j1"})
	store.UpdateStatus(ctx, "j1", queue.JobCancelled)

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
	ctx := context.Background()
	store := queue.NewMemJobStore()
	store.Put(ctx, &queue.Job{ID: "j1"})
	store.UpdateStatus(ctx, "j1", queue.JobFailed)

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
	ctx := context.Background()
	store := queue.NewMemJobStore()
	store.Put(ctx, &queue.Job{ID: "j1"})
	store.UpdateStatus(ctx, "j1", queue.JobRunning)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result := classifyResult(&queue.Job{ID: "j1"}, time.Now(),
		errors.New("exit 143"), "/tmp/repo", ctx, store)

	if result.Status != "failed" {
		t.Errorf("status = %q, want failed (store not yet JobCancelled)", result.Status)
	}
}

func TestClassifyResult_DeadlineExceededIsFailed(t *testing.T) {
	ctx := context.Background()
	store := queue.NewMemJobStore()
	store.Put(ctx, &queue.Job{ID: "j1"})
	store.UpdateStatus(ctx, "j1", queue.JobCancelled) // even with cancelled store…

	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	result := classifyResult(&queue.Job{ID: "j1"}, time.Now(),
		fmt.Errorf("timeout"), "/tmp/repo", ctx, store)

	if result.Status != "failed" {
		t.Errorf("DeadlineExceeded must yield failed, got %q", result.Status)
	}
}

func TestClassifyResult_NoErrorRoutesToFailed(t *testing.T) {
	ctx := context.Background()
	store := queue.NewMemJobStore()
	store.Put(ctx, &queue.Job{ID: "j1"})

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
	ctx := context.Background()
	store := queue.NewMemJobStore()
	job := &queue.Job{ID: "jnil", Repo: "o/r"} // no PromptContext
	store.Put(ctx, job)

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

// ── new tests for #140: isEmptyRepoReference ─────────────────────────────────

// TestIsEmptyRepoReference covers the three cases described in issue #140:
//   1. CloneURL == ""                       → not flagged (Ask-with-no-repo)
//   2. CloneURL == "https://github.com/.git" → flagged
//   3. CloneURL non-empty but Repo == ""    → flagged
func TestIsEmptyRepoReference(t *testing.T) {
	tests := []struct {
		name     string
		job      *queue.Job
		wantFlag bool
	}{
		{
			name:     "empty CloneURL is Ask-with-no-repo — not flagged",
			job:      &queue.Job{CloneURL: "", Repo: ""},
			wantFlag: false,
		},
		{
			name:     "cleanCloneURL('') artefact — flagged",
			job:      &queue.Job{CloneURL: "https://github.com/.git", Repo: ""},
			wantFlag: true,
		},
		{
			name:     "non-empty CloneURL but empty Repo — flagged",
			job:      &queue.Job{CloneURL: "https://github.com/foo/bar.git", Repo: ""},
			wantFlag: true,
		},
		{
			name:     "normal job — not flagged",
			job:      &queue.Job{CloneURL: "https://github.com/foo/bar.git", Repo: "foo/bar"},
			wantFlag: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := isEmptyRepoReference(tc.job)
			if got != tc.wantFlag {
				t.Errorf("isEmptyRepoReference(%+v) = %v, want %v", tc.job, got, tc.wantFlag)
			}
		})
	}
}

// TestExecuteJob_EmptyRepoReferenceFailsBeforeClone verifies that executeJob
// returns a failed result containing "empty repo reference" when the job has
// CloneURL == "https://github.com/.git" (the cleanCloneURL("") artefact),
// and that Prepare is never called.
func TestExecuteJob_EmptyRepoReferenceFailsBeforeClone(t *testing.T) {
	ctx := context.Background()
	store := queue.NewMemJobStore()
	job := &queue.Job{
		ID:       "j-empty-repo",
		CloneURL: "https://github.com/.git",
		Repo:     "",
	}
	store.Put(ctx, job)

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
		t.Error("Prepare must not be invoked for empty repo reference")
	}
	if result.Status != "failed" {
		t.Errorf("status = %q, want failed", result.Status)
	}
	if !strings.Contains(result.Error, "empty repo reference") {
		t.Errorf("error = %q, want substring \"empty repo reference\"", result.Error)
	}
}

// Scenario B-race — Pre-Prepare ctx guard: store set to JobCancelled before Prepare runs
// → Prepare is not invoked and the result is cancelled.
func TestExecuteJob_PrePrepareGuardSkipsClone(t *testing.T) {
	ctx := context.Background()
	store := queue.NewMemJobStore()
	job := &queue.Job{ID: "jguard", Repo: "o/r"}
	store.Put(ctx, job)
	store.UpdateStatus(ctx, "jguard", queue.JobCancelled)

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

// initGitWorktree creates a tiny initialised git repo at dir with one empty
// commit. Returns the path. Used by the ref-guard tests so `git status` has
// a real worktree to walk.
func initGitWorktree(t *testing.T, dir string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}
	for _, args := range [][]string{
		{"init", "-q"},
		{"-c", "user.email=t@t", "-c", "user.name=t",
			"commit", "--allow-empty", "-q", "-m", "init"},
	} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return dir
}

// TestRunRefGuard_DetectsViolation creates a real git worktree with an
// untracked file (representing an agent write) and asserts the guard returns
// the violating ref's owner/name in the slice. Worker is task-agnostic — the
// slice is the contract; app side acts on it.
func TestRunRefGuard_DetectsViolation(t *testing.T) {
	dir := initGitWorktree(t, filepath.Join(t.TempDir(), "ref"))
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("agent wrote here"), 0644); err != nil {
		t.Fatal(err)
	}

	violations := runRefGuard([]queue.RefRepoContext{{Repo: "foo/bar", Path: dir}}, slog.Default())

	if len(violations) != 1 || violations[0] != "foo/bar" {
		t.Fatalf("expected violations=[foo/bar]; got %v", violations)
	}
}

// TestRunRefGuard_CleanWorktree_NoNoise asserts the no-violation path returns
// nil — important so app side's `len(violations) > 0` check only triggers on
// real writes, not on every successful job.
func TestRunRefGuard_CleanWorktree_NoNoise(t *testing.T) {
	dir := initGitWorktree(t, filepath.Join(t.TempDir(), "ref"))

	violations := runRefGuard([]queue.RefRepoContext{{Repo: "foo/bar", Path: dir}}, slog.Default())

	if violations != nil {
		t.Fatalf("clean worktree must return nil violations; got %v", violations)
	}
}

// TestRunRefGuard_EmptyRefs_NoOp covers the no-refs path: returns nil without
// running any git command (no panic on nil/empty input).
func TestRunRefGuard_EmptyRefs_NoOp(t *testing.T) {
	violations := runRefGuard(nil, slog.Default())
	if violations != nil {
		t.Fatalf("empty refs must return nil; got %v", violations)
	}
}

// TestTruncateRefDiff covers the log-volume cap on diff previews.
func TestTruncateRefDiff(t *testing.T) {
	short := "M f.txt"
	if got := truncateRefDiff(short); got != short {
		t.Errorf("short input changed: in=%q out=%q", short, got)
	}
	long := strings.Repeat("x", 500)
	got := truncateRefDiff(long)
	if !strings.HasSuffix(got, "…(truncated)") {
		t.Errorf("long input not truncated: %q", got)
	}
	if len(got) > 220 { // 200 + truncation marker
		t.Errorf("truncated length too long: %d", len(got))
	}
}

// recordingRepo is a RepoProvider that records every call and lets the test
// dictate per-target PrepareAt outcomes. Used by the multi-repo executeJob
// test below where we need both success and failure paths exercised in one
// run plus the ability to assert cleanup ordering.
type recordingRepo struct {
	primaryPath   string
	prepareAtFail map[string]bool // key: target path suffix; value: should fail
	prepareAtCalls []string         // recorded target paths in call order
	removedWorktrees []string
}

func (r *recordingRepo) Prepare(cloneURL, branch, token string) (string, error) {
	if err := os.MkdirAll(r.primaryPath, 0755); err != nil {
		return "", err
	}
	return r.primaryPath, nil
}

func (r *recordingRepo) PrepareAt(cloneURL, branch, token, target string) error {
	r.prepareAtCalls = append(r.prepareAtCalls, target)
	for suffix, shouldFail := range r.prepareAtFail {
		if strings.HasSuffix(target, suffix) && shouldFail {
			return fmt.Errorf("simulated clone failure for %s", target)
		}
	}
	// Initialise a real git worktree so the post-execute guard's git status
	// has something to walk. Without this, guard would log debug + skip.
	return initBareGitFolder(target)
}

func (r *recordingRepo) RemoveWorktree(path string) error {
	r.removedWorktrees = append(r.removedWorktrees, path)
	return nil
}
func (r *recordingRepo) CleanAll() error   { return nil }
func (r *recordingRepo) PurgeStale() error { return nil }

func initBareGitFolder(dir string) error {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	for _, args := range [][]string{
		{"init", "-q"},
		{"-c", "user.email=t@t", "-c", "user.name=t",
			"commit", "--allow-empty", "-q", "-m", "init"},
	} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("git %v: %w\n%s", args, err, out)
		}
	}
	return nil
}

// TestExecuteJob_MultiRepo_EndToEnd is the executor-level integration test
// for the ref pipeline: schema → prepareRefs → PromptContext fill → builder
// renders ref blocks → guard fires on dirty refs → cleanup walks all paths
// in reverse order. One ref clones successfully (and gets dirtied to
// trigger the lenient guard); a second ref deliberately fails to clone.
func TestExecuteJob_MultiRepo_EndToEnd(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}

	tmp := t.TempDir()
	primaryPath := filepath.Join(tmp, "triage-repo-abc")

	repo := &recordingRepo{
		primaryPath: primaryPath,
		prepareAtFail: map[string]bool{
			"broken__repo": true, // second ref clone fails
		},
	}

	// dirtyingRunner writes into the successful ref's worktree before
	// returning so the post-execute guard sees a non-empty `git status` and
	// fires the lenient callback. Path is deterministic from refsRootPath +
	// refDirName.
	runner := &dirtyingRunner{
		dirtyPath: filepath.Join(primaryPath, ".refs", "frontend__web", "agent-wrote-here.txt"),
		output:    "answer body",
	}

	store := queue.NewMemJobStore()
	job := &queue.Job{
		ID:       "j-multi",
		Repo:     "primary/repo",
		CloneURL: "https://example.com/primary/repo.git",
		Branch:   "main",
		PromptContext: &queue.PromptContext{
			Channel:  "general",
			Reporter: "Alice",
			Goal:     "answer the question",
			OutputRules: []string{"do not write to refs"},
		},
		RefRepos: []queue.RefRepo{
			{Repo: "frontend/web", CloneURL: "https://example.com/frontend/web.git", Branch: "main"},
			{Repo: "broken/repo", CloneURL: "https://example.com/broken/repo.git"},
		},
	}
	store.Put(context.Background(), job)

	deps := executionDeps{
		attachments: queuetest.NewAttachmentStore(),
		repoCache:   repo,
		runner:      runner,
		store:       store,
	}

	res := executeJob(context.Background(), job, deps, agent.RunOptions{}, slog.Default())

	if res.Status != "completed" {
		t.Fatalf("status = %q, want completed (Error=%q)", res.Status, res.Error)
	}

	// Successful ref clones once at <primary>-refs/<owner>__<name>.
	gotTargets := repo.prepareAtCalls
	if len(gotTargets) != 2 {
		t.Fatalf("PrepareAt call count = %d, want 2 (got %v)", len(gotTargets), gotTargets)
	}
	wantSuffixes := []string{"frontend__web", "broken__repo"}
	for i, want := range wantSuffixes {
		if !strings.HasSuffix(gotTargets[i], want) {
			t.Errorf("PrepareAt[%d] target = %q, want suffix %q", i, gotTargets[i], want)
		}
	}

	// Prompt contains the successful ref + the unavailable ref.
	if !strings.Contains(runner.gotPrompt, "<ref_repos>") {
		t.Errorf("prompt missing <ref_repos> block:\n%s", runner.gotPrompt)
	}
	if !strings.Contains(runner.gotPrompt, `repo="frontend/web"`) {
		t.Errorf("prompt missing successful ref:\n%s", runner.gotPrompt)
	}
	if !strings.Contains(runner.gotPrompt, "<unavailable_refs>") {
		t.Errorf("prompt missing <unavailable_refs> block:\n%s", runner.gotPrompt)
	}
	if !strings.Contains(runner.gotPrompt, "<repo>broken/repo</repo>") {
		t.Errorf("prompt missing unavailable entry for broken/repo:\n%s", runner.gotPrompt)
	}

	// Cleanup: ref worktree removed (RemoveWorktree called for the successful
	// ref's path), refs root rm'd. Order is reversed (only one successful
	// ref here), and the refs-root dir should be gone after job.
	if len(repo.removedWorktrees) != 1 {
		t.Errorf("RemoveWorktree call count = %d, want 1 (got %v)",
			len(repo.removedWorktrees), repo.removedWorktrees)
	}
	if !strings.HasSuffix(repo.removedWorktrees[0], "frontend__web") {
		t.Errorf("RemoveWorktree path = %q, want suffix frontend__web",
			repo.removedWorktrees[0])
	}
	refsRoot := filepath.Join(primaryPath, ".refs")
	if _, err := os.Stat(refsRoot); !os.IsNotExist(err) {
		t.Errorf("refs root should be cleaned up; stat err = %v", err)
	}

	// Post-execute guard: dirtied frontend ref → JobResult.RefViolations
	// carries owner/name. Worker is task-agnostic; app side reads this.
	if len(res.RefViolations) != 1 || res.RefViolations[0] != "frontend/web" {
		t.Errorf("RefViolations = %v, want [frontend/web]", res.RefViolations)
	}
}

// dirtyingRunner writes a file into a ref worktree before returning, to
// trigger the post-execute guard. Used only by the multi-repo end-to-end
// test above.
type dirtyingRunner struct {
	dirtyPath  string
	output     string
	gotPrompt  string
	gotWorkDir string
}

func (d *dirtyingRunner) Run(ctx context.Context, workDir, prompt string, opts agent.RunOptions) (string, error) {
	d.gotPrompt = prompt
	d.gotWorkDir = workDir
	if d.dirtyPath != "" {
		_ = os.WriteFile(d.dirtyPath, []byte("agent leaked"), 0644)
	}
	return d.output, nil
}
