# CLI Cobra Migration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把 AgentDock CLI 從 stdlib `flag` 換成 spf13/cobra + knadh/koanf，提供 `app` / `worker` / `init` 子命令，所有 scalar 配置開出 flag，merge 後 delta-only 寫回 `~/.config/agentdock/config.yaml`，hard break 至 v1.0.0。

**Architecture:** cobra 命令樹（root + 三個 subcommand）+ koanf 兩 instance（kEff for runtime / kSave for persistence，env 只進 kEff）+ 顯式 `flagToKey` 映射表把 dash-flag 接到 yaml snake_case 路徑。

**Tech Stack:** Go 1.25, `spf13/cobra`, `spf13/pflag`, `knadh/koanf/v2`, `gopkg.in/yaml.v3`（已有）, `golang.org/x/term`（已有）

**Spec:** `docs/superpowers/specs/2026-04-15-cli-cobra-migration-design.md` (commit `9705c9a`)

**Related Issue:** [#32](https://github.com/Ivantseng123/agentdock/issues/32)

---

## File Structure

### Files to Create

```
cmd/agentdock/                     # was cmd/bot/, all renamed
  root.go                          # rootCmd, persistent flags, version vars
  app.go                           # appCmd, runs current main() body
  init.go                          # initCmd (non-interactive + -i)
  flags.go                         # flag registration helpers + flagToKey map + pflag enums
  config.go                        # buildKoanf, mergeBuiltinAgents, saveConfig
  validate.go                      # validate(cfg) cross-field validation
  prompts.go                       # promptLine/promptHidden/promptYesNo + check helpers
  adapters.go                      # agentRunnerAdapter / repoCacheAdapter / slackPosterAdapter

internal/config/
  builtin_agents.go                # BuiltinAgents map (claude/codex/opencode)

docs/
  MIGRATION-v1.md                  # v0.x → v1.0 upgrade guide
```

### Files to Modify

```
cmd/agentdock/main.go              # was cmd/bot/main.go, becomes ~10 lines
cmd/agentdock/worker.go            # was cmd/bot/worker.go, refactored as workerCmd
cmd/agentdock/preflight.go         # was cmd/bot/preflight.go, runPreflight gains scope param
cmd/agentdock/local_adapter.go     # was cmd/bot/local_adapter.go, just moved
internal/config/config.go          # add EnvOverrideMap, DefaultsMap; remove Load/LoadDefaults/applyEnvOverrides
Dockerfile                         # ./cmd/bot/ → ./cmd/agentdock/, binary bot → agentdock
run.sh                             # same path/name updates
.github/workflows/*.yml            # same
README.md                          # new usage; link MIGRATION-v1.md
go.mod / go.sum                    # add cobra, koanf, providers
```

### Files to Delete

```
config.example.yaml                # D22: init replaces it
```

---

## Phase 1: Refactor without semantic change

Goal: rename directory + extract helpers. Old `Load()` / `LoadDefaults()` / `applyEnvOverrides()` STILL EXIST. Build + tests pass.

### Task 1: Rename `cmd/bot/` → `cmd/agentdock/` (no logic change)

**Files:**
- Rename: `cmd/bot/*` → `cmd/agentdock/*` (8 files: main.go, worker.go, preflight.go, local_adapter.go, plus their `_test.go`)
- Modify: `Dockerfile` (path + binary name)
- Modify: `run.sh` (path + binary name)
- Modify: `.github/workflows/*.yml` (any reference)

- [ ] **Step 1: Git mv the directory**

```bash
git mv cmd/bot cmd/agentdock
```

- [ ] **Step 2: Verify no internal Go imports broken**

Run:
```bash
go build ./...
```

Expected: PASS (cmd/agentdock/ files are `package main`, internal imports use `agentdock/internal/...` not `cmd/bot/...`).

- [ ] **Step 3: Update `Dockerfile`**

Find references to `cmd/bot` and binary name `bot`. Replace `./cmd/bot/` → `./cmd/agentdock/` and `bot` (binary) → `agentdock`. Common patterns:

```dockerfile
RUN go build -o /agentdock ./cmd/agentdock/
ENTRYPOINT ["/agentdock"]
```

(Subcommand `app` / `worker` left to docker-compose / k8s manifest, not Dockerfile.)

- [ ] **Step 4: Update `run.sh`**

Replace `./cmd/bot/` → `./cmd/agentdock/` and any output binary name `bot` → `agentdock`.

- [ ] **Step 5: Update `.github/workflows/*.yml`**

`grep -rl 'cmd/bot' .github/` shows files. For each, replace `cmd/bot` → `cmd/agentdock` and binary name where applicable. Likely affects release / docker / test workflows.

- [ ] **Step 6: Run all tests + build**

Run:
```bash
go build ./...
go test ./...
```

Expected: 150 tests pass (no test changes; just file paths moved).

- [ ] **Step 7: Commit**

```bash
git add cmd/ Dockerfile run.sh .github/
git commit -m "refactor: rename cmd/bot to cmd/agentdock"
```

---

### Task 2: Add `EnvOverrideMap()` helper to `internal/config/config.go`

**Files:**
- Modify: `internal/config/config.go` (add new function alongside existing `applyEnvOverrides`)
- Test: `internal/config/config_test.go` (add new test)

- [ ] **Step 1: Write failing test in `internal/config/config_test.go`**

```go
func TestEnvOverrideMap(t *testing.T) {
    t.Setenv("REDIS_ADDR", "10.0.0.1:6379")
    t.Setenv("GITHUB_TOKEN", "ghp_test")
    t.Setenv("PROVIDERS", "claude,codex")
    t.Setenv("PROVIDERS_EMPTY", "")  // not used; ensures empty doesn't show

    m := EnvOverrideMap()

    if got := m["redis.addr"]; got != "10.0.0.1:6379" {
        t.Errorf("redis.addr = %v, want 10.0.0.1:6379", got)
    }
    if got := m["github.token"]; got != "ghp_test" {
        t.Errorf("github.token = %v, want ghp_test", got)
    }
    providers, ok := m["providers"].([]string)
    if !ok || len(providers) != 2 || providers[0] != "claude" || providers[1] != "codex" {
        t.Errorf("providers = %v, want [claude codex]", m["providers"])
    }
}

func TestEnvOverrideMap_Unset(t *testing.T) {
    t.Setenv("REDIS_ADDR", "")  // explicit clear
    m := EnvOverrideMap()
    if _, ok := m["redis.addr"]; ok {
        t.Errorf("redis.addr should be absent when env empty, got %v", m["redis.addr"])
    }
}

func TestEnvOverrideMap_ProvidersFiltersEmpty(t *testing.T) {
    t.Setenv("PROVIDERS", "claude,,codex,")
    m := EnvOverrideMap()
    providers := m["providers"].([]string)
    if len(providers) != 2 || providers[0] != "claude" || providers[1] != "codex" {
        t.Errorf("providers should filter empty tokens, got %v", providers)
    }
}
```

- [ ] **Step 2: Run test, verify it fails**

Run:
```bash
go test ./internal/config/ -run TestEnvOverrideMap -v
```

Expected: FAIL with `undefined: EnvOverrideMap`.

- [ ] **Step 3: Implement `EnvOverrideMap()` in `internal/config/config.go`**

Add (just append to file; do NOT remove existing `applyEnvOverrides` yet):

```go
// EnvOverrideMap returns a koanf-friendly map[string]any of values currently
// set in env vars. Maps each known env var to its koanf path. Unset env vars
// are absent from the result. Used by cmd/agentdock to build the env layer in
// the koanf provider chain.
func EnvOverrideMap() map[string]any {
    out := map[string]any{}
    if v := os.Getenv("SLACK_BOT_TOKEN"); v != "" {
        out["slack.bot_token"] = v
    }
    if v := os.Getenv("SLACK_APP_TOKEN"); v != "" {
        out["slack.app_token"] = v
    }
    if v := os.Getenv("GITHUB_TOKEN"); v != "" {
        out["github.token"] = v
    }
    if v := os.Getenv("MANTIS_API_TOKEN"); v != "" {
        out["mantis.api_token"] = v
    }
    if v := os.Getenv("REDIS_ADDR"); v != "" {
        out["redis.addr"] = v
    }
    if v := os.Getenv("REDIS_PASSWORD"); v != "" {
        out["redis.password"] = v
    }
    if v := os.Getenv("ACTIVE_AGENT"); v != "" {
        out["active_agent"] = v
    }
    if v := os.Getenv("PROVIDERS"); v != "" {
        // Filter empty tokens (handles "a,,b," etc.)
        var providers []string
        for _, p := range strings.Split(v, ",") {
            if p = strings.TrimSpace(p); p != "" {
                providers = append(providers, p)
            }
        }
        if len(providers) > 0 {
            out["providers"] = providers
        }
    }
    return out
}
```

- [ ] **Step 4: Run tests, verify they pass**

Run:
```bash
go test ./internal/config/ -run TestEnvOverrideMap -v
```

Expected: PASS for all 3 tests.

- [ ] **Step 5: Run full test suite**

Run:
```bash
go test ./...
```

Expected: 150 + 3 = 153 tests pass. (Existing `applyEnvOverrides` callers untouched.)

- [ ] **Step 6: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat(config): add EnvOverrideMap helper for koanf env layer"
```

---

### Task 3: Add `DefaultsMap()` helper to `internal/config/config.go`

**Files:**
- Modify: `internal/config/config.go`
- Test: `internal/config/config_test.go`

Per D12: derive from `applyDefaults(empty Config)` round-trip. Single source of truth.

- [ ] **Step 1: Write failing test**

```go
func TestDefaultsMap(t *testing.T) {
    m := DefaultsMap()

    // Spot-check non-zero defaults set by applyDefaults
    workers, ok := m["workers"].(map[string]any)
    if !ok {
        t.Fatalf("workers should be a map, got %T", m["workers"])
    }
    if got := workers["count"]; got != 3 {
        t.Errorf("workers.count = %v, want 3", got)
    }

    queue, _ := m["queue"].(map[string]any)
    if got := queue["transport"]; got != "inmem" {
        t.Errorf("queue.transport = %v, want inmem", got)
    }
    if got := queue["capacity"]; got != 50 {
        t.Errorf("queue.capacity = %v, want 50", got)
    }

    logging, _ := m["logging"].(map[string]any)
    if got := logging["dir"]; got != "logs" {
        t.Errorf("logging.dir = %v, want logs", got)
    }
}

func TestDefaultsMap_AgreesWithApplyDefaults(t *testing.T) {
    // The map must match what applyDefaults produces
    var cfg Config
    applyDefaults(&cfg)

    m := DefaultsMap()
    workers := m["workers"].(map[string]any)
    if got := workers["count"]; got != cfg.Workers.Count {
        t.Errorf("DefaultsMap.workers.count=%v != applyDefaults.Workers.Count=%v", got, cfg.Workers.Count)
    }
}
```

- [ ] **Step 2: Run, verify fail**

Run:
```bash
go test ./internal/config/ -run TestDefaultsMap -v
```

Expected: FAIL with `undefined: DefaultsMap`.

- [ ] **Step 3: Implement `DefaultsMap()`**

Append to `internal/config/config.go`:

```go
// DefaultsMap returns a koanf-friendly map[string]any of all default values
// produced by applyDefaults. Round-trips via YAML to preserve nested struct
// shape and yaml tags. applyDefaults is the single source of truth; this is
// just a different representation for the koanf provider chain.
func DefaultsMap() map[string]any {
    var cfg Config
    applyDefaults(&cfg)
    data, err := yaml.Marshal(&cfg)
    if err != nil {
        // Should not happen with our struct; panic on bug
        panic(fmt.Sprintf("DefaultsMap marshal failed: %v", err))
    }
    out := map[string]any{}
    if err := yaml.Unmarshal(data, &out); err != nil {
        panic(fmt.Sprintf("DefaultsMap unmarshal failed: %v", err))
    }
    return out
}
```

Add `"fmt"` to imports if not already present.

- [ ] **Step 4: Run, verify pass**

Run:
```bash
go test ./internal/config/ -run TestDefaultsMap -v
go test ./...
```

Expected: All pass.

- [ ] **Step 5: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat(config): add DefaultsMap helper derived from applyDefaults"
```

---

### Task 4: Add `BuiltinAgents` map in `internal/config/builtin_agents.go`

**Files:**
- Create: `internal/config/builtin_agents.go`
- Test: `internal/config/builtin_agents_test.go`

Per D16: BuiltinAgents serves as runtime fallback. `init` writes them too, but runtime doesn't depend on file having them.

- [ ] **Step 1: Write failing test in new file `internal/config/builtin_agents_test.go`**

```go
package config

import "testing"

func TestBuiltinAgents_HasExpected(t *testing.T) {
    expected := []string{"claude", "codex", "opencode"}
    for _, name := range expected {
        agent, ok := BuiltinAgents[name]
        if !ok {
            t.Errorf("BuiltinAgents missing %q", name)
            continue
        }
        if agent.Command == "" {
            t.Errorf("BuiltinAgents[%q].Command is empty", name)
        }
        if agent.SkillDir == "" {
            t.Errorf("BuiltinAgents[%q].SkillDir is empty", name)
        }
    }
}
```

- [ ] **Step 2: Run, verify fail**

```bash
go test ./internal/config/ -run TestBuiltinAgents -v
```

Expected: FAIL `undefined: BuiltinAgents`.

- [ ] **Step 3: Create `internal/config/builtin_agents.go`**

Copy the agents map literal from current `LoadDefaults()` (`internal/config/config.go:170-196`):

```go
package config

// BuiltinAgents is the canonical registry of agent CLI configurations shipped
// with AgentDock. config files may override individual entries by defining a
// same-named entry under `agents:`; missing names fall back to these defaults.
//
// Adding a new built-in agent: just add an entry here. Existing users get it
// automatically on next startup; no `init` rerun needed.
var BuiltinAgents = map[string]AgentConfig{
    "claude": {
        Command:  "claude",
        Args:     []string{"--print", "--output-format", "stream-json", "-p", "{prompt}"},
        SkillDir: ".claude/skills",
        Stream:   true,
    },
    "codex": {
        Command:  "codex",
        Args:     []string{"--print", "--output-format", "stream-json", "-p", "{prompt}"},
        SkillDir: ".codex/skills",
        Stream:   true,
    },
    "opencode": {
        Command:  "opencode",
        Args:     []string{"--prompt", "{prompt}"},
        SkillDir: ".opencode/skills",
    },
}
```

- [ ] **Step 4: Run, verify pass**

```bash
go test ./internal/config/ -v
```

Expected: All pass including new `TestBuiltinAgents_HasExpected`.

- [ ] **Step 5: Commit**

```bash
git add internal/config/builtin_agents.go internal/config/builtin_agents_test.go
git commit -m "feat(config): extract BuiltinAgents map for runtime fallback"
```

---

## Phase 2: Cobra skeleton (still uses old Load() inside RunE)

Goal: introduce cobra command tree. RunE bodies CALL THE EXISTING `runWorker()` / main bot body verbatim. Old `config.Load()` flow unchanged.

### Task 5: Add `cobra` dependency

**Files:** `go.mod`, `go.sum`

- [ ] **Step 1: Add cobra**

```bash
go get github.com/spf13/cobra@latest
```

- [ ] **Step 2: Verify download**

```bash
grep cobra go.mod
```

Expected: line like `github.com/spf13/cobra v1.x.x`.

- [ ] **Step 3: Verify build still works**

```bash
go build ./...
```

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add go.mod go.sum
git commit -m "build: add spf13/cobra dependency"
```

---

### Task 6: Create cobra root command in `cmd/agentdock/root.go`

**Files:**
- Create: `cmd/agentdock/root.go`
- Modify: `cmd/agentdock/main.go` (gut to ~10 lines that call Execute)

- [ ] **Step 1: Create `cmd/agentdock/root.go`**

```go
package main

import (
    "fmt"
    "os"

    "github.com/spf13/cobra"
)

var (
    version = "dev"
    commit  = "unknown"
    date    = "unknown"
)

var rootCmd = &cobra.Command{
    Use:   "agentdock",
    Short: "AgentDock — Slack to GitHub issue triage",
    Long:  "AgentDock turns Slack threads into structured GitHub issues with AI-assisted triage.",
    Version: fmt.Sprintf("%s (commit %s, built %s)", version, commit, date),
}

// Execute runs the root command. Called from main().
func Execute() {
    if err := rootCmd.Execute(); err != nil {
        os.Exit(1)
    }
}
```

- [ ] **Step 2: Gut `cmd/agentdock/main.go`**

Replace entire file content (was ~440 lines, now ~7):

```go
package main

func main() {
    Execute()
}
```

(All previous main() body will move into appCmd.RunE in Task 7.)

- [ ] **Step 3: Verify build fails**

```bash
go build ./cmd/agentdock/
```

Expected: FAIL — symbols from old main.go (parseLogLevel, agentRunnerAdapter, repoCacheAdapter, slackPosterAdapter) are now undefined. We'll fix in next steps.

- [ ] **Step 4: Restore the deleted helpers temporarily — extract to `cmd/agentdock/adapters.go`**

Create `cmd/agentdock/adapters.go` with the symbols that were in old main.go and are still needed:

```go
package main

import (
    "context"
    "log/slog"
    "strings"

    "agentdock/internal/bot"
    ghclient "agentdock/internal/github"
    "agentdock/internal/queue"
    slackclient "agentdock/internal/slack"
)

// agentRunnerAdapter wraps AgentRunner to satisfy worker.Runner interface.
type agentRunnerAdapter struct {
    runner *bot.AgentRunner
}

func (a *agentRunnerAdapter) Run(ctx context.Context, workDir, prompt string, opts bot.RunOptions) (string, error) {
    return a.runner.Run(ctx, slog.Default(), workDir, prompt, opts)
}

// repoCacheAdapter wraps RepoCache to satisfy worker.RepoProvider interface.
type repoCacheAdapter struct {
    cache *ghclient.RepoCache
}

func (a *repoCacheAdapter) Prepare(cloneURL, branch string) (string, error) {
    repoPath, err := a.cache.EnsureRepo(cloneURL)
    if err != nil {
        return "", err
    }
    if branch != "" {
        if err := a.cache.Checkout(repoPath, branch); err != nil {
            return "", err
        }
    }
    return repoPath, nil
}

// slackPosterAdapter wraps slackclient.Client to satisfy bot.SlackPoster interface.
type slackPosterAdapter struct {
    client *slackclient.Client
}

func (a *slackPosterAdapter) PostMessage(channelID, text, threadTS string) {
    if err := a.client.PostMessage(channelID, text, threadTS); err != nil {
        slog.Warn("failed to post slack message", "channel", channelID, "error", err)
    }
}

func (a *slackPosterAdapter) UpdateMessage(channelID, messageTS, text string) {
    if err := a.client.UpdateMessage(channelID, messageTS, text); err != nil {
        slog.Warn("failed to update slack message", "channel", channelID, "error", err)
    }
}

func (a *slackPosterAdapter) PostMessageWithButton(channelID, text, threadTS, actionID, buttonText, value string) (string, error) {
    return a.client.PostMessageWithButton(channelID, text, threadTS, actionID, buttonText, value)
}

func parseLogLevel(level string) slog.Level {
    switch strings.ToLower(strings.TrimSpace(level)) {
    case "debug":
        return slog.LevelDebug
    case "warn", "warning":
        return slog.LevelWarn
    case "error":
        return slog.LevelError
    default:
        return slog.LevelInfo
    }
}

// queue interface + bundle types referenced by adapters and main flow
var _ queue.JobQueue = (*queueAdapter)(nil)

type queueAdapter struct{ queue.JobQueue }
```

(Note: `queueAdapter` is a placeholder; remove if not actually used elsewhere — it's a guard against missing imports.)

Actually clean: just include the four functions and adjust imports. The `queueAdapter` line can be removed; here purely to keep `queue` import if otherwise unused. Verify with `go build`.

- [ ] **Step 5: Run build, expect remaining errors only for missing app/worker subcommand registrations**

```bash
go build ./cmd/agentdock/
```

Expected: build still fails because `Execute()` is called but rootCmd has no subcommands wired AND old main body symbols (Slack handler etc.) aren't yet placed. We accept this — Task 7 wires app, Task 8 wires worker.

- [ ] **Step 6: Commit progress (compiles or not, this is mid-refactor)**

If build passes (it might if adapters.go is clean), commit. If not, leave uncommitted and proceed to Task 7. 

```bash
git add cmd/agentdock/root.go cmd/agentdock/main.go cmd/agentdock/adapters.go
git commit -m "feat(cli): introduce cobra root command and extract adapters" || echo "skipping commit; will bundle with appCmd"
```

---

### Task 7: Move existing main bot body into `cmd/agentdock/app.go` (appCmd)

**Files:**
- Create: `cmd/agentdock/app.go`

The 380-line body of original `main.go` (Slack socket mode setup, handler wiring, etc.) becomes appCmd.RunE.

- [ ] **Step 1: Create `cmd/agentdock/app.go`**

```go
package main

import (
    "context"
    "fmt"
    "log/slog"
    "net/http"
    "os"
    "strings"
    "time"

    "agentdock/internal/bot"
    "agentdock/internal/config"
    ghclient "agentdock/internal/github"
    "agentdock/internal/logging"
    "agentdock/internal/mantis"
    "agentdock/internal/queue"
    "agentdock/internal/skill"
    slackclient "agentdock/internal/slack"

    "github.com/slack-go/slack"
    "github.com/slack-go/slack/slackevents"
    "github.com/slack-go/slack/socketmode"
    "github.com/spf13/cobra"
)

var appConfigPath string

var appCmd = &cobra.Command{
    Use:   "app",
    Short: "Run the main Slack bot",
    RunE: func(cmd *cobra.Command, args []string) error {
        return runApp(appConfigPath)
    },
}

func init() {
    appCmd.Flags().StringVarP(&appConfigPath, "config", "c", "config.yaml", "path to config file")
    rootCmd.AddCommand(appCmd)
}

// runApp is the entire former main() body, lifted as-is. Will be refactored
// in Phase 3 to use koanf instead of config.Load().
func runApp(configPath string) error {
    // ... (paste verbatim from old cmd/bot/main.go:51 onwards, minus the flag.Parse / showVersion / os.Exit calls;
    //      replace os.Exit(1) with `return fmt.Errorf(...)` where used for fatal errors)

    slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))

    cfg, err := config.Load(configPath)
    if err != nil {
        return fmt.Errorf("failed to load config: %w", err)
    }

    // ... continue with rest of original main() body, ending with sm.Run() ...

    stderrHandler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: parseLogLevel(cfg.LogLevel)})
    rotator, err := logging.NewRotator(cfg.Logging.Dir)
    if err != nil {
        return fmt.Errorf("failed to init log rotator: %w", err)
    }
    rotator.StartCleanup(cfg.Logging.RetentionDays)
    fileHandler := slog.NewJSONHandler(rotator, &slog.HandlerOptions{Level: parseLogLevel(cfg.Logging.Level)})
    slog.SetDefault(slog.New(logging.NewMultiHandler(stderrHandler, fileHandler)))

    slackClient := slackclient.NewClient(cfg.Slack.BotToken)
    repoCache := ghclient.NewRepoCache(cfg.RepoCache.Dir, cfg.RepoCache.MaxAge, cfg.GitHub.Token)
    repoDiscovery := ghclient.NewRepoDiscovery(cfg.GitHub.Token)

    if cfg.AutoBind {
        go func() {
            _, err := repoDiscovery.ListRepos(context.Background())
            if err != nil {
                slog.Warn("failed to pre-warm repo cache", "error", err)
            }
        }()
    }

    agentRunner := bot.NewAgentRunnerFromConfig(cfg)

    bakedInDir := "agents/skills"
    if _, err := os.Stat("/opt/agents/skills"); err == nil {
        bakedInDir = "/opt/agents/skills"
    }
    skillLoader, err := skill.NewLoader(cfg.SkillsConfig, bakedInDir)
    if err != nil {
        return fmt.Errorf("failed to create skill loader: %w", err)
    }
    skillLoader.Warmup(context.Background())
    if cfg.SkillsConfig != "" {
        stopWatcher, err := skillLoader.StartWatcher(cfg.SkillsConfig)
        if err != nil {
            slog.Warn("failed to start skill config watcher", "error", err)
        } else {
            defer stopWatcher()
        }
    }

    mantisClient := mantis.NewClient(cfg.Mantis.BaseURL, cfg.Mantis.APIToken, cfg.Mantis.Username, cfg.Mantis.Password)
    if mantisClient.IsConfigured() {
        slog.Info("mantis integration enabled", "url", cfg.Mantis.BaseURL)
    }

    jobStore := queue.NewMemJobStore()
    jobStore.StartCleanup(1 * time.Hour)

    var bundle *queue.Bundle
    switch cfg.Queue.Transport {
    case "redis":
        rdb, err := queue.NewRedisClient(queue.RedisConfig{
            Addr: cfg.Redis.Addr, Password: cfg.Redis.Password, DB: cfg.Redis.DB, TLS: cfg.Redis.TLS,
        })
        if err != nil {
            return fmt.Errorf("failed to connect to Redis: %w", err)
        }
        bundle = queue.NewRedisBundle(rdb, jobStore, "triage")
        slog.Info("using Redis transport", "addr", cfg.Redis.Addr)
    default:
        bundle = queue.NewInMemBundle(cfg.Queue.Capacity, cfg.Workers.Count, jobStore)
        slog.Info("using in-memory transport")
    }

    seen := make(map[string]bool)
    var skillDirs []string
    for _, name := range cfg.Providers {
        if agent, ok := cfg.Agents[name]; ok && agent.SkillDir != "" && !seen[agent.SkillDir] {
            skillDirs = append(skillDirs, agent.SkillDir)
            seen[agent.SkillDir] = true
        }
    }
    if len(skillDirs) == 0 && cfg.ActiveAgent != "" {
        if agent, ok := cfg.Agents[cfg.ActiveAgent]; ok && agent.SkillDir != "" {
            skillDirs = append(skillDirs, agent.SkillDir)
        }
    }

    coordinator := queue.NewCoordinator(bundle.Queue)
    coordinator.RegisterQueue("triage", bundle.Queue)

    if cfg.Queue.Transport != "redis" {
        localAdapter := NewLocalAdapter(LocalAdapterConfig{
            Runner:         &agentRunnerAdapter{runner: agentRunner},
            RepoCache:      &repoCacheAdapter{cache: repoCache},
            SkillDirs:      skillDirs,
            WorkerCount:    cfg.Workers.Count,
            StatusInterval: cfg.Queue.StatusInterval,
            Capabilities:   []string{"triage"},
            Store:          jobStore,
        })
        if err := localAdapter.Start(queue.AdapterDeps{
            Jobs: bundle.Queue, Results: bundle.Results, Status: bundle.Status, Commands: bundle.Commands, Attachments: bundle.Attachments,
        }); err != nil {
            return fmt.Errorf("failed to start local adapter: %w", err)
        }
    }

    wf := bot.NewWorkflow(cfg, slackClient, repoCache, repoDiscovery, agentRunner, mantisClient, coordinator, jobStore, bundle.Attachments, skillLoader)

    handler := slackclient.NewHandler(slackclient.HandlerConfig{
        MaxConcurrent: cfg.MaxConcurrent, DedupTTL: 5 * time.Minute,
        PerUserLimit: cfg.RateLimit.PerUser, PerChannelLimit: cfg.RateLimit.PerChannel,
        RateWindow: cfg.RateLimit.Window, OnEvent: wf.HandleTrigger,
        OnRejected: func(e slackclient.TriggerEvent, reason string) {
            slackClient.PostMessage(e.ChannelID, fmt.Sprintf(":warning: %s", reason), e.ThreadTS)
        },
    })
    wf.SetHandler(handler)

    issueClient := ghclient.NewIssueClient(cfg.GitHub.Token)
    resultListener := bot.NewResultListener(bundle.Results, jobStore, bundle.Attachments,
        &slackPosterAdapter{client: slackClient}, issueClient,
        func(channelID, threadTS string) { handler.ClearThreadDedup(channelID, threadTS) })
    go resultListener.Listen(context.Background())

    retryHandler := bot.NewRetryHandler(jobStore, coordinator, &slackPosterAdapter{client: slackClient})
    statusListener := bot.NewStatusListener(bundle.Status, jobStore)
    go statusListener.Listen(context.Background())

    watchdog := queue.NewWatchdog(jobStore, bundle.Commands, bundle.Results, queue.WatchdogConfig{
        JobTimeout: cfg.Queue.JobTimeout, IdleTimeout: cfg.Queue.AgentIdleTimeout, PrepareTimeout: cfg.Queue.PrepareTimeout,
    })
    go watchdog.Start(make(chan struct{}))

    if cfg.Server.Port > 0 {
        go func() {
            http.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
                w.WriteHeader(http.StatusOK); w.Write([]byte("ok"))
            })
            http.HandleFunc("/jobs", queue.StatusHandler(jobStore, coordinator))
            http.HandleFunc("/jobs/", queue.KillHandler(jobStore, bundle.Commands))
            addr := fmt.Sprintf(":%d", cfg.Server.Port)
            slog.Info("http endpoints listening", "addr", addr, "endpoints", []string{"/healthz", "/jobs", "/jobs/{id}"})
            http.ListenAndServe(addr, nil)
        }()
    }

    api := slack.New(cfg.Slack.BotToken, slack.OptionAppLevelToken(cfg.Slack.AppToken))
    sm := socketmode.New(api)

    botUserID := ""
    if authResp, err := api.AuthTest(); err == nil {
        botUserID = authResp.UserID
        slog.Info("bot identity resolved", "userID", botUserID)
    } else {
        slog.Warn("failed to resolve bot identity, auto-bind may not filter correctly", "error", err)
    }

    slog.Info("starting bot", "version", version, "commit", commit, "date", date)

    go runSocketModeLoop(sm, handler, wf, retryHandler, slackClient, jobStore, bundle, cfg, botUserID)

    if err := sm.Run(); err != nil {
        return fmt.Errorf("socket mode error: %w", err)
    }
    return nil
}

// runSocketModeLoop is the inner event-handling goroutine extracted from main()
// for readability. It receives all the wired dependencies.
func runSocketModeLoop(
    sm *socketmode.Client,
    handler *slackclient.Handler,
    wf *bot.Workflow,
    retryHandler *bot.RetryHandler,
    slackClient *slackclient.Client,
    jobStore queue.JobStore,
    bundle *queue.Bundle,
    cfg *config.Config,
    botUserID string,
) {
    // ... paste the original `for evt := range sm.Events { switch ... }` block here verbatim.
    // (Kept as separate function only to keep RunE readable. Body is identical to old main.go:252-374.)
    for evt := range sm.Events {
        switch evt.Type {
        case socketmode.EventTypeEventsAPI:
            sm.Ack(*evt.Request)
            ea, ok := evt.Data.(slackevents.EventsAPIEvent)
            if !ok {
                continue
            }
            switch inner := ea.InnerEvent.Data.(type) {
            case *slackevents.AppMentionEvent:
                handler.HandleTrigger(slackclient.TriggerEvent{
                    ChannelID: inner.Channel, ThreadTS: inner.ThreadTimeStamp,
                    TriggerTS: inner.TimeStamp, UserID: inner.User, Text: inner.Text,
                })
            case *slackevents.MemberJoinedChannelEvent:
                if cfg.AutoBind && inner.User == botUserID {
                    wf.RegisterChannel(inner.Channel)
                }
            case *slackevents.MemberLeftChannelEvent:
                if cfg.AutoBind && inner.User == botUserID {
                    wf.UnregisterChannel(inner.Channel)
                }
            }
        case socketmode.EventTypeSlashCommand:
            sm.Ack(*evt.Request)
            cmd, ok := evt.Data.(slack.SlashCommand)
            if !ok || cmd.Command != "/triage" || cmd.ChannelID == "" {
                continue
            }
            slackClient.PostMessage(cmd.ChannelID,
                ":point_right: 請在對話串中使用 `@bot` 來觸發 triage，或直接在 thread 中 mention bot。\n`/triage` 指令目前不支援 thread 偵測。", "")
        case socketmode.EventTypeInteractive:
            cb, ok := evt.Data.(slack.InteractionCallback)
            if !ok {
                sm.Ack(*evt.Request)
                continue
            }
            if cb.Type == slack.InteractionTypeBlockSuggestion {
                if cb.ActionID == "repo_search" {
                    options := wf.HandleRepoSuggestion(cb.Value)
                    var opts []*slack.OptionBlockObject
                    for _, r := range options {
                        opts = append(opts, slack.NewOptionBlockObject(r, slack.NewTextBlockObject("plain_text", r, false, false), nil))
                    }
                    sm.Ack(*evt.Request, slack.OptionsResponse{Options: opts})
                } else {
                    sm.Ack(*evt.Request)
                }
                continue
            }
            sm.Ack(*evt.Request)
            switch cb.Type {
            case slack.InteractionTypeBlockActions:
                if len(cb.ActionCallback.BlockActions) == 0 {
                    continue
                }
                action := cb.ActionCallback.BlockActions[0]
                selectorTS := cb.Message.Timestamp
                switch {
                case action.ActionID == "repo_search" && action.SelectedOption.Value != "":
                    wf.HandleSelection(cb.Channel.ID, action.ActionID, action.SelectedOption.Value, selectorTS)
                case strings.HasPrefix(action.ActionID, "repo_select"):
                    wf.HandleSelection(cb.Channel.ID, action.ActionID, action.Value, selectorTS)
                case strings.HasPrefix(action.ActionID, "branch_select"):
                    wf.HandleSelection(cb.Channel.ID, action.ActionID, action.Value, selectorTS)
                case strings.HasPrefix(action.ActionID, "description_action"):
                    wf.HandleDescriptionAction(cb.Channel.ID, action.Value, selectorTS, cb.TriggerID)
                case action.ActionID == "retry_job":
                    retryHandler.Handle(cb.Channel.ID, action.Value, selectorTS)
                case strings.HasPrefix(action.ActionID, "cancel_job"):
                    jobID := action.Value
                    state, err := jobStore.Get(jobID)
                    if err == nil && state.Status != queue.JobFailed && state.Status != queue.JobCompleted {
                        bundle.Commands.Send(context.Background(), queue.Command{JobID: jobID, Action: "kill"})
                        jobStore.UpdateStatus(jobID, queue.JobFailed)
                        slackClient.UpdateMessage(cb.Channel.ID, selectorTS, ":stop_sign: 正在取消...")
                        handler.ClearThreadDedup(cb.Channel.ID, state.Job.ThreadTS)
                    } else {
                        slackClient.UpdateMessage(cb.Channel.ID, selectorTS, ":information_source: 此任務已結束")
                    }
                }
            case slack.InteractionTypeViewSubmission:
                meta := cb.View.PrivateMetadata
                desc := ""
                if v, ok := cb.View.State.Values["description_block"]["description_input"]; ok {
                    desc = v.Value
                }
                wf.HandleDescriptionSubmit(meta, desc)
            case slack.InteractionTypeViewClosed:
                wf.HandleDescriptionSubmit(cb.View.PrivateMetadata, "")
            }
        }
    }
}
```

- [ ] **Step 2: Build, verify**

```bash
go build ./cmd/agentdock/
```

Expected: PASS (rootCmd has appCmd registered via init(); appCmd.RunE works).

- [ ] **Step 3: Smoke test**

```bash
./agentdock --version
./agentdock --help
./agentdock app --help
```

Expected: 
- `--version` prints `agentdock dev (commit unknown, built unknown)` (or build-time injected values)
- `--help` shows root help with `app` listed
- `app --help` shows `--config` flag

- [ ] **Step 4: Run all existing tests**

```bash
go test ./...
```

Expected: PASS (no test changes).

- [ ] **Step 5: Commit**

```bash
git add cmd/agentdock/
git commit -m "feat(cli): add app subcommand wrapping current main() body"
```

---

### Task 8: Wrap existing worker as `workerCmd`

**Files:**
- Modify: `cmd/agentdock/worker.go`

Current `runWorker()` becomes the body of `workerCmd.RunE`.

- [ ] **Step 1: Refactor `cmd/agentdock/worker.go`**

Replace top of file:

```go
package main

import (
    "context"
    "fmt"
    "log/slog"
    "os"
    "os/signal"
    "syscall"

    "agentdock/internal/bot"
    "agentdock/internal/config"
    ghclient "agentdock/internal/github"
    "agentdock/internal/queue"
    "agentdock/internal/worker"

    "github.com/spf13/cobra"
)

var workerConfigPath string

var workerCmd = &cobra.Command{
    Use:   "worker",
    Short: "Run worker pool that processes jobs from Redis queue",
    RunE: func(cmd *cobra.Command, args []string) error {
        return runWorker(workerConfigPath)
    },
}

func init() {
    workerCmd.Flags().StringVarP(&workerConfigPath, "config", "c", "", "path to config file (optional, can use env vars only)")
    rootCmd.AddCommand(workerCmd)
}

// runWorker is the existing function, signature changed to accept configPath
// and return error instead of calling os.Exit.
func runWorker(configPath string) error {
    var cfg *config.Config
    var err error
    if configPath != "" {
        cfg, err = config.Load(configPath)
        if err != nil {
            return fmt.Errorf("failed to load config: %w", err)
        }
    } else {
        cfg, err = config.LoadDefaults()
        if err != nil {
            return fmt.Errorf("failed to load defaults: %w", err)
        }
    }

    if err := runPreflight(cfg); err != nil {
        return fmt.Errorf("preflight: %w", err)
    }

    slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))

    rdb, err := queue.NewRedisClient(queue.RedisConfig{
        Addr: cfg.Redis.Addr, Password: cfg.Redis.Password, DB: cfg.Redis.DB, TLS: cfg.Redis.TLS,
    })
    if err != nil {
        return fmt.Errorf("failed to connect to Redis: %w", err)
    }
    slog.Info("connected to Redis", "addr", cfg.Redis.Addr)

    jobStore := queue.NewMemJobStore()
    bundle := queue.NewRedisBundle(rdb, jobStore, "triage")

    agentRunner := bot.NewAgentRunnerFromConfig(cfg)
    repoCache := ghclient.NewRepoCache(cfg.RepoCache.Dir, cfg.RepoCache.MaxAge, cfg.GitHub.Token)

    var skillDirs []string
    seen := make(map[string]bool)
    for _, name := range cfg.Providers {
        if agent, ok := cfg.Agents[name]; ok && agent.SkillDir != "" && !seen[agent.SkillDir] {
            skillDirs = append(skillDirs, agent.SkillDir)
            seen[agent.SkillDir] = true
        }
    }

    hostname, _ := os.Hostname()
    if hostname == "" {
        hostname = "unknown"
    }

    pool := worker.NewPool(worker.Config{
        Queue: bundle.Queue, Attachments: bundle.Attachments, Results: bundle.Results, Store: jobStore,
        Runner: &agentRunnerAdapter{runner: agentRunner}, RepoCache: &repoCacheAdapter{cache: repoCache},
        WorkerCount: cfg.Workers.Count, Hostname: hostname, SkillDirs: skillDirs,
        Commands: bundle.Commands, Status: bundle.Status, StatusInterval: cfg.Queue.StatusInterval,
    })

    ctx, cancel := context.WithCancel(context.Background())
    pool.Start(ctx)
    slog.Info("worker started", "workers", cfg.Workers.Count)

    sigCh := make(chan os.Signal, 1)
    signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
    sig := <-sigCh
    slog.Info("shutting down", "signal", sig)
    cancel()
    bundle.Close()
    return nil
}
```

(Removed: `flag.NewFlagSet` block, `fmt.Fprintf(os.Stderr, ...)` error prints; replaced by `cobra.Command.RunE` returning errors.)

- [ ] **Step 2: Build**

```bash
go build ./cmd/agentdock/
```

Expected: PASS.

- [ ] **Step 3: Smoke test**

```bash
./agentdock worker --help
./agentdock worker -c /tmp/nonexistent.yaml
```

Expected:
- `worker --help` shows `--config / -c` flag
- `worker -c /tmp/nonexistent.yaml` errors out (file not found from old `Load()`)

- [ ] **Step 4: Run all tests**

```bash
go test ./...
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/agentdock/worker.go
git commit -m "feat(cli): wrap worker as cobra subcommand"
```

---

### Task 9: Add `init` subcommand skeleton (no body yet)

**Files:**
- Create: `cmd/agentdock/init.go`

Skeleton just registers the command; full impl comes in Phase 5.

- [ ] **Step 1: Create `cmd/agentdock/init.go`**

```go
package main

import (
    "fmt"

    "github.com/spf13/cobra"
)

var (
    initConfigPath  string
    initForce       bool
    initInteractive bool
)

var initCmd = &cobra.Command{
    Use:   "init",
    Short: "Generate a starter config file",
    Long:  "Writes a starter config to the path specified by --config (default ~/.config/agentdock/config.yaml).",
    RunE: func(cmd *cobra.Command, args []string) error {
        return fmt.Errorf("init not yet implemented; coming in Phase 5")
    },
}

func init() {
    initCmd.Flags().StringVarP(&initConfigPath, "config", "c", "", "path for new config file (default ~/.config/agentdock/config.yaml)")
    initCmd.Flags().BoolVar(&initForce, "force", false, "overwrite if file exists")
    initCmd.Flags().BoolVarP(&initInteractive, "interactive", "i", false, "prompt for required values")
    rootCmd.AddCommand(initCmd)
}
```

- [ ] **Step 2: Build + smoke test**

```bash
go build ./cmd/agentdock/
./agentdock init --help
./agentdock init && echo "should have errored" || echo "ok, errored as expected"
```

Expected:
- `init --help` shows three flags
- `init` (without phase 5 impl) errors with "not yet implemented"

- [ ] **Step 3: Commit**

```bash
git add cmd/agentdock/init.go
git commit -m "feat(cli): add init subcommand skeleton"
```

---

## Phase 3: Koanf integration

Goal: replace `config.Load()` calls inside `runApp()` and `runWorker()` with the koanf two-instance flow. Add flag layer (covers all the persistent flags). Old `Load()` / `applyEnvOverrides()` removed (or kept temporarily — see Task 12).

### Task 10: Add koanf dependencies

**Files:** `go.mod`, `go.sum`

- [ ] **Step 1: Add koanf v2 + needed providers/parsers**

```bash
go get github.com/knadh/koanf/v2@latest
go get github.com/knadh/koanf/parsers/yaml@latest
go get github.com/knadh/koanf/parsers/json@latest
go get github.com/knadh/koanf/providers/confmap@latest
go get github.com/knadh/koanf/providers/file@latest
```

- [ ] **Step 2: Verify go.mod**

```bash
grep koanf go.mod
```

Expected: 5 lines with `koanf` paths.

- [ ] **Step 3: Build**

```bash
go build ./...
```

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add go.mod go.sum
git commit -m "build: add knadh/koanf v2 dependencies"
```

---

### Task 11: Add `flagToKey` map and persistent flag registration in `cmd/agentdock/flags.go`

**Files:**
- Create: `cmd/agentdock/flags.go`
- Test: `cmd/agentdock/flags_test.go`

- [ ] **Step 1: Write failing test**

```go
// cmd/agentdock/flags_test.go
package main

import (
    "reflect"
    "strings"
    "testing"

    "agentdock/internal/config"
)

func TestFlagToKey_ValuesMapToConfigYAMLPaths(t *testing.T) {
    // Walk Config struct yaml tags; build set of valid dotted paths.
    valid := map[string]bool{}
    walkYAMLPaths(reflect.TypeOf(config.Config{}), "", valid)

    for flag, key := range flagToKey {
        if !valid[key] {
            t.Errorf("flagToKey[%q] = %q, but %q is not a valid yaml path in config.Config", flag, key, key)
        }
    }
}

// walkYAMLPaths recursively traverses struct yaml tags, recording dotted paths.
func walkYAMLPaths(t reflect.Type, prefix string, out map[string]bool) {
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
        // Recurse into nested struct fields
        ft := f.Type
        if ft.Kind() == reflect.Pointer {
            ft = ft.Elem()
        }
        if ft.Kind() == reflect.Struct {
            walkYAMLPaths(ft, path, out)
        }
    }
}
```

- [ ] **Step 2: Run test, verify fail**

```bash
go test ./cmd/agentdock/ -run TestFlagToKey -v
```

Expected: FAIL `undefined: flagToKey` and `undefined: walkYAMLPaths` (the helper is in the test file so it should compile).

Actually it'll fail with `undefined: flagToKey` from the test itself.

- [ ] **Step 3: Create `cmd/agentdock/flags.go`**

```go
package main

import (
    "fmt"

    "github.com/spf13/cobra"
    "github.com/spf13/pflag"
)

// flagToKey maps cobra flag names (dash-case) to koanf keys (dot.snake_case).
// SINGLE source of truth for flag-to-config mapping. Add an entry here whenever
// you add a new flag; flags not present here are skipped by buildFlagOverrideMap.
var flagToKey = map[string]string{
    // Persistent flags (root)
    "log-level":                "log_level",
    "redis-addr":               "redis.addr",
    "redis-password":           "redis.password",
    "redis-db":                 "redis.db",
    "redis-tls":                "redis.tls",
    "github-token":             "github.token",
    "mantis-base-url":          "mantis.base_url",
    "mantis-api-token":         "mantis.api_token",
    "mantis-username":          "mantis.username",
    "mantis-password":          "mantis.password",
    "queue-capacity":           "queue.capacity",
    "queue-transport":          "queue.transport",
    "queue-job-timeout":        "queue.job_timeout",
    "queue-agent-idle-timeout": "queue.agent_idle_timeout",
    "queue-prepare-timeout":    "queue.prepare_timeout",
    "queue-status-interval":    "queue.status_interval",
    "logging-dir":              "logging.dir",
    "logging-level":            "logging.level",
    "logging-retention-days":   "logging.retention_days",
    "logging-agent-output-dir": "logging.agent_output_dir",
    "repo-cache-dir":           "repo_cache.dir",
    "repo-cache-max-age":       "repo_cache.max_age",
    "attachments-store":        "attachments.store",
    "attachments-temp-dir":     "attachments.temp_dir",
    "attachments-ttl":          "attachments.ttl",
    "workers":                  "workers.count",
    "active-agent":             "active_agent",
    "providers":                "providers",
    "skills-config":            "skills_config",

    // app-specific flags
    "slack-bot-token":         "slack.bot_token",
    "slack-app-token":         "slack.app_token",
    "server-port":             "server.port",
    "auto-bind":               "auto_bind",
    "max-concurrent":          "max_concurrent",
    "max-thread-messages":     "max_thread_messages",
    "semaphore-timeout":       "semaphore_timeout",
    "rate-limit-per-user":     "rate_limit.per_user",
    "rate-limit-per-channel":  "rate_limit.per_channel",
    "rate-limit-window":       "rate_limit.window",
}

// addPersistentFlags registers all persistent flags on the root command.
// Called from root.go's init().
func addPersistentFlags(cmd *cobra.Command) {
    cmd.PersistentFlags().String("log-level", "", "log level: debug|info|warn|error")
    cmd.PersistentFlags().String("redis-addr", "", "Redis server address")
    cmd.PersistentFlags().String("redis-password", "", "Redis password")
    cmd.PersistentFlags().Int("redis-db", 0, "Redis database number")
    cmd.PersistentFlags().Bool("redis-tls", false, "use TLS for Redis connection")
    cmd.PersistentFlags().String("github-token", "", "GitHub token (ghp_... or github_pat_...)")
    cmd.PersistentFlags().String("mantis-base-url", "", "Mantis bug tracker base URL")
    cmd.PersistentFlags().String("mantis-api-token", "", "Mantis API token")
    cmd.PersistentFlags().String("mantis-username", "", "Mantis username")
    cmd.PersistentFlags().String("mantis-password", "", "Mantis password")
    cmd.PersistentFlags().Int("queue-capacity", 0, "in-memory queue capacity")
    cmd.PersistentFlags().String("queue-transport", "", "queue transport: redis|inmem")
    cmd.PersistentFlags().Duration("queue-job-timeout", 0, "max job duration")
    cmd.PersistentFlags().Duration("queue-agent-idle-timeout", 0, "agent idle timeout")
    cmd.PersistentFlags().Duration("queue-prepare-timeout", 0, "repo prepare timeout")
    cmd.PersistentFlags().Duration("queue-status-interval", 0, "status report interval")
    cmd.PersistentFlags().String("logging-dir", "", "log file directory")
    cmd.PersistentFlags().String("logging-level", "", "file log level: debug|info|warn|error")
    cmd.PersistentFlags().Int("logging-retention-days", 0, "log retention in days")
    cmd.PersistentFlags().String("logging-agent-output-dir", "", "agent output log directory")
    cmd.PersistentFlags().String("repo-cache-dir", "", "repo clone cache directory")
    cmd.PersistentFlags().Duration("repo-cache-max-age", 0, "repo cache max age")
    cmd.PersistentFlags().String("attachments-store", "", "attachments store backend")
    cmd.PersistentFlags().String("attachments-temp-dir", "", "attachments temp directory")
    cmd.PersistentFlags().Duration("attachments-ttl", 0, "attachments TTL")
    cmd.PersistentFlags().Int("workers", 0, "worker pool size")
    cmd.PersistentFlags().String("active-agent", "", "active agent name")
    cmd.PersistentFlags().StringSlice("providers", nil, "comma-separated provider names")
    cmd.PersistentFlags().String("skills-config", "", "skills.yaml path")

    // -c, --config short alias is registered on each subcommand individually
    // (already done in app.go / worker.go / init.go).
}

// addAppFlags registers app-specific flags on the given command.
func addAppFlags(cmd *cobra.Command) {
    cmd.Flags().String("slack-bot-token", "", "Slack bot OAuth token (xoxb-...)")
    cmd.Flags().String("slack-app-token", "", "Slack app-level token (xapp-...)")
    cmd.Flags().Int("server-port", 0, "HTTP server port (0 disables)")
    cmd.Flags().Bool("auto-bind", false, "auto-register channels on join")
    cmd.Flags().Int("max-concurrent", 0, "max concurrent triggers")
    cmd.Flags().Int("max-thread-messages", 0, "max thread messages to read")
    cmd.Flags().Duration("semaphore-timeout", 0, "semaphore acquire timeout")
    cmd.Flags().Int("rate-limit-per-user", 0, "rate limit per user per window")
    cmd.Flags().Int("rate-limit-per-channel", 0, "rate limit per channel per window")
    cmd.Flags().Duration("rate-limit-window", 0, "rate limit window")
}

// buildFlagOverrideMap walks Changed flags on cmd and returns a koanf-friendly
// map[string]any. Skips flags not in flagToKey.
func buildFlagOverrideMap(cmd *cobra.Command) map[string]any {
    out := map[string]any{}
    cmd.Flags().Visit(func(f *pflag.Flag) {
        key, ok := flagToKey[f.Name]
        if !ok {
            return
        }
        switch f.Value.Type() {
        case "string":
            v, _ := cmd.Flags().GetString(f.Name)
            out[key] = v
        case "int":
            v, _ := cmd.Flags().GetInt(f.Name)
            out[key] = v
        case "bool":
            v, _ := cmd.Flags().GetBool(f.Name)
            out[key] = v
        case "duration":
            v, _ := cmd.Flags().GetDuration(f.Name)
            out[key] = v.String()  // koanf stores duration as string for round-trip
        case "stringSlice":
            v, _ := cmd.Flags().GetStringSlice(f.Name)
            out[key] = v
        default:
            // Unhandled types — log via fmt for now; likely a bug
            _ = fmt.Sprintf("buildFlagOverrideMap: unhandled flag type %q for %q", f.Value.Type(), f.Name)
        }
    })
    return out
}
```

- [ ] **Step 4: Wire `addPersistentFlags(rootCmd)` into `cmd/agentdock/root.go`**

In `root.go`, add an `init()`:

```go
func init() {
    addPersistentFlags(rootCmd)
}
```

- [ ] **Step 5: Wire `addAppFlags(appCmd)` into `cmd/agentdock/app.go`**

In `app.go`'s existing `init()`, after `rootCmd.AddCommand(appCmd)`:

```go
func init() {
    appCmd.Flags().StringVarP(&appConfigPath, "config", "c", "config.yaml", "path to config file")
    addAppFlags(appCmd)
    rootCmd.AddCommand(appCmd)
}
```

- [ ] **Step 6: Run test, verify pass**

```bash
go test ./cmd/agentdock/ -run TestFlagToKey -v
```

Expected: PASS — every flagToKey value matches a path in Config struct.

- [ ] **Step 7: Smoke test all flags appear**

```bash
go build ./cmd/agentdock/ -o agentdock
./agentdock --help
./agentdock app --help
```

Expected: `--help` shows persistent flags; `app --help` shows persistent + app-specific.

- [ ] **Step 8: Commit**

```bash
git add cmd/agentdock/flags.go cmd/agentdock/flags_test.go cmd/agentdock/root.go cmd/agentdock/app.go
git commit -m "feat(cli): register all persistent and app-specific flags with cobra"
```

---

### Task 12: Implement `buildKoanf` + `mergeBuiltinAgents` in `cmd/agentdock/config.go`

**Files:**
- Create: `cmd/agentdock/config.go`
- Test: `cmd/agentdock/config_test.go`

- [ ] **Step 1: Write failing test**

```go
// cmd/agentdock/config_test.go
package main

import (
    "os"
    "path/filepath"
    "testing"

    "agentdock/internal/config"

    "github.com/spf13/cobra"
)

func TestBuildKoanf_DefaultsLayer(t *testing.T) {
    cmd := &cobra.Command{Use: "test"}
    addPersistentFlags(cmd)

    cfg, _, _, err := buildKoanf(cmd, "")
    if err != nil {
        t.Fatalf("buildKoanf failed: %v", err)
    }
    if cfg.Workers.Count != 3 {
        t.Errorf("Workers.Count = %d, want 3 (default)", cfg.Workers.Count)
    }
    if cfg.Queue.Transport != "inmem" {
        t.Errorf("Queue.Transport = %q, want inmem (default)", cfg.Queue.Transport)
    }
}

func TestBuildKoanf_FileLayerOverridesDefaults(t *testing.T) {
    dir := t.TempDir()
    path := filepath.Join(dir, "test.yaml")
    yaml := `
workers:
  count: 7
queue:
  transport: redis
`
    if err := os.WriteFile(path, []byte(yaml), 0600); err != nil {
        t.Fatal(err)
    }

    cmd := &cobra.Command{Use: "test"}
    addPersistentFlags(cmd)

    cfg, _, _, err := buildKoanf(cmd, path)
    if err != nil {
        t.Fatalf("buildKoanf failed: %v", err)
    }
    if cfg.Workers.Count != 7 {
        t.Errorf("Workers.Count = %d, want 7 (from file)", cfg.Workers.Count)
    }
    if cfg.Queue.Transport != "redis" {
        t.Errorf("Queue.Transport = %q, want redis (from file)", cfg.Queue.Transport)
    }
}

func TestBuildKoanf_EnvLayerOverridesFile(t *testing.T) {
    t.Setenv("REDIS_ADDR", "10.0.0.1:6379")

    cmd := &cobra.Command{Use: "test"}
    addPersistentFlags(cmd)

    cfg, _, kSave, err := buildKoanf(cmd, "")
    if err != nil {
        t.Fatalf("buildKoanf failed: %v", err)
    }
    if cfg.Redis.Addr != "10.0.0.1:6379" {
        t.Errorf("kEff Redis.Addr = %q, want 10.0.0.1:6379 (from env)", cfg.Redis.Addr)
    }
    // kSave should NOT have env-derived value (D1)
    if got := kSave.String("redis.addr"); got == "10.0.0.1:6379" {
        t.Errorf("kSave redis.addr = %q, env should NOT persist", got)
    }
}

func TestBuildKoanf_FlagLayerOverridesEverything(t *testing.T) {
    t.Setenv("REDIS_ADDR", "10.0.0.1:6379")

    cmd := &cobra.Command{Use: "test"}
    addPersistentFlags(cmd)
    cmd.SetArgs([]string{"--redis-addr=192.168.1.1:6379"})
    cmd.ParseFlags([]string{"--redis-addr=192.168.1.1:6379"})

    cfg, _, kSave, err := buildKoanf(cmd, "")
    if err != nil {
        t.Fatalf("buildKoanf failed: %v", err)
    }
    if cfg.Redis.Addr != "192.168.1.1:6379" {
        t.Errorf("Redis.Addr = %q, want 192.168.1.1:6379 (from flag)", cfg.Redis.Addr)
    }
    // kSave SHOULD have flag value (flags persist)
    if got := kSave.String("redis.addr"); got != "192.168.1.1:6379" {
        t.Errorf("kSave redis.addr = %q, want flag value to persist", got)
    }
}
```

- [ ] **Step 2: Run, verify fail**

```bash
go test ./cmd/agentdock/ -run TestBuildKoanf -v
```

Expected: FAIL `undefined: buildKoanf`.

- [ ] **Step 3: Create `cmd/agentdock/config.go`**

```go
package main

import (
    "fmt"
    "os"
    "path/filepath"
    "strings"

    "agentdock/internal/config"

    "github.com/knadh/koanf/parsers/json"
    "github.com/knadh/koanf/parsers/yaml"
    "github.com/knadh/koanf/providers/confmap"
    "github.com/knadh/koanf/providers/file"
    "github.com/knadh/koanf/v2"
    "github.com/spf13/cobra"
)

// DeltaInfo records what triggers save-back. Returned from buildKoanf.
type DeltaInfo struct {
    FileExisted     bool
    HadFlagOverride bool
}

// buildKoanf loads the four-layer config (defaults / file / env / flags) into
// two koanf instances:
//   - kEff: effective config used at runtime (includes env)
//   - kSave: save-back config (excludes env per D1)
//
// Returns the unmarshaled Config, the two instances, and a DeltaInfo for
// save-back trigger logic.
func buildKoanf(cmd *cobra.Command, configPath string) (*config.Config, *koanf.Koanf, *koanf.Koanf, DeltaInfo, error) {
    kEff := koanf.New(".")
    kSave := koanf.New(".")

    // L0: defaults — both
    defaults := config.DefaultsMap()
    _ = kEff.Load(confmap.Provider(defaults, "."), nil)
    _ = kSave.Load(confmap.Provider(defaults, "."), nil)

    // L1: --config file (yaml or json by extension) — both
    var fileExisted bool
    if configPath != "" {
        if _, err := os.Stat(configPath); err == nil {
            fileExisted = true
            parser, err := pickParser(configPath)
            if err != nil {
                return nil, nil, nil, DeltaInfo{}, err
            }
            if err := kEff.Load(file.Provider(configPath), parser); err != nil {
                return nil, nil, nil, DeltaInfo{}, fmt.Errorf("load config %s: %w", configPath, err)
            }
            if err := kSave.Load(file.Provider(configPath), parser); err != nil {
                return nil, nil, nil, DeltaInfo{}, fmt.Errorf("load config %s: %w", configPath, err)
            }
        } else if !os.IsNotExist(err) {
            return nil, nil, nil, DeltaInfo{}, fmt.Errorf("stat config %s: %w", configPath, err)
        }
        // file doesn't exist — OK, fileExisted=false; caller decides if fatal
    }

    // L2: env — kEff only
    envMap := config.EnvOverrideMap()
    _ = kEff.Load(confmap.Provider(envMap, "."), nil)

    // L3: cobra flags (Changed only) — both
    flagMap := buildFlagOverrideMap(cmd)
    _ = kEff.Load(confmap.Provider(flagMap, "."), nil)
    _ = kSave.Load(confmap.Provider(flagMap, "."), nil)

    // Unmarshal kEff to Config
    var cfg config.Config
    if err := kEff.UnmarshalWithConf("", &cfg, koanf.UnmarshalConf{Tag: "yaml"}); err != nil {
        return nil, nil, nil, DeltaInfo{}, fmt.Errorf("unmarshal config: %w", err)
    }

    // Built-in agents fallback (D16)
    mergeBuiltinAgents(&cfg)

    return &cfg, kEff, kSave, DeltaInfo{
        FileExisted:     fileExisted,
        HadFlagOverride: len(flagMap) > 0,
    }, nil
}

// pickParser returns the koanf parser for the given file extension.
func pickParser(path string) (koanf.Parser, error) {
    ext := strings.ToLower(filepath.Ext(path))
    switch ext {
    case ".yaml", ".yml":
        return yaml.Parser(), nil
    case ".json":
        return json.Parser(), nil
    default:
        return nil, fmt.Errorf("unsupported config format: %s; only .yaml/.yml/.json supported", ext)
    }
}

// mergeBuiltinAgents fills in agents from BuiltinAgents map for any name not
// already in cfg.Agents (D16). User-defined entries fully override built-in.
func mergeBuiltinAgents(cfg *config.Config) {
    if cfg.Agents == nil {
        cfg.Agents = map[string]config.AgentConfig{}
    }
    for name, agent := range config.BuiltinAgents {
        if _, exists := cfg.Agents[name]; !exists {
            cfg.Agents[name] = agent
        }
    }
}

// resolveConfigPath expands ~ to home dir and returns absolute path.
// Empty input → default ~/.config/agentdock/config.yaml.
func resolveConfigPath(in string) (string, error) {
    if in == "" {
        in = "~/.config/agentdock/config.yaml"
    }
    if strings.HasPrefix(in, "~/") || in == "~" {
        home, err := os.UserHomeDir()
        if err != nil {
            return "", fmt.Errorf("resolve ~: %w", err)
        }
        in = filepath.Join(home, strings.TrimPrefix(in, "~/"))
    }
    return filepath.Abs(in)
}
```

- [ ] **Step 4: Run tests, verify pass**

```bash
go test ./cmd/agentdock/ -run TestBuildKoanf -v
```

Expected: PASS for all 4 test cases.

- [ ] **Step 5: Add test for `mergeBuiltinAgents` and `resolveConfigPath`**

```go
func TestMergeBuiltinAgents_FillsMissing(t *testing.T) {
    cfg := &config.Config{Agents: map[string]config.AgentConfig{
        "claude": {Command: "/usr/local/bin/claude-canary"},
    }}
    mergeBuiltinAgents(cfg)

    if got := cfg.Agents["claude"].Command; got != "/usr/local/bin/claude-canary" {
        t.Errorf("user override lost: got %q", got)
    }
    if _, ok := cfg.Agents["codex"]; !ok {
        t.Error("BuiltinAgents['codex'] should be merged in")
    }
    if _, ok := cfg.Agents["opencode"]; !ok {
        t.Error("BuiltinAgents['opencode'] should be merged in")
    }
}

func TestResolveConfigPath_DefaultsToHome(t *testing.T) {
    home, _ := os.UserHomeDir()
    got, err := resolveConfigPath("")
    if err != nil {
        t.Fatal(err)
    }
    want := filepath.Join(home, ".config", "agentdock", "config.yaml")
    if got != want {
        t.Errorf("got %q, want %q", got, want)
    }
}

func TestResolveConfigPath_ExpandsTilde(t *testing.T) {
    home, _ := os.UserHomeDir()
    got, err := resolveConfigPath("~/foo.yaml")
    if err != nil {
        t.Fatal(err)
    }
    want := filepath.Join(home, "foo.yaml")
    if got != want {
        t.Errorf("got %q, want %q", got, want)
    }
}
```

- [ ] **Step 6: Run, verify pass**

```bash
go test ./cmd/agentdock/ -v
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add cmd/agentdock/config.go cmd/agentdock/config_test.go
git commit -m "feat(cli): implement koanf two-instance load with built-in agents merge"
```

---

### Task 13: Replace `config.Load()` callers with `buildKoanf` in app + worker

**Files:**
- Modify: `cmd/agentdock/app.go`
- Modify: `cmd/agentdock/worker.go`

PreRunE pattern: build koanf, stash cfg into context. RunE reads from context.

- [ ] **Step 1: Add ctx key + helper in `cmd/agentdock/config.go`**

Append:

```go
type ctxKey int

const (
    ctxKeyConfig ctxKey = iota
    ctxKeyKSave
    ctxKeyDelta
)

func cfgFromCtx(ctx context.Context) *config.Config {
    return ctx.Value(ctxKeyConfig).(*config.Config)
}

func kSaveFromCtx(ctx context.Context) *koanf.Koanf {
    return ctx.Value(ctxKeyKSave).(*koanf.Koanf)
}

func deltaFromCtx(ctx context.Context) DeltaInfo {
    return ctx.Value(ctxKeyDelta).(DeltaInfo)
}
```

Add `"context"` to imports.

- [ ] **Step 2: Add PersistentPreRunE wiring helper**

In `cmd/agentdock/config.go`, append:

```go
// loadAndStash builds the koanf config from cmd flags + the configPath,
// then stashes the result into cmd's context for RunE to retrieve.
// Used by app/worker PersistentPreRunE.
func loadAndStash(cmd *cobra.Command, configPath string) error {
    resolved, err := resolveConfigPath(configPath)
    if err != nil {
        return err
    }
    cfg, _, kSave, delta, err := buildKoanf(cmd, resolved)
    if err != nil {
        return err
    }
    // Strict mode: explicit --config that doesn't exist → fatal
    if configPath != "" && !delta.FileExisted {
        return fmt.Errorf("config file not found: %s; run 'agentdock init -c %s' first", resolved, resolved)
    }
    ctx := cmd.Context()
    ctx = context.WithValue(ctx, ctxKeyConfig, cfg)
    ctx = context.WithValue(ctx, ctxKeyKSave, kSave)
    ctx = context.WithValue(ctx, ctxKeyDelta, delta)
    cmd.SetContext(ctx)
    return nil
}
```

- [ ] **Step 3: Wire `app.go` to use loadAndStash**

Modify `cmd/agentdock/app.go`:

```go
var appCmd = &cobra.Command{
    Use:   "app",
    Short: "Run the main Slack bot",
    PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
        return loadAndStash(cmd, appConfigPath)
    },
    RunE: func(cmd *cobra.Command, args []string) error {
        cfg := cfgFromCtx(cmd.Context())
        return runApp(cfg)
    },
}
```

Change `runApp` signature to accept `*config.Config` instead of `string` path:

```go
func runApp(cfg *config.Config) error {
    // (delete the `cfg, err := config.Load(configPath)` block at the top)
    // (rest of body stays the same)
    ...
}
```

Update the default for `appConfigPath` from `"config.yaml"` to `""` (so default → resolveConfigPath default):

```go
appCmd.Flags().StringVarP(&appConfigPath, "config", "c", "", "path to config file (default ~/.config/agentdock/config.yaml)")
```

- [ ] **Step 4: Wire `worker.go` to use loadAndStash**

Same pattern. Modify `cmd/agentdock/worker.go`:

```go
var workerCmd = &cobra.Command{
    Use:   "worker",
    Short: "Run worker pool that processes jobs from Redis queue",
    PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
        return loadAndStash(cmd, workerConfigPath)
    },
    RunE: func(cmd *cobra.Command, args []string) error {
        cfg := cfgFromCtx(cmd.Context())
        return runWorker(cfg)
    },
}
```

`runWorker` becomes `func runWorker(cfg *config.Config) error`. Delete the old `cfg, err := config.Load/LoadDefaults` block.

- [ ] **Step 5: Build + smoke**

```bash
go build ./cmd/agentdock/
./agentdock app --help
./agentdock worker --help
```

Expected: build PASS, help works.

- [ ] **Step 6: Run tests**

```bash
go test ./...
```

Expected: PASS (existing tests don't exercise PreRunE flow).

- [ ] **Step 7: Commit**

```bash
git add cmd/agentdock/
git commit -m "feat(cli): replace config.Load with koanf-based PreRunE flow"
```

---

## Phase 4: Validation

### Task 14: Add `validate(cfg)` cross-field rules in `cmd/agentdock/validate.go`

**Files:**
- Create: `cmd/agentdock/validate.go`
- Test: `cmd/agentdock/validate_test.go`

- [ ] **Step 1: Write failing test**

```go
// cmd/agentdock/validate_test.go
package main

import (
    "strings"
    "testing"
    "time"

    "agentdock/internal/config"
)

func TestValidate_OK(t *testing.T) {
    cfg := goodConfig()
    if err := validate(cfg); err != nil {
        t.Errorf("validate(goodConfig) returned %v, want nil", err)
    }
}

func TestValidate_WorkersZero(t *testing.T) {
    cfg := goodConfig()
    cfg.Workers.Count = 0
    err := validate(cfg)
    if err == nil || !strings.Contains(err.Error(), "workers.count must be >= 1") {
        t.Errorf("expected workers.count error, got %v", err)
    }
}

func TestValidate_MultipleErrors_ListedAtOnce(t *testing.T) {
    cfg := goodConfig()
    cfg.Workers.Count = 0
    cfg.Queue.Capacity = -5
    cfg.Queue.JobTimeout = 0
    err := validate(cfg)
    if err == nil {
        t.Fatal("expected error")
    }
    msg := err.Error()
    for _, want := range []string{
        "workers.count must be >= 1",
        "queue.capacity must be >= 1",
        "queue.job_timeout must be > 0",
    } {
        if !strings.Contains(msg, want) {
            t.Errorf("expected %q in error, got: %s", want, msg)
        }
    }
}

func goodConfig() *config.Config {
    return &config.Config{
        Workers:   config.WorkersConfig{Count: 3},
        Queue:     config.QueueConfig{Capacity: 50, JobTimeout: 20 * time.Minute, AgentIdleTimeout: 5 * time.Minute, PrepareTimeout: 3 * time.Minute, StatusInterval: 5 * time.Second},
        RateLimit: config.RateLimitConfig{PerUser: 0, PerChannel: 0, Window: time.Minute},
    }
}
```

- [ ] **Step 2: Run, verify fail**

```bash
go test ./cmd/agentdock/ -run TestValidate -v
```

Expected: FAIL `undefined: validate`.

- [ ] **Step 3: Implement `cmd/agentdock/validate.go`**

```go
package main

import (
    "fmt"
    "strings"

    "agentdock/internal/config"
)

// validate runs all cross-field range checks on the merged Config and returns
// a single error listing every problem found (not fail-fast). Per D15.
func validate(cfg *config.Config) error {
    var errs []string

    if cfg.Workers.Count < 1 {
        errs = append(errs, "workers.count must be >= 1")
    }
    if cfg.Queue.Capacity < 1 {
        errs = append(errs, "queue.capacity must be >= 1")
    }
    if cfg.Queue.JobTimeout <= 0 {
        errs = append(errs, "queue.job_timeout must be > 0")
    }
    if cfg.Queue.AgentIdleTimeout <= 0 {
        errs = append(errs, "queue.agent_idle_timeout must be > 0")
    }
    if cfg.Queue.PrepareTimeout <= 0 {
        errs = append(errs, "queue.prepare_timeout must be > 0")
    }
    if cfg.Queue.StatusInterval <= 0 {
        errs = append(errs, "queue.status_interval must be > 0")
    }
    if cfg.RateLimit.PerUser < 0 {
        errs = append(errs, "rate_limit.per_user must be >= 0")
    }
    if cfg.RateLimit.PerChannel < 0 {
        errs = append(errs, "rate_limit.per_channel must be >= 0")
    }
    if cfg.RateLimit.Window <= 0 {
        errs = append(errs, "rate_limit.window must be > 0")
    }
    if cfg.MaxConcurrent < 1 {
        errs = append(errs, "max_concurrent must be >= 1")
    }
    if cfg.MaxThreadMessages < 1 {
        errs = append(errs, "max_thread_messages must be >= 1")
    }
    if cfg.SemaphoreTimeout <= 0 {
        errs = append(errs, "semaphore_timeout must be > 0")
    }
    if cfg.Logging.RetentionDays < 1 {
        errs = append(errs, "logging.retention_days must be >= 1")
    }

    if len(errs) > 0 {
        return fmt.Errorf("config validation failed:\n  %s", strings.Join(errs, "\n  "))
    }
    return nil
}
```

- [ ] **Step 4: Run, verify pass**

```bash
go test ./cmd/agentdock/ -run TestValidate -v
```

Expected: PASS.

- [ ] **Step 5: Wire validate into PreRunE**

Modify `loadAndStash` in `config.go`:

```go
func loadAndStash(cmd *cobra.Command, configPath string) error {
    resolved, err := resolveConfigPath(configPath)
    if err != nil {
        return err
    }
    cfg, _, kSave, delta, err := buildKoanf(cmd, resolved)
    if err != nil {
        return err
    }
    if configPath != "" && !delta.FileExisted {
        return fmt.Errorf("config file not found: %s; run 'agentdock init -c %s' first", resolved, resolved)
    }
    if err := validate(cfg); err != nil {
        return err
    }
    // ... rest unchanged
}
```

- [ ] **Step 6: Run all tests**

```bash
go test ./...
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add cmd/agentdock/validate.go cmd/agentdock/validate_test.go cmd/agentdock/config.go
git commit -m "feat(cli): add validate(cfg) cross-field validation in PreRunE"
```

---

### Task 15: Add pflag enum types for `--queue-transport` and `--log-level`

**Files:**
- Modify: `cmd/agentdock/flags.go`

- [ ] **Step 1: Append enum types to `cmd/agentdock/flags.go`**

```go
// queueTransportFlag is a pflag.Value that accepts only "redis" or "inmem".
type queueTransportFlag string

func (q *queueTransportFlag) String() string { return string(*q) }
func (q *queueTransportFlag) Type() string   { return "queue-transport" }
func (q *queueTransportFlag) Set(v string) error {
    switch v {
    case "redis", "inmem":
        *q = queueTransportFlag(v)
        return nil
    }
    return fmt.Errorf("must be one of [redis inmem]")
}

// logLevelFlag is a pflag.Value that accepts only debug/info/warn/error.
type logLevelFlag string

func (l *logLevelFlag) String() string { return string(*l) }
func (l *logLevelFlag) Type() string   { return "log-level" }
func (l *logLevelFlag) Set(v string) error {
    switch strings.ToLower(v) {
    case "debug", "info", "warn", "warning", "error":
        *l = logLevelFlag(v)
        return nil
    }
    return fmt.Errorf("must be one of [debug info warn error]")
}
```

Add `"strings"` to imports.

- [ ] **Step 2: Replace plain `String` registration with `Var` for these flags**

In `addPersistentFlags`:

Replace:
```go
cmd.PersistentFlags().String("log-level", "", "...")
cmd.PersistentFlags().String("logging-level", "", "...")
cmd.PersistentFlags().String("queue-transport", "", "...")
```

With:
```go
{
    var v queueTransportFlag
    cmd.PersistentFlags().Var(&v, "queue-transport", "queue transport: redis|inmem")
}
{
    var v logLevelFlag
    cmd.PersistentFlags().Var(&v, "log-level", "log level: debug|info|warn|error")
}
{
    var v logLevelFlag
    cmd.PersistentFlags().Var(&v, "logging-level", "file log level: debug|info|warn|error")
}
```

- [ ] **Step 3: Update `buildFlagOverrideMap` to handle enum types**

In flags.go, add cases:

```go
case "queue-transport", "log-level":
    out[key] = f.Value.String()
```

- [ ] **Step 4: Smoke test**

```bash
go build ./cmd/agentdock/
./agentdock app --queue-transport=foo
```

Expected: error like `invalid argument "foo" for "--queue-transport" flag: must be one of [redis inmem]`, exit 2.

```bash
./agentdock app --queue-transport=redis --help
```

Expected: works (no error).

- [ ] **Step 5: Run all tests**

```bash
go test ./...
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add cmd/agentdock/flags.go
git commit -m "feat(cli): add pflag enum types for queue-transport and log-level"
```

---

## Phase 5: Init subcommand

### Task 16: Move prompt helpers from `preflight.go` to `prompts.go`

**Files:**
- Create: `cmd/agentdock/prompts.go`
- Modify: `cmd/agentdock/preflight.go` (delete moved functions)

- [ ] **Step 1: Create `cmd/agentdock/prompts.go`**

Move from `preflight.go`: `promptLine`, `promptHidden`, `promptYesNo`, `printOK`, `printFail`, `printWarn`, `checkRedis`, `checkGitHubToken`, `checkAgentCLI`, plus the package-level `stderr` and `scanner` vars.

Move them verbatim — no signature changes. The new file:

```go
package main

import (
    "bufio"
    "bytes"
    "context"
    "encoding/json"
    "errors"
    "fmt"
    "net/http"
    "os"
    "os/exec"
    "strings"
    "syscall"
    "time"

    "github.com/redis/go-redis/v9"
    "golang.org/x/term"
)

var (
    stderr  = os.Stderr
    scanner = bufio.NewScanner(os.Stdin)
)

// (paste promptLine, promptHidden, promptYesNo, printOK, printFail, printWarn, checkRedis, checkGitHubToken, checkAgentCLI here verbatim from preflight.go)
```

- [ ] **Step 2: Delete those functions from `preflight.go`**

Remove the 9 functions + `stderr` / `scanner` vars from `preflight.go`. Leave `runPreflight`, `needsInput`, `sortedAgentNames`, `parseSelection`, `maxRetries` constant.

- [ ] **Step 3: Build, verify**

```bash
go build ./cmd/agentdock/
go test ./...
```

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add cmd/agentdock/prompts.go cmd/agentdock/preflight.go
git commit -m "refactor: extract prompt helpers from preflight to prompts.go"
```

---

### Task 17: Add `checkSlackToken` helper in `prompts.go`

**Files:**
- Modify: `cmd/agentdock/prompts.go`
- Test: `cmd/agentdock/prompts_test.go` (create if missing)

- [ ] **Step 1: Append `checkSlackToken` to `prompts.go`**

```go
// checkSlackToken verifies the bot token via Slack auth.test API.
// Returns the bot's user_id on success.
func checkSlackToken(token string) (string, error) {
    if token == "" {
        return "", errors.New("token is empty")
    }
    if !strings.HasPrefix(token, "xoxb-") {
        return "", errors.New("Slack bot token must start with xoxb-")
    }

    httpClient := &http.Client{Timeout: 10 * time.Second}
    req, _ := http.NewRequest(http.MethodPost, "https://slack.com/api/auth.test", nil)
    req.Header.Set("Authorization", "Bearer "+token)

    resp, err := httpClient.Do(req)
    if err != nil {
        return "", err
    }
    defer resp.Body.Close()

    var body struct {
        OK     bool   `json:"ok"`
        UserID string `json:"user_id"`
        Error  string `json:"error"`
    }
    if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
        return "", err
    }
    if !body.OK {
        return "", fmt.Errorf("auth.test failed: %s", body.Error)
    }
    return body.UserID, nil
}
```

- [ ] **Step 2: Add minimal sanity test**

```go
// cmd/agentdock/prompts_test.go
package main

import "testing"

func TestCheckSlackToken_RejectsEmpty(t *testing.T) {
    if _, err := checkSlackToken(""); err == nil {
        t.Error("expected error for empty token")
    }
}

func TestCheckSlackToken_RejectsBadPrefix(t *testing.T) {
    if _, err := checkSlackToken("not-a-slack-token"); err == nil {
        t.Error("expected error for token without xoxb- prefix")
    }
}
```

- [ ] **Step 3: Run, verify pass**

```bash
go test ./cmd/agentdock/ -run TestCheckSlackToken -v
```

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add cmd/agentdock/prompts.go cmd/agentdock/prompts_test.go
git commit -m "feat(cli): add checkSlackToken helper for Slack auth.test validation"
```

---

### Task 18: Implement `init` non-interactive (YAML + JSON)

**Files:**
- Modify: `cmd/agentdock/init.go`
- Test: `cmd/agentdock/init_test.go`

- [ ] **Step 1: Write failing test**

```go
// cmd/agentdock/init_test.go
package main

import (
    "encoding/json"
    "os"
    "path/filepath"
    "strings"
    "testing"
)

func TestInitNonInteractive_YAML(t *testing.T) {
    dir := t.TempDir()
    path := filepath.Join(dir, "config.yaml")
    if err := runInit(path, false /*interactive*/, false /*force*/); err != nil {
        t.Fatalf("runInit: %v", err)
    }
    data, err := os.ReadFile(path)
    if err != nil {
        t.Fatal(err)
    }
    content := string(data)
    if !strings.Contains(content, "# REQUIRED") {
        t.Error("YAML output should contain # REQUIRED comments")
    }
    if !strings.Contains(content, "claude:") {
        t.Error("YAML output should contain claude agent block")
    }
    info, _ := os.Stat(path)
    if info.Mode().Perm() != 0600 {
        t.Errorf("file mode = %o, want 0600", info.Mode().Perm())
    }
}

func TestInitNonInteractive_JSON(t *testing.T) {
    dir := t.TempDir()
    path := filepath.Join(dir, "config.json")
    if err := runInit(path, false, false); err != nil {
        t.Fatalf("runInit: %v", err)
    }
    data, err := os.ReadFile(path)
    if err != nil {
        t.Fatal(err)
    }
    var parsed map[string]any
    if err := json.Unmarshal(data, &parsed); err != nil {
        t.Fatalf("output is not valid JSON: %v", err)
    }
    // No comment lines allowed in JSON
    if strings.Contains(string(data), "# REQUIRED") {
        t.Error("JSON output should NOT contain comments")
    }
}

func TestInitNonInteractive_RejectsExisting(t *testing.T) {
    dir := t.TempDir()
    path := filepath.Join(dir, "config.yaml")
    if err := os.WriteFile(path, []byte("existing"), 0600); err != nil {
        t.Fatal(err)
    }
    err := runInit(path, false, false /*force*/)
    if err == nil || !strings.Contains(err.Error(), "already exists") {
        t.Errorf("expected 'already exists' error, got %v", err)
    }
}

func TestInitNonInteractive_ForceOverwrites(t *testing.T) {
    dir := t.TempDir()
    path := filepath.Join(dir, "config.yaml")
    os.WriteFile(path, []byte("existing"), 0600)
    if err := runInit(path, false, true /*force*/); err != nil {
        t.Fatalf("runInit force: %v", err)
    }
    data, _ := os.ReadFile(path)
    if string(data) == "existing" {
        t.Error("existing content should have been overwritten")
    }
}
```

- [ ] **Step 2: Run, verify fail**

```bash
go test ./cmd/agentdock/ -run TestInitNonInteractive -v
```

Expected: FAIL `undefined: runInit`.

- [ ] **Step 3: Implement `runInit` in `cmd/agentdock/init.go`**

Replace the file:

```go
package main

import (
    "encoding/json"
    "fmt"
    "os"
    "path/filepath"
    "strings"

    "agentdock/internal/config"

    "github.com/spf13/cobra"
    "gopkg.in/yaml.v3"
)

var (
    initConfigPath  string
    initForce       bool
    initInteractive bool
)

var initCmd = &cobra.Command{
    Use:   "init",
    Short: "Generate a starter config file",
    Long:  "Writes a starter config to the path specified by --config (default ~/.config/agentdock/config.yaml).",
    RunE: func(cmd *cobra.Command, args []string) error {
        path, err := resolveConfigPath(initConfigPath)
        if err != nil {
            return err
        }
        return runInit(path, initInteractive, initForce)
    },
}

func init() {
    initCmd.Flags().StringVarP(&initConfigPath, "config", "c", "", "path for new config file (default ~/.config/agentdock/config.yaml)")
    initCmd.Flags().BoolVar(&initForce, "force", false, "overwrite if file exists")
    initCmd.Flags().BoolVarP(&initInteractive, "interactive", "i", false, "prompt for required values")
    rootCmd.AddCommand(initCmd)
}

// runInit creates the starter config file at path. Format chosen by extension
// (.yaml/.yml → YAML w/ comments; .json → JSON; otherwise → YAML).
// If interactive, runs prompts for required secrets first.
func runInit(path string, interactive, force bool) error {
    if _, err := os.Stat(path); err == nil && !force {
        return fmt.Errorf("config already exists at %s; pass --force to overwrite", path)
    }
    if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
        return fmt.Errorf("create config dir: %w", err)
    }

    // Build the starter Config (defaults + built-in agents)
    var cfg config.Config
    // Round-trip via DefaultsMap to populate defaults
    data, _ := yaml.Marshal(config.DefaultsMap())
    yaml.Unmarshal(data, &cfg)
    cfg.Agents = map[string]config.AgentConfig{}
    for k, v := range config.BuiltinAgents {
        cfg.Agents[k] = v
    }

    // Interactive prompts (D14 set + Slack)
    prompted := map[string]any{}
    if interactive {
        if err := initPromptAll(&cfg, prompted); err != nil {
            return err
        }
    }

    // Marshal by extension (D17)
    var out []byte
    switch strings.ToLower(filepath.Ext(path)) {
    case ".json":
        b, err := json.MarshalIndent(&cfg, "", "  ")
        if err != nil {
            return fmt.Errorf("marshal json: %w", err)
        }
        out = b
    default:
        // YAML with comments
        b, err := marshalYAMLWithComments(&cfg)
        if err != nil {
            return fmt.Errorf("marshal yaml: %w", err)
        }
        out = b
    }

    return atomicWrite(path, out, 0600)
}

// marshalYAMLWithComments serializes cfg to YAML and inserts # REQUIRED markers
// for slack/github/redis blocks if their key fields are empty.
func marshalYAMLWithComments(cfg *config.Config) ([]byte, error) {
    raw, err := yaml.Marshal(cfg)
    if err != nil {
        return nil, err
    }
    text := string(raw)

    // Naive comment insertion: prepend a section header before each REQUIRED block.
    insertBefore := func(s, anchor, comment string) string {
        if strings.Contains(s, anchor) {
            return strings.Replace(s, anchor, comment+"\n"+anchor, 1)
        }
        return s
    }
    if cfg.Slack.BotToken == "" {
        text = insertBefore(text, "slack:", "# REQUIRED for `agentdock app`: Slack bot+app tokens")
    }
    if cfg.GitHub.Token == "" {
        text = insertBefore(text, "github:", "# REQUIRED for both subcommands: GitHub token")
    }
    if cfg.Redis.Addr == "" {
        text = insertBefore(text, "redis:", "# REQUIRED for both subcommands: Redis address")
    }
    text = "# Generated by `agentdock init`. See agentdock --help for flag overrides.\n" + text
    return []byte(text), nil
}

// atomicWrite writes data to path via tmp + rename, with mode 0600.
func atomicWrite(path string, data []byte, mode os.FileMode) error {
    tmp := path + ".tmp"
    if err := os.WriteFile(tmp, data, mode); err != nil {
        return err
    }
    return os.Rename(tmp, path)
}

// initPromptAll runs the 5 prompts (Slack bot, Slack app, GitHub, Redis, Providers).
// Implemented in Task 19; for Task 18 just stub.
func initPromptAll(cfg *config.Config, prompted map[string]any) error {
    return fmt.Errorf("init -i not yet implemented; coming in next task")
}
```

- [ ] **Step 4: Run tests, verify pass (non-interactive ones)**

```bash
go test ./cmd/agentdock/ -run TestInitNonInteractive -v
```

Expected: All 4 pass.

- [ ] **Step 5: Smoke test**

```bash
go build ./cmd/agentdock/
./agentdock init -c /tmp/agentdock-test.yaml
cat /tmp/agentdock-test.yaml | head -20
```

Expected: file created, contains `# REQUIRED` comments, `chmod 0600`.

```bash
./agentdock init -c /tmp/agentdock-test.yaml
```

Expected: error `config already exists at ...; pass --force to overwrite`.

- [ ] **Step 6: Commit**

```bash
git add cmd/agentdock/init.go cmd/agentdock/init_test.go
git commit -m "feat(cli): implement init non-interactive (YAML and JSON)"
```

---

### Task 19: Implement `init -i` interactive prompts

**Files:**
- Modify: `cmd/agentdock/init.go` (replace `initPromptAll` stub)

- [ ] **Step 1: Replace `initPromptAll` stub with real impl**

```go
// initPromptAll runs the 5 prompts for `init -i`: Slack bot/app tokens,
// GitHub token, Redis address, providers. Mutates cfg in-place and records
// keys in `prompted` for save-back tracking.
func initPromptAll(cfg *config.Config, prompted map[string]any) error {
    fmt.Fprintln(stderr)

    // Slack bot token
    fmt.Fprintln(stderr, "  Slack bot token (xoxb-...):")
    for attempt := 1; attempt <= maxRetries; attempt++ {
        token := promptHidden("Token: ")
        if token == "" {
            printFail("Slack bot token is required")
            if attempt == maxRetries {
                return fmt.Errorf("max retries exceeded for Slack bot token")
            }
            continue
        }
        userID, err := checkSlackToken(token)
        if err != nil {
            printFail("%v (attempt %d/%d)", err, attempt, maxRetries)
            if attempt == maxRetries {
                return fmt.Errorf("max retries exceeded for Slack bot token")
            }
            continue
        }
        cfg.Slack.BotToken = token
        prompted["slack.bot_token"] = token
        printOK("Slack bot token valid (user_id: %s)", userID)
        break
    }

    // Slack app token
    fmt.Fprintln(stderr)
    fmt.Fprintln(stderr, "  Slack app-level token (xapp-...):")
    for attempt := 1; attempt <= maxRetries; attempt++ {
        token := promptHidden("Token: ")
        if token == "" || !strings.HasPrefix(token, "xapp-") {
            printFail("must start with xapp- (attempt %d/%d)", attempt, maxRetries)
            if attempt == maxRetries {
                return fmt.Errorf("max retries exceeded for Slack app token")
            }
            continue
        }
        cfg.Slack.AppToken = token
        prompted["slack.app_token"] = token
        printOK("Slack app token format OK")
        break
    }

    // GitHub token
    fmt.Fprintln(stderr)
    fmt.Fprintln(stderr, "  GitHub token (ghp_... or github_pat_...):")
    fmt.Fprintln(stderr, "  Generate at: https://github.com/settings/tokens")
    for attempt := 1; attempt <= maxRetries; attempt++ {
        token := promptHidden("Token: ")
        if token == "" {
            printFail("GitHub token is required")
            if attempt == maxRetries {
                return fmt.Errorf("max retries exceeded for GitHub token")
            }
            continue
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
        prompted["github.token"] = token
        printOK("GitHub token valid (user: %s)", username)
        break
    }

    // Redis address
    fmt.Fprintln(stderr)
    for attempt := 1; attempt <= maxRetries; attempt++ {
        addr := promptLine("Redis address: ")
        if addr == "" {
            printFail("Redis address is required")
            if attempt == maxRetries {
                return fmt.Errorf("max retries exceeded for Redis address")
            }
            continue
        }
        if err := checkRedis(addr); err != nil {
            printFail("Redis connect failed: %v (attempt %d/%d)", err, attempt, maxRetries)
            if attempt == maxRetries {
                return fmt.Errorf("max retries exceeded for Redis")
            }
            continue
        }
        cfg.Redis.Addr = addr
        prompted["redis.addr"] = addr
        printOK("Redis connected")
        break
    }

    // Providers
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
                return fmt.Errorf("max retries exceeded for providers")
            }
            continue
        }
        cfg.Providers = selected
        prompted["providers"] = selected
        break
    }

    return nil
}
```

- [ ] **Step 2: Verify build**

```bash
go build ./cmd/agentdock/
```

Expected: PASS.

- [ ] **Step 3: Manual smoke test (optional, requires real tokens)**

```bash
./agentdock init -c /tmp/agentdock-test-i.yaml -i --force
```

Expected: prompts for 5 values; on completion writes file with values + chmod 0600.

(Skip if no real tokens available — tested via stubs in next task.)

- [ ] **Step 4: Commit**

```bash
git add cmd/agentdock/init.go
git commit -m "feat(cli): implement init -i interactive prompts (5 fields)"
```

---

## Phase 6: Save-back delta-only + preflight scopes

### Task 20: Add `ScopeApp` / `ScopeWorker` to `runPreflight`

**Files:**
- Modify: `cmd/agentdock/preflight.go`

- [ ] **Step 1: Add scope param to `runPreflight`**

Replace function signature and dispatch:

```go
type PreflightScope string

const (
    ScopeApp    PreflightScope = "app"
    ScopeWorker PreflightScope = "worker"
)

// runPreflight validates dependencies for the given scope and prompts (interactive)
// for missing values. Returns map of newly-prompted koanf keys for save-back.
func runPreflight(cfg *config.Config, scope PreflightScope) (map[string]any, error) {
    prompted := map[string]any{}
    interactive := term.IsTerminal(int(syscall.Stdin)) && needsInput(cfg, scope)

    fmt.Fprintln(stderr)

    // Common: Redis, GitHub token, Providers (existing logic)
    if err := preflightRedis(cfg, interactive, prompted); err != nil {
        return prompted, err
    }
    if err := preflightGitHub(cfg, interactive, prompted); err != nil {
        return prompted, err
    }
    if err := preflightProviders(cfg, interactive, prompted); err != nil {
        return prompted, err
    }

    // App-only: Slack tokens (D14)
    if scope == ScopeApp {
        if err := preflightSlackBot(cfg, interactive, prompted); err != nil {
            return prompted, err
        }
        if err := preflightSlackApp(cfg, interactive, prompted); err != nil {
            return prompted, err
        }
    }

    // Agent CLI version check (existing)
    if err := preflightAgentCLIs(cfg, interactive); err != nil {
        return prompted, err
    }

    fmt.Fprintf(stderr, "\n  Starting %s with: %s\n\n", scope, strings.Join(cfg.Providers, ", "))
    return prompted, nil
}

func needsInput(cfg *config.Config, scope PreflightScope) bool {
    base := cfg.Redis.Addr == "" || cfg.GitHub.Token == "" || len(cfg.Providers) == 0
    if scope == ScopeApp {
        return base || cfg.Slack.BotToken == "" || cfg.Slack.AppToken == ""
    }
    return base
}
```

- [ ] **Step 2: Extract existing prompt blocks into `preflightRedis` / `preflightGitHub` / `preflightProviders` / `preflightAgentCLIs` functions**

These are mechanical extractions of the existing if/for blocks in `runPreflight`. Each function takes `(cfg, interactive, prompted)` and follows the existing retry / printOK / printFail pattern. After successful prompt, ALSO write to `prompted` map:

```go
// Inside preflightRedis after successful checkRedis(addr):
cfg.Redis.Addr = addr
prompted["redis.addr"] = addr
```

Same for GitHub (`prompted["github.token"] = token`), Providers (`prompted["providers"] = selected`).

- [ ] **Step 3: Add `preflightSlackBot` and `preflightSlackApp`**

```go
func preflightSlackBot(cfg *config.Config, interactive bool, prompted map[string]any) error {
    if cfg.Slack.BotToken != "" {
        userID, err := checkSlackToken(cfg.Slack.BotToken)
        if err != nil {
            printFail("Slack bot token invalid: %v", err)
            return err
        }
        printOK("Slack bot token valid (user_id: %s)", userID)
        return nil
    }
    if !interactive {
        return fmt.Errorf("SLACK_BOT_TOKEN is required")
    }
    fmt.Fprintln(stderr)
    fmt.Fprintln(stderr, "  Slack bot token (xoxb-...):")
    for attempt := 1; attempt <= maxRetries; attempt++ {
        token := promptHidden("Token: ")
        if token == "" {
            printFail("Slack bot token is required")
            if attempt == maxRetries { return fmt.Errorf("max retries exceeded for Slack bot token") }
            continue
        }
        userID, err := checkSlackToken(token)
        if err != nil {
            printFail("%v (attempt %d/%d)", err, attempt, maxRetries)
            if attempt == maxRetries { return fmt.Errorf("max retries exceeded for Slack bot token") }
            continue
        }
        cfg.Slack.BotToken = token
        prompted["slack.bot_token"] = token
        printOK("Slack bot token valid (user_id: %s)", userID)
        return nil
    }
    return fmt.Errorf("unreachable")
}

func preflightSlackApp(cfg *config.Config, interactive bool, prompted map[string]any) error {
    if cfg.Slack.AppToken != "" {
        if !strings.HasPrefix(cfg.Slack.AppToken, "xapp-") {
            return fmt.Errorf("Slack app token must start with xapp-")
        }
        printOK("Slack app token format OK")
        return nil
    }
    if !interactive {
        return fmt.Errorf("SLACK_APP_TOKEN is required")
    }
    fmt.Fprintln(stderr)
    fmt.Fprintln(stderr, "  Slack app-level token (xapp-...):")
    for attempt := 1; attempt <= maxRetries; attempt++ {
        token := promptHidden("Token: ")
        if token == "" || !strings.HasPrefix(token, "xapp-") {
            printFail("must start with xapp- (attempt %d/%d)", attempt, maxRetries)
            if attempt == maxRetries { return fmt.Errorf("max retries exceeded for Slack app token") }
            continue
        }
        cfg.Slack.AppToken = token
        prompted["slack.app_token"] = token
        printOK("Slack app token format OK")
        return nil
    }
    return fmt.Errorf("unreachable")
}
```

- [ ] **Step 4: Update existing callers**

`worker.go`'s call site:
```go
prompted, err := runPreflight(cfg, ScopeWorker)
if err != nil { return err }
```

`app.go` will be wired in next task.

- [ ] **Step 5: Build**

```bash
go build ./cmd/agentdock/
```

Expected: PASS.

- [ ] **Step 6: Run tests**

```bash
go test ./...
```

Expected: PASS (existing preflight tests still apply for ScopeWorker; ScopeApp tested separately).

- [ ] **Step 7: Commit**

```bash
git add cmd/agentdock/preflight.go cmd/agentdock/worker.go
git commit -m "feat(cli): scope-aware preflight (App / Worker) with Slack token checks"
```

---

### Task 21: Implement `saveConfig` with delta-only + atomic write + chmod

**Files:**
- Modify: `cmd/agentdock/config.go`
- Test: `cmd/agentdock/config_test.go`

- [ ] **Step 1: Write failing test**

```go
// cmd/agentdock/config_test.go (append)

func TestSaveConfig_NoDelta_NoWrite(t *testing.T) {
    dir := t.TempDir()
    path := filepath.Join(dir, "config.yaml")
    if err := os.WriteFile(path, []byte("workers:\n  count: 7\n"), 0600); err != nil {
        t.Fatal(err)
    }

    // Build koanf with file existing, no flags, no env
    cmd := &cobra.Command{Use: "test"}
    addPersistentFlags(cmd)
    _, _, kSave, delta, _ := buildKoanf(cmd, path)

    // No prompted, file existed, no flag → must skip
    written, err := saveConfig(kSave, path, map[string]any{}, delta)
    if err != nil {
        t.Fatal(err)
    }
    if written {
        t.Error("saveConfig should skip when no delta")
    }
}

func TestSaveConfig_FlagOverride_Writes(t *testing.T) {
    dir := t.TempDir()
    path := filepath.Join(dir, "config.yaml")
    os.WriteFile(path, []byte("workers:\n  count: 7\n"), 0600)

    cmd := &cobra.Command{Use: "test"}
    addPersistentFlags(cmd)
    cmd.ParseFlags([]string{"--workers=5"})
    _, _, kSave, delta, _ := buildKoanf(cmd, path)

    written, err := saveConfig(kSave, path, map[string]any{}, delta)
    if err != nil {
        t.Fatal(err)
    }
    if !written {
        t.Error("saveConfig should write when flag override present")
    }
    data, _ := os.ReadFile(path)
    if !strings.Contains(string(data), "count: 5") {
        t.Errorf("file should contain count: 5, got: %s", data)
    }
}

func TestSaveConfig_PreflightPrompt_Writes(t *testing.T) {
    dir := t.TempDir()
    path := filepath.Join(dir, "config.yaml")
    os.WriteFile(path, []byte("workers:\n  count: 3\n"), 0600)

    cmd := &cobra.Command{Use: "test"}
    addPersistentFlags(cmd)
    _, _, kSave, delta, _ := buildKoanf(cmd, path)

    prompted := map[string]any{"redis.addr": "10.0.0.1:6379"}
    written, err := saveConfig(kSave, path, prompted, delta)
    if err != nil {
        t.Fatal(err)
    }
    if !written {
        t.Error("saveConfig should write when preflight prompted")
    }
    data, _ := os.ReadFile(path)
    if !strings.Contains(string(data), "10.0.0.1:6379") {
        t.Errorf("file should contain 10.0.0.1:6379, got: %s", data)
    }
}

func TestSaveConfig_Chmod0600(t *testing.T) {
    dir := t.TempDir()
    path := filepath.Join(dir, "config.yaml")

    cmd := &cobra.Command{Use: "test"}
    addPersistentFlags(cmd)
    _, _, kSave, delta, _ := buildKoanf(cmd, path)

    delta.FileExisted = false  // force write
    _, err := saveConfig(kSave, path, map[string]any{}, delta)
    if err != nil {
        t.Fatal(err)
    }
    info, _ := os.Stat(path)
    if info.Mode().Perm() != 0600 {
        t.Errorf("mode = %o, want 0600", info.Mode().Perm())
    }
}
```

- [ ] **Step 2: Run, verify fail**

```bash
go test ./cmd/agentdock/ -run TestSaveConfig -v
```

Expected: FAIL `undefined: saveConfig`.

- [ ] **Step 3: Implement `saveConfig`**

Append to `cmd/agentdock/config.go`:

```go
// saveConfig writes kSave to path if any delta condition is met (D13):
//   A. preflight prompted any value (`prompted` non-empty), or
//   B. flag override happened (`delta.HadFlagOverride`), or
//   C. config file didn't exist (`!delta.FileExisted`)
//
// Even if a condition is met, skips write if the marshaled output equals
// existing file content (race / no-op protection).
//
// Returns (written, error). Save failures are non-fatal; caller should warn-log.
func saveConfig(kSave *koanf.Koanf, path string, prompted map[string]any, delta DeltaInfo) (bool, error) {
    shouldWrite := len(prompted) > 0 || delta.HadFlagOverride || !delta.FileExisted
    if !shouldWrite {
        return false, nil
    }

    // Add prompted values to kSave so they marshal into output
    for k, v := range prompted {
        if err := kSave.Set(k, v); err != nil {
            return false, fmt.Errorf("kSave.Set(%s): %w", k, err)
        }
    }

    parser, err := pickParser(path)
    if err != nil {
        return false, err
    }
    data, err := kSave.Marshal(parser)
    if err != nil {
        return false, fmt.Errorf("marshal: %w", err)
    }

    // Skip if content is byte-identical to existing
    if existing, err := os.ReadFile(path); err == nil && bytes.Equal(existing, data) {
        return false, nil
    }

    if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
        return false, fmt.Errorf("mkdir: %w", err)
    }
    if err := atomicWrite(path, data, 0600); err != nil {
        return false, fmt.Errorf("write: %w", err)
    }
    return true, nil
}
```

Add `"bytes"` to imports.

(`atomicWrite` was defined in init.go at Task 18 — it's in the same package, accessible.)

- [ ] **Step 4: Run tests, verify pass**

```bash
go test ./cmd/agentdock/ -run TestSaveConfig -v
```

Expected: PASS for all 4 cases.

- [ ] **Step 5: Wire `saveConfig` into `loadAndStash` after preflight**

Modify `loadAndStash` in `config.go` — but actually we don't have preflight in PersistentPreRunE yet. Move the save-back call there:

Actually, save-back depends on having `prompted` from preflight. So the flow is:
- PersistentPreRunE: loadAndStash (build kSave, validate, run preflight with scope, save-back)

Update `loadAndStash` to take a scope and run preflight + save:

```go
func loadAndStash(cmd *cobra.Command, configPath string, scope PreflightScope) error {
    resolved, err := resolveConfigPath(configPath)
    if err != nil {
        return err
    }
    cfg, _, kSave, delta, err := buildKoanf(cmd, resolved)
    if err != nil {
        return err
    }
    if configPath != "" && !delta.FileExisted {
        return fmt.Errorf("config file not found: %s; run 'agentdock init -c %s' first", resolved, resolved)
    }
    if err := validate(cfg); err != nil {
        return err
    }
    prompted, err := runPreflight(cfg, scope)
    if err != nil {
        return fmt.Errorf("preflight: %w", err)
    }
    if _, err := saveConfig(kSave, resolved, prompted, delta); err != nil {
        slog.Warn("config save failed", "path", resolved, "error", err)
        // continue; not fatal
    }

    ctx := cmd.Context()
    ctx = context.WithValue(ctx, ctxKeyConfig, cfg)
    ctx = context.WithValue(ctx, ctxKeyKSave, kSave)
    ctx = context.WithValue(ctx, ctxKeyDelta, delta)
    cmd.SetContext(ctx)
    return nil
}
```

Add `"log/slog"` to imports.

- [ ] **Step 6: Update PersistentPreRunE callers in app.go and worker.go**

`app.go`:
```go
PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
    return loadAndStash(cmd, appConfigPath, ScopeApp)
},
```

`worker.go`:
```go
PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
    return loadAndStash(cmd, workerConfigPath, ScopeWorker)
},
RunE: func(cmd *cobra.Command, args []string) error {
    cfg := cfgFromCtx(cmd.Context())
    return runWorker(cfg)
},
```

Also delete the `runPreflight(cfg)` call inside `runWorker`'s body (preflight now runs in PreRunE).

- [ ] **Step 7: Run all tests + smoke**

```bash
go test ./...
go build ./cmd/agentdock/
./agentdock app --help  # PreRunE doesn't run on --help
```

Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add cmd/agentdock/
git commit -m "feat(cli): delta-only save-back wired into PreRunE after preflight"
```

---

## Phase 7: Docs and infrastructure

### Task 22: Delete `config.example.yaml` and write `docs/MIGRATION-v1.md`

**Files:**
- Delete: `config.example.yaml`
- Create: `docs/MIGRATION-v1.md`

- [ ] **Step 1: Delete config.example.yaml**

```bash
git rm config.example.yaml
```

- [ ] **Step 2: Create `docs/MIGRATION-v1.md`**

```markdown
# Migrating from AgentDock v0.x to v1.0

v1.0 introduces a new CLI based on spf13/cobra, persistent config in
`~/.config/agentdock/`, and an `init` subcommand for bootstrapping.
This is a hard breaking change — there is no `bot` alias.

## Quick reference

| Before (v0.x)                          | After (v1.0)                                            |
|----------------------------------------|---------------------------------------------------------|
| `./bot`                                | `agentdock app`                                         |
| `./bot worker`                         | `agentdock worker`                                      |
| `./bot -config /etc/agentdock.yaml`    | `agentdock app -c /etc/agentdock.yaml`                  |
| `./bot worker -config X`               | `agentdock worker -c X`                                 |
| (none)                                 | `agentdock init` to generate starter config             |
| `./bot -version`                       | `agentdock --version` or `agentdock -v`                 |
| `./bot -help`                          | `agentdock --help` or `agentdock -h`                    |

## Behavior changes

### 1. Env vars no longer override YAML

In v0.x, env vars (`REDIS_ADDR`, `GITHUB_TOKEN`, etc.) overrode YAML config.
In v1.0, the merge order is `flag > env > --config > default`. YAML wins
over env. CLI flags win over both.

If you relied on env-overriding-YAML, change to either:
- Pass via `--redis-addr=...` flag (highest priority)
- Edit the YAML and remove the env var

### 2. Env-derived secrets are NOT persisted

`REDIS_PASSWORD=xxx agentdock worker` works for that session, but the password
is NOT written to the config file. Next launch without env: Redis auth fails.

To persist secrets:
- Use `agentdock init -i` (interactive setup writes secrets to file)
- Pass via `--github-token=ghp_...` flag — flags ARE persisted

### 3. Default config path moved

v0.x defaulted to `./config.yaml` in the current directory.
v1.0 defaults to `~/.config/agentdock/config.yaml` (literal `~/.config`,
not `os.UserConfigDir`, on every platform).

To keep using your existing path: pass `-c /your/old/path/config.yaml`.

### 4. Save-back happens after every successful startup

v1.0 writes back the merged config when:
- A flag overrode any value, OR
- An interactive preflight prompt filled a value, OR
- The config file didn't exist

Pure read-only startups do NOT touch the file. Your manual YAML comments are
preserved across normal launches.

### 5. `config.example.yaml` removed

Use `agentdock init -c /tmp/sample.yaml` to see the schema.

### 6. Built-in agents are runtime fallback

`claude` / `codex` / `opencode` are built into the binary. Your config can
override individual entries by name; missing names fall back to built-in.
After upgrade, new built-in agents added in future versions appear automatically.

## Docker / docker-compose

```diff
# docker-compose.yml
services:
  app:
    image: ghcr.io/ivantseng123/agentdock:v1.0.0
-   command: ["./bot", "-config", "/etc/agentdock/config.yaml"]
+   command: ["agentdock", "app", "-c", "/etc/agentdock/config.yaml"]
  worker:
    image: ghcr.io/ivantseng123/agentdock:v1.0.0
-   command: ["./bot", "worker", "-config", "/etc/agentdock/config.yaml"]
+   command: ["agentdock", "worker", "-c", "/etc/agentdock/config.yaml"]
```

## systemd

```diff
# /etc/systemd/system/agentdock-app.service
[Service]
-ExecStart=/opt/agentdock/bot -config /etc/agentdock/config.yaml
+ExecStart=/opt/agentdock/agentdock app -c /etc/agentdock/config.yaml
```

## Validation behavior change

v1.0 validates config values on startup and lists ALL errors at once.
Previously, invalid values like `workers: 0` were silently auto-fixed
to defaults. Now the startup fails with a clear error message.

If you have config files with intentionally-invalid values that v0.x
silently fixed, update them to valid values before upgrading.

## Need help?

File an issue at https://github.com/Ivantseng123/agentdock/issues with
your old setup and what's broken — we'll add to this guide.
```

- [ ] **Step 3: Build, ensure config.example.yaml absence doesn't break anything**

```bash
go build ./...
go test ./...
```

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add docs/MIGRATION-v1.md
git commit -m "docs: add MIGRATION-v1.md and remove config.example.yaml

BREAKING CHANGE: binary renamed bot -> agentdock; subcommand required;
default config path moved to ~/.config/agentdock/config.yaml; env priority
inverted (YAML now wins over env). See docs/MIGRATION-v1.md."
```

---

### Task 23: Update Dockerfile, run.sh, workflows, README

**Files:**
- Modify: `Dockerfile`
- Modify: `run.sh`
- Modify: `.github/workflows/*.yml`
- Modify: `README.md`

(Most edits done in Task 1 for path; this task fixes binary entrypoint and adds subcommand to commands.)

- [ ] **Step 1: Verify `Dockerfile` ENTRYPOINT**

```dockerfile
# Should be:
ENTRYPOINT ["/agentdock"]
# CMD or actual subcommand provided by docker-compose / k8s manifest
```

If currently `ENTRYPOINT ["/agentdock", "app"]` or similar, that locks the image to one role. Better: keep ENTRYPOINT as just the binary, let runtime specify subcommand.

- [ ] **Step 2: Update `run.sh` to use new subcommand**

```bash
#!/usr/bin/env bash
go build -o agentdock ./cmd/agentdock/ && ./agentdock app -c config.yaml
```

(Or whatever the original did, with `app` subcommand added.)

- [ ] **Step 3: Update README.md**

Find Build & Run section, replace:

```bash
# Was
./run.sh
# or
go build -o bot ./cmd/bot/ && ./bot -config config.yaml

# Now
./run.sh
# or
go build -o agentdock ./cmd/agentdock/ && ./agentdock app -c config.yaml

# First time? Generate a starter config:
./agentdock init -i
```

Add link near top:

> **Upgrading from v0.x?** See [docs/MIGRATION-v1.md](docs/MIGRATION-v1.md).

- [ ] **Step 4: Update workflows**

`grep -rl 'cmd/bot\|/bot ' .github/` → for each file, update path + binary references.

Common spots:
- Test workflow: `go test ./...` unchanged
- Release workflow: `go build -o agentdock ./cmd/agentdock/`
- Docker workflow: image entrypoint as Dockerfile

- [ ] **Step 5: Smoke test build via Docker (if available)**

```bash
docker build -t agentdock:test .
docker run --rm agentdock:test --version
```

Expected: prints version.

- [ ] **Step 6: Commit**

```bash
git add Dockerfile run.sh README.md .github/
git commit -m "build: update Docker / run.sh / workflows / README for agentdock binary"
```

---

## Phase 8: Schema unknown-key warning

### Task 24: Warn on unknown config file keys after koanf load

**Files:**
- Modify: `cmd/agentdock/config.go`
- Test: `cmd/agentdock/config_test.go`

- [ ] **Step 1: Write failing test**

```go
// cmd/agentdock/config_test.go (append)

func TestBuildKoanf_WarnsOnUnknownKey(t *testing.T) {
    dir := t.TempDir()
    path := filepath.Join(dir, "test.yaml")
    yaml := `
workers:
  count: 3
reactions:
  approved: thumbsup  # v1 leftover
`
    os.WriteFile(path, []byte(yaml), 0600)

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
    if !strings.Contains(logBuf.String(), "unknown config key") || !strings.Contains(logBuf.String(), "reactions") {
        t.Errorf("expected warn about unknown key 'reactions', got log:\n%s", logBuf.String())
    }
}
```

Add `"log/slog"` import to test.

- [ ] **Step 2: Run, verify fail**

```bash
go test ./cmd/agentdock/ -run TestBuildKoanf_WarnsOnUnknownKey -v
```

Expected: FAIL (no warning yet).

- [ ] **Step 3: Implement key-walking helper + integrate into `buildKoanf`**

Append to `cmd/agentdock/config.go`:

```go
// validKoanfKeys returns the set of valid dotted koanf paths derived from
// Config struct yaml tags.
func validKoanfKeys() map[string]bool {
    out := map[string]bool{}
    walkYAMLPathsKeyOnly(reflect.TypeOf(config.Config{}), "", out)
    return out
}

func walkYAMLPathsKeyOnly(t reflect.Type, prefix string, out map[string]bool) {
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
        if ft.Kind() == reflect.Struct {
            walkYAMLPathsKeyOnly(ft, path, out)
        }
    }
}

// warnUnknownKeys logs warning for any koanf key not in the valid Config schema.
// Used after kEff load to catch typos / leftover v1 keys.
func warnUnknownKeys(k *koanf.Koanf) {
    valid := validKoanfKeys()
    for _, key := range k.Keys() {
        // Strip array indices like "channels.foo.repos.0" → "channels.foo.repos"
        // Then check top-level path matches a known prefix
        topLevel := strings.SplitN(key, ".", 2)[0]
        if !valid[topLevel] && !valid[key] {
            slog.Warn("unknown config key", "key", key)
        }
    }
}
```

Add `"reflect"` and `"log/slog"` imports.

In `buildKoanf`, after L1 file load, call `warnUnknownKeys(kEff)`:

```go
// L1: --config file ...
if err := kEff.Load(file.Provider(configPath), parser); err != nil {
    return ...
}
// ...
warnUnknownKeys(kEff)
```

Note: the test from Task 11 (`TestFlagToKey_ValuesMapToConfigYAMLPaths`) used a similar `walkYAMLPaths` helper in test code — consider extracting to a shared helper if you want to dedupe. For now, keep separate (test-only vs production).

- [ ] **Step 4: Run, verify pass**

```bash
go test ./cmd/agentdock/ -run TestBuildKoanf_WarnsOnUnknownKey -v
```

Expected: PASS.

- [ ] **Step 5: Run all tests**

```bash
go test ./...
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add cmd/agentdock/config.go cmd/agentdock/config_test.go
git commit -m "feat(cli): warn on unknown config keys (replaces v1 detection)"
```

---

## Final Verification

### Task 25: Remove deprecated `config.Load()` / `config.LoadDefaults()` / `applyEnvOverrides()`

These were preserved during phases 1-3 to keep build green. Now that all callers use `buildKoanf`, they're dead.

- [ ] **Step 1: Verify no callers**

```bash
grep -rn 'config\.\(Load\|LoadDefaults\|applyEnvOverrides\)' --include='*.go' .
```

Expected: only matches inside `internal/config/config.go` (the definitions themselves) and possibly `internal/config/config_test.go` (existing tests).

- [ ] **Step 2: If tests use them, update or remove**

If `internal/config/config_test.go` tests `TestLoad` / `TestLoadDefaults`: those tested the OLD loader. Replace with tests for `EnvOverrideMap` / `DefaultsMap` (from Task 2/3). Or keep if they still pass (they exercise yaml unmarshal which is fine).

- [ ] **Step 3: Delete the three functions from `internal/config/config.go`**

Remove: `Load()`, `LoadDefaults()`, `applyEnvOverrides()`, `v1RawCheck` struct (unused after deletion).

- [ ] **Step 4: Build + test**

```bash
go build ./...
go test ./...
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "refactor(config): remove deprecated Load/LoadDefaults/applyEnvOverrides

All callers migrated to cmd/agentdock buildKoanf flow."
```

---

## Self-Review Notes

(Plan author self-review — not part of execution)

**Spec coverage check:**

- D1 env layer not persisted → Task 12 (kSave excludes env)
- D2 literal `~/.config` → Task 12 (`resolveConfigPath`)
- D3 single shared config → Tasks 13, 21 (one path for both subcommands)
- D4 chmod 0600 + atomic → Tasks 18, 21 (`atomicWrite`)
- D5 Channels/Agents not flags → Task 11 (only listed scalar flags in `flagToKey`)
- D6 preflight retained → Task 20 (extended)
- D7 cobra + koanf hand-written → All
- D8 cobra root + 3 subs → Tasks 6, 7, 8, 9
- D9 cmd/agentdock flat → Task 1
- D10 prompts shared → Task 16
- D11 binary `agentdock` → Tasks 1, 23
- D12 DefaultsMap from applyDefaults → Task 3
- D13 delta-only save-back → Task 21
- D14 app preflight scope → Tasks 17, 20
- D15 pflag enum + validate → Tasks 14, 15
- D16 BuiltinAgents fallback → Tasks 4, 12
- D17 init follows extension → Task 18
- D18 v1.0.0 → Task 22 (commit message), eventually release-please
- D19 MIGRATION-v1.md → Task 22
- D20 no --config-format → (no task; explicitly omitted)
- D21 init --force -i wipe → Tasks 18, 19 (no preserve logic)
- D22 delete config.example.yaml → Task 22
- D23 -v = --version → Task 6 (cobra default)

All decisions covered.

**Type consistency:**

- `runApp(cfg *config.Config) error` — Task 7 (initial), Task 13 (sig change). Consistent.
- `runWorker(cfg *config.Config) error` — same.
- `runPreflight(cfg, scope) (map[string]any, error)` — Task 20 (introduced), used by Task 21. Consistent.
- `saveConfig(kSave, path, prompted, delta) (bool, error)` — Task 21 (introduced), used by Task 21 wiring. Consistent.
- `buildKoanf(cmd, path) (*Config, *koanf.Koanf, *koanf.Koanf, DeltaInfo, error)` — Task 12 introduces 5-tuple. Used in Task 13, 21, 24. Consistent.

**Placeholder scan:** All steps have either complete code or specific commands. No "TBD" or "implement later".

**Total tasks: 25**, organized in 8 phases matching the spec's phase guidance.
