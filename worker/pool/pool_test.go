package pool

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Ivantseng123/agentdock/shared/crypto"
	"github.com/Ivantseng123/agentdock/shared/queue"
	"github.com/Ivantseng123/agentdock/shared/queue/queuetest"
	"github.com/Ivantseng123/agentdock/worker/agent"
)

type mockRunner struct {
	output string
	err    error
}

func (m *mockRunner) Run(ctx context.Context, workDir, prompt string, opts agent.RunOptions) (string, error) {
	return m.output, m.err
}

type mockRepo struct {
	path             string
	err              error
	removedWorktrees []string
	cleanAllCalled   bool
	purgedStale      bool
	prepareHook      func()
}

func (m *mockRepo) Prepare(cloneURL, branch, token string) (string, error) {
	if m.prepareHook != nil {
		m.prepareHook()
	}
	return m.path, m.err
}

func (m *mockRepo) RemoveWorktree(path string) error {
	m.removedWorktrees = append(m.removedWorktrees, path)
	return nil
}

func (m *mockRepo) CleanAll() error {
	m.cleanAllCalled = true
	return nil
}

func (m *mockRepo) PurgeStale() error {
	m.purgedStale = true
	return nil
}

func TestPool_ExecutesJobAndPublishesResult(t *testing.T) {
	store := queue.NewMemJobStore()
	bundle := queuetest.NewBundle(10, 3, store)
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
		Logger:      slog.Default(),
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
		PromptContext: &queue.PromptContext{
			ThreadMessages: []queue.ThreadMessage{{User: "T", Timestamp: "1", Text: "test prompt"}},
			Channel:        "test",
			Reporter:       "tester",
			Goal:           "test goal",
		},
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
		// Worker no longer parses agent output — it just forwards it.
		// The app-side ResultListener is responsible for decoding Title/etc.
		if result.RawOutput != agentOutput {
			t.Errorf("RawOutput mismatch; got %q", result.RawOutput)
		}
		if !strings.Contains(result.RawOutput, "===TRIAGE_RESULT===") {
			t.Errorf("RawOutput missing TRIAGE_RESULT marker: %q", result.RawOutput)
		}
		// Title is no longer a JobResult field — parsing is app-side.
	case <-ctx.Done():
		t.Fatal("timeout waiting for result")
	}
}

func TestPool_WorkerIDIncludesHostname(t *testing.T) {
	store := queue.NewMemJobStore()
	bundle := queuetest.NewBundle(10, 3, store)
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
		Hostname:    "test-host",
		Logger:      slog.Default(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pool.Start(ctx)
	bundle.Attachments.Prepare(ctx, "j1", nil)
	bundle.Queue.Submit(ctx, &queue.Job{ID: "j1", Priority: 50, PromptContext: &queue.PromptContext{ThreadMessages: []queue.ThreadMessage{{User: "T", Timestamp: "1", Text: "test"}}, Channel: "test", Reporter: "tester", Goal: "test goal"}})

	ch, _ := bundle.Results.Subscribe(ctx)
	select {
	case <-ch:
		state, _ := store.Get("j1")
		if state.WorkerID == "" {
			t.Error("WorkerID should be set after execution")
		}
		if state.WorkerID != "test-host/worker-0" {
			t.Errorf("WorkerID = %q, want test-host/worker-0", state.WorkerID)
		}
	case <-ctx.Done():
		t.Fatal("timeout")
	}
}

func TestPool_AgentFailurePublishesFailedResult(t *testing.T) {
	store := queue.NewMemJobStore()
	bundle := queuetest.NewBundle(10, 3, store)
	defer bundle.Close()

	pool := NewPool(Config{
		Queue:       bundle.Queue,
		Attachments: bundle.Attachments,
		Results:     bundle.Results,
		Store:       store,
		Runner:      &mockRunner{err: fmt.Errorf("agent crashed")},
		RepoCache:   &mockRepo{path: "/tmp/test-repo"},
		WorkerCount: 1,
		Logger:      slog.Default(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pool.Start(ctx)
	bundle.Attachments.Prepare(ctx, "j1", nil)
	bundle.Queue.Submit(ctx, &queue.Job{ID: "j1", Priority: 50, PromptContext: &queue.PromptContext{ThreadMessages: []queue.ThreadMessage{{User: "T", Timestamp: "1", Text: "test"}}, Channel: "test", Reporter: "tester", Goal: "test goal"}})

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

type secretCapturingRunner struct {
	onRun func(opts agent.RunOptions)
}

func (r *secretCapturingRunner) Run(ctx context.Context, workDir, prompt string, opts agent.RunOptions) (string, error) {
	if r.onRun != nil {
		r.onRun(opts)
	}
	return "Analysis done.\n\n===TRIAGE_RESULT===\n" + `{
  "status": "CREATED",
  "title": "test",
  "body": "## Problem\nTest",
  "labels": ["bug"],
  "confidence": "high",
  "files_found": 0,
  "open_questions": 0
}`, nil
}

func TestExecuteJob_DecryptsAndMergesSecrets(t *testing.T) {
	dir := t.TempDir()

	secretKey := make([]byte, 32)
	rand.Read(secretKey)

	appSecrets := map[string]string{
		"GH_TOKEN":  "ghp_from_app",
		"K8S_TOKEN": "k8s_from_app",
	}
	secretsJSON, _ := json.Marshal(appSecrets)
	encrypted, _ := crypto.Encrypt(secretKey, secretsJSON)

	workerSecrets := map[string]string{
		"GH_TOKEN": "ghp_worker_override",
	}

	var capturedSecrets map[string]string
	runner := &secretCapturingRunner{
		onRun: func(opts agent.RunOptions) {
			capturedSecrets = opts.Secrets
		},
	}

	job := &queue.Job{
		ID:               "test-job",
		Repo:             "owner/repo",
		CloneURL:         "https://github.com/owner/repo.git",
		EncryptedSecrets: encrypted,
		PromptContext: &queue.PromptContext{
			ThreadMessages: []queue.ThreadMessage{{User: "T", Timestamp: "1", Text: "test"}},
			Channel:        "test",
			Reporter:       "tester",
			Goal:           "test goal",
		},
	}

	deps := executionDeps{
		attachments:   queuetest.NewAttachmentStore(),
		repoCache:     &mockRepo{path: dir},
		runner:        runner,
		store:         queue.NewMemJobStore(),
		secretKey:     secretKey,
		workerSecrets: workerSecrets,
	}

	result := executeJob(context.Background(), job, deps, agent.RunOptions{}, slog.Default())
	if result.Status == "failed" {
		t.Fatalf("job failed: %s", result.Error)
	}

	if capturedSecrets["GH_TOKEN"] != "ghp_worker_override" {
		t.Errorf("GH_TOKEN = %q, want ghp_worker_override", capturedSecrets["GH_TOKEN"])
	}
	if capturedSecrets["K8S_TOKEN"] != "k8s_from_app" {
		t.Errorf("K8S_TOKEN = %q, want k8s_from_app", capturedSecrets["K8S_TOKEN"])
	}
}

func TestExecuteJob_NoSecretKey_EncryptedSecrets_Fails(t *testing.T) {
	dir := t.TempDir()

	job := &queue.Job{
		ID:               "test-job",
		Repo:             "owner/repo",
		CloneURL:         "https://github.com/owner/repo.git",
		EncryptedSecrets: []byte("some-encrypted-data"),
	}

	deps := executionDeps{
		attachments: queuetest.NewAttachmentStore(),
		repoCache:   &mockRepo{path: dir},
		runner:      &mockRunner{output: "ok"},
		store:       queue.NewMemJobStore(),
		secretKey:   nil,
	}

	result := executeJob(context.Background(), job, deps, agent.RunOptions{}, slog.Default())
	if result.Status != "failed" {
		t.Error("expected job to fail when EncryptedSecrets present but no secretKey")
	}
}

func TestPool_ShortCircuitsCancelledJobAsCancelled(t *testing.T) {
	store := queue.NewMemJobStore()
	bundle := queuetest.NewBundle(10, 3, store)
	defer bundle.Close()

	job := &queue.Job{ID: "jc", Repo: "o/r", SubmittedAt: time.Now()}

	pool := NewPool(Config{
		Queue:       bundle.Queue,
		Attachments: bundle.Attachments,
		Results:     bundle.Results,
		Store:       store,
		Runner:      &mockRunner{},
		RepoCache:   &mockRepo{},
		WorkerCount: 1,
		Logger:      slog.Default(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Submit first (puts job in store as pending), then mark cancelled before
	// starting the pool so the worker sees cancelled status deterministically.
	if err := bundle.Queue.Submit(ctx, job); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	store.UpdateStatus("jc", queue.JobCancelled)

	pool.Start(ctx)

	ch, _ := bundle.Results.Subscribe(ctx)
	select {
	case r := <-ch:
		if r.Status != "cancelled" {
			t.Errorf("status = %q, want cancelled", r.Status)
		}
	case <-ctx.Done():
		t.Fatal("no result")
	}
}

func TestPool_ShortCircuitsFailedJobAsFailed(t *testing.T) {
	store := queue.NewMemJobStore()
	bundle := queuetest.NewBundle(10, 3, store)
	defer bundle.Close()

	job := &queue.Job{ID: "jf", Repo: "o/r", SubmittedAt: time.Now()}

	pool := NewPool(Config{
		Queue:       bundle.Queue,
		Attachments: bundle.Attachments,
		Results:     bundle.Results,
		Store:       store,
		Runner:      &mockRunner{},
		RepoCache:   &mockRepo{},
		WorkerCount: 1,
		Logger:      slog.Default(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Submit first (puts job in store as pending), then mark failed before
	// starting the pool so the worker sees failed status deterministically.
	if err := bundle.Queue.Submit(ctx, job); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	store.UpdateStatus("jf", queue.JobFailed)

	pool.Start(ctx)

	ch, _ := bundle.Results.Subscribe(ctx)
	select {
	case r := <-ch:
		if r.Status != "failed" {
			t.Errorf("status = %q, want failed", r.Status)
		}
	case <-ctx.Done():
		t.Fatal("no result")
	}
}

type blockingRunner struct {
	started chan struct{}
}

func (b *blockingRunner) Run(ctx context.Context, workDir, prompt string, opts agent.RunOptions) (string, error) {
	if opts.OnStarted != nil {
		opts.OnStarted(1234, "fake")
	}
	close(b.started)
	<-ctx.Done()
	return "", ctx.Err()
}

// prepBlockingRunner blocks inside Run (simulating prep-like work) until ctx is cancelled.
// It does NOT call OnStarted so the process registry never transitions to "started".
type prepBlockingRunner struct {
	started chan struct{}
}

func (b *prepBlockingRunner) Run(ctx context.Context, workDir, prompt string, opts agent.RunOptions) (string, error) {
	close(b.started)
	<-ctx.Done()
	return "", ctx.Err()
}

// Scenario B — Kill arrives while runner is blocked during prep-like work (before OnStarted).
func TestPool_KillDuringPrepProducesCancelledResult(t *testing.T) {
	store := queue.NewMemJobStore()
	bundle := queuetest.NewBundle(10, 3, store)
	defer bundle.Close()

	job := &queue.Job{
		ID:          "jprep",
		Repo:        "o/r",
		SubmittedAt: time.Now(),
		PromptContext: &queue.PromptContext{
			ThreadMessages: []queue.ThreadMessage{{User: "T", Timestamp: "1", Text: "test"}},
			Channel:        "test",
			Reporter:       "tester",
			Goal:           "test goal",
		},
	}

	runner := &prepBlockingRunner{started: make(chan struct{})}
	pool := NewPool(Config{
		Queue:       bundle.Queue,
		Attachments: bundle.Attachments,
		Results:     bundle.Results,
		Store:       store,
		Runner:      runner,
		RepoCache:   &mockRepo{path: "/tmp/r"},
		Commands:    bundle.Commands,
		WorkerCount: 1,
		Logger:      slog.Default(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	pool.Start(ctx)

	if err := bundle.Queue.Submit(ctx, job); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	<-runner.started
	store.UpdateStatus("jprep", queue.JobCancelled)
	bundle.Commands.Send(ctx, queue.Command{JobID: "jprep", Action: "kill"})

	ch, _ := bundle.Results.Subscribe(ctx)
	select {
	case r := <-ch:
		if r.Status != "cancelled" {
			t.Errorf("status = %q, want cancelled", r.Status)
		}
	case <-ctx.Done():
		t.Fatal("no result")
	}
}

// Scenario 7 — Watchdog-level cancel fallback (JobCancelled + CancelledAt past timeout + no worker publish)
// is covered by TestWatchdog_CancelFallbackAfterTimeout in shared/queue/watchdog_test.go.
// No pool-level duplication needed.

func TestHandleJob_PublishesPrepStatusReport(t *testing.T) {
	store := queue.NewMemJobStore()
	bundle := queuetest.NewBundle(10, 3, store)
	defer bundle.Close()

	statusBus := queuetest.NewStatusBus(16)

	var (
		mu          sync.Mutex
		reports     []queue.StatusReport
		reportReady = make(chan struct{}, 1)
	)

	// Collect StatusReports in background.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	go func() {
		ch, _ := statusBus.Subscribe(ctx)
		for r := range ch {
			mu.Lock()
			reports = append(reports, r)
			if len(reports) == 1 {
				select {
				case reportReady <- struct{}{}:
				default:
				}
			}
			mu.Unlock()
		}
	}()

	// Use a runner that blocks until cancelled, without calling OnStarted,
	// so we can verify the prep-phase report arrives before the agent starts.
	runner := &prepBlockingRunner{started: make(chan struct{})}

	job := &queue.Job{
		ID:          "jprep2",
		Repo:        "o/r",
		SubmittedAt: time.Now(),
		PromptContext: &queue.PromptContext{
			ThreadMessages: []queue.ThreadMessage{{User: "T", Timestamp: "1", Text: "test"}},
			Channel:        "test",
			Reporter:       "tester",
			Goal:           "test goal",
		},
	}

	pool := NewPool(Config{
		Queue:       bundle.Queue,
		Attachments: bundle.Attachments,
		Results:     bundle.Results,
		Store:       store,
		Runner:      runner,
		RepoCache:   &mockRepo{path: "/tmp/r"},
		Commands:    bundle.Commands,
		Status:      statusBus,
		WorkerCount: 1,
		Hostname:    "test-host",
		Logger:      slog.Default(),
	})

	pool.Start(ctx)

	if err := bundle.Queue.Submit(ctx, job); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	// Wait for the runner to enter Run (prep phase has started).
	select {
	case <-runner.started:
	case <-ctx.Done():
		t.Fatal("timeout waiting for runner.started")
	}

	// Wait for the prep-phase StatusReport to land in the bus.
	select {
	case <-reportReady:
	case <-ctx.Done():
		t.Fatal("timeout waiting for first prep StatusReport")
	}

	mu.Lock()
	defer mu.Unlock()

	if len(reports) == 0 {
		t.Fatal("expected at least one StatusReport before OnStarted fires")
	}
	first := reports[0]
	if first.JobID != "jprep2" {
		t.Errorf("first report JobID = %q, want jprep2", first.JobID)
	}
	if first.WorkerID == "" {
		t.Error("first report WorkerID should be set")
	}
	if first.WorkerID != "test-host/worker-0" {
		t.Errorf("first report WorkerID = %q, want test-host/worker-0", first.WorkerID)
	}
	if first.PID != 0 {
		t.Errorf("first report PID = %d, want 0 (prep phase)", first.PID)
	}
	if !first.Alive {
		t.Error("first report Alive should be true")
	}
}

func TestPool_KillOnRunningAgentProducesCancelledResult(t *testing.T) {
	store := queue.NewMemJobStore()
	bundle := queuetest.NewBundle(10, 3, store)
	defer bundle.Close()

	job := &queue.Job{
		ID:          "jrun",
		Repo:        "o/r",
		SubmittedAt: time.Now(),
		PromptContext: &queue.PromptContext{
			ThreadMessages: []queue.ThreadMessage{{User: "T", Timestamp: "1", Text: "test"}},
			Channel:        "test",
			Reporter:       "tester",
			Goal:           "test goal",
		},
	}

	runner := &blockingRunner{started: make(chan struct{})}
	pool := NewPool(Config{
		Queue:       bundle.Queue,
		Attachments: bundle.Attachments,
		Results:     bundle.Results,
		Store:       store,
		Runner:      runner,
		RepoCache:   &mockRepo{path: "/tmp/r"},
		Commands:    bundle.Commands,
		WorkerCount: 1,
		Logger:      slog.Default(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	pool.Start(ctx)

	if err := bundle.Queue.Submit(ctx, job); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	<-runner.started
	// User cancel: mark store then send kill.
	store.UpdateStatus("jrun", queue.JobCancelled)
	bundle.Commands.Send(ctx, queue.Command{JobID: "jrun", Action: "kill"})

	ch, _ := bundle.Results.Subscribe(ctx)
	select {
	case r := <-ch:
		if r.Status != "cancelled" {
			t.Errorf("status = %q, want cancelled", r.Status)
		}
	case <-ctx.Done():
		t.Fatal("no result")
	}
}

// recordingStatusBus captures every Report call for later inspection.
type recordingStatusBus struct {
	mu      sync.Mutex
	reports []queue.StatusReport
}

func (b *recordingStatusBus) Report(_ context.Context, r queue.StatusReport) error {
	b.mu.Lock()
	b.reports = append(b.reports, r)
	b.mu.Unlock()
	return nil
}

func (b *recordingStatusBus) Subscribe(_ context.Context) (<-chan queue.StatusReport, error) {
	ch := make(chan queue.StatusReport)
	close(ch)
	return ch, nil
}

func (b *recordingStatusBus) Close() error { return nil }

func (b *recordingStatusBus) snapshot() []queue.StatusReport {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]queue.StatusReport, len(b.reports))
	copy(out, b.reports)
	return out
}

func TestPool_StatusReportsIncludeJobStatus(t *testing.T) {
	store := queue.NewMemJobStore()
	bundle := queuetest.NewBundle(10, 3, store)
	defer bundle.Close()

	recorder := &recordingStatusBus{}
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
		Status:      recorder,
		Logger:      slog.Default(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pool.Start(ctx)
	bundle.Attachments.Prepare(ctx, "j1", nil)
	bundle.Queue.Submit(ctx, &queue.Job{ID: "j1", Priority: 50, PromptContext: &queue.PromptContext{ThreadMessages: []queue.ThreadMessage{{User: "T", Timestamp: "1", Text: "test"}}, Channel: "test", Reporter: "tester", Goal: "test goal"}})

	resCh, _ := bundle.Results.Subscribe(ctx)
	select {
	case <-resCh:
	case <-ctx.Done():
		t.Fatal("timeout waiting for result")
	}

	// Expect at least one report carrying the worker-side lifecycle state —
	// otherwise the app pod stays wedged at JobPending forever.
	reports := recorder.snapshot()
	seen := map[queue.JobStatus]bool{}
	for _, r := range reports {
		if r.JobStatus != "" {
			seen[r.JobStatus] = true
		}
	}
	if len(seen) == 0 {
		t.Fatalf("no report carried JobStatus; got %d reports", len(reports))
	}
	// The prep-phase report (after Ack, before executeJob) must advertise
	// JobPreparing so the app surfaces 準備中 rather than pending.
	if !seen[queue.JobPreparing] {
		t.Errorf("no report with JobStatus=JobPreparing; seen=%v", seen)
	}
}
