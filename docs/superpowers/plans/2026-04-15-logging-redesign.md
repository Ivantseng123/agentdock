# Logging 重設計 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Redesign the logging system with Chinese messages, component/phase classification, attribute standardization, and debug log expansion for intuitive debugging.

**Architecture:** Custom `StyledTextHandler` intercepts `component` and `phase` attributes to render structured `[Component][Phase]` prefixes on stderr. JSON output remains unchanged. Each struct receives a component-tagged `*slog.Logger` via constructor injection; phase is passed per-log-call.

**Tech Stack:** Go `log/slog` (standard library), no new dependencies.

---

## File Map

### New Files
| File | Responsibility |
|------|---------------|
| `internal/logging/constants.go` | Component + Phase constants |
| `internal/logging/attributes.go` | Attribute key constants |
| `internal/logging/helpers.go` | `ComponentLogger()` helper |
| `internal/logging/helpers_test.go` | Tests for helpers |
| `internal/logging/styled_handler.go` | `StyledTextHandler` implementation |
| `internal/logging/styled_handler_test.go` | Handler tests with edge cases |
| `internal/logging/GUIDE.md` | Developer guide for logging conventions |

### Modified Files
| File | Changes |
|------|---------|
| `cmd/agentdock/app.go` | Swap stderr handler to `StyledTextHandler`; pass component loggers to constructors |
| `cmd/agentdock/worker.go` | Swap stderr handler; pass component loggers; Chinese messages |
| `cmd/agentdock/adapters.go` | Accept logger in `slackPosterAdapter`; Chinese messages |
| `internal/github/repo.go` | Add `logger` field to `RepoCache`; Chinese messages + duration |
| `internal/github/repo_test.go` | Update `NewRepoCache` calls with logger param |
| `internal/github/discovery.go` | Add `logger` field to `RepoDiscovery`; Chinese messages |
| `internal/github/issue.go` | Add `logger` field to `IssueClient`; Chinese messages + duration |
| `internal/slack/client.go` | Add `logger` field to `Client`; Chinese messages + duration; fix attribute naming |
| `internal/slack/client_test.go` | Update `NewClient` calls with logger param |
| `internal/skill/loader.go` | Add `logger` field to `Loader`; Chinese messages; `"err"` → `"error"` |
| `internal/skill/loader_test.go` | Update `NewLoader` calls with logger param |
| `internal/skill/watcher.go` | Use `Loader.logger`; Chinese messages |
| `internal/skill/watcher_test.go` | Update test setup with logger |
| `internal/config/config.go` | Chinese deprecation warning |
| `internal/queue/watchdog.go` | Add `logger` field to `Watchdog`; Chinese messages |
| `internal/queue/watchdog_test.go` | Update `NewWatchdog` calls with logger param |
| `internal/worker/pool.go` | Add `Logger` field to `Config`; Chinese messages |
| `internal/worker/pool_test.go` | Update `Config` with logger |
| `internal/worker/executor.go` | Use logger from config; Chinese messages; debug logs |
| `internal/bot/result_listener.go` | Add `logger` field to `ResultListener`; Chinese messages |
| `internal/bot/result_listener_test.go` | Update `NewResultListener` calls with logger param |
| `internal/bot/status_listener.go` | Add `logger` field to `StatusListener`; Chinese messages |
| `internal/bot/retry_handler.go` | Add `logger` field to `RetryHandler`; Chinese messages |
| `internal/bot/retry_handler_test.go` | Update `NewRetryHandler` calls with logger param |
| `internal/bot/workflow.go` | Chinese messages; component/phase on workflow logs |
| `internal/bot/enrich.go` | Accept logger param; Chinese messages |
| `internal/bot/agent.go` | Chinese messages; component/phase |
| `internal/queue/integration_test.go` | Update `worker.Config` with logger |
| `internal/queue/redis_integration_test.go` | Update `worker.Config` with logger |

---

### Task 1: Constants and Helpers

**Files:**
- Create: `internal/logging/constants.go`
- Create: `internal/logging/attributes.go`
- Create: `internal/logging/helpers.go`
- Create: `internal/logging/helpers_test.go`

- [ ] **Step 1: Create constants.go**

```go
// internal/logging/constants.go
package logging

// Component identifies which subsystem produced a log entry.
const (
	CompSlack  = "Slack"
	CompGitHub = "GitHub"
	CompAgent  = "Agent"
	CompQueue  = "Queue"
	CompWorker = "Worker"
	CompSkill  = "Skill"
	CompConfig = "Config"
	CompMantis = "Mantis"
	CompApp    = "App"
)

// Phase identifies the lifecycle stage of an operation.
const (
	PhaseReceive    = "接收"
	PhaseProcessing = "處理中"
	PhaseWaiting    = "等待中"
	PhaseComplete   = "完成"
	PhaseDegraded   = "降級"
	PhaseFailed     = "失敗"
	PhaseRetry      = "重試"
)
```

- [ ] **Step 2: Create attributes.go**

```go
// internal/logging/attributes.go
package logging

// Standardized attribute keys. Use these for high-frequency keys to prevent typos.
// One-off keys can be written as literal strings.
const (
	KeyRequestID = "request_id"
	KeyJobID     = "job_id"
	KeyWorkerID  = "worker_id"
	KeyChannelID = "channel_id"
	KeyThreadTS  = "thread_ts"
	KeyUserID    = "user_id"
	KeyRepo      = "repo"
	KeyProvider  = "provider"
	KeyStatus    = "status"
	KeyError     = "error"
	KeyURL       = "url"
	KeyDuration  = "duration_ms"
	KeyActionID  = "action_id"
	KeyVersion   = "version"
	KeyAddr      = "addr"
	KeyPath      = "path"
	KeyName      = "name"
	KeyCount     = "count"
)
```

- [ ] **Step 3: Create helpers.go**

```go
// internal/logging/helpers.go
package logging

import "log/slog"

// ComponentLogger returns a logger tagged with the given component name.
// The component attribute is intercepted by StyledTextHandler to render
// as a [Component] prefix on stderr.
func ComponentLogger(base *slog.Logger, component string) *slog.Logger {
	return base.With("component", component)
}
```

- [ ] **Step 4: Write helpers_test.go**

```go
// internal/logging/helpers_test.go
package logging

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"
)

func TestComponentLogger(t *testing.T) {
	var buf bytes.Buffer
	base := slog.New(slog.NewJSONHandler(&buf, nil))
	logger := ComponentLogger(base, CompSlack)

	logger.Info("test")

	var entry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatal(err)
	}
	if entry["component"] != CompSlack {
		t.Errorf("component = %v, want %q", entry["component"], CompSlack)
	}
}
```

- [ ] **Step 5: Run tests**

Run: `cd /Users/ivantseng/local_file/slack-issue-bot && go test ./internal/logging/...`
Expected: All PASS

- [ ] **Step 6: Commit**

```bash
git add internal/logging/constants.go internal/logging/attributes.go internal/logging/helpers.go internal/logging/helpers_test.go
git commit -m "feat(logging): add component/phase constants, attribute keys, and ComponentLogger helper"
```

---

### Task 2: StyledTextHandler

**Files:**
- Create: `internal/logging/styled_handler.go`
- Create: `internal/logging/styled_handler_test.go`

- [ ] **Step 1: Write styled_handler_test.go**

```go
// internal/logging/styled_handler_test.go
package logging

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestStyledHandler_ComponentAndPhase(t *testing.T) {
	var buf bytes.Buffer
	h := NewStyledTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	logger := slog.New(h).With("component", "Slack")

	logger.Info("收到觸發事件", "phase", PhaseReceive, "channel_id", "C0123")

	out := buf.String()
	if !strings.Contains(out, "[Slack][接收]") {
		t.Errorf("missing [Slack][接收] prefix, got: %s", out)
	}
	if !strings.Contains(out, "收到觸發事件") {
		t.Errorf("missing message, got: %s", out)
	}
	if !strings.Contains(out, "channel_id=C0123") {
		t.Errorf("missing attribute, got: %s", out)
	}
	// component and phase should NOT appear as regular attributes
	if strings.Contains(out, "component=") {
		t.Errorf("component should be in prefix, not attributes: %s", out)
	}
	if strings.Contains(out, "phase=") {
		t.Errorf("phase should be in prefix, not attributes: %s", out)
	}
}

func TestStyledHandler_ComponentOnly(t *testing.T) {
	var buf bytes.Buffer
	h := NewStyledTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	logger := slog.New(h).With("component", "GitHub")

	logger.Info("開始 clone repo")

	out := buf.String()
	if !strings.Contains(out, "[GitHub]") {
		t.Errorf("missing [GitHub] prefix, got: %s", out)
	}
	if strings.Contains(out, "[][") {
		t.Errorf("should not have empty brackets, got: %s", out)
	}
}

func TestStyledHandler_PhaseOnly(t *testing.T) {
	var buf bytes.Buffer
	h := NewStyledTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	logger := slog.New(h)

	logger.Info("some message", "phase", PhaseComplete)

	out := buf.String()
	if !strings.Contains(out, "[完成]") {
		t.Errorf("missing [完成] prefix, got: %s", out)
	}
}

func TestStyledHandler_NoComponentNoPhase(t *testing.T) {
	var buf bytes.Buffer
	h := NewStyledTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	logger := slog.New(h)

	logger.Info("plain message", "key", "val")

	out := buf.String()
	if strings.Contains(out, "[") {
		t.Errorf("should have no brackets, got: %s", out)
	}
	if !strings.Contains(out, "plain message") {
		t.Errorf("missing message, got: %s", out)
	}
	if !strings.Contains(out, "key=val") {
		t.Errorf("missing attribute, got: %s", out)
	}
}

func TestStyledHandler_TimeFormat(t *testing.T) {
	var buf bytes.Buffer
	h := NewStyledTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	logger := slog.New(h)

	logger.Info("test")

	out := buf.String()
	now := time.Now().Format("15:04")
	if !strings.Contains(out, now) {
		t.Errorf("expected time format HH:MM:SS, got: %s", out)
	}
}

func TestStyledHandler_LevelAlignment(t *testing.T) {
	var buf bytes.Buffer
	h := NewStyledTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	logger := slog.New(h)

	logger.Info("info msg")
	logger.Warn("warn msg")

	out := buf.String()
	if !strings.Contains(out, "INFO ") {
		t.Errorf("INFO should be padded, got: %s", out)
	}
	if !strings.Contains(out, "WARN ") {
		t.Errorf("WARN should be padded, got: %s", out)
	}
}

func TestStyledHandler_WithGroup(t *testing.T) {
	var buf bytes.Buffer
	h := NewStyledTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	logger := slog.New(h).WithGroup("grp").With("component", "Agent")

	logger.Info("grouped msg", "k", "v")

	out := buf.String()
	if !strings.Contains(out, "[Agent]") {
		t.Errorf("missing [Agent] prefix in grouped handler, got: %s", out)
	}
	if !strings.Contains(out, "grp.k=v") {
		t.Errorf("missing grouped attribute, got: %s", out)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /Users/ivantseng/local_file/slack-issue-bot && go test ./internal/logging/... -run TestStyled -v`
Expected: FAIL — `NewStyledTextHandler` not defined

- [ ] **Step 3: Implement styled_handler.go**

```go
// internal/logging/styled_handler.go
package logging

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"
)

// StyledTextHandler is a slog.Handler that renders structured log lines with
// [Component][Phase] prefixes. It intercepts "component" and "phase" attributes,
// pulling them into the prefix instead of the key=value list.
//
// Output format: HH:MM:SS LEVEL [Component][Phase] message key=value ...
type StyledTextHandler struct {
	w     io.Writer
	mu    *sync.Mutex
	opts  slog.HandlerOptions
	attrs []slog.Attr // pre-attached attrs (from WithAttrs)
	group string      // current group prefix
	comp  string      // pre-attached component (from WithAttrs)
}

// NewStyledTextHandler creates a StyledTextHandler writing to w.
func NewStyledTextHandler(w io.Writer, opts *slog.HandlerOptions) *StyledTextHandler {
	if opts == nil {
		opts = &slog.HandlerOptions{}
	}
	return &StyledTextHandler{
		w:    w,
		mu:   &sync.Mutex{},
		opts: *opts,
	}
}

func (h *StyledTextHandler) Enabled(_ context.Context, level slog.Level) bool {
	minLevel := slog.LevelInfo
	if h.opts.Level != nil {
		minLevel = h.opts.Level.Level()
	}
	return level >= minLevel
}

func (h *StyledTextHandler) Handle(_ context.Context, r slog.Record) error {
	// Collect component and phase, separate from other attrs.
	comp := h.comp
	phase := ""
	var attrs []slog.Attr

	// Pre-attached attrs (excluding component, already extracted).
	for _, a := range h.attrs {
		attrs = append(attrs, a)
	}

	// Record-level attrs.
	r.Attrs(func(a slog.Attr) bool {
		switch a.Key {
		case "component":
			comp = a.Value.String()
		case "phase":
			phase = a.Value.String()
		default:
			attrs = append(attrs, a)
		}
		return true
	})

	// Build output line.
	// Time
	line := r.Time.Format("15:04:05")

	// Level (padded to 5 chars)
	line += " " + padLevel(r.Level)

	// Prefix
	prefix := ""
	if comp != "" {
		prefix += "[" + comp + "]"
	}
	if phase != "" {
		prefix += "[" + phase + "]"
	}
	if prefix != "" {
		line += " " + prefix
	}

	// Message
	line += " " + r.Message

	// Attributes
	for _, a := range attrs {
		key := a.Key
		if h.group != "" {
			key = h.group + "." + key
		}
		line += " " + key + "=" + fmtValue(a.Value)
	}

	line += "\n"

	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := io.WriteString(h.w, line)
	return err
}

func (h *StyledTextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	newH := h.clone()
	for _, a := range attrs {
		if a.Key == "component" {
			newH.comp = a.Value.String()
		} else {
			newH.attrs = append(newH.attrs, a)
		}
	}
	return newH
}

func (h *StyledTextHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	newH := h.clone()
	if newH.group != "" {
		newH.group += "." + name
	} else {
		newH.group = name
	}
	return newH
}

func (h *StyledTextHandler) clone() *StyledTextHandler {
	return &StyledTextHandler{
		w:     h.w,
		mu:    h.mu,
		opts:  h.opts,
		attrs: append([]slog.Attr{}, h.attrs...),
		group: h.group,
		comp:  h.comp,
	}
}

func padLevel(l slog.Level) string {
	switch {
	case l >= slog.LevelError:
		return "ERROR"
	case l >= slog.LevelWarn:
		return "WARN "
	case l >= slog.LevelInfo:
		return "INFO "
	default:
		return "DEBUG"
	}
}

func fmtValue(v slog.Value) string {
	switch v.Kind() {
	case slog.KindString:
		s := v.String()
		if len(s) > 0 && s[0] != ' ' && s[len(s)-1] != ' ' {
			return s
		}
		return fmt.Sprintf("%q", s)
	default:
		return fmt.Sprintf("%v", v.Any())
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /Users/ivantseng/local_file/slack-issue-bot && go test ./internal/logging/... -v`
Expected: All PASS

- [ ] **Step 5: Commit**

```bash
git add internal/logging/styled_handler.go internal/logging/styled_handler_test.go
git commit -m "feat(logging): add StyledTextHandler with [Component][Phase] prefix rendering"
```

---

### Task 3: Wire StyledTextHandler into app startup

**Files:**
- Modify: `cmd/agentdock/app.go:50-53`
- Modify: `cmd/agentdock/worker.go:41`

- [ ] **Step 1: Update app.go to use StyledTextHandler for stderr**

In `cmd/agentdock/app.go`, change the stderr handler initialization:

```go
// Line 50: Change initial handler
slog.SetDefault(slog.New(logging.NewStyledTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))

// Line 53: Change configured handler
stderrHandler := logging.NewStyledTextHandler(os.Stderr, &slog.HandlerOptions{Level: parseLogLevel(cfg.LogLevel)})
```

Add `"agentdock/internal/logging"` to imports (it's already there).

- [ ] **Step 2: Update worker.go to use StyledTextHandler**

In `cmd/agentdock/worker.go`, change line 41:

```go
slog.SetDefault(slog.New(logging.NewStyledTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))
```

Add `"agentdock/internal/logging"` to imports.

- [ ] **Step 3: Run tests**

Run: `cd /Users/ivantseng/local_file/slack-issue-bot && go test ./...`
Expected: All PASS. Existing logs work as before (no component/phase yet, just no prefixes).

- [ ] **Step 4: Commit**

```bash
git add cmd/agentdock/app.go cmd/agentdock/worker.go
git commit -m "feat(logging): wire StyledTextHandler as stderr handler in app and worker"
```

---

### Task 4: Migrate internal/github

**Files:**
- Modify: `internal/github/repo.go`
- Modify: `internal/github/repo_test.go`
- Modify: `internal/github/discovery.go`
- Modify: `internal/github/issue.go`
- Modify: `cmd/agentdock/app.go` (constructor calls)
- Modify: `cmd/agentdock/worker.go` (constructor call)

- [ ] **Step 1: Add logger to RepoCache**

In `internal/github/repo.go`, add `logger *slog.Logger` to the struct and constructor:

```go
type RepoCache struct {
	dir       string
	maxAge    time.Duration
	githubPAT string
	logger    *slog.Logger
	mu        sync.Mutex
	lastPull  map[string]time.Time
}

func NewRepoCache(dir string, maxAge time.Duration, githubPAT string, logger *slog.Logger) *RepoCache {
	return &RepoCache{
		dir:       dir,
		maxAge:    maxAge,
		githubPAT: githubPAT,
		logger:    logger,
		lastPull:  make(map[string]time.Time),
	}
}
```

- [ ] **Step 2: Replace all slog calls in repo.go with rc.logger, add phase, Chinese messages, and duration**

Replace each `slog.Xxx(...)` in `EnsureRepo()` with `rc.logger.Xxx(...)` with phase and Chinese messages:

```go
func (rc *RepoCache) EnsureRepo(repoRef string) (string, error) {
	rc.mu.Lock()
	defer rc.mu.Unlock()

	start := time.Now()
	cloneURL := rc.ResolveURL(repoRef)
	localPath := filepath.Join(rc.dir, rc.dirName(repoRef))

	if _, err := os.Stat(filepath.Join(localPath, ".git")); os.IsNotExist(err) {
		rc.logger.Info("開始 clone repo", "phase", "處理中", "repo", SanitizeURL(repoRef), "path", localPath)
		if err := os.MkdirAll(filepath.Dir(localPath), 0755); err != nil {
			return "", fmt.Errorf("mkdir: %w", err)
		}
		cmd := exec.Command("git", "clone", cloneURL, localPath)
		if _, err := cmd.CombinedOutput(); err != nil {
			return "", fmt.Errorf("git clone failed: %w", err)
		}
		rc.lastPull[repoRef] = time.Now()
		rc.logger.Info("Repo 同步完成", "phase", "完成", "repo", SanitizeURL(repoRef), "duration_ms", time.Since(start).Milliseconds())
		return localPath, nil
	}

	if last, ok := rc.lastPull[repoRef]; ok && rc.maxAge > 0 && time.Since(last) < rc.maxAge {
		return localPath, nil
	}

	rc.logger.Info("開始 fetch repo", "phase", "處理中", "repo", SanitizeURL(repoRef))
	cmd := exec.Command("git", "-C", localPath, "fetch", "--all", "--prune")
	if out, err := cmd.CombinedOutput(); err != nil {
		rc.logger.Warn("Git fetch 失敗", "phase", "失敗", "error", err)
		if strings.Contains(string(out), "not a git repository") {
			rc.logger.Info("移除損壞目錄並重新 clone", "phase", "處理中", "repo", SanitizeURL(repoRef))
			os.RemoveAll(localPath)
			if err := os.MkdirAll(filepath.Dir(localPath), 0755); err != nil {
				return "", fmt.Errorf("mkdir: %w", err)
			}
			cmd = exec.Command("git", "clone", cloneURL, localPath)
			if _, err := cmd.CombinedOutput(); err != nil {
				return "", fmt.Errorf("git clone (retry) failed: %w", err)
			}
			rc.lastPull[repoRef] = time.Now()
			rc.logger.Info("Repo 同步完成", "phase", "完成", "repo", SanitizeURL(repoRef), "duration_ms", time.Since(start).Milliseconds())
			return localPath, nil
		}
	}
	cmd = exec.Command("git", "-C", localPath, "pull", "--ff-only")
	if out, err := cmd.CombinedOutput(); err != nil {
		rc.logger.Debug("Git pull fast-forward 失敗（可能在 detached head）", "phase", "處理中", "output", string(out))
	}
	rc.lastPull[repoRef] = time.Now()
	rc.logger.Info("Repo 同步完成", "phase", "完成", "repo", SanitizeURL(repoRef), "duration_ms", time.Since(start).Milliseconds())
	return localPath, nil
}
```

- [ ] **Step 3: Add logger to RepoDiscovery**

In `internal/github/discovery.go`:

```go
type RepoDiscovery struct {
	client *gh.Client
	logger *slog.Logger

	mu      sync.Mutex
	cache   []string
	fetched time.Time
	ttl     time.Duration
}

func NewRepoDiscovery(token string, logger *slog.Logger) *RepoDiscovery {
	return &RepoDiscovery{
		client: gh.NewClient(nil).WithAuthToken(token),
		logger: logger,
		ttl:    5 * time.Minute,
	}
}
```

Replace the `slog.Info` in `ListRepos()`:

```go
d.logger.Info("探索到 GitHub repos", "phase", "完成", "count", len(allRepos))
```

- [ ] **Step 4: Add logger to IssueClient**

In `internal/github/issue.go`:

```go
type IssueClient struct {
	client *gh.Client
	logger *slog.Logger
}

func NewIssueClient(token string, logger *slog.Logger) *IssueClient {
	return &IssueClient{
		client: gh.NewClient(nil).WithAuthToken(token),
		logger: logger,
	}
}

func (ic *IssueClient) CreateIssue(ctx context.Context, owner, repo, title, body string, labels []string) (string, error) {
	start := time.Now()
	req := &gh.IssueRequest{
		Title:  gh.String(title),
		Body:   gh.String(body),
		Labels: &labels,
	}

	issue, _, err := ic.client.Issues.Create(ctx, owner, repo, req)
	if err != nil {
		ic.logger.Error("Issue 建立失敗", "phase", "失敗", "owner", owner, "repo", repo, "error", err)
		return "", fmt.Errorf("create issue: %w", err)
	}

	ic.logger.Info("Issue 建立成功", "phase", "完成", "owner", owner, "repo", repo, "url", issue.GetHTMLURL(), "duration_ms", time.Since(start).Milliseconds())
	return issue.GetHTMLURL(), nil
}
```

Add `"time"` to imports.

- [ ] **Step 5: Update repo_test.go**

In `internal/github/repo_test.go`, update `NewRepoCache` calls to pass a logger:

```go
cache := NewRepoCache(cacheDir, time.Hour, "", slog.Default())
```

And:

```go
cache := NewRepoCache(cacheDir, 0, "", slog.Default())
```

- [ ] **Step 6: Update app.go constructor calls**

In `cmd/agentdock/app.go`:

```go
// Line 66-67:
githubLogger := logging.ComponentLogger(slog.Default(), logging.CompGitHub)
repoCache := ghclient.NewRepoCache(cfg.RepoCache.Dir, cfg.RepoCache.MaxAge, cfg.GitHub.Token, githubLogger)
repoDiscovery := ghclient.NewRepoDiscovery(cfg.GitHub.Token, githubLogger)

// Line 189:
issueClient := ghclient.NewIssueClient(cfg.GitHub.Token, githubLogger)
```

- [ ] **Step 7: Update worker.go constructor calls**

In `cmd/agentdock/worker.go`:

```go
// After line 58:
githubLogger := logging.ComponentLogger(slog.Default(), logging.CompGitHub)

// Line 59:
repoCache := ghclient.NewRepoCache(cfg.RepoCache.Dir, cfg.RepoCache.MaxAge, cfg.GitHub.Token, githubLogger)
```

Add `"agentdock/internal/logging"` to imports.

- [ ] **Step 8: Run tests**

Run: `cd /Users/ivantseng/local_file/slack-issue-bot && go test ./...`
Expected: All PASS

- [ ] **Step 9: Commit**

```bash
git add internal/github/ cmd/agentdock/app.go cmd/agentdock/worker.go
git commit -m "feat(logging): migrate internal/github to Chinese messages with component/phase injection"
```

---

### Task 5: Migrate internal/slack

**Files:**
- Modify: `internal/slack/client.go`
- Modify: `internal/slack/client_test.go`
- Modify: `cmd/agentdock/app.go` (constructor call)
- Modify: `cmd/agentdock/adapters.go` (logger injection)

- [ ] **Step 1: Add logger to Client**

In `internal/slack/client.go`:

```go
type Client struct {
	api    *slack.Client
	logger *slog.Logger
}

func NewClient(botToken string, logger *slog.Logger) *Client {
	return &Client{
		api:    slack.New(botToken),
		logger: logger,
	}
}
```

- [ ] **Step 2: Replace all slog calls with c.logger, fix attribute names, add Chinese messages**

Key replacements (apply to all `slog.Warn/Info/Debug` calls in `client.go`):

- `slog.Warn("failed to download slack file", "name", ...)` → `c.logger.Warn("Slack 檔案下載失敗", "phase", "失敗", "name", ...)`
- `slog.Warn("failed to download xlsx", "name", ...)` → `c.logger.Warn("XLSX 下載失敗", "phase", "失敗", "name", ...)`
- `slog.Warn("failed to parse xlsx", "name", ...)` → `c.logger.Warn("XLSX 解析失敗", "phase", "失敗", "name", ...)`
- `slog.Warn("failed to download image", "name", ...)` → `c.logger.Warn("圖片下載失敗", "phase", "失敗", "name", ...)`
- `slog.Warn("image too large, skipping", "name", ...)` → `c.logger.Warn("圖片過大，跳過", "phase", "失敗", "name", ...)`
- `slog.Warn("failed to resolve user", "userID", ...)` → `c.logger.Warn("使用者名稱解析失敗", "phase", "失敗", "user_id", ...)` (fix: `userID` → `user_id`)
- `slog.Warn("failed to resolve channel name", "channelID", ...)` → `c.logger.Warn("頻道名稱解析失敗", "phase", "失敗", "channel_id", ...)` (fix: `channelID` → `channel_id`)
- `slog.Warn("attachment download failed", ...)` → `c.logger.Warn("附件下載失敗", "phase", "失敗", ...)`
- `slog.Warn("attachment write failed", ...)` → `c.logger.Warn("附件寫入失敗", "phase", "失敗", ...)`

Add duration tracking in `FetchThreadContext`:

```go
func (c *Client) FetchThreadContext(channelID, threadTS, triggerTS, botUserID string, limit int) ([]ThreadRawMessage, error) {
	start := time.Now()
	if limit <= 0 {
		limit = 50
	}
	// ... existing logic ...
	result := filterThreadMessages(allMessages, triggerTS, botUserID)
	c.logger.Debug("訊息串內容已讀取", "phase", "處理中", "channel_id", channelID, "message_count", len(result), "duration_ms", time.Since(start).Milliseconds())
	return result, nil
}
```

Add `"time"` to imports.

- [ ] **Step 3: Update client_test.go**

Update `NewClient` calls in `internal/slack/client_test.go` to pass `slog.Default()` as logger.

- [ ] **Step 4: Update adapters.go**

In `cmd/agentdock/adapters.go`, add logger to `slackPosterAdapter`:

```go
type slackPosterAdapter struct {
	client *slackclient.Client
	logger *slog.Logger
}

func (a *slackPosterAdapter) PostMessage(channelID, text, threadTS string) {
	if err := a.client.PostMessage(channelID, text, threadTS); err != nil {
		a.logger.Warn("發送訊息失敗", "phase", "失敗", "channel_id", channelID, "error", err)
	}
}

func (a *slackPosterAdapter) UpdateMessage(channelID, messageTS, text string) {
	if err := a.client.UpdateMessage(channelID, messageTS, text); err != nil {
		a.logger.Warn("更新訊息失敗", "phase", "失敗", "channel_id", channelID, "error", err)
	}
}
```

Add `"agentdock/internal/logging"` to imports if not already present.

- [ ] **Step 5: Update app.go constructor calls**

In `cmd/agentdock/app.go`:

```go
// Line 64:
slackLogger := logging.ComponentLogger(slog.Default(), logging.CompSlack)
slackClient := slackclient.NewClient(cfg.Slack.BotToken, slackLogger)

// Lines 191, 197 — slackPosterAdapter:
&slackPosterAdapter{client: slackClient, logger: slackLogger}
```

- [ ] **Step 6: Run tests**

Run: `cd /Users/ivantseng/local_file/slack-issue-bot && go test ./...`
Expected: All PASS

- [ ] **Step 7: Commit**

```bash
git add internal/slack/client.go internal/slack/client_test.go cmd/agentdock/app.go cmd/agentdock/adapters.go
git commit -m "feat(logging): migrate internal/slack to Chinese messages with component/phase injection"
```

---

### Task 6: Migrate internal/skill

**Files:**
- Modify: `internal/skill/loader.go`
- Modify: `internal/skill/loader_test.go`
- Modify: `internal/skill/watcher.go`
- Modify: `internal/skill/watcher_test.go`
- Modify: `cmd/agentdock/app.go` (constructor call)

- [ ] **Step 1: Add logger to Loader**

In `internal/skill/loader.go`:

```go
type Loader struct {
	mu      sync.RWMutex
	config  *SkillsFileConfig
	cache   map[string]*cacheEntry
	bakedIn map[string]*SkillFiles
	fetcher fetchFunc
	group   singleflight.Group
	logger  *slog.Logger
}

func NewLoader(configPath, bakedInDir string, logger *slog.Logger) (*Loader, error) {
	// ... existing logic ...
	return &Loader{
		config:  cfg,
		cache:   make(map[string]*cacheEntry),
		bakedIn: bakedIn,
		fetcher: FetchPackage,
		logger:  logger,
	}, nil
}
```

Note: `loadBakedInSkills` is a package function, not a method. Pass `logger` to it:

```go
func loadBakedInSkills(dir string, logger *slog.Logger) map[string]*SkillFiles {
	result := make(map[string]*SkillFiles)
	entries, err := os.ReadDir(dir)
	if err != nil {
		logger.Warn("無法讀取內建技能目錄", "phase", "失敗", "dir", dir, "error", err)
		return result
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		skillDir := filepath.Join(dir, entry.Name())
		sf, err := loadSingleBakedIn(skillDir)
		if err != nil {
			logger.Warn("跳過內建技能", "phase", "失敗", "dir", skillDir, "error", err)
			continue
		}
		result[sf.Name] = sf
	}
	return result
}
```

Call it with logger in `NewLoader`:

```go
if bakedInDir != "" {
	bakedIn = loadBakedInSkills(bakedInDir, logger)
}
```

- [ ] **Step 2: Replace all slog calls in loader.go with l.logger, fix "err" → "error"**

- `slog.Warn("skill: unknown type", "type", sc.Type)` → `l.logger.Warn("未知技能類型", "phase", "失敗", "type", sc.Type)`
- `slog.Warn("skill: local skill not found in baked-in map", "name", skillName)` → `l.logger.Warn("本地技能未找到", "phase", "失敗", "name", skillName)`
- `slog.Warn("skill: fetch failed, recording negative cache", "pkg", cacheKey, "err", fetchErr)` → `l.logger.Warn("技能下載失敗，記錄負向快取", "phase", "失敗", "pkg", cacheKey, "error", fetchErr)`
- `slog.Warn("skill: validation failed, recording negative cache", "pkg", cacheKey, "err", valErr)` → `l.logger.Warn("技能驗證失敗，記錄負向快取", "phase", "失敗", "pkg", cacheKey, "error", valErr)`

- [ ] **Step 3: Update watcher.go to use l.logger**

In `internal/skill/watcher.go`, replace:

```go
slog.Error("skill.config_reload_failed", "path", configPath, "error", err)
```
with:
```go
l.logger.Error("技能設定重新載入失敗", "phase", "失敗", "path", configPath, "error", err)
```

Replace:
```go
slog.Info("skill.config_reloaded", "path", configPath)
```
with:
```go
l.logger.Info("技能設定已重新載入", "phase", "完成", "path", configPath)
```

Replace:
```go
slog.Error("skill.watcher_error", "error", err)
```
with:
```go
l.logger.Error("技能監視器錯誤", "phase", "失敗", "error", err)
```

- [ ] **Step 4: Update loader_test.go and watcher_test.go**

Add `slog.Default()` as the logger parameter to all `NewLoader()` calls in tests.

- [ ] **Step 5: Update app.go constructor call**

In `cmd/agentdock/app.go`:

```go
skillLogger := logging.ComponentLogger(slog.Default(), logging.CompSkill)
skillLoader, err := skill.NewLoader(cfg.SkillsConfig, bakedInDir, skillLogger)
```

- [ ] **Step 6: Run tests**

Run: `cd /Users/ivantseng/local_file/slack-issue-bot && go test ./...`
Expected: All PASS

- [ ] **Step 7: Commit**

```bash
git add internal/skill/ cmd/agentdock/app.go
git commit -m "feat(logging): migrate internal/skill to Chinese messages with component/phase injection"
```

---

### Task 7: Migrate internal/config

**Files:**
- Modify: `internal/config/config.go`

- [ ] **Step 1: Update deprecation warning to Chinese**

In `internal/config/config.go:146`, change:

```go
slog.Warn("max_concurrent is deprecated, use workers.count instead")
```
to:
```go
slog.Warn("max_concurrent 已棄用，請改用 workers.count", "phase", "失敗")
```

Note: `config.go` uses slog at package init time (during `applyDefaults`), before any struct is created. We use `slog.Default()` here — it will have the StyledTextHandler by the time it runs, but no component tag. This is acceptable because config loading happens once at startup.

- [ ] **Step 2: Update cmd/agentdock/config.go**

In `cmd/agentdock/config.go:173`:

```go
slog.Warn("設定儲存失敗", "phase", "失敗", "path", resolved, "error", err)
```

In `cmd/agentdock/config.go:299`:

```go
slog.Warn("未知設定鍵", "phase", "失敗", "key", key)
```

- [ ] **Step 3: Run tests**

Run: `cd /Users/ivantseng/local_file/slack-issue-bot && go test ./...`
Expected: All PASS

- [ ] **Step 4: Commit**

```bash
git add internal/config/config.go cmd/agentdock/config.go
git commit -m "feat(logging): migrate config to Chinese messages"
```

---

### Task 8: Migrate internal/queue (Watchdog)

**Files:**
- Modify: `internal/queue/watchdog.go`
- Modify: `internal/queue/watchdog_test.go`
- Modify: `cmd/agentdock/app.go` (constructor call)

- [ ] **Step 1: Add logger to Watchdog**

In `internal/queue/watchdog.go`:

```go
type Watchdog struct {
	store          JobStore
	commands       CommandBus
	results        ResultBus
	logger         *slog.Logger
	jobTimeout     time.Duration
	idleTimeout    time.Duration
	prepareTimeout time.Duration
	interval       time.Duration
}

func NewWatchdog(store JobStore, commands CommandBus, results ResultBus, cfg WatchdogConfig, logger *slog.Logger) *Watchdog {
	interval := cfg.JobTimeout / 3
	if interval < 30*time.Second {
		interval = 30 * time.Second
	}
	return &Watchdog{
		store:          store,
		commands:       commands,
		results:        results,
		logger:         logger,
		jobTimeout:     cfg.JobTimeout,
		idleTimeout:    cfg.IdleTimeout,
		prepareTimeout: cfg.PrepareTimeout,
		interval:       interval,
	}
}
```

- [ ] **Step 2: Replace all slog calls with w.logger + Chinese messages**

In `Start()`:
```go
w.logger.Info("Watchdog 已啟動", "phase", "處理中",
	"job_timeout", w.jobTimeout,
	"idle_timeout", w.idleTimeout,
	"prepare_timeout", w.prepareTimeout,
	"check_interval", w.interval,
)
```

```go
w.logger.Info("Watchdog 已停止", "phase", "完成")
```

In `check()`:
```go
w.logger.Warn("Watchdog 列舉工作失敗", "phase", "失敗", "error", err)
```

In `killAndPublish()`:
```go
w.logger.Warn("強制終止逾時工作", "phase", "失敗",
	"job_id", state.Job.ID, "status", state.Status, "reason", reason)
```

- [ ] **Step 3: Update watchdog_test.go**

In `internal/queue/watchdog_test.go`, update `NewWatchdog` call:

```go
wd := NewWatchdog(store, commands, results, WatchdogConfig{
	JobTimeout: 100 * time.Millisecond,
}, slog.Default())
```

- [ ] **Step 4: Update app.go constructor call**

In `cmd/agentdock/app.go`:

```go
queueLogger := logging.ComponentLogger(slog.Default(), logging.CompQueue)
watchdog := queue.NewWatchdog(jobStore, bundle.Commands, bundle.Results, queue.WatchdogConfig{
	JobTimeout:     cfg.Queue.JobTimeout,
	IdleTimeout:    cfg.Queue.AgentIdleTimeout,
	PrepareTimeout: cfg.Queue.PrepareTimeout,
}, queueLogger)
```

- [ ] **Step 5: Run tests**

Run: `cd /Users/ivantseng/local_file/slack-issue-bot && go test ./...`
Expected: All PASS

- [ ] **Step 6: Commit**

```bash
git add internal/queue/watchdog.go internal/queue/watchdog_test.go cmd/agentdock/app.go
git commit -m "feat(logging): migrate Watchdog to Chinese messages with component/phase injection"
```

---

### Task 9: Migrate internal/worker

**Files:**
- Modify: `internal/worker/pool.go`
- Modify: `internal/worker/executor.go`
- Modify: `internal/worker/pool_test.go`
- Modify: `internal/queue/integration_test.go`
- Modify: `internal/queue/redis_integration_test.go`
- Modify: `cmd/agentdock/local_adapter.go`
- Modify: `cmd/agentdock/app.go`
- Modify: `cmd/agentdock/worker.go`

- [ ] **Step 1: Add Logger to worker.Config**

In `internal/worker/pool.go`:

```go
type Config struct {
	Queue          queue.JobQueue
	Attachments    queue.AttachmentStore
	Results        queue.ResultBus
	Store          queue.JobStore
	Runner         Runner
	RepoCache      RepoProvider
	WorkerCount    int
	Hostname       string
	SkillDirs      []string
	Commands       queue.CommandBus
	Status         queue.StatusBus
	StatusInterval time.Duration
	Logger         *slog.Logger
}
```

- [ ] **Step 2: Replace all slog calls in pool.go with p.cfg.Logger + Chinese messages**

```go
func (p *Pool) Start(ctx context.Context) {
	if p.cfg.Commands != nil {
		go p.commandListener(ctx)
	}
	for i := 0; i < p.cfg.WorkerCount; i++ {
		go p.runWorker(ctx, i)
	}
	go p.workerHeartbeat(ctx)
	p.cfg.Logger.Info("Worker pool 已啟動", "phase", "處理中", "count", p.cfg.WorkerCount)
}
```

In `commandListener()`:
```go
p.cfg.Logger.Error("接收指令失敗", "phase", "失敗", "error", err)
```
```go
p.cfg.Logger.Warn("終止指令失敗", "phase", "失敗", "job_id", cmd.JobID, "error", err)
```

In `runWorker()`:
```go
logger := p.cfg.Logger.With("worker_id", id)
```

In `executeWithTracking()`:
```go
logger := p.cfg.Logger.With("worker_id", workerIndex, "job_id", job.ID)
```
```go
logger.Info("工作完成", "phase", "完成", "status", result.Status)
```

In `workerHeartbeat()`:
```go
p.cfg.Logger.Warn("Worker 註冊失敗", "phase", "失敗", "worker_id", info.WorkerID, "error", err)
```

- [ ] **Step 3: Update executor.go Chinese messages and debug logs**

In `internal/worker/executor.go`, replace `slog.With(...)` with a passed-in logger. Since `executeJob` is a package function, pass the logger:

```go
func executeJob(ctx context.Context, job *queue.Job, deps executionDeps, opts bot.RunOptions, logger *slog.Logger) *queue.JobResult {
```

Update caller in `pool.go:168`:
```go
result := executeJob(jobCtx, job, deps, opts, logger)
```

Replace all log calls in `executeJob`:
```go
logger.Info("解析附件中", "phase", "處理中", "count", len(job.Attachments))
// ...
logger.Info("準備 repo 中", "phase", "處理中", "branch", job.Branch)
// ...
logger.Info("Repo 已就緒", "phase", "處理中", "path", repoPath)
// ...
logger.Info("掛載技能中", "phase", "處理中", "count", len(job.Skills), "skill_dirs", deps.skillDirs)
// ...
logger.Warn("工作中無技能 payload", "phase", "處理中")
// ...
logger.Info("執行 agent 中", "phase", "處理中")
// ...
logger.Info("Agent 執行完成", "phase", "完成", "output_len", len(output))
// ...
logger.Warn("解析失敗，輸出原始內容", "phase", "失敗", "output", truncated)
// ...
logger.Info("解析成功", "phase", "完成", "status", parsed.Status, "confidence", parsed.Confidence, "files_found", parsed.FilesFound)
```

- [ ] **Step 4: Update pool_test.go**

Add `Logger: slog.Default()` to all `Config{}` structs in `internal/worker/pool_test.go`:

```go
pool := NewPool(Config{
	// ... existing fields ...
	Logger: slog.Default(),
})
```

- [ ] **Step 5: Update integration tests**

In `internal/queue/integration_test.go` and `internal/queue/redis_integration_test.go`, add `Logger: slog.Default()` to `worker.Config{}`.

- [ ] **Step 6: Update app.go and worker.go constructor calls**

In `cmd/agentdock/local_adapter.go`, add `Logger` to `LocalAdapterConfig` and thread it into `worker.Config`:

```go
type LocalAdapterConfig struct {
	Runner         worker.Runner
	RepoCache      worker.RepoProvider
	SkillDirs      []string
	WorkerCount    int
	StatusInterval time.Duration
	Capabilities   []string
	Store          queue.JobStore
	Logger         *slog.Logger
}
```

In `Start()`, add `Logger: a.cfg.Logger` to the `worker.Config{}`.

In `cmd/agentdock/app.go`, pass the worker logger when creating the local adapter:

```go
workerLogger := logging.ComponentLogger(slog.Default(), logging.CompWorker)
localAdapter := NewLocalAdapter(LocalAdapterConfig{
	// ... existing fields ...
	Logger: workerLogger,
})
```

In `cmd/agentdock/worker.go`:

```go
workerLogger := logging.ComponentLogger(slog.Default(), logging.CompWorker)
pool := worker.NewPool(worker.Config{
	// ... existing fields ...
	Logger: workerLogger,
})
```

- [ ] **Step 7: Run tests**

Run: `cd /Users/ivantseng/local_file/slack-issue-bot && go test ./...`
Expected: All PASS

- [ ] **Step 8: Commit**

```bash
git add internal/worker/ internal/queue/integration_test.go internal/queue/redis_integration_test.go cmd/agentdock/app.go cmd/agentdock/worker.go
git commit -m "feat(logging): migrate internal/worker to Chinese messages with component/phase injection"
```

---

### Task 10: Migrate internal/bot

**Files:**
- Modify: `internal/bot/result_listener.go`
- Modify: `internal/bot/result_listener_test.go`
- Modify: `internal/bot/status_listener.go`
- Modify: `internal/bot/retry_handler.go`
- Modify: `internal/bot/retry_handler_test.go`
- Modify: `internal/bot/workflow.go`
- Modify: `internal/bot/enrich.go`
- Modify: `internal/bot/agent.go`
- Modify: `cmd/agentdock/app.go` (constructor calls)

- [ ] **Step 1: Add logger to ResultListener**

In `internal/bot/result_listener.go`:

```go
type ResultListener struct {
	results       queue.ResultBus
	store         queue.JobStore
	attachments   queue.AttachmentStore
	slack         SlackPoster
	github        IssueCreator
	onDedupClear  func(channelID, threadTS string)
	logger        *slog.Logger

	mu            sync.Mutex
	processedJobs map[string]bool
}

func NewResultListener(
	results queue.ResultBus,
	store queue.JobStore,
	attachments queue.AttachmentStore,
	slack SlackPoster,
	github IssueCreator,
	onDedupClear func(channelID, threadTS string),
	logger *slog.Logger,
) *ResultListener {
	return &ResultListener{
		results:       results,
		store:         store,
		attachments:   attachments,
		slack:         slack,
		github:        github,
		onDedupClear:  onDedupClear,
		logger:        logger,
		processedJobs: make(map[string]bool),
	}
}
```

Replace slog calls in `Listen()` and `handleResult()`:
```go
r.logger.Error("訂閱結果匯流排失敗", "phase", "失敗", "error", err)
```
```go
r.logger.Debug("重複結果已忽略", "phase", "處理中", "job_id", result.JobID)
```
```go
r.logger.Error("找不到工作結果對應的工作", "phase", "失敗", "job_id", result.JobID, "error", err)
```
```go
logger.Warn("工作失敗", "phase", "降級", ...)
```
```go
logger.Info("工作完成", "phase", "完成", ...)
```

- [ ] **Step 2: Add logger to StatusListener**

```go
type StatusListener struct {
	status queue.StatusBus
	store  queue.JobStore
	logger *slog.Logger
}

func NewStatusListener(status queue.StatusBus, store queue.JobStore, logger *slog.Logger) *StatusListener {
	return &StatusListener{status: status, store: store, logger: logger}
}
```

Replace:
```go
l.logger.Error("訂閱狀態匯流排失敗", "phase", "失敗", "error", err)
```

- [ ] **Step 3: Add logger to RetryHandler**

```go
type RetryHandler struct {
	store  queue.JobStore
	queue  JobSubmitter
	slack  SlackPoster
	logger *slog.Logger
}

func NewRetryHandler(store queue.JobStore, q JobSubmitter, slack SlackPoster, logger *slog.Logger) *RetryHandler {
	return &RetryHandler{store: store, queue: q, slack: slack, logger: logger}
}
```

Replace slog calls in `Handle()`:
```go
h.logger.Warn("重試：找不到工作", "phase", "重試", "job_id", jobID, "error", err)
```
```go
h.logger.Info("重試：工作非失敗狀態，忽略", "phase", "重試", "job_id", jobID, "status", state.Status)
```
```go
h.logger.Error("重試：提交失敗", "phase", "重試", "job_id", newJob.ID, "error", err)
```
```go
h.logger.Info("重試工作已提交", "phase", "重試",
	"original_job_id", original.ID,
	"new_job_id", newJob.ID,
	"retry_count", newJob.RetryCount)
```

- [ ] **Step 4: Update workflow.go Chinese messages**

In `internal/bot/workflow.go`, the workflow uses `pt.Logger` (request-scoped). Add phase to existing log calls:

Line 357: `pt.Logger.Info("訊息串已讀取", "phase", "處理中", "messages", len(rawMsgs), "repo", pt.SelectedRepo)`
Line 401: `pt.Logger.Info("Prompt 已組裝", "phase", "處理中", "length", len(prompt))`
Line 199: `slog.Warn("Repo 搜尋失敗", "phase", "失敗", "error", err)` (this stays global since it's in a handler method without struct logger)
Line 468: `slog.Warn("載入技能失敗", "phase", "失敗", "error", err)`

- [ ] **Step 5: Update enrich.go Chinese messages**

In `internal/bot/enrich.go`, `enrichMessage` is a package function that receives `mantisClient`. Since it doesn't belong to a struct, keep using slog but add phase:

```go
slog.Warn("Mantis issue 擴充失敗", "phase", "失敗", "id", issueID, "error", err)
```
```go
slog.Info("Mantis issue 已擴充", "phase", "完成", "id", issueID, "title", title)
```

- [ ] **Step 6: Update agent.go Chinese messages**

In `internal/bot/agent.go`, the `Run` method already accepts `logger *slog.Logger`. Update messages:

Line 41: `slog.Warn("Provider 未找到", "phase", "失敗", "name", name)`
Line 57: `logger.Info("嘗試 agent", "phase", "處理中", ...)`
Line 60: `logger.Warn("Agent 失敗", "phase", "失敗", ...)`
Line 64: `logger.Info("Agent 執行成功", "phase", "完成", ...)`
Line 67: `logger.Error("所有 agent 已耗盡", "phase", "失敗", ...)`
Line 99: `logger.Info("Prompt 過大，改用 stdin", "phase", "處理中", ...)`
Line 135: `logger.Info("Agent process 已啟動", "phase", "處理中", ...)`

- [ ] **Step 7: Update test files**

In `internal/bot/result_listener_test.go`, add `slog.Default()` as last parameter to all `NewResultListener()` calls.

In `internal/bot/retry_handler_test.go`, add `slog.Default()` as last parameter to all `NewRetryHandler()` calls.

- [ ] **Step 8: Update app.go constructor calls**

```go
agentLogger := logging.ComponentLogger(slog.Default(), logging.CompAgent)
queueLogger := logging.ComponentLogger(slog.Default(), logging.CompQueue)
workerLogger := logging.ComponentLogger(slog.Default(), logging.CompWorker)

// ResultListener — classified as Agent component
resultListener := bot.NewResultListener(bundle.Results, jobStore, bundle.Attachments,
	&slackPosterAdapter{client: slackClient, logger: slackLogger}, issueClient,
	func(channelID, threadTS string) {
		handler.ClearThreadDedup(channelID, threadTS)
	}, agentLogger)

// RetryHandler — classified as Worker component
retryHandler := bot.NewRetryHandler(jobStore, coordinator, &slackPosterAdapter{client: slackClient, logger: slackLogger}, workerLogger)

// StatusListener — classified as Queue component
statusListener := bot.NewStatusListener(bundle.Status, jobStore, queueLogger)
```

- [ ] **Step 9: Run tests**

Run: `cd /Users/ivantseng/local_file/slack-issue-bot && go test ./...`
Expected: All PASS

- [ ] **Step 10: Commit**

```bash
git add internal/bot/ cmd/agentdock/app.go
git commit -m "feat(logging): migrate internal/bot to Chinese messages with component/phase injection"
```

---

### Task 11: Migrate cmd/agentdock (app-level logs)

**Files:**
- Modify: `cmd/agentdock/app.go` (remaining log statements)
- Modify: `cmd/agentdock/worker.go` (remaining log statements)

- [ ] **Step 1: Create app logger and update app.go log statements**

In `cmd/agentdock/app.go`, after the logger is set (after line 62), create an app logger:

```go
appLogger := logging.ComponentLogger(slog.Default(), logging.CompApp)
```

Replace remaining `slog.Xxx(...)` calls:

Line 73: `appLogger.Warn("Repo 快取預熱失敗", "phase", "失敗", "error", err)`
Line 93: `appLogger.Warn("技能設定監視器啟動失敗", "phase", "失敗", "error", err)`
Line 106: `appLogger.Info("Mantis 整合已啟用", "phase", "處理中", "url", cfg.Mantis.BaseURL)`
Line 125: `appLogger.Info("使用 Redis 傳輸層", "phase", "處理中", "addr", cfg.Redis.Addr)`
Line 128: `appLogger.Info("使用記憶體內傳輸層", "phase", "處理中")`
Line 219: `appLogger.Info("HTTP 端點已啟動", "phase", "處理中", "addr", addr, "endpoints", []string{"/healthz", "/jobs", "/jobs/{id}"})`
Line 233: `appLogger.Info("Bot 身份已解析", "phase", "處理中", "user_id", botUserID)` (fix: `userID` → `user_id`)
Line 235: `appLogger.Warn("Bot 身份解析失敗", "phase", "失敗", "error", err)`
Line 238: `appLogger.Info("啟動 Bot", "phase", "處理中", "version", version, "commit", commit, "date", date)`
Line 293: `appLogger.Info("收到搜尋建議", "phase", "接收", "action_id", cb.ActionID, "value", cb.Value)` (fix: `actionID` → `action_id`)
Line 296: `appLogger.Info("Repo 搜尋結果", "phase", "處理中", "query", cb.Value, "count", len(options))`
Line 317: `appLogger.Info("收到按鈕互動", "phase", "接收", "action_id", action.ActionID, "value", action.Value, "selector_ts", selectorTS)` (fix: `actionID` → `action_id`, `selectorTS` → `selector_ts`)

- [ ] **Step 2: Update worker.go log statements**

In `cmd/agentdock/worker.go`:

```go
appLogger := logging.ComponentLogger(slog.Default(), logging.CompApp)
```

Line 52: `appLogger.Info("已連線至 Redis", "phase", "處理中", "addr", cfg.Redis.Addr)`
Line 93: `appLogger.Info("Worker 已啟動", "phase", "完成", "workers", cfg.Workers.Count)`
Line 99: `appLogger.Info("正在關閉", "phase", "完成", "signal", sig)`

- [ ] **Step 3: Run tests**

Run: `cd /Users/ivantseng/local_file/slack-issue-bot && go test ./...`
Expected: All PASS

- [ ] **Step 4: Commit**

```bash
git add cmd/agentdock/app.go cmd/agentdock/worker.go
git commit -m "feat(logging): migrate cmd/agentdock to Chinese messages with component/phase"
```

---

### Task 12: Write GUIDE.md

**Files:**
- Create: `internal/logging/GUIDE.md`

- [ ] **Step 1: Write the logging developer guide**

```markdown
# Logging 開發指南

## 概述

本專案使用 Go 標準庫 `log/slog` 搭配自訂 `StyledTextHandler`，提供雙維度分類的結構化 log。

## 輸出格式

### Terminal (stderr)
```
15:03:22 INFO  [Slack][接收] 收到觸發事件 channel_id=C0123 thread_ts=1234.5678
```

### File (JSON)
```json
{"time":"...","level":"INFO","msg":"收到觸發事件","component":"Slack","phase":"接收","channel_id":"C0123"}
```

## Component 注入

每個 struct 在建構時接收一個已綁定 component 的 `*slog.Logger`：

```go
// 建構端 (app.go)
logger := logging.ComponentLogger(slog.Default(), logging.CompSlack)
client := slack.NewClient(token, logger)

// struct 內部
func (c *Client) DoSomething() {
    c.logger.Info("做某件事", "phase", logging.PhaseProcessing, "key", value)
}
```

## Phase 使用方式

Phase 是**逐條帶入**的，不綁定到 logger（避免 slog.With 疊加）：

```go
c.logger.Info("訊息", "phase", logging.PhaseReceive, ...)
c.logger.Warn("錯誤", "phase", logging.PhaseFailed, ...)
```

## Component 清單

| 常數 | 值 | 適用模組 |
|------|---|---------|
| CompSlack | Slack | internal/slack, adapters |
| CompGitHub | GitHub | internal/github |
| CompAgent | Agent | internal/bot (agent, result_listener) |
| CompQueue | Queue | internal/queue (watchdog, status_listener) |
| CompWorker | Worker | internal/worker, retry_handler |
| CompSkill | Skill | internal/skill |
| CompConfig | Config | internal/config |
| CompMantis | Mantis | internal/bot/enrich |
| CompApp | App | cmd/agentdock |

## Phase 清單

| 常數 | 值 | 用途 |
|------|---|------|
| PhaseReceive | 接收 | 收到外部事件 |
| PhaseProcessing | 處理中 | 執行核心邏輯 |
| PhaseWaiting | 等待中 | 等外部回應 |
| PhaseComplete | 完成 | 成功結束 |
| PhaseDegraded | 降級 | 部分成功 |
| PhaseFailed | 失敗 | 出錯 |
| PhaseRetry | 重試 | 重試流程 |

## Attribute 規範

- 全部使用 **snake_case**
- Error key 統一用 `"error"`，不用 `"err"`
- 高頻 key 用 `logging.KeyXxx` 常數
- 一次性 key 直接寫字串

## 新增 Log 的 Checklist

1. 選擇正確的 component（看你在哪個 struct 裡）
2. 選擇正確的 phase（看當前操作的生命週期階段）
3. 中文 message，動詞開頭，不加句號
4. Attribute key 用 snake_case
5. 專有名詞維持英文（GitHub, Redis, agent, clone）
6. 如果是耗時操作，加 `"duration_ms", time.Since(start).Milliseconds()`

## Debug Log 判斷原則

加 Debug log 的時機：
- 操作的輸入/輸出摘要（長度、數量）
- 快取命中/未命中
- 分支選擇的原因（為什麼走這條路）
- 外部 API 呼叫的細節

不需要 Debug log 的時機：
- 每一行程式碼的執行（太 noisy）
- 已經有 Info/Warn 覆蓋的路徑
```

- [ ] **Step 2: Commit**

```bash
git add internal/logging/GUIDE.md
git commit -m "docs(logging): add developer guide for logging conventions"
```

---

### Task 13: Final Sweep and Verification

**Files:**
- Possibly modify: any files with remaining English log messages or camelCase attributes

- [ ] **Step 1: Grep for remaining English slog messages**

Run: `cd /Users/ivantseng/local_file/slack-issue-bot && grep -rn 'slog\.\(Info\|Warn\|Error\|Debug\)(' --include='*.go' | grep -v '_test.go' | grep -v 'GUIDE.md'`

Check that all remaining log messages are either Chinese or in test files.

- [ ] **Step 2: Grep for remaining camelCase attribute keys**

Run: `cd /Users/ivantseng/local_file/slack-issue-bot && grep -rn '"userID"\|"channelID"\|"actionID"\|"selectorTS"\|"err",' --include='*.go' | grep -v '_test.go'`

Expected: No matches (all fixed).

- [ ] **Step 3: Run full test suite**

Run: `cd /Users/ivantseng/local_file/slack-issue-bot && go test ./...`
Expected: All PASS

- [ ] **Step 4: Update CLAUDE.md Lessons Learned**

Add to the Lessons Learned section in `CLAUDE.md`:

```markdown
- **Logging conventions**: See `internal/logging/GUIDE.md` for component/phase classification, Chinese message format, and attribute naming rules.
```

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "feat(logging): final sweep — verify no remaining English messages or camelCase keys"
```
