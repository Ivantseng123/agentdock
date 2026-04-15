# Worker Interactive Preflight Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add preflight validation and interactive prompts to `./bot worker` so dependencies (Redis, GitHub token, agent CLIs) are verified before the worker pool starts.

**Architecture:** All new code lives in `cmd/bot/worker.go` (preflight logic) and a new `cmd/bot/preflight.go` (check functions + prompt helpers). The existing `runWorker()` calls `runPreflight(cfg)` before creating the Redis client. Preflight uses `fmt.Fprintf(os.Stderr)` for human-readable output; `slog` is initialized only after preflight passes.

**Tech Stack:** Go stdlib (`bufio`, `os/exec`, `net/http`), `golang.org/x/term` (new dep), `github.com/redis/go-redis/v9` (existing)

---

## File Structure

| File | Action | Responsibility |
|------|--------|----------------|
| `cmd/bot/preflight.go` | Create | All preflight logic: checks, prompts, retry loop |
| `cmd/bot/preflight_test.go` | Create | Unit tests for check functions |
| `cmd/bot/worker.go` | Modify | Call `runPreflight(cfg)` before Redis client setup, move slog init after preflight |

---

### Task 1: Add `golang.org/x/term` dependency

**Files:**
- Modify: `go.mod`

- [ ] **Step 1: Add the dependency**

Run:
```bash
go get golang.org/x/term
```

- [ ] **Step 2: Verify it's in go.mod**

Run:
```bash
grep "golang.org/x/term" go.mod
```
Expected: a line like `golang.org/x/term v0.x.x`

- [ ] **Step 3: Commit**

```bash
git add go.mod go.sum
git commit -m "chore: add golang.org/x/term dependency for preflight"
```

---

### Task 2: Create preflight check functions

**Files:**
- Create: `cmd/bot/preflight.go`
- Create: `cmd/bot/preflight_test.go`

- [ ] **Step 1: Write tests for `checkRedis`**

`cmd/bot/preflight_test.go`:
```go
package main

import (
	"testing"
)

func TestCheckRedis_InvalidAddr(t *testing.T) {
	err := checkRedis("localhost:99999")
	if err == nil {
		t.Fatal("expected error for invalid redis address")
	}
}

func TestCheckRedis_EmptyAddr(t *testing.T) {
	err := checkRedis("")
	if err == nil {
		t.Fatal("expected error for empty address")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run:
```bash
go test ./cmd/bot/ -run TestCheckRedis -v
```
Expected: compilation error — `checkRedis` not defined

- [ ] **Step 3: Implement `checkRedis`**

`cmd/bot/preflight.go`:
```go
package main

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// checkRedis verifies connectivity by sending PING.
func checkRedis(addr string) error {
	if addr == "" {
		return fmt.Errorf("address is empty")
	}
	client := redis.NewClient(&redis.Options{Addr: addr})
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := client.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("dial %s: %w", addr, err)
	}
	return nil
}
```

- [ ] **Step 4: Run tests**

Run:
```bash
go test ./cmd/bot/ -run TestCheckRedis -v
```
Expected: both PASS (invalid addr returns error, empty addr returns error)

- [ ] **Step 5: Write tests for `checkGitHubToken`**

Append to `cmd/bot/preflight_test.go`:
```go
func TestCheckGitHubToken_EmptyToken(t *testing.T) {
	_, err := checkGitHubToken("")
	if err == nil {
		t.Fatal("expected error for empty token")
	}
}

func TestCheckGitHubToken_InvalidToken(t *testing.T) {
	_, err := checkGitHubToken("ghp_invalid_token_value")
	if err == nil {
		t.Fatal("expected error for invalid token")
	}
}
```

- [ ] **Step 6: Implement `checkGitHubToken`**

Append to `cmd/bot/preflight.go`:
```go
import (
	"encoding/json"
	"io"
	"net/http"
)

// checkGitHubToken validates the token by calling GET /user and GET /user/repos.
// Returns the authenticated username on success.
func checkGitHubToken(token string) (string, error) {
	if token == "" {
		return "", fmt.Errorf("token is empty")
	}

	// Step 1: verify identity
	req, _ := http.NewRequest("GET", "https://api.github.com/user", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("API request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 401 {
		return "", fmt.Errorf("invalid or expired token")
	}
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("unexpected status %d: %s", resp.StatusCode, body)
	}

	var user struct {
		Login string `json:"login"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}

	// Step 2: verify repo access
	req2, _ := http.NewRequest("GET", "https://api.github.com/user/repos?per_page=1", nil)
	req2.Header.Set("Authorization", "Bearer "+token)
	req2.Header.Set("Accept", "application/vnd.github+json")

	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		return "", fmt.Errorf("repo access check failed: %w", err)
	}
	defer resp2.Body.Close()

	if resp2.StatusCode == 403 || resp2.StatusCode == 404 {
		return "", fmt.Errorf("token lacks repository access (user: %s)", user.Login)
	}

	return user.Login, nil
}
```

- [ ] **Step 7: Run tests**

Run:
```bash
go test ./cmd/bot/ -run TestCheckGitHubToken -v
```
Expected: both PASS

- [ ] **Step 8: Write tests for `checkAgentCLI`**

Append to `cmd/bot/preflight_test.go`:
```go
func TestCheckAgentCLI_NotFound(t *testing.T) {
	_, err := checkAgentCLI("nonexistent_binary_xyz")
	if err == nil {
		t.Fatal("expected error for missing binary")
	}
}

func TestCheckAgentCLI_ValidBinary(t *testing.T) {
	// "go" is available in any Go test environment
	version, err := checkAgentCLI("go")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if version == "" {
		t.Fatal("expected non-empty version string")
	}
}
```

- [ ] **Step 9: Implement `checkAgentCLI`**

Append to `cmd/bot/preflight.go`:
```go
import (
	"os/exec"
	"strings"
)

// checkAgentCLI runs `<command> --version` and returns the first line of output.
func checkAgentCLI(command string) (string, error) {
	cmd := exec.Command(command, "--version")
	out, err := cmd.Output()
	if err != nil {
		if execErr, ok := err.(*exec.Error); ok {
			return "", fmt.Errorf("%s: %w", command, execErr)
		}
		return "", fmt.Errorf("%s --version failed: %w", command, err)
	}
	version := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
	return version, nil
}
```

- [ ] **Step 10: Run all preflight tests**

Run:
```bash
go test ./cmd/bot/ -run "TestCheck" -v
```
Expected: all PASS

- [ ] **Step 11: Commit**

```bash
git add cmd/bot/preflight.go cmd/bot/preflight_test.go
git commit -m "feat: add preflight check functions (redis, github, agent cli)"
```

---

### Task 3: Add interactive prompt helpers

**Files:**
- Modify: `cmd/bot/preflight.go`

- [ ] **Step 1: Implement prompt helpers**

Append to `cmd/bot/preflight.go`:
```go
import (
	"bufio"
	"os"
	"syscall"

	"golang.org/x/term"
)

var (
	stderr  = os.Stderr
	scanner = bufio.NewScanner(os.Stdin)
)

// printOK prints a success line to stderr.
func printOK(format string, args ...any) {
	fmt.Fprintf(stderr, "  \033[32m✓\033[0m %s\n", fmt.Sprintf(format, args...))
}

// printFail prints a failure line to stderr.
func printFail(format string, args ...any) {
	fmt.Fprintf(stderr, "  \033[31m✗\033[0m %s\n", fmt.Sprintf(format, args...))
}

// printWarn prints a warning line to stderr.
func printWarn(format string, args ...any) {
	fmt.Fprintf(stderr, "  \033[33m⚠\033[0m %s\n", fmt.Sprintf(format, args...))
}

// promptLine prints a prompt and reads a line of text input.
func promptLine(prompt string) string {
	fmt.Fprintf(stderr, "  %s", prompt)
	if scanner.Scan() {
		return strings.TrimSpace(scanner.Text())
	}
	return ""
}

// promptHidden prints a prompt and reads input without echo (for secrets).
func promptHidden(prompt string) string {
	fmt.Fprintf(stderr, "  %s", prompt)
	b, err := term.ReadPassword(int(syscall.Stdin))
	fmt.Fprintln(stderr) // newline after hidden input
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// promptYesNo prints a yes/no prompt. Default is yes.
func promptYesNo(prompt string) bool {
	answer := promptLine(fmt.Sprintf("%s [Y/n]: ", prompt))
	return answer == "" || strings.ToLower(answer) == "y" || strings.ToLower(answer) == "yes"
}
```

- [ ] **Step 2: Verify build**

Run:
```bash
go build ./cmd/bot/
```
Expected: success

- [ ] **Step 3: Commit**

```bash
git add cmd/bot/preflight.go
git commit -m "feat: add interactive prompt helpers for preflight"
```

---

### Task 4: Implement `runPreflight` orchestrator

**Files:**
- Modify: `cmd/bot/preflight.go`

- [ ] **Step 1: Implement the main preflight function**

Append to `cmd/bot/preflight.go`:
```go
import "agentdock/internal/config"

const maxRetries = 3

// runPreflight validates Redis, GitHub token, and agent CLI availability.
// In interactive mode (terminal + missing values), prompts the user.
// Returns error if validation fails.
func runPreflight(cfg *config.Config) error {
	interactive := term.IsTerminal(int(syscall.Stdin)) && needsInput(cfg)

	fmt.Fprintln(stderr)

	// --- Redis ---
	if cfg.Redis.Addr == "" {
		if !interactive {
			return fmt.Errorf("REDIS_ADDR is required")
		}
		for attempt := 1; attempt <= maxRetries; attempt++ {
			addr := promptLine("Redis address: ")
			if addr == "" {
				printFail("Redis address is required")
				if attempt < maxRetries {
					continue
				}
				return fmt.Errorf("max retries exceeded for Redis address")
			}
			if err := checkRedis(addr); err != nil {
				printFail("Redis connect failed: %v (attempt %d/%d)", err, attempt, maxRetries)
				if attempt == maxRetries {
					return fmt.Errorf("max retries exceeded for Redis")
				}
				continue
			}
			cfg.Redis.Addr = addr
			printOK("Redis connected")
			break
		}
	} else {
		if err := checkRedis(cfg.Redis.Addr); err != nil {
			printFail("Redis connect failed: %v", err)
			return err
		}
		printOK("Redis connected (%s)", cfg.Redis.Addr)
	}

	// --- GitHub Token ---
	if cfg.GitHub.Token == "" {
		if !interactive {
			return fmt.Errorf("GITHUB_TOKEN is required")
		}
		fmt.Fprintln(stderr)
		fmt.Fprintln(stderr, "  GitHub token (ghp_... or github_pat_...):")
		fmt.Fprintln(stderr, "  Generate at: https://github.com/settings/tokens")
		fmt.Fprintln(stderr, "  Required permissions: Contents (Read), Issues (Write)")
		for attempt := 1; attempt <= maxRetries; attempt++ {
			token := promptHidden("Token: ")
			if token == "" {
				printFail("Token is required")
				if attempt < maxRetries {
					continue
				}
				return fmt.Errorf("max retries exceeded for GitHub token")
			}
			username, err := checkGitHubToken(token)
			if err != nil {
				printFail("%v (attempt %d/%d)", err, attempt, maxRetries)
				if attempt == maxRetries {
					return fmt.Errorf("max retries exceeded for GitHub token")
				}
				continue
			}
			cfg.GitHub.Token = token
			printOK("Token valid (user: %s)", username)
			break
		}
	} else {
		username, err := checkGitHubToken(cfg.GitHub.Token)
		if err != nil {
			printFail("GitHub token invalid: %v", err)
			return err
		}
		printOK("Token valid (user: %s)", username)
	}

	// --- Providers ---
	if len(cfg.Providers) == 0 {
		if !interactive {
			return fmt.Errorf("PROVIDERS is required")
		}
		fmt.Fprintln(stderr)
		agents := sortedAgentNames(cfg)
		fmt.Fprintln(stderr, "  Available providers:")
		for i, name := range agents {
			fmt.Fprintf(stderr, "    %d) %s\n", i+1, name)
		}
		for attempt := 1; attempt <= maxRetries; attempt++ {
			input := promptLine("Select (comma-separated, e.g. 1,2): ")
			selected := parseSelection(input, agents)
			if len(selected) == 0 {
				printFail("At least one provider is required (attempt %d/%d)", attempt, maxRetries)
				if attempt == maxRetries {
					return fmt.Errorf("max retries exceeded for provider selection")
				}
				continue
			}
			cfg.Providers = selected
			break
		}
	}

	// --- Agent CLI version check ---
	fmt.Fprintln(stderr)
	var validProviders []string
	for _, name := range cfg.Providers {
		agent, ok := cfg.Agents[name]
		if !ok {
			printWarn("%s: not configured in agents", name)
			continue
		}
		version, err := checkAgentCLI(agent.Command)
		if err != nil {
			printWarn("%s: %v", name, err)
			continue
		}
		printOK("%s %s", name, version)
		validProviders = append(validProviders, name)
	}

	if len(validProviders) == 0 {
		printFail("No providers available")
		return fmt.Errorf("all providers failed CLI check")
	}

	if len(validProviders) < len(cfg.Providers) {
		if interactive {
			if !promptYesNo("\n  Some providers are unavailable. Continue anyway?") {
				return fmt.Errorf("user cancelled")
			}
		}
		cfg.Providers = validProviders
	}

	fmt.Fprintf(stderr, "\n  Starting worker with: %s\n\n", strings.Join(cfg.Providers, ", "))
	return nil
}

// needsInput returns true if any required config value is empty.
func needsInput(cfg *config.Config) bool {
	return cfg.Redis.Addr == "" || cfg.GitHub.Token == "" || len(cfg.Providers) == 0
}

// sortedAgentNames returns agent names from config in stable order.
func sortedAgentNames(cfg *config.Config) []string {
	names := make([]string, 0, len(cfg.Agents))
	for name := range cfg.Agents {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// parseSelection parses "1,2" style input into agent names.
func parseSelection(input string, agents []string) []string {
	var selected []string
	for _, part := range strings.Split(input, ",") {
		part = strings.TrimSpace(part)
		idx := 0
		if _, err := fmt.Sscanf(part, "%d", &idx); err == nil && idx >= 1 && idx <= len(agents) {
			selected = append(selected, agents[idx-1])
		}
	}
	return selected
}
```

- [ ] **Step 2: Add missing import `sort`**

Ensure the import block at the top of `cmd/bot/preflight.go` includes `sort`.

- [ ] **Step 3: Verify build**

Run:
```bash
go build ./cmd/bot/
```
Expected: success

- [ ] **Step 4: Write tests for `parseSelection` and `needsInput`**

Append to `cmd/bot/preflight_test.go`:
```go
import "agentdock/internal/config"

func TestParseSelection_Valid(t *testing.T) {
	agents := []string{"claude", "codex", "opencode"}
	got := parseSelection("1,3", agents)
	want := []string{"claude", "opencode"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("got[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestParseSelection_Invalid(t *testing.T) {
	agents := []string{"claude", "codex"}
	got := parseSelection("0,5,abc", agents)
	if len(got) != 0 {
		t.Fatalf("expected empty, got %v", got)
	}
}

func TestParseSelection_Empty(t *testing.T) {
	agents := []string{"claude"}
	got := parseSelection("", agents)
	if len(got) != 0 {
		t.Fatalf("expected empty, got %v", got)
	}
}

func TestNeedsInput_AllEmpty(t *testing.T) {
	cfg := &config.Config{}
	if !needsInput(cfg) {
		t.Fatal("expected true when all values empty")
	}
}

func TestNeedsInput_AllSet(t *testing.T) {
	cfg := &config.Config{
		Providers: []string{"claude"},
	}
	cfg.Redis.Addr = "localhost:6379"
	cfg.GitHub.Token = "ghp_test"
	if needsInput(cfg) {
		t.Fatal("expected false when all values set")
	}
}
```

- [ ] **Step 5: Run all tests**

Run:
```bash
go test ./cmd/bot/ -run "TestParseSelection|TestNeedsInput" -v
```
Expected: all PASS

- [ ] **Step 6: Commit**

```bash
git add cmd/bot/preflight.go cmd/bot/preflight_test.go
git commit -m "feat: implement runPreflight orchestrator with interactive prompts"
```

---

### Task 5: Wire preflight into `runWorker()`

**Files:**
- Modify: `cmd/bot/worker.go`

- [ ] **Step 1: Move slog init after preflight and insert `runPreflight` call**

In `cmd/bot/worker.go`, replace the section from after config loading to Redis client creation. The current code (lines 23-51):

```go
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))

	var cfg *config.Config
	var err error
	if *configPath != "" {
		cfg, err = config.Load(*configPath)
		if err != nil {
			slog.Error("failed to load config", "error", err)
			os.Exit(1)
		}
	} else {
		cfg, err = config.LoadDefaults()
		if err != nil {
			slog.Error("failed to load defaults", "error", err)
			os.Exit(1)
		}
	}

	rdb, err := queue.NewRedisClient(queue.RedisConfig{
		Addr:     cfg.Redis.Addr,
		Password: cfg.Redis.Password,
		DB:       cfg.Redis.DB,
		TLS:      cfg.Redis.TLS,
	})
	if err != nil {
		slog.Error("failed to connect to Redis", "error", err)
		os.Exit(1)
	}
	slog.Info("connected to Redis", "addr", cfg.Redis.Addr)
```

Replace with:

```go
	var cfg *config.Config
	var err error
	if *configPath != "" {
		cfg, err = config.Load(*configPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  ✗ failed to load config: %v\n", err)
			os.Exit(1)
		}
	} else {
		cfg, err = config.LoadDefaults()
		if err != nil {
			fmt.Fprintf(os.Stderr, "  ✗ failed to load defaults: %v\n", err)
			os.Exit(1)
		}
	}

	// Preflight: validate dependencies, prompt if interactive.
	if err := runPreflight(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "  Error: %v\n", err)
		os.Exit(1)
	}

	// slog initialized AFTER preflight to keep interactive output clean.
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))

	rdb, err := queue.NewRedisClient(queue.RedisConfig{
		Addr:     cfg.Redis.Addr,
		Password: cfg.Redis.Password,
		DB:       cfg.Redis.DB,
		TLS:      cfg.Redis.TLS,
	})
	if err != nil {
		slog.Error("failed to connect to Redis", "error", err)
		os.Exit(1)
	}
	slog.Info("connected to Redis", "addr", cfg.Redis.Addr)
```

- [ ] **Step 2: Add `"fmt"` to imports in worker.go if not present**

Ensure `"fmt"` is in the import block of `cmd/bot/worker.go`.

- [ ] **Step 3: Verify build**

Run:
```bash
go build ./cmd/bot/
```
Expected: success

- [ ] **Step 4: Run full test suite**

Run:
```bash
go test ./... -count=1
```
Expected: all PASS

- [ ] **Step 5: Manual smoke test — non-interactive mode with env vars**

Run:
```bash
REDIS_ADDR=localhost:6379 GITHUB_TOKEN=ghp_invalid PROVIDERS=claude ./bot worker
```
Expected: `✗ invalid or expired token` then exit

- [ ] **Step 6: Manual smoke test — interactive mode (no env vars)**

Run:
```bash
./bot worker
```
Expected: prompts for Redis address, then GitHub token, then provider selection

- [ ] **Step 7: Commit**

```bash
git add cmd/bot/worker.go
git commit -m "feat: wire preflight into worker startup"
```

---

### Task 6: Update operations docs

**Files:**
- Modify: `docs/operations.md`

- [ ] **Step 1: Add worker startup section to operations.md**

Add after the existing "HTTP Endpoints" section:

```markdown
## Worker 啟動

### 互動模式（本地開發）

直接執行，缺少的參數會互動式提問：

\```bash
./bot worker
\```

### 非互動模式（env 帶齊）

\```bash
REDIS_ADDR=<host>:<port> GITHUB_TOKEN=<token> PROVIDERS=claude ./bot worker
\```

### Preflight 驗證項目

| 檢查項 | 驗證方式 | 失敗行為 |
|--------|---------|---------|
| Redis 連線 | PING | 互動：重新輸入（最多 3 次）；非互動：退出 |
| GitHub Token | GET /user + GET /user/repos | 互動：重新輸入（最多 3 次）；非互動：退出 |
| Agent CLI | `<cmd> --version` | 警告，至少一個可用才啟動 |
```

- [ ] **Step 2: Commit**

```bash
git add docs/operations.md
git commit -m "docs: add worker preflight startup section"
```
