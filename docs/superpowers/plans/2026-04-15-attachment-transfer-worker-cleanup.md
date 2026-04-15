# Attachment Transfer & Worker Cleanup Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Transfer Slack file attachments from App to Worker via Redis, and add aggressive worker cleanup (per-job worktree removal, shutdown cleanup, startup purge).

**Architecture:** App downloads files from Slack, stores raw bytes in Redis. Worker pulls bytes from Redis, writes to local temp dir, appends file paths to prompt. Repos use bare cache + git worktree for per-job isolation — worktrees are cleaned after each job, bare cache on shutdown/startup.

**Tech Stack:** Go, Redis, git worktree

---

## File Map

| File | Role | Action |
|------|------|--------|
| `internal/queue/job.go` | Data model: `AttachmentReady`, `AttachmentPayload` | Modify |
| `internal/queue/interface.go` | `AttachmentStore.Prepare` signature | Modify |
| `internal/queue/redis_attachments.go` | Redis storage with file bytes + size limits | Modify |
| `internal/queue/redis_attachments_test.go` | Tests for bytes storage + limits | Modify |
| `internal/queue/inmem_attachments.go` | In-memory store for dev/test | Modify |
| `internal/bot/prompt.go` | Move attachment section to `AppendAttachmentSection` | Modify |
| `internal/bot/prompt_test.go` | Tests for prompt changes | Modify |
| `internal/bot/workflow.go` | Read file bytes, build payloads, fail on Prepare error | Modify |
| `internal/worker/executor.go` | Write attachments to disk, dedup filenames, append to prompt; expand `RepoProvider` interface | Modify |
| `internal/worker/pool.go` | Worktree cleanup after job; `CleanAll` on shutdown | Modify |
| `internal/worker/pool_test.go` | Update mock to satisfy new `RepoProvider` | Modify |
| `internal/github/repo.go` | Bare clone, `AddWorktree`, `RemoveWorktree`, `CleanAll`, `PurgeStale` | Modify |
| `internal/github/repo_test.go` | Tests for new methods | Modify |
| `cmd/agentdock/adapters.go` | Adapter for new `RepoProvider` methods | Modify |
| `cmd/agentdock/worker.go` | Call `PurgeStale` on startup | Modify |

---

### Task 1: Data Model — `AttachmentPayload` and updated `AttachmentReady`

**Files:**
- Modify: `internal/queue/job.go:43-72`
- Modify: `internal/queue/interface.go:20-24`

- [ ] **Step 1: Update `AttachmentReady` — remove `URL`, add `Data` and `MimeType`**

In `internal/queue/job.go`, replace the current `AttachmentReady` struct:

```go
// Before (lines 69-72):
type AttachmentReady struct {
	Filename string `json:"filename"`
	URL      string `json:"url"`
}

// After:
type AttachmentReady struct {
	Filename string `json:"filename"`
	Data     []byte `json:"data"`
	MimeType string `json:"mime_type"`
}
```

- [ ] **Step 2: Add `AttachmentPayload` struct**

In `internal/queue/job.go`, add after `AttachmentReady`:

```go
// AttachmentPayload carries file bytes from App to AttachmentStore.
type AttachmentPayload struct {
	Filename string
	MimeType string
	Data     []byte
	Size     int64
}
```

- [ ] **Step 3: Update `AttachmentStore.Prepare` signature in interface**

In `internal/queue/interface.go`, change the `Prepare` method:

```go
// Before:
Prepare(ctx context.Context, jobID string, attachments []AttachmentMeta) error

// After:
Prepare(ctx context.Context, jobID string, payloads []AttachmentPayload) error
```

- [ ] **Step 4: Verify compilation fails (expected — callers still use old signature)**

Run: `go build ./...`
Expected: compile errors in `redis_attachments.go`, `inmem_attachments.go`, `workflow.go`, and tests referencing old `Prepare` signature and `AttachmentReady.URL`.

- [ ] **Step 5: Commit**

```bash
git add internal/queue/job.go internal/queue/interface.go
git commit -m "feat: update AttachmentReady/AttachmentPayload data model for file transfer"
```

---

### Task 2: Redis Attachment Store — store and return file bytes with size limits

**Files:**
- Modify: `internal/queue/redis_attachments.go`
- Modify: `internal/queue/redis_attachments_test.go`

- [ ] **Step 1: Write tests for bytes storage, size limits, and cleanup**

Replace the contents of `internal/queue/redis_attachments_test.go`:

```go
package queue

import (
	"context"
	"testing"
	"time"
)

func TestRedisAttachmentStore_PrepareAndResolve(t *testing.T) {
	client := testRedisClient(t)
	store := NewRedisAttachmentStore(client)
	ctx := context.Background()
	jobID := "job-attach-bytes-1"

	payloads := []AttachmentPayload{
		{Filename: "screenshot.png", MimeType: "image", Data: []byte("fake-png-data"), Size: 13},
		{Filename: "error.log", MimeType: "text", Data: []byte("stack trace here"), Size: 16},
	}

	if err := store.Prepare(ctx, jobID, payloads); err != nil {
		t.Fatalf("Prepare failed: %v", err)
	}

	results, err := store.Resolve(ctx, jobID)
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}

	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
	if results[0].Filename != "screenshot.png" {
		t.Errorf("results[0].Filename = %q, want screenshot.png", results[0].Filename)
	}
	if string(results[0].Data) != "fake-png-data" {
		t.Errorf("results[0].Data = %q, want fake-png-data", string(results[0].Data))
	}
	if results[0].MimeType != "image" {
		t.Errorf("results[0].MimeType = %q, want image", results[0].MimeType)
	}
	if results[1].Filename != "error.log" {
		t.Errorf("results[1].Filename = %q, want error.log", results[1].Filename)
	}
	if string(results[1].Data) != "stack trace here" {
		t.Errorf("results[1].Data = %q", string(results[1].Data))
	}
}

func TestRedisAttachmentStore_SkipsOversizedFile(t *testing.T) {
	client := testRedisClient(t)
	store := NewRedisAttachmentStore(client)
	ctx := context.Background()
	jobID := "job-attach-oversize"

	bigData := make([]byte, 11*1024*1024) // 11 MB — exceeds 10 MB limit
	payloads := []AttachmentPayload{
		{Filename: "huge.bin", MimeType: "document", Data: bigData, Size: int64(len(bigData))},
		{Filename: "small.txt", MimeType: "text", Data: []byte("ok"), Size: 2},
	}

	if err := store.Prepare(ctx, jobID, payloads); err != nil {
		t.Fatalf("Prepare failed: %v", err)
	}

	results, err := store.Resolve(ctx, jobID)
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}

	// Only small.txt should be stored; huge.bin skipped.
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1 (oversized file should be skipped)", len(results))
	}
	if results[0].Filename != "small.txt" {
		t.Errorf("results[0].Filename = %q, want small.txt", results[0].Filename)
	}
}

func TestRedisAttachmentStore_SkipsWhenJobTotalExceeded(t *testing.T) {
	client := testRedisClient(t)
	store := NewRedisAttachmentStore(client)
	ctx := context.Background()
	jobID := "job-attach-total-exceeded"

	chunk := make([]byte, 9*1024*1024) // 9 MB each
	payloads := []AttachmentPayload{
		{Filename: "a.bin", MimeType: "document", Data: chunk, Size: int64(len(chunk))},
		{Filename: "b.bin", MimeType: "document", Data: chunk, Size: int64(len(chunk))},
		{Filename: "c.bin", MimeType: "document", Data: chunk, Size: int64(len(chunk))},
		{Filename: "d.bin", MimeType: "document", Data: chunk, Size: int64(len(chunk))}, // pushes over 30 MB
	}

	if err := store.Prepare(ctx, jobID, payloads); err != nil {
		t.Fatalf("Prepare failed: %v", err)
	}

	results, err := store.Resolve(ctx, jobID)
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}

	// 3 × 9 MB = 27 MB OK, 4th would be 36 MB — exceeds 30 MB.
	if len(results) != 3 {
		t.Fatalf("got %d results, want 3", len(results))
	}
}

func TestRedisAttachmentStore_ResolveBeforePrepare(t *testing.T) {
	client := testRedisClient(t)
	store := NewRedisAttachmentStore(client)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	jobID := "job-attach-poll"

	payloads := []AttachmentPayload{
		{Filename: "data.csv", MimeType: "text", Data: []byte("a,b,c"), Size: 5},
	}

	go func() {
		time.Sleep(500 * time.Millisecond)
		if err := store.Prepare(context.Background(), jobID, payloads); err != nil {
			t.Errorf("Prepare in goroutine failed: %v", err)
		}
	}()

	results, err := store.Resolve(ctx, jobID)
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}
	if len(results) != 1 || results[0].Filename != "data.csv" {
		t.Errorf("unexpected results: %+v", results)
	}
}

func TestRedisAttachmentStore_Cleanup(t *testing.T) {
	client := testRedisClient(t)
	store := NewRedisAttachmentStore(client)
	ctx := context.Background()
	jobID := "job-attach-cleanup"

	payloads := []AttachmentPayload{
		{Filename: "report.pdf", MimeType: "document", Data: []byte("pdf-bytes"), Size: 9},
	}
	if err := store.Prepare(ctx, jobID, payloads); err != nil {
		t.Fatalf("Prepare failed: %v", err)
	}
	if err := store.Cleanup(ctx, jobID); err != nil {
		t.Fatalf("Cleanup failed: %v", err)
	}

	key := keyPrefix + "jobs:attachments:" + jobID
	exists, err := client.Exists(ctx, key).Result()
	if err != nil {
		t.Fatalf("Exists check failed: %v", err)
	}
	if exists != 0 {
		t.Errorf("key %q still exists after Cleanup", key)
	}
}

func TestRedisAttachmentStore_EmptyPayloads(t *testing.T) {
	client := testRedisClient(t)
	store := NewRedisAttachmentStore(client)
	ctx := context.Background()
	jobID := "job-attach-empty"

	if err := store.Prepare(ctx, jobID, nil); err != nil {
		t.Fatalf("Prepare with nil failed: %v", err)
	}

	results, err := store.Resolve(ctx, jobID)
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("got %d results, want 0", len(results))
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/queue/ -run TestRedisAttachment -v -count=1`
Expected: compile errors (Prepare signature mismatch).

- [ ] **Step 3: Update `redis_attachments.go` implementation**

Replace `internal/queue/redis_attachments.go`:

```go
package queue

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	attachmentTTL     = 30 * time.Minute
	maxFileSize int64 = 10 * 1024 * 1024 // 10 MB
	maxJobSize  int64 = 30 * 1024 * 1024 // 30 MB
)

type RedisAttachmentStore struct {
	client *redis.Client
}

func NewRedisAttachmentStore(client *redis.Client) *RedisAttachmentStore {
	return &RedisAttachmentStore{client: client}
}

func (s *RedisAttachmentStore) attachmentKey(jobID string) string {
	return keyPrefix + "jobs:attachments:" + jobID
}

func (s *RedisAttachmentStore) Prepare(ctx context.Context, jobID string, payloads []AttachmentPayload) error {
	var result []AttachmentReady
	var totalSize int64

	for _, p := range payloads {
		if p.Size > maxFileSize {
			slog.Warn("附件超過單檔上限，略過",
				"job_id", jobID, "filename", p.Filename, "size", p.Size, "limit", maxFileSize)
			continue
		}
		if totalSize+p.Size > maxJobSize {
			slog.Warn("附件超過工作總量上限，略過剩餘檔案",
				"job_id", jobID, "filename", p.Filename, "total_so_far", totalSize, "limit", maxJobSize)
			break
		}
		totalSize += p.Size
		result = append(result, AttachmentReady{
			Filename: p.Filename,
			Data:     p.Data,
			MimeType: p.MimeType,
		})
	}

	if result == nil {
		result = []AttachmentReady{}
	}

	data, err := json.Marshal(result)
	if err != nil {
		return err
	}
	return s.client.Set(ctx, s.attachmentKey(jobID), data, attachmentTTL).Err()
}

func (s *RedisAttachmentStore) Resolve(ctx context.Context, jobID string) ([]AttachmentReady, error) {
	key := s.attachmentKey(jobID)

	for {
		data, err := s.client.Get(ctx, key).Bytes()
		if err == nil {
			var result []AttachmentReady
			if err := json.Unmarshal(data, &result); err != nil {
				return nil, err
			}
			return result, nil
		}
		if err != redis.Nil {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func (s *RedisAttachmentStore) Cleanup(ctx context.Context, jobID string) error {
	return s.client.Del(ctx, s.attachmentKey(jobID)).Err()
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/queue/ -run TestRedisAttachment -v -count=1`
Expected: all 6 tests PASS (requires Redis running locally).

- [ ] **Step 5: Commit**

```bash
git add internal/queue/redis_attachments.go internal/queue/redis_attachments_test.go
git commit -m "feat: store attachment bytes in Redis with size limits"
```

---

### Task 3: In-memory Attachment Store — mirror changes for dev/test

**Files:**
- Modify: `internal/queue/inmem_attachments.go`

- [ ] **Step 1: Update `inmem_attachments.go`**

Replace `internal/queue/inmem_attachments.go`:

```go
package queue

import (
	"context"
	"sync"
)

type InMemAttachmentStore struct {
	mu    sync.Mutex
	ready map[string]chan []AttachmentReady
}

func NewInMemAttachmentStore() *InMemAttachmentStore {
	return &InMemAttachmentStore{
		ready: make(map[string]chan []AttachmentReady),
	}
}

func (s *InMemAttachmentStore) Prepare(ctx context.Context, jobID string, payloads []AttachmentPayload) error {
	s.mu.Lock()
	ch, ok := s.ready[jobID]
	if !ok {
		ch = make(chan []AttachmentReady, 1)
		s.ready[jobID] = ch
	}
	s.mu.Unlock()
	result := make([]AttachmentReady, len(payloads))
	for i, p := range payloads {
		result[i] = AttachmentReady{Filename: p.Filename, Data: p.Data, MimeType: p.MimeType}
	}
	ch <- result
	return nil
}

func (s *InMemAttachmentStore) Resolve(ctx context.Context, jobID string) ([]AttachmentReady, error) {
	s.mu.Lock()
	ch, ok := s.ready[jobID]
	if !ok {
		ch = make(chan []AttachmentReady, 1)
		s.ready[jobID] = ch
	}
	s.mu.Unlock()
	select {
	case result := <-ch:
		return result, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *InMemAttachmentStore) Cleanup(ctx context.Context, jobID string) error {
	s.mu.Lock()
	delete(s.ready, jobID)
	s.mu.Unlock()
	return nil
}
```

- [ ] **Step 2: Verify compilation**

Run: `go build ./internal/queue/...`
Expected: PASS

- [ ] **Step 3: Commit**

```bash
git add internal/queue/inmem_attachments.go
git commit -m "feat: update in-memory attachment store for payload bytes"
```

---

### Task 4: Prompt — extract `AppendAttachmentSection`, remove from `BuildPrompt`

**Files:**
- Modify: `internal/bot/prompt.go`
- Modify: `internal/bot/prompt_test.go`

- [ ] **Step 1: Write test for `AppendAttachmentSection`**

Add to `internal/bot/prompt_test.go`:

```go
func TestAppendAttachmentSection(t *testing.T) {
	base := "some prompt text"
	attachments := []AttachmentInfo{
		{Path: "/tmp/triage-attach-j1/screenshot.png", Name: "screenshot.png", Type: "image"},
		{Path: "/tmp/triage-attach-j1/error.log", Name: "error.log", Type: "text"},
		{Path: "/tmp/triage-attach-j1/report.pdf", Name: "report.pdf", Type: "document"},
	}
	result := AppendAttachmentSection(base, attachments)

	if !strings.Contains(result, "some prompt text") {
		t.Error("missing base prompt")
	}
	if !strings.Contains(result, "## Attachments") {
		t.Error("missing attachments header")
	}
	if !strings.Contains(result, "screenshot.png (image") {
		t.Error("missing image hint")
	}
	if !strings.Contains(result, "error.log (text") {
		t.Error("missing text hint")
	}
	if !strings.Contains(result, "report.pdf (document)") {
		t.Error("missing document hint")
	}
}

func TestAppendAttachmentSection_Empty(t *testing.T) {
	base := "prompt"
	result := AppendAttachmentSection(base, nil)
	if result != "prompt" {
		t.Errorf("expected unchanged prompt for nil attachments, got %q", result)
	}
}
```

- [ ] **Step 2: Update test `TestBuildPrompt_WithAttachments`**

The existing test passes `Attachments` to `BuildPrompt`. After our change, `BuildPrompt` no longer renders attachments. Update the test to verify attachments are NOT in the output:

```go
func TestBuildPrompt_WithAttachments(t *testing.T) {
	input := PromptInput{
		ThreadMessages: []ThreadMessage{
			{User: "Alice", Timestamp: "10:30", Text: "see screenshot"},
		},
		Attachments: []AttachmentInfo{
			{Path: "/tmp/triage-abc/screenshot.png", Name: "screenshot.png", Type: "image"},
			{Path: "/tmp/triage-abc/error.log", Name: "error.log", Type: "text"},
		},
		Prompt: config.PromptConfig{Language: "en"},
	}
	result := BuildPrompt(input)
	// BuildPrompt no longer renders attachments — worker handles it.
	if strings.Contains(result, "## Attachments") {
		t.Error("BuildPrompt should no longer render attachment section")
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./internal/bot/ -run "TestAppendAttachment|TestBuildPrompt_WithAttachments" -v`
Expected: `TestAppendAttachmentSection` fails (function not defined), `TestBuildPrompt_WithAttachments` fails (still contains attachments).

- [ ] **Step 4: Update `prompt.go` — remove attachment section from `BuildPrompt`, add `AppendAttachmentSection`**

In `internal/bot/prompt.go`, remove lines 58-74 (the `if len(input.Attachments) > 0` block) from `BuildPrompt`.

Add the new function at the bottom of the file:

```go
// AppendAttachmentSection appends an attachment list to a prompt.
// Called by the worker after writing files to its local temp dir.
func AppendAttachmentSection(prompt string, attachments []AttachmentInfo) string {
	if len(attachments) == 0 {
		return prompt
	}
	var sb strings.Builder
	sb.WriteString(prompt)
	sb.WriteString("\n## Attachments\n\n")
	for _, att := range attachments {
		hint := ""
		switch att.Type {
		case "image":
			hint = " (image — use your file reading tools to view)"
		case "text":
			hint = " (text — read directly)"
		case "document":
			hint = " (document)"
		}
		sb.WriteString(fmt.Sprintf("- %s%s\n", att.Path, hint))
	}
	return sb.String()
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/bot/ -run "TestAppendAttachment|TestBuildPrompt" -v`
Expected: all PASS

- [ ] **Step 6: Commit**

```bash
git add internal/bot/prompt.go internal/bot/prompt_test.go
git commit -m "feat: extract AppendAttachmentSection, remove from BuildPrompt"
```

---

### Task 5: Worker Executor — write attachments to disk with filename dedup

**Files:**
- Modify: `internal/worker/executor.go`

- [ ] **Step 1: Update `RepoProvider` interface**

In `internal/worker/executor.go`, expand the interface (lines 22-24):

```go
// Before:
type RepoProvider interface {
	Prepare(cloneURL, branch string) (string, error)
}

// After:
type RepoProvider interface {
	Prepare(cloneURL, branch string) (string, error)
	RemoveWorktree(worktreePath string) error
	CleanAll() error
	PurgeStale() error
}
```

- [ ] **Step 2: Add `writeAttachments` helper function**

Add to `internal/worker/executor.go`:

```go
// writeAttachments writes resolved attachment bytes to a temp dir.
// Returns the list of written files. Deduplicates filenames by appending _2, _3, etc.
func writeAttachments(attachments []queue.AttachmentReady, dir string) ([]bot.AttachmentInfo, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create attachment dir: %w", err)
	}

	seen := make(map[string]int)
	var infos []bot.AttachmentInfo

	for _, att := range attachments {
		filename := att.Filename
		if count, exists := seen[filename]; exists {
			ext := filepath.Ext(filename)
			base := strings.TrimSuffix(filename, ext)
			filename = fmt.Sprintf("%s_%d%s", base, count+1, ext)
		}
		seen[att.Filename]++

		path := filepath.Join(dir, filename)
		if err := os.WriteFile(path, att.Data, 0644); err != nil {
			return nil, fmt.Errorf("write attachment %s: %w", filename, err)
		}
		infos = append(infos, bot.AttachmentInfo{
			Path: path,
			Name: filename,
			Type: att.MimeType,
		})
	}
	return infos, nil
}
```

- [ ] **Step 3: Replace the no-op attachment loop in `executeJob`**

In `executeJob`, replace lines 57-62:

```go
// Before:
// Copy attachments to repo workspace.
for _, att := range attachments {
	if att.URL != "" {
		_ = att // For local file:// URLs, path is already accessible.
	}
}

// After:
// Write attachments to local temp dir and append to prompt.
prompt := job.Prompt
if len(attachments) > 0 {
	attachDir := filepath.Join(os.TempDir(), fmt.Sprintf("triage-attach-%s", job.ID))
	defer os.RemoveAll(attachDir)
	attachInfos, err := writeAttachments(attachments, attachDir)
	if err != nil {
		logger.Warn("附件寫入失敗，繼續執行", "phase", "處理中", "error", err)
	} else {
		prompt = bot.AppendAttachmentSection(prompt, attachInfos)
		logger.Info("附件已寫入", "phase", "處理中", "count", len(attachInfos), "dir", attachDir)
	}
}
```

Also update line 80 where `deps.runner.Run` is called — use `prompt` instead of `job.Prompt`:

```go
// Before:
output, err := deps.runner.Run(ctx, repoPath, job.Prompt, opts)

// After:
output, err := deps.runner.Run(ctx, repoPath, prompt, opts)
```

- [ ] **Step 4: Verify compilation**

Run: `go build ./internal/worker/...`
Expected: compile errors in `pool_test.go` because `mockRepo` doesn't implement new `RepoProvider` methods. That's expected — we fix it in Task 7.

- [ ] **Step 5: Commit**

```bash
git add internal/worker/executor.go
git commit -m "feat: write attachment bytes to disk with filename dedup"
```

---

### Task 6: RepoCache — bare clone + worktree + cleanup methods

**Files:**
- Modify: `internal/github/repo.go`
- Modify: `internal/github/repo_test.go`

- [ ] **Step 1: Write tests for new methods**

Add to `internal/github/repo_test.go`:

```go
func TestRepoCache_BareCloneAndWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}

	// Create a source bare repo with one commit.
	sourceDir := t.TempDir()
	run(t, sourceDir, "git", "init", "--bare")

	workDir := t.TempDir()
	run(t, workDir, "git", "clone", sourceDir, ".")
	os.WriteFile(filepath.Join(workDir, "main.go"), []byte("package main"), 0644)
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "-c", "user.name=test", "-c", "user.email=test@test.com", "commit", "-m", "init")
	run(t, workDir, "git", "push")

	cacheDir := t.TempDir()
	cache := NewRepoCache(cacheDir, time.Hour, "", slog.Default())

	// EnsureRepo should create a bare clone.
	barePath, err := cache.EnsureRepo("file://" + sourceDir)
	if err != nil {
		t.Fatalf("EnsureRepo failed: %v", err)
	}

	// Bare repo should NOT have a working tree file (e.g., main.go at top level).
	if _, err := os.Stat(filepath.Join(barePath, "main.go")); !os.IsNotExist(err) {
		t.Error("bare repo should not have working tree files")
	}

	// AddWorktree should create a working directory.
	wtPath := filepath.Join(t.TempDir(), "wt1")
	if err := cache.AddWorktree(barePath, "", wtPath); err != nil {
		t.Fatalf("AddWorktree failed: %v", err)
	}

	content, err := os.ReadFile(filepath.Join(wtPath, "main.go"))
	if err != nil {
		t.Fatalf("worktree file not found: %v", err)
	}
	if string(content) != "package main" {
		t.Errorf("unexpected content: %s", string(content))
	}

	// RemoveWorktree should delete it.
	if err := cache.RemoveWorktree(wtPath); err != nil {
		t.Fatalf("RemoveWorktree failed: %v", err)
	}
	if _, err := os.Stat(wtPath); !os.IsNotExist(err) {
		t.Error("worktree dir should be deleted")
	}
}

func TestRepoCache_CleanAll(t *testing.T) {
	cacheDir := t.TempDir()
	cache := NewRepoCache(cacheDir, time.Hour, "", slog.Default())

	// Create a dummy file in the cache dir.
	os.WriteFile(filepath.Join(cacheDir, "dummy"), []byte("x"), 0644)

	if err := cache.CleanAll(); err != nil {
		t.Fatalf("CleanAll failed: %v", err)
	}
	if _, err := os.Stat(cacheDir); !os.IsNotExist(err) {
		t.Error("cache dir should be deleted")
	}
}

func TestRepoCache_PurgeStale(t *testing.T) {
	cacheDir := t.TempDir()
	cache := NewRepoCache(cacheDir, time.Hour, "", slog.Default())

	// Create a dummy file in the cache dir.
	os.WriteFile(filepath.Join(cacheDir, "leftover"), []byte("x"), 0644)

	if err := cache.PurgeStale(); err != nil {
		t.Fatalf("PurgeStale failed: %v", err)
	}
	// Dir should exist but be empty.
	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		t.Fatalf("ReadDir failed: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected empty cache dir, got %d entries", len(entries))
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/github/ -run "TestRepoCache_Bare|TestRepoCache_Clean|TestRepoCache_Purge" -v`
Expected: fail (methods don't exist yet).

- [ ] **Step 3: Modify `EnsureRepo` to use bare clone**

In `internal/github/repo.go`, update `EnsureRepo`:

1. Change the `.git` existence check (line 42) — a bare repo has `HEAD` at the root, not `.git/`:

```go
// Before:
if _, err := os.Stat(filepath.Join(localPath, ".git")); os.IsNotExist(err) {

// After:
if _, err := os.Stat(filepath.Join(localPath, "HEAD")); os.IsNotExist(err) {
```

2. Change `git clone` to `git clone --bare` (line 48):

```go
// Before:
cmd := exec.Command("git", "clone", cloneURL, localPath)

// After:
cmd := exec.Command("git", "clone", "--bare", cloneURL, localPath)
```

3. Remove the `pull --ff-only` section (lines 81-85) — bare repos don't have a working tree to pull into. Replace with just updating `lastPull`:

```go
// Before (lines 81-85):
cmd = exec.Command("git", "-C", localPath, "pull", "--ff-only")
if out, err := cmd.CombinedOutput(); err != nil {
	rc.logger.Debug("Git pull fast-forward 失敗（可能在 detached head）", "phase", "處理中", "output", string(out))
}

// After: (remove these lines entirely — fetch --all already updates refs)
```

4. Same change for the retry clone path (line 72):

```go
// Before:
cmd = exec.Command("git", "clone", cloneURL, localPath)

// After:
cmd = exec.Command("git", "clone", "--bare", cloneURL, localPath)
```

- [ ] **Step 4: Add `AddWorktree`, `RemoveWorktree`, `CleanAll`, `PurgeStale` methods**

Add to `internal/github/repo.go`:

```go
// AddWorktree creates an isolated working directory from a bare cache.
// If branch is empty, checks out the default branch (HEAD).
func (rc *RepoCache) AddWorktree(barePath, branch, worktreePath string) error {
	var cmd *exec.Cmd
	if branch == "" {
		cmd = exec.Command("git", "-C", barePath, "worktree", "add", worktreePath, "HEAD")
	} else {
		cmd = exec.Command("git", "-C", barePath, "worktree", "add", worktreePath, "origin/"+branch)
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git worktree add: %w\n%s", err, out)
	}
	return nil
}

// RemoveWorktree removes a worktree directory.
func (rc *RepoCache) RemoveWorktree(worktreePath string) error {
	// Try git worktree remove first (cleans up .git/worktrees entry).
	cmd := exec.Command("git", "worktree", "remove", "--force", worktreePath)
	if err := cmd.Run(); err != nil {
		// Fallback: just delete the directory. The bare repo's worktree list
		// will have a stale entry, but git worktree prune (or next PurgeStale) fixes it.
		return os.RemoveAll(worktreePath)
	}
	return nil
}

// CleanAll removes the entire cache directory (bare repos + any leftover worktrees).
func (rc *RepoCache) CleanAll() error {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	rc.lastPull = make(map[string]time.Time)
	return os.RemoveAll(rc.dir)
}

// PurgeStale wipes and recreates the cache directory.
// Call on startup to recover from previous unclean shutdown.
func (rc *RepoCache) PurgeStale() error {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	rc.lastPull = make(map[string]time.Time)
	os.RemoveAll(rc.dir)
	return os.MkdirAll(rc.dir, 0755)
}
```

- [ ] **Step 5: Update existing tests**

The existing `TestRepoCache_EnsureRepo_ClonesNewRepo` and `TestRepoCache_EnsureRepo_PullsExistingRepo` now fail because bare repos don't have working tree files. Update them to verify bare repo structure and use worktrees to check content:

In `TestRepoCache_EnsureRepo_ClonesNewRepo`, change the content check:

```go
// Before:
content, err := os.ReadFile(filepath.Join(repoPath, "main.go"))

// After:
// Bare repo — verify HEAD exists, not working files.
if _, err := os.Stat(filepath.Join(repoPath, "HEAD")); err != nil {
	t.Fatalf("bare repo missing HEAD: %v", err)
}
```

In `TestRepoCache_EnsureRepo_PullsExistingRepo`, the pull verification needs a worktree. Simplify to just verify the second EnsureRepo returns the same path (cache hit):

```go
// Remove the content check at the end (line 80-83). Replace with:
// Bare repo re-fetched — just verify same path returned.
if repoPath != repoPath2 {
	t.Error("expected same path for cached repo")
}
```

- [ ] **Step 6: Run all repo tests**

Run: `go test ./internal/github/ -v`
Expected: all PASS

- [ ] **Step 7: Commit**

```bash
git add internal/github/repo.go internal/github/repo_test.go
git commit -m "feat: switch RepoCache to bare clone with worktree support and cleanup methods"
```

---

### Task 7: Worker Pool — worktree cleanup after job + shutdown cleanup

**Files:**
- Modify: `internal/worker/pool.go`
- Modify: `internal/worker/pool_test.go`

- [ ] **Step 1: Update `mockRepo` in tests to satisfy new `RepoProvider`**

In `internal/worker/pool_test.go`, update the mock:

```go
// Before:
type mockRepo struct {
	path string
	err  error
}

func (m *mockRepo) Prepare(cloneURL, branch string) (string, error) {
	return m.path, m.err
}

// After:
type mockRepo struct {
	path             string
	err              error
	removedWorktrees []string
	cleanAllCalled   bool
	purgedStale      bool
}

func (m *mockRepo) Prepare(cloneURL, branch string) (string, error) {
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
```

- [ ] **Step 2: Replace post-kill cleanup with `RemoveWorktree` in `pool.go`**

In `internal/worker/pool.go`, the `executeWithTracking` function currently has this post-kill cleanup block (lines 185-191):

```go
// Before:
// Post-kill cleanup.
if result.Status == "failed" {
	if repoPath, err := p.cfg.RepoCache.Prepare(job.CloneURL, job.Branch); err == nil {
		exec.Command("git", "-C", repoPath, "checkout", ".").Run()
		exec.Command("git", "-C", repoPath, "clean", "-fd").Run()
	}
}

// After:
// Clean up this job's worktree.
// executeJob returns repoPath implicitly — we need to get it.
// But executeJob doesn't expose repoPath. We need to refactor slightly.
```

The issue: `executeJob` currently uses `repoPath` internally but doesn't expose it. We need to return it. Change `executeJob` return type to include `repoPath`:

In `executor.go`, add a wrapper return struct or simply pass repoPath back through the result. The simplest approach: add `RepoPath` field to `JobResult`.

Actually, simpler: store the repoPath in a variable captured by closure. In `executeWithTracking`, the cleanest approach is to have the pool track the repoPath. Let's add it to `JobResult`:

In `internal/queue/job.go`, add to `JobResult`:

```go
type JobResult struct {
	// ... existing fields ...
	RepoPath string `json:"-"` // local only, not serialized
}
```

In `executor.go`, set it before returning:

```go
// In the success return (line 98):
return &queue.JobResult{
	// ... existing fields ...
	RepoPath: repoPath,
}
```

Also in `failedResult`, accept and pass repoPath:

```go
func failedResult(job *queue.Job, startedAt time.Time, err error, repoPath string) *queue.JobResult {
	return &queue.JobResult{
		// ... existing fields ...
		RepoPath: repoPath,
	}
}
```

Update all `failedResult` calls in `executeJob` to pass `repoPath` (empty string for early failures before repo is ready, actual path after).

Then in `pool.go`, replace the post-kill block:

```go
// After (replaces lines 185-191):
if result.RepoPath != "" {
	if err := p.cfg.RepoCache.RemoveWorktree(result.RepoPath); err != nil {
		logger.Warn("Worktree 清理失敗", "phase", "失敗", "path", result.RepoPath, "error", err)
	}
}
```

Also remove the `"os/exec"` import from `pool.go` since we no longer call `exec.Command` there.

- [ ] **Step 3: Add `CleanAll` to shutdown in `workerHeartbeat`**

In `pool.go`, in the `ctx.Done()` case of `workerHeartbeat` (lines 227-234), add after the unregister loop:

```go
case <-ctx.Done():
	// Best-effort unregister with short timeout; TTL expires in 30s anyway.
	unregCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	for i := 0; i < p.cfg.WorkerCount; i++ {
		wID := fmt.Sprintf("%s/worker-%d", p.cfg.Hostname, i)
		p.cfg.Queue.Unregister(unregCtx, wID)
	}
	cancel()

	// Clean up all cached repos and worktrees.
	if err := p.cfg.RepoCache.CleanAll(); err != nil {
		p.cfg.Logger.Warn("關機時 repo 清理失敗", "phase", "失敗", "error", err)
	}
	return
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/worker/ -v`
Expected: all PASS

- [ ] **Step 5: Commit**

```bash
git add internal/queue/job.go internal/worker/executor.go internal/worker/pool.go internal/worker/pool_test.go
git commit -m "feat: worktree cleanup after job completion and on shutdown"
```

---

### Task 8: Adapter + Worker Startup — wire everything together

**Files:**
- Modify: `cmd/agentdock/adapters.go`
- Modify: `cmd/agentdock/worker.go`

- [ ] **Step 1: Update `repoCacheAdapter` in `adapters.go`**

```go
// repoCacheAdapter wraps RepoCache to satisfy worker.RepoProvider interface.
type repoCacheAdapter struct {
	cache *ghclient.RepoCache
}

func (a *repoCacheAdapter) Prepare(cloneURL, branch string) (string, error) {
	barePath, err := a.cache.EnsureRepo(cloneURL)
	if err != nil {
		return "", err
	}
	worktreePath, err := os.MkdirTemp("", "triage-repo-*")
	if err != nil {
		return "", fmt.Errorf("create worktree temp dir: %w", err)
	}
	// MkdirTemp creates the dir; worktree add needs it to not exist.
	os.Remove(worktreePath)
	if err := a.cache.AddWorktree(barePath, branch, worktreePath); err != nil {
		return "", err
	}
	return worktreePath, nil
}

func (a *repoCacheAdapter) RemoveWorktree(path string) error {
	return a.cache.RemoveWorktree(path)
}

func (a *repoCacheAdapter) CleanAll() error {
	return a.cache.CleanAll()
}

func (a *repoCacheAdapter) PurgeStale() error {
	return a.cache.PurgeStale()
}
```

Add `"os"` and `"fmt"` to imports if not already present.

- [ ] **Step 2: Add `PurgeStale` call to `worker.go` startup**

In `cmd/agentdock/worker.go`, after creating `repoCache` (line 62) and before creating the pool (line 80), add:

```go
// Purge stale repos from previous unclean shutdown.
repoAdapter := &repoCacheAdapter{cache: repoCache}
if err := repoAdapter.PurgeStale(); err != nil {
	appLogger.Warn("啟動時清理舊 repo 快取失敗", "phase", "處理中", "error", err)
}
```

Then use `repoAdapter` in the Pool config instead of creating a new one:

```go
// Before:
RepoCache: &repoCacheAdapter{cache: repoCache},

// After:
RepoCache: repoAdapter,
```

- [ ] **Step 3: Verify compilation**

Run: `go build ./cmd/agentdock/...`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add cmd/agentdock/adapters.go cmd/agentdock/worker.go
git commit -m "feat: wire bare repo + worktree adapter and startup purge"
```

---

### Task 9: App Side — read file bytes, build payloads, fail on Prepare error

**Files:**
- Modify: `internal/bot/workflow.go:373-445`

- [ ] **Step 1: Update `runTriage` — build payloads with file bytes and remove attachment paths from prompt**

In `internal/bot/workflow.go`, replace the attachment handling section (steps 3-5, approximately lines 373-413):

```go
	// 3. Download attachments and read bytes.
	tempDir, err := os.MkdirTemp("", "triage-meta-*")
	if err != nil {
		w.notifyError(pt.Logger, pt.ChannelID, pt.ThreadTS, "Failed to create temp dir: %v", err)
		w.clearDedup(pt)
		return
	}
	defer os.RemoveAll(tempDir)

	downloads := w.slack.DownloadAttachments(rawMsgs, tempDir)
	var payloads []queue.AttachmentPayload
	for _, d := range downloads {
		if d.Failed {
			continue
		}
		data, err := os.ReadFile(d.Path)
		if err != nil {
			pt.Logger.Warn("讀取附件失敗", "phase", "處理中", "filename", d.Name, "error", err)
			continue
		}
		payloads = append(payloads, queue.AttachmentPayload{
			Filename: d.Name,
			MimeType: d.Type,
			Data:     data,
			Size:     int64(len(data)),
		})
	}

	// 4. Build prompt (without attachments — worker adds them).
	prompt := BuildPrompt(PromptInput{
		ThreadMessages:   threadMsgs,
		ExtraDescription: pt.ExtraDesc,
		Branch:           pt.SelectedBranch,
		Channel:          pt.ChannelName,
		Reporter:         pt.Reporter,
		Prompt:           w.cfg.Prompt,
	})
	pt.Logger.Info("Prompt 已組裝", "phase", "處理中", "length", len(prompt))

	// 5. Build attachment metadata for queue (still needed for job.Attachments count).
	var attachMeta []queue.AttachmentMeta
	for _, p := range payloads {
		attachMeta = append(attachMeta, queue.AttachmentMeta{
			Filename: p.Filename,
			MimeType: p.MimeType,
			Size:     p.Size,
		})
	}
```

- [ ] **Step 2: Add error handling for `Prepare` failure**

After the existing `Prepare` call (around line 443-445), add error handling:

```go
	// Signal attachment readiness so workers can proceed.
	if len(payloads) > 0 {
		if err := w.attachments.Prepare(ctx, job.ID, payloads); err != nil {
			pt.Logger.Error("附件上傳至 Redis 失敗", "phase", "失敗", "error", err)
			w.store.UpdateStatus(job.ID, queue.JobFailed)
			w.queue.Submit(ctx, &queue.Job{}) // noop — job already submitted
			w.results.Publish(ctx, &queue.JobResult{
				JobID:  job.ID,
				Status: "failed",
				Error:  fmt.Sprintf("attachment prepare failed: %v", err),
			})
			w.clearDedup(pt)
			return
		}
	}
```

Wait — the job is already submitted. We can't un-submit it. The right approach: publish a failed result so the worker (if it picks up the job) sees it's already failed, and the ResultListener posts the error to Slack.

Simplify to:

```go
	// Signal attachment readiness so workers can proceed.
	if len(payloads) > 0 {
		if err := w.attachments.Prepare(ctx, job.ID, payloads); err != nil {
			pt.Logger.Error("附件上傳至 Redis 失敗", "phase", "失敗", "error", err)
			w.store.UpdateStatus(job.ID, queue.JobFailed)
			w.results.Publish(ctx, &queue.JobResult{
				JobID:      job.ID,
				Status:     "failed",
				Error:      fmt.Sprintf("attachment prepare failed: %v", err),
				StartedAt:  time.Now(),
				FinishedAt: time.Now(),
			})
			return
		}
	}
```

- [ ] **Step 3: Verify compilation**

Run: `go build ./internal/bot/...`
Expected: PASS

- [ ] **Step 4: Run all tests**

Run: `go test ./...`
Expected: PASS (some Redis tests may skip if no Redis)

- [ ] **Step 5: Commit**

```bash
git add internal/bot/workflow.go
git commit -m "feat: read attachment bytes and fail job on Prepare error"
```

---

### Task 10: Final integration verification

- [ ] **Step 1: Run full test suite**

Run: `go test ./... -count=1`
Expected: all PASS

- [ ] **Step 2: Build both binaries**

Run: `go build ./cmd/agentdock/...`
Expected: PASS

- [ ] **Step 3: Verify no references to removed `AttachmentReady.URL`**

Run: `grep -r "\.URL" internal/queue/ internal/worker/ internal/bot/ cmd/agentdock/ --include="*.go" | grep -i attachment`
Expected: no matches (or only unrelated URL references)

- [ ] **Step 4: Commit any fixups**

If any issues found, fix and commit.

- [ ] **Step 5: Final commit — update existing test fixtures**

Check if any integration tests in `internal/queue/` reference the old `Prepare(ctx, jobID, []AttachmentMeta{})` signature and update them:

Run: `grep -rn "AttachmentMeta{" internal/queue/ --include="*_test.go"`

Update any remaining callers to use `[]AttachmentPayload{}` or `nil`.

```bash
git add -u
git commit -m "fix: update remaining test fixtures for new attachment API"
```
