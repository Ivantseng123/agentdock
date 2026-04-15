# PR #33 Review Fixes Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix three merge-blocking issues found in code review of PR #33 (cobra + koanf CLI migration).

**Architecture:** Three independent, small fixes on the `feat/cobra-cli-migration` branch. Each fix gets its own test-first commit. All changes are in `cmd/agentdock/`.

**Tech Stack:** Go, spf13/cobra, knadh/koanf, golang.org/x/term

**Important:** All work happens on the `feat/cobra-cli-migration` branch. Before starting, run:
```bash
git checkout feat/cobra-cli-migration
```

---

### Task 1: Fix `atomicWrite` stale tmp file permissions

**Files:**
- Modify: `cmd/agentdock/init_test.go` (add test)
- Modify: `cmd/agentdock/init.go:115-121` (`atomicWrite` function)

- [ ] **Step 1: Write the failing test**

Add to `cmd/agentdock/init_test.go`:

```go
func TestAtomicWrite_RemovesStaleTmp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	tmp := path + ".tmp"

	// Simulate stale .tmp from a previous failed write with lax permissions.
	if err := os.WriteFile(tmp, []byte("stale"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := atomicWrite(path, []byte("fresh"), 0600); err != nil {
		t.Fatalf("atomicWrite: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("file mode = %o, want 0600", info.Mode().Perm())
	}

	// .tmp should not linger.
	if _, err := os.Stat(tmp); err == nil {
		t.Error(".tmp file should not exist after successful atomicWrite")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/agentdock/ -run TestAtomicWrite_RemovesStaleTmp -v`
Expected: FAIL — the stale `.tmp` keeps its 0644 permissions, so the renamed file inherits 0644 instead of 0600.

- [ ] **Step 3: Write minimal implementation**

In `cmd/agentdock/init.go`, modify `atomicWrite`:

```go
func atomicWrite(path string, data []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	os.Remove(tmp) // Ensure WriteFile creates a new file so mode takes effect.
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/agentdock/ -run TestAtomicWrite_RemovesStaleTmp -v`
Expected: PASS

- [ ] **Step 5: Run full init test suite**

Run: `go test ./cmd/agentdock/ -run TestInit -v`
Expected: All init tests PASS (no regressions).

- [ ] **Step 6: Commit**

```bash
git add cmd/agentdock/init.go cmd/agentdock/init_test.go
git commit -m "fix(cli): remove stale .tmp before atomicWrite to preserve file mode

A leftover .tmp from a previous failed write retains its old permissions.
os.WriteFile does not chmod existing files, so the renamed config could
briefly sit at lax permissions. os.Remove(tmp) ensures WriteFile always
creates a fresh file with the requested 0600 mode."
```

---

### Task 2: Fix `warnUnknownKeys` false negative on nested struct keys

**Files:**
- Modify: `cmd/agentdock/config_test.go` (add test)
- Modify: `cmd/agentdock/config.go:245-295` (`validKoanfKeys`, `walkYAMLPathsKeyOnly`, `warnUnknownKeys`)

- [ ] **Step 1: Write the failing test**

Add to `cmd/agentdock/config_test.go`:

```go
func TestWarnUnknownKeys_NestedStructKey(t *testing.T) {
	clearEnv(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "test.yaml")
	// queue.bogus is under a struct (QueueConfig), should warn.
	// agents.myagent.command is under a map, should NOT warn.
	yamlBody := `
queue:
  capacity: 50
  bogus: true
agents:
  myagent:
    command: echo
`
	if err := os.WriteFile(path, []byte(yamlBody), 0600); err != nil {
		t.Fatal(err)
	}

	cmd := &cobra.Command{Use: "test"}
	addPersistentFlags(cmd)

	var logBuf strings.Builder
	oldHandler := slog.Default().Handler()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	defer slog.SetDefault(slog.New(oldHandler))

	_, _, _, _, err := buildKoanf(cmd, path)
	if err != nil {
		t.Fatal(err)
	}

	logOutput := logBuf.String()

	// queue.bogus must trigger a warning.
	if !strings.Contains(logOutput, "queue.bogus") {
		t.Errorf("expected warn about 'queue.bogus', got log:\n%s", logOutput)
	}

	// agents.myagent.command must NOT trigger a warning (map type).
	if strings.Contains(logOutput, "agents.myagent") {
		t.Errorf("should not warn about agents sub-keys, got log:\n%s", logOutput)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/agentdock/ -run TestWarnUnknownKeys_NestedStructKey -v`
Expected: FAIL — `queue.bogus` does not produce a warning because the current code short-circuits on `valid["queue"] == true`.

- [ ] **Step 3: Write minimal implementation**

In `cmd/agentdock/config.go`, replace the three functions:

Replace `validKoanfKeys`:
```go
// validKoanfKeys returns the set of valid dotted koanf paths and the set of
// top-level keys whose Config type is a map (allowing arbitrary sub-keys).
func validKoanfKeys() (valid map[string]bool, mapKeys map[string]bool) {
	valid = map[string]bool{}
	mapKeys = map[string]bool{}
	walkYAMLPathsKeyOnly(reflect.TypeOf(config.Config{}), "", valid, mapKeys)
	return
}
```

Replace `walkYAMLPathsKeyOnly`:
```go
func walkYAMLPathsKeyOnly(t reflect.Type, prefix string, out map[string]bool, mapKeys map[string]bool) {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return
	}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := strings.Split(f.Tag.Get("yaml"), ",")[0]
		if tag == "" || tag == "-" {
			continue
		}
		path := tag
		if prefix != "" {
			path = prefix + "." + tag
		}
		out[path] = true
		ft := f.Type
		if ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		if ft.Kind() == reflect.Map {
			// Map-typed fields have dynamic sub-keys (e.g. agents, channels).
			// Record and skip — sub-keys are user-defined, not schema-validated.
			// NOTE: only top-level maps are handled. If a nested struct ever
			// contains a map field, extend warnUnknownKeys to check prefixes.
			mapKeys[path] = true
			continue
		}
		if ft.Kind() == reflect.Struct {
			walkYAMLPathsKeyOnly(ft, path, out, mapKeys)
		}
	}
}
```

Replace `warnUnknownKeys`:
```go
// warnUnknownKeys logs warnings for any koanf key not in the valid Config
// schema. Map-valued fields (e.g. channels, agents, channel_priority) allow
// arbitrary sub-keys and are skipped entirely.
func warnUnknownKeys(k *koanf.Koanf) {
	valid, mapKeys := validKoanfKeys()
	for _, key := range k.Keys() {
		topLevel := strings.SplitN(key, ".", 2)[0]
		if mapKeys[topLevel] {
			continue
		}
		if !valid[key] {
			slog.Warn("unknown config key", "key", key)
		}
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/agentdock/ -run TestWarnUnknownKeys_NestedStructKey -v`
Expected: PASS

- [ ] **Step 5: Run existing unknown-key test to check for regressions**

Run: `go test ./cmd/agentdock/ -run TestBuildKoanf_WarnsOnUnknownKey -v`
Expected: PASS — the existing test checks a completely unknown top-level key (`reactions`), which should still warn.

- [ ] **Step 6: Commit**

```bash
git add cmd/agentdock/config.go cmd/agentdock/config_test.go
git commit -m "fix(cli): warn on unknown nested struct keys in config

warnUnknownKeys short-circuited on any valid top-level key, silently
accepting typos like queue.bogus. Now uses reflection to distinguish
map-typed fields (agents, channels, channel_priority) whose sub-keys
are dynamic from struct-typed fields that must match the schema."
```

---

### Task 3: Add TTY guard to `init -i`

**Files:**
- Modify: `cmd/agentdock/init_test.go` (add test)
- Modify: `cmd/agentdock/init.go:3-14` (imports) and `cmd/agentdock/init.go:45-48` (`runInit` top)

- [ ] **Step 1: Write the failing test**

Add to `cmd/agentdock/init_test.go`:

```go
func TestInitInteractive_RejectsNonTTY(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	err := runInit(path, true, false)
	if err == nil {
		t.Fatal("expected error for interactive mode without TTY")
	}
	if !strings.Contains(err.Error(), "requires a terminal") {
		t.Errorf("expected 'requires a terminal' error, got: %v", err)
	}
	// File should NOT have been created.
	if _, statErr := os.Stat(path); statErr == nil {
		t.Error("config file should not exist after TTY rejection")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/agentdock/ -run TestInitInteractive_RejectsNonTTY -v`
Expected: FAIL — `runInit` currently enters interactive mode unconditionally and the prompt functions fail or hang without a TTY.

- [ ] **Step 3: Write minimal implementation**

In `cmd/agentdock/init.go`, add imports `"syscall"` and `"golang.org/x/term"`:

```go
import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"agentdock/internal/config"

	"github.com/spf13/cobra"
	"golang.org/x/term"
	"gopkg.in/yaml.v3"
)
```

Add the TTY guard as the first line in `runInit`, before the `os.Stat` check:

```go
func runInit(path string, interactive, force bool) error {
	if interactive && !term.IsTerminal(int(syscall.Stdin)) {
		return fmt.Errorf("--interactive requires a terminal (stdin is not a TTY)")
	}
	if _, err := os.Stat(path); err == nil && !force {
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/agentdock/ -run TestInitInteractive_RejectsNonTTY -v`
Expected: PASS

- [ ] **Step 5: Run full init test suite**

Run: `go test ./cmd/agentdock/ -run TestInit -v`
Expected: All init tests PASS (no regressions — existing tests use `interactive=false`).

- [ ] **Step 6: Commit**

```bash
git add cmd/agentdock/init.go cmd/agentdock/init_test.go
git commit -m "fix(cli): reject init -i when stdin is not a TTY

Without a terminal, promptHidden silently returns empty strings and the
retry loop fails 3 times with confusing errors. Now fails fast with a
clear message, consistent with preflight.go's term.IsTerminal guard."
```

---

### Task 4: Final validation

- [ ] **Step 1: Run full package tests**

Run: `go test ./cmd/agentdock/... -v`
Expected: All tests PASS.

- [ ] **Step 2: Run go vet**

Run: `go vet ./cmd/agentdock/...`
Expected: Clean, no warnings.

- [ ] **Step 3: Verify build**

Run: `go build ./cmd/agentdock/`
Expected: Clean build, no errors.
