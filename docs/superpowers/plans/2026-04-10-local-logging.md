# Local Logging Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add dual-output logging (stderr text + file JSONL) with daily rotation, agent output isolation, and per-request correlation.

**Architecture:** Custom `slog.Handler` that fans out to TextHandler (stderr) + JSONHandler (rotating file). Each triage workflow gets a scoped logger with `request_id` and context attrs. Agent output stored as separate `.md` files.

**Tech Stack:** Go stdlib `log/slog`, no external dependencies.

---

## File Structure

| File | Responsibility |
|------|---------------|
| `internal/logging/rotator.go` | Date-based rotating `io.Writer` + cleanup goroutine |
| `internal/logging/rotator_test.go` | Tests for rotation + cleanup |
| `internal/logging/handler.go` | `MultiHandler` — fan-out slog.Handler (stderr + file) |
| `internal/logging/handler_test.go` | Tests for multi-handler |
| `internal/logging/agent.go` | Save agent output to isolated files |
| `internal/logging/agent_test.go` | Tests for agent output writer |
| `internal/logging/request_id.go` | Request ID generation |
| `internal/logging/request_id_test.go` | Tests for request ID |
| `internal/config/config.go` | Add `LoggingConfig` struct + defaults (modify) |
| `internal/config/config_test.go` | Test logging config defaults (modify) |
| `internal/bot/workflow.go` | Add scoped logger to pendingTriage, wire agent output saving (modify) |
| `internal/bot/agent.go` | Accept `*slog.Logger` param (modify) |
| `internal/bot/agent_test.go` | Update tests for new logger param (modify) |
| `cmd/bot/main.go` | Init MultiHandler (modify) |

---

### Task 1: LoggingConfig in config.go

**Files:**
- Modify: `internal/config/config.go:11-29` (Config struct), `internal/config/config.go:125-143` (applyDefaults)
- Modify: `internal/config/config_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/config/config_test.go`:

```go
func TestLoggingConfigDefaults(t *testing.T) {
	cfg := writeAndLoad(t, `
slack:
  bot_token: "xoxb-test"
  app_token: "xapp-test"
`)
	if cfg.Logging.Dir != "logs" {
		t.Errorf("Logging.Dir = %q, want %q", cfg.Logging.Dir, "logs")
	}
	if cfg.Logging.Level != "debug" {
		t.Errorf("Logging.Level = %q, want %q", cfg.Logging.Level, "debug")
	}
	if cfg.Logging.RetentionDays != 30 {
		t.Errorf("Logging.RetentionDays = %d, want 30", cfg.Logging.RetentionDays)
	}
	if cfg.Logging.AgentOutputDir != "logs/agent-outputs" {
		t.Errorf("Logging.AgentOutputDir = %q, want %q", cfg.Logging.AgentOutputDir, "logs/agent-outputs")
	}
}
```

Note: check if `writeAndLoad` helper exists in `config_test.go`. If not, you'll need a helper that writes YAML to a temp file and calls `config.Load()`.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/ivantseng/local_file/slack-issue-bot && go test ./internal/config/ -run TestLoggingConfigDefaults -v`
Expected: FAIL — `cfg.Logging` field does not exist.

- [ ] **Step 3: Implement LoggingConfig**

In `internal/config/config.go`, add the struct and field:

```go
type LoggingConfig struct {
	Dir            string `yaml:"dir"`
	Level          string `yaml:"level"`
	RetentionDays  int    `yaml:"retention_days"`
	AgentOutputDir string `yaml:"agent_output_dir"`
}
```

Add to `Config` struct:

```go
Logging           LoggingConfig            `yaml:"logging"`
```

Add defaults in `applyDefaults`:

```go
if cfg.Logging.Dir == "" {
	cfg.Logging.Dir = "logs"
}
if cfg.Logging.Level == "" {
	cfg.Logging.Level = "debug"
}
if cfg.Logging.RetentionDays <= 0 {
	cfg.Logging.RetentionDays = 30
}
if cfg.Logging.AgentOutputDir == "" {
	cfg.Logging.AgentOutputDir = "logs/agent-outputs"
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /Users/ivantseng/local_file/slack-issue-bot && go test ./internal/config/ -run TestLoggingConfigDefaults -v`
Expected: PASS

- [ ] **Step 5: Run all config tests**

Run: `cd /Users/ivantseng/local_file/slack-issue-bot && go test ./internal/config/ -v`
Expected: All PASS

- [ ] **Step 6: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat: add LoggingConfig to config"
```

---

### Task 2: Date-based rotating writer

**Files:**
- Create: `internal/logging/rotator.go`
- Create: `internal/logging/rotator_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/logging/rotator_test.go`:

```go
package logging

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRotator_WritesToDateFile(t *testing.T) {
	dir := t.TempDir()
	r, err := NewRotator(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	_, err = r.Write([]byte("hello\n"))
	if err != nil {
		t.Fatal(err)
	}

	expected := filepath.Join(dir, time.Now().Format("2006-01-02")+".jsonl")
	data, err := os.ReadFile(expected)
	if err != nil {
		t.Fatalf("expected file %s: %v", expected, err)
	}
	if string(data) != "hello\n" {
		t.Errorf("file content = %q, want %q", string(data), "hello\n")
	}
}

func TestRotator_MultipleWrites(t *testing.T) {
	dir := t.TempDir()
	r, err := NewRotator(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	r.Write([]byte("line1\n"))
	r.Write([]byte("line2\n"))

	expected := filepath.Join(dir, time.Now().Format("2006-01-02")+".jsonl")
	data, _ := os.ReadFile(expected)
	if string(data) != "line1\nline2\n" {
		t.Errorf("file content = %q", string(data))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/ivantseng/local_file/slack-issue-bot && go test ./internal/logging/ -run TestRotator -v`
Expected: FAIL — package does not exist.

- [ ] **Step 3: Implement Rotator**

Create `internal/logging/rotator.go`:

```go
package logging

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Rotator is an io.Writer that writes to daily-rotated JSONL files.
type Rotator struct {
	mu      sync.Mutex
	dir     string
	current *os.File
	curDate string
}

// NewRotator creates a Rotator that writes to dir/YYYY-MM-DD.jsonl.
func NewRotator(dir string) (*Rotator, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create log dir: %w", err)
	}
	r := &Rotator{dir: dir}
	if err := r.rotate(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *Rotator) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	today := time.Now().Format("2006-01-02")
	if today != r.curDate {
		if err := r.rotateLocked(); err != nil {
			return 0, err
		}
	}
	return r.current.Write(p)
}

func (r *Rotator) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.current != nil {
		return r.current.Close()
	}
	return nil
}

func (r *Rotator) rotate() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.rotateLocked()
}

func (r *Rotator) rotateLocked() error {
	if r.current != nil {
		r.current.Close()
	}
	today := time.Now().Format("2006-01-02")
	path := filepath.Join(r.dir, today+".jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("open log file: %w", err)
	}
	r.current = f
	r.curDate = today
	return nil
}

// Cleanup deletes .jsonl files older than retentionDays. Safe to call while
// writing — it only targets files from previous days.
func (r *Rotator) Cleanup(retentionDays int) {
	cutoff := time.Now().AddDate(0, 0, -retentionDays)
	today := time.Now().Format("2006-01-02")

	entries, err := os.ReadDir(r.dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		dateStr := strings.TrimSuffix(name, ".jsonl")
		if dateStr == today {
			continue
		}
		t, err := time.Parse("2006-01-02", dateStr)
		if err != nil {
			continue
		}
		if t.Before(cutoff) {
			os.Remove(filepath.Join(r.dir, name))
		}
	}
}

// StartCleanup runs Cleanup every hour in a background goroutine.
func (r *Rotator) StartCleanup(retentionDays int) {
	go func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			r.Cleanup(retentionDays)
		}
	}()
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /Users/ivantseng/local_file/slack-issue-bot && go test ./internal/logging/ -run TestRotator -v`
Expected: PASS

- [ ] **Step 5: Write cleanup test**

Add to `internal/logging/rotator_test.go`:

```go
func TestRotator_Cleanup(t *testing.T) {
	dir := t.TempDir()
	r, _ := NewRotator(dir)
	defer r.Close()

	// Create fake old log files.
	oldDate := time.Now().AddDate(0, 0, -31).Format("2006-01-02")
	recentDate := time.Now().AddDate(0, 0, -5).Format("2006-01-02")
	os.WriteFile(filepath.Join(dir, oldDate+".jsonl"), []byte("old"), 0644)
	os.WriteFile(filepath.Join(dir, recentDate+".jsonl"), []byte("recent"), 0644)

	r.Cleanup(30)

	if _, err := os.Stat(filepath.Join(dir, oldDate+".jsonl")); !os.IsNotExist(err) {
		t.Error("old file should have been deleted")
	}
	if _, err := os.Stat(filepath.Join(dir, recentDate+".jsonl")); err != nil {
		t.Error("recent file should still exist")
	}
}
```

- [ ] **Step 6: Run test to verify it passes**

Run: `cd /Users/ivantseng/local_file/slack-issue-bot && go test ./internal/logging/ -run TestRotator_Cleanup -v`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add internal/logging/rotator.go internal/logging/rotator_test.go
git commit -m "feat: add date-based rotating log writer"
```

---

### Task 3: MultiHandler

**Files:**
- Create: `internal/logging/handler.go`
- Create: `internal/logging/handler_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/logging/handler_test.go`:

```go
package logging

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func TestMultiHandler_WritesToBoth(t *testing.T) {
	var stderrBuf, fileBuf bytes.Buffer
	h := NewMultiHandler(
		slog.NewTextHandler(&stderrBuf, &slog.HandlerOptions{Level: slog.LevelInfo}),
		slog.NewJSONHandler(&fileBuf, &slog.HandlerOptions{Level: slog.LevelDebug}),
	)
	logger := slog.New(h)

	logger.Info("test message", "key", "value")

	if !strings.Contains(stderrBuf.String(), "test message") {
		t.Error("stderr missing log entry")
	}

	var entry map[string]any
	if err := json.Unmarshal(fileBuf.Bytes(), &entry); err != nil {
		t.Fatalf("file output not valid JSON: %v", err)
	}
	if entry["msg"] != "test message" {
		t.Errorf("file msg = %v, want %q", entry["msg"], "test message")
	}
}

func TestMultiHandler_IndependentLevels(t *testing.T) {
	var stderrBuf, fileBuf bytes.Buffer
	h := NewMultiHandler(
		slog.NewTextHandler(&stderrBuf, &slog.HandlerOptions{Level: slog.LevelWarn}),
		slog.NewJSONHandler(&fileBuf, &slog.HandlerOptions{Level: slog.LevelDebug}),
	)
	logger := slog.New(h)

	logger.Info("info only")

	if stderrBuf.Len() > 0 {
		t.Error("stderr should not have INFO when level is WARN")
	}
	if !strings.Contains(fileBuf.String(), "info only") {
		t.Error("file should have INFO when level is DEBUG")
	}
}

func TestMultiHandler_WithAttrs(t *testing.T) {
	var fileBuf bytes.Buffer
	h := NewMultiHandler(
		slog.NewTextHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelInfo}),
		slog.NewJSONHandler(&fileBuf, &slog.HandlerOptions{Level: slog.LevelDebug}),
	)
	logger := slog.New(h).With("request_id", "abc123")

	logger.Info("with attr")

	var entry map[string]any
	json.Unmarshal(fileBuf.Bytes(), &entry)
	if entry["request_id"] != "abc123" {
		t.Errorf("request_id = %v, want %q", entry["request_id"], "abc123")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/ivantseng/local_file/slack-issue-bot && go test ./internal/logging/ -run TestMultiHandler -v`
Expected: FAIL — `NewMultiHandler` not defined.

- [ ] **Step 3: Implement MultiHandler**

Create `internal/logging/handler.go`:

```go
package logging

import (
	"context"
	"log/slog"
)

// MultiHandler fans out log records to multiple slog.Handlers.
// Each inner handler applies its own level filter independently.
type MultiHandler struct {
	handlers []slog.Handler
}

// NewMultiHandler creates a handler that writes to all provided handlers.
func NewMultiHandler(handlers ...slog.Handler) *MultiHandler {
	return &MultiHandler{handlers: handlers}
}

func (m *MultiHandler) Enabled(_ context.Context, level slog.Level) bool {
	for _, h := range m.handlers {
		if h.Enabled(context.Background(), level) {
			return true
		}
	}
	return false
}

func (m *MultiHandler) Handle(ctx context.Context, r slog.Record) error {
	for _, h := range m.handlers {
		if h.Enabled(ctx, r.Level) {
			if err := h.Handle(ctx, r.Clone()); err != nil {
				return err
			}
		}
	}
	return nil
}

func (m *MultiHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	handlers := make([]slog.Handler, len(m.handlers))
	for i, h := range m.handlers {
		handlers[i] = h.WithAttrs(attrs)
	}
	return &MultiHandler{handlers: handlers}
}

func (m *MultiHandler) WithGroup(name string) slog.Handler {
	handlers := make([]slog.Handler, len(m.handlers))
	for i, h := range m.handlers {
		handlers[i] = h.WithGroup(name)
	}
	return &MultiHandler{handlers: handlers}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /Users/ivantseng/local_file/slack-issue-bot && go test ./internal/logging/ -run TestMultiHandler -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/logging/handler.go internal/logging/handler_test.go
git commit -m "feat: add MultiHandler slog fan-out"
```

---

### Task 4: Request ID generation

**Files:**
- Create: `internal/logging/request_id.go`
- Create: `internal/logging/request_id_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/logging/request_id_test.go`:

```go
package logging

import (
	"regexp"
	"testing"
)

func TestNewRequestID_Format(t *testing.T) {
	id := NewRequestID()
	// Expected: YYYYMMDD-HHmmss-xxxx
	pattern := `^\d{8}-\d{6}-[0-9a-f]{4}$`
	matched, _ := regexp.MatchString(pattern, id)
	if !matched {
		t.Errorf("request ID %q does not match pattern %s", id, pattern)
	}
}

func TestNewRequestID_Unique(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		id := NewRequestID()
		if seen[id] {
			t.Fatalf("duplicate ID: %s", id)
		}
		seen[id] = true
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/ivantseng/local_file/slack-issue-bot && go test ./internal/logging/ -run TestNewRequestID -v`
Expected: FAIL — `NewRequestID` not defined.

- [ ] **Step 3: Implement NewRequestID**

Create `internal/logging/request_id.go`:

```go
package logging

import (
	"crypto/rand"
	"fmt"
	"time"
)

// NewRequestID generates a short, time-based request ID: YYYYMMDD-HHmmss-xxxx.
func NewRequestID() string {
	b := make([]byte, 2)
	rand.Read(b)
	return fmt.Sprintf("%s-%04x", time.Now().Format("20060102-150405"), b)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /Users/ivantseng/local_file/slack-issue-bot && go test ./internal/logging/ -run TestNewRequestID -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/logging/request_id.go internal/logging/request_id_test.go
git commit -m "feat: add request ID generation"
```

---

### Task 5: Agent output file writer

**Files:**
- Create: `internal/logging/agent.go`
- Create: `internal/logging/agent_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/logging/agent_test.go`:

```go
package logging

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSaveAgentOutput(t *testing.T) {
	dir := t.TempDir()
	path, err := SaveAgentOutput(dir, "20260410-143052-a3f8", "org/backend", "## Issue\n\nSome content")
	if err != nil {
		t.Fatal(err)
	}

	if !strings.HasSuffix(path, "20260410-143052-a3f8.md") {
		t.Errorf("path = %q, want suffix 20260410-143052-a3f8.md", path)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "## Issue\n\nSome content" {
		t.Errorf("content = %q", string(data))
	}
}

func TestSaveAgentOutput_CreatesDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "dir")
	_, err := SaveAgentOutput(dir, "test-id", "org/repo", "content")
	if err != nil {
		t.Fatalf("should create nested dirs: %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/ivantseng/local_file/slack-issue-bot && go test ./internal/logging/ -run TestSaveAgentOutput -v`
Expected: FAIL — `SaveAgentOutput` not defined.

- [ ] **Step 3: Implement SaveAgentOutput**

Create `internal/logging/agent.go`:

```go
package logging

import (
	"fmt"
	"os"
	"path/filepath"
)

// SaveAgentOutput writes agent raw output to a separate .md file.
// Returns the file path for logging reference.
func SaveAgentOutput(dir, requestID, repo, output string) (string, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("create agent output dir: %w", err)
	}
	filename := requestID + ".md"
	path := filepath.Join(dir, filename)
	if err := os.WriteFile(path, []byte(output), 0644); err != nil {
		return "", fmt.Errorf("write agent output: %w", err)
	}
	return path, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /Users/ivantseng/local_file/slack-issue-bot && go test ./internal/logging/ -run TestSaveAgentOutput -v`
Expected: PASS

- [ ] **Step 5: Run all logging tests**

Run: `cd /Users/ivantseng/local_file/slack-issue-bot && go test ./internal/logging/ -v`
Expected: All PASS

- [ ] **Step 6: Commit**

```bash
git add internal/logging/agent.go internal/logging/agent_test.go
git commit -m "feat: add agent output file writer"
```

---

### Task 6: Wire MultiHandler in main.go

**Files:**
- Modify: `cmd/bot/main.go:28-38`

- [ ] **Step 1: Update main.go initialization**

Replace the second `slog.SetDefault` block (line 38) in `cmd/bot/main.go`. The first `slog.SetDefault` (line 29, pre-config) stays unchanged.

After `cfg` is loaded (after line 35), replace line 38:

```go
// Old:
// slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: parseLogLevel(cfg.LogLevel)})))

// New:
stderrHandler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: parseLogLevel(cfg.LogLevel)})

rotator, err := logging.NewRotator(cfg.Logging.Dir)
if err != nil {
	slog.Error("failed to init log rotator", "error", err)
	os.Exit(1)
}
rotator.StartCleanup(cfg.Logging.RetentionDays)

fileHandler := slog.NewJSONHandler(rotator, &slog.HandlerOptions{Level: parseLogLevel(cfg.Logging.Level)})
slog.SetDefault(slog.New(logging.NewMultiHandler(stderrHandler, fileHandler)))
```

Add import `"slack-issue-bot/internal/logging"` to the import block.

- [ ] **Step 2: Verify it compiles**

Run: `cd /Users/ivantseng/local_file/slack-issue-bot && go build ./cmd/bot/`
Expected: Success, no errors.

- [ ] **Step 3: Run all tests**

Run: `cd /Users/ivantseng/local_file/slack-issue-bot && go test ./...`
Expected: All PASS

- [ ] **Step 4: Commit**

```bash
git add cmd/bot/main.go
git commit -m "feat: wire MultiHandler with file rotation in main"
```

---

### Task 7: Add scoped logger to workflow + save agent output

**Files:**
- Modify: `internal/bot/workflow.go:19-32` (pendingTriage struct), `internal/bot/workflow.go:83-156` (HandleTrigger), `internal/bot/workflow.go:306-452` (runTriage)
- Modify: `internal/bot/agent.go:44` (Run signature)
- Modify: `internal/bot/agent_test.go`

- [ ] **Step 1: Add fields to pendingTriage**

In `internal/bot/workflow.go`, add to the `pendingTriage` struct (line 20-32):

```go
type pendingTriage struct {
	ChannelID      string
	ThreadTS       string
	TriggerTS      string
	Attachments    []string
	SelectedRepo   string
	SelectedBranch string
	Phase          string
	SelectorTS     string
	Reporter       string
	ChannelName    string
	ExtraDesc      string
	CmdArgs        string
	RequestID      string
	Logger         *slog.Logger
}
```

Add import `"slack-issue-bot/internal/logging"` to the import block.

- [ ] **Step 2: Generate request ID and scoped logger in HandleTrigger**

In `internal/bot/workflow.go`, at the start of `HandleTrigger` (after the ThreadTS check, around line 88), add request ID and logger setup:

```go
reqID := logging.NewRequestID()
logger := slog.With(
	"request_id", reqID,
	"channel_id", event.ChannelID,
	"thread_ts", event.ThreadTS,
	"user_id", event.UserID,
)
```

Then set them on `pt` after `pt` is created (around line 103):

```go
pt.RequestID = reqID
pt.Logger = logger
```

- [ ] **Step 3: Replace slog calls in runTriage with pt.Logger**

In `internal/bot/workflow.go` `runTriage` method (line 306+), replace all `slog.Info`, `slog.Warn`, `slog.Error`, `slog.Debug` calls with `pt.Logger.Info`, `pt.Logger.Warn`, `pt.Logger.Error`, `pt.Logger.Debug`. Specifically:

- Line 343: `slog.Info("thread context read"...` → `pt.Logger.Info("thread context read"...`
- Line 350: `slog.Warn("attachment download failed"...` → `pt.Logger.Warn("attachment download failed"...`
- Line 352: `slog.Info("attachment downloaded"...` → `pt.Logger.Info("attachment downloaded"...`
- Line 408: `slog.Info("prompt built"...` → `pt.Logger.Info("prompt built"...`
- Line 409: `slog.Debug("prompt content"...` → `pt.Logger.Debug("prompt content"...`
- Line 419: `slog.Info("agent output received"...` → `pt.Logger.Info("agent output received"...`
- Line 420: `slog.Debug("agent raw output"...` → `pt.Logger.Debug("agent raw output"...`
- Line 425: `slog.Warn("agent output parse failed"...` → `pt.Logger.Warn("agent output parse failed"...`
- Line 431: `slog.Info("triage result"...` → `pt.Logger.Info("triage result"...`

Also replace in `notifyError` — change the method to accept a logger:

```go
func (w *Workflow) notifyError(logger *slog.Logger, channelID, threadTS string, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	logger.Error("workflow error", "message", msg)
	w.slack.PostMessage(channelID, fmt.Sprintf(":x: %s", msg), threadTS)
}
```

Update all `notifyError` call sites in `runTriage` to pass `pt.Logger`. For call sites outside `runTriage` (where `pt.Logger` is not yet set, e.g. in `HandleTrigger` before logger is created), continue using `slog.Error` directly.

- [ ] **Step 4: Save agent output after agent run**

In `runTriage`, after the agent output is received (after line 420 `slog.Debug("agent raw output"...`), add:

```go
outputPath, saveErr := logging.SaveAgentOutput(w.cfg.Logging.AgentOutputDir, pt.RequestID, pt.SelectedRepo, output)
if saveErr != nil {
	pt.Logger.Warn("failed to save agent output", "error", saveErr)
} else {
	pt.Logger.Info("agent output saved", "path", outputPath, "length", len(output))
}
```

- [ ] **Step 5: Update AgentRunner.Run to accept logger**

In `internal/bot/agent.go`, change `Run` signature (line 44):

```go
func (r *AgentRunner) Run(ctx context.Context, logger *slog.Logger, workDir, prompt string) (string, error) {
```

Replace all `slog.Info`, `slog.Warn`, `slog.Error` in `Run` with `logger.Info`, `logger.Warn`, `logger.Error`:

- Line 47: `slog.Info("trying agent"...` → `logger.Info("trying agent"...`
- Line 50: `slog.Warn("agent failed"...` → `logger.Warn("agent failed"...`
- Line 54: `slog.Info("agent succeeded"...` → `logger.Info("agent succeeded"...`
- Line 57: `slog.Error("all agents exhausted"...` → `logger.Error("all agents exhausted"...`
- Line 89: `slog.Info("prompt too large"...` → `logger.Info("prompt too large"...`

Update the call site in `runTriage` (around line 412):

```go
output, err := w.agentRunner.Run(ctx, pt.Logger, repoPath, prompt)
```

- [ ] **Step 6: Update agent tests**

In `internal/bot/agent_test.go`, update all `runner.Run(...)` calls to pass `slog.Default()` as the logger argument:

```go
// Before:
output, err := runner.Run(context.Background(), dir, "test prompt")
// After:
output, err := runner.Run(context.Background(), slog.Default(), dir, "test prompt")
```

Apply to all test functions: `TestAgentRunner_Success`, `TestAgentRunner_Fallback`, `TestAgentRunner_AllFail`, `TestAgentRunner_Timeout`, `TestAgentRunner_PromptSubstitution`.

Add `"log/slog"` to the test file imports.

- [ ] **Step 7: Verify all tests pass**

Run: `cd /Users/ivantseng/local_file/slack-issue-bot && go test ./...`
Expected: All PASS

- [ ] **Step 8: Commit**

```bash
git add internal/bot/workflow.go internal/bot/agent.go internal/bot/agent_test.go
git commit -m "feat: add request correlation and agent output saving"
```

---

### Task 8: Update config.example.yaml and .gitignore

**Files:**
- Modify: `config.example.yaml`
- Modify: `.gitignore` (create if not exists)

- [ ] **Step 1: Add logging section to config.example.yaml**

Append to `config.example.yaml` before the mantis section:

```yaml
# === Logging ===
logging:
  dir: "logs"
  level: "debug"                  # file log level (stderr uses log_level above)
  retention_days: 30
  agent_output_dir: "logs/agent-outputs"
```

- [ ] **Step 2: Add logs/ to .gitignore**

Ensure `.gitignore` contains:

```
logs/
```

- [ ] **Step 3: Run all tests one final time**

Run: `cd /Users/ivantseng/local_file/slack-issue-bot && go test ./...`
Expected: All PASS

- [ ] **Step 4: Commit**

```bash
git add config.example.yaml .gitignore
git commit -m "chore: add logging config example and gitignore logs"
```
