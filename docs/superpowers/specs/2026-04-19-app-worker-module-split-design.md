# App / Worker Module Split — Design

**Date:** 2026-04-19
**Status:** Revised after grill-me review (2026-04-19)
**Related:** Issue #63 as originating context (actual `extra_args` implementation deferred to follow-up). Parent umbrella #54.

## Problem

`agentdock` is currently a single Go module with a single `Config` struct shared by app and worker. Every design discussion wastes time re-litigating the boundary between app (Slack orchestrator) and worker (agent-CLI executor), because the struct, the load flow, and several internal packages are shared.

The user has decided that app and worker are two fully independent entities (see memory: `app-worker-distinction`) and that the codebase should reflect that via module-level separation, with the long-term option of splitting into separate repositories (see memory: `app-worker-future-split`).

Originating use case: worker-side agent CLI behavior control (issue #63). That feature was blocked on deciding whether config should carry extra fields, which in turn exposed that the app/worker boundary in the code is not expressed strongly enough to reason about without ambiguity. Hence this refactor precedes #63.

## Goals

1. App, worker, and their shared types become **three separate Go modules** under one git repo, with dependencies only flowing `app → shared` and `worker → shared`. Module boundaries prevent accidental coupling by making cross-cuts require explicit import paths.
2. Config schema splits into `AppConfig` (owned by `app` module) and `WorkerConfig` (owned by `worker` module). **No shared Config struct; no shared yaml-tagged struct types**. Each module declares its own yaml types; overlap (RedisConfig, LoggingConfig, etc.) is accepted as a few dozen lines of duplication in exchange for schema-evolution independence.
3. Each module has its own `config.yaml` file (`app.yaml` / `worker.yaml`). **No automatic migration tool** — users manually rebuild two files via `agentdock init app` and `agentdock init worker`, guided by `docs/MIGRATION-v2.md`. Justified by pre-launch deployment scale (1 app + 3 worker machines).
4. **All existing user-observable behavior is preserved**, including inmem mode. The only user-visible change is the config file split and the new `--worker-config` flag for inmem.
5. **CI/CD pipeline is kept untouched** — single binary `agentdock`, single Docker image `ghcr.io/ivantseng123/agentdock:X`, single homebrew formula, single release-please flow. Binary dispatches to `app.Run()` or `worker.Run()` via cobra subcommands.
6. Module boundaries are enforced by a **whitelist-based** `import_direction_test.go` in the `shared` module, added in Phase 6.
7. **New `test.yml` GitHub Actions workflow** added in Phase 1 to run `go test ./...` across the four module roots on every PR.

## Non-goals

- Splitting the git repository (future step).
- Splitting the binary into `agentdock-app` and `agentdock-worker` (single binary retained).
- Per-module Docker images (`Dockerfile.release` unchanged).
- Per-module release versioning / release-please manifest mode (single tag retained).
- Issue #63 `extra_args` implementation (deferred to follow-up, 1-2 days once refactor lands).
- Fixing the `mergeBuiltinAgents` all-or-nothing merge bug (the parent #63 concern; deferred).
- Agent CLI environment isolation (XDG / HOME redirect to defeat `oh my opencode` style plugin pollution) — deferred as low-frequency concern.
- **Automatic config migration tool `agentdock migrate`** — with only 4 deployment machines pre-launch, manual rebuild via `init` is simpler than the tool plus golden-file tests.
- **Legacy `config.yaml` auto-detection** at startup — users who see "config file not found" can follow the `init` hint; no extra parsing code.

## Design

### Module Structure

```
agentdock/                            # git repo root
  go.mod                              # module github.com/Ivantseng123/agentdock
                                      # hosts cmd/agentdock (single binary entry)
  go.work                             # dev convenience; release build uses replace directive

  cmd/agentdock/
    main.go, root.go
    app.go                            # cobra 'app' subcommand → orchestrates app.Run + (inmem) LocalAdapter
    worker.go                         # cobra 'worker' subcommand → calls worker.Run
    init.go                           # agentdock init app / init worker / init (no sub = show help)

  app/
    go.mod                            # module github.com/Ivantseng123/agentdock/app
    app.go                            # func Run(cfg *Config, deps Deps) error
    config/                           # AppConfig, applyAppDefaults, buildAppKoanf, preflight, validate, init
    bot/                              # workflow, result_listener, retry_handler, status_listener, parser, enrich, skill_provider
    slack/
    mantis/
    metrics/
    skill/                            # loader, watcher, npx, validate (app-side only)

  worker/
    go.mod                            # module github.com/Ivantseng123/agentdock/worker
    worker.go                         # func Run(cfg *Config, deps Deps) error
    config/                           # WorkerConfig, applyWorkerDefaults, buildWorkerKoanf, preflight, validate, init, builtin_agents
    pool/                             # ex internal/worker (pool.go, executor.go) + local.go (ex cmd/agentdock/local_adapter.go)
    agent/                            # ex internal/bot/agent.go (AgentRunner)
    prompt/                           # ex internal/worker/prompt.go (XML builder)

  shared/
    go.mod                            # module github.com/Ivantseng123/agentdock/shared
    queue/                            # Job, JobResult, PromptContext, SkillPayload types + redis transport + mem store
    crypto/
    github/                           # shared GitHub client helpers
    logging/
    configloader/                     # pure helpers: pickParser, resolveConfigPath, atomicWrite,
                                      #               walkYAMLPathsKeyOnly, warnUnknownKeys, saveConfig
    connectivity/                     # pure helpers: CheckGitHubToken, CheckSlackToken, CheckRedis, VerifySecretBeacon
    test/
      import_direction_test.go        # whitelist enforcement (added Phase 6)
```

**Note: no `shared/config/` and no `shared/skill/` packages.** Yaml-tagged struct types and skill loader/watcher are not cross-module concerns.

**`SkillPayload`** lives in `shared/queue/job.go` alongside `Job` (since it is part of Job payload).

Root `go.mod` uses `replace` to point at local sibling modules:

```
module github.com/Ivantseng123/agentdock

require (
    github.com/Ivantseng123/agentdock/app    v0.0.0
    github.com/Ivantseng123/agentdock/worker v0.0.0
    github.com/Ivantseng123/agentdock/shared v0.0.0
)
replace (
    github.com/Ivantseng123/agentdock/app    => ./app
    github.com/Ivantseng123/agentdock/worker => ./worker
    github.com/Ivantseng123/agentdock/shared => ./shared
)
```

`app/go.mod` and `worker/go.mod` each declare their own `replace` for `shared`.

### Dependency Rules

```
cmd/agentdock ─► app
              ─► worker          (allowed because dispatcher lives in root module)
              ─► shared  (indirect via app/worker, and direct use is also OK)

app   ─► shared
worker ─► shared

app   ✗ worker            (forbidden; enforced)
worker ✗ app              (forbidden; enforced)
shared ✗ app | worker     (forbidden; enforced)
```

**Enforcement (Phase 6)**: `shared/test/import_direction_test.go` uses a **whitelist**:

```go
var moduleAllowedImports = map[string][]string{
    "github.com/Ivantseng123/agentdock/app":    {"github.com/Ivantseng123/agentdock/shared"},
    "github.com/Ivantseng123/agentdock/worker": {"github.com/Ivantseng123/agentdock/shared"},
    "github.com/Ivantseng123/agentdock/shared": {},  // cannot import any internal module
    "github.com/Ivantseng123/agentdock/cmd":    {"app", "worker", "shared"},
}
```

Uses `go/packages.Load` to scan every file; stdlib and external third-party imports (not starting with `github.com/Ivantseng123/agentdock/`) are automatically allowed. Violations fail the test with file:import details.

### Config Field Allocation

| Field | App | Worker | Notes |
|---|:-:|:-:|---|
| `LogLevel`, `Logging` | ✓ | ✓ | Each has its own |
| `Slack` | ✓ | | |
| `Server` (HTTP port) | ✓ | | Worker has no HTTP |
| `GitHub` (token) | ✓ | ✓ | Each independent (can share value) |
| `Agents`, `ActiveAgent`, `Providers` | | ✓ | App does not execute agents; agent name not passed in Job payload |
| `Channels`, `ChannelDefaults`, `AutoBind`, `ChannelPriority` | ✓ | | Slack routing |
| `MaxThreadMessages`, `SemaphoreTimeout`, `RateLimit` | ✓ | | Slack handler |
| `Mantis` | ✓ | | |
| `Prompt` (goal, output_rules, language, allow_worker_rules) | ✓ | | App assembles `Job.PromptContext`; `AllowWorkerRules` passed as bool to worker via Job payload |
| `Count`, `Prompt.ExtraRules` | | ✓ | **Flat in worker.yaml — no `worker:` prefix nest** |
| `Attachments` | ✓ | | App downloads from Slack to temp dir; worker consumes via Redis AttachmentBus |
| `SkillsConfig` | ✓ | | App loads skills → `Job.Skills` payload; worker materializes them in cloned repo |
| `RepoCache` | ✓ | ✓ | Each side has its own cache dir (different processes) |
| `Queue` (timeouts) | ✓ | ✓ | Watchdog on app, pool on worker; values should match |
| `Redis` | ✓ | ✓ | Same Redis, each side configures its own connection |
| `SecretKey` | ✓ | ✓ | Values must match (app writes beacon, worker verifies) |
| `Secrets` | ✓ | ✓ | App encrypts; worker decrypts; worker config may override |

**Sample `worker.yaml` (flat schema)**:

```yaml
log_level: info
github:
  token: ghp-...
count: 3                              # was worker.count
prompt:
  extra_rules:                        # was worker.prompt.extra_rules
    - "no guessing"
agents:
  claude:
    command: claude
    args: ["--print", "--output-format", "stream-json", "-p", "{prompt}"]
    timeout: 15m
    skill_dir: ".claude/skills"
    stream: true
active_agent: claude
providers: [claude, codex]
redis:
  addr: redis:6379
queue:
  job_timeout: 20m
secret_key: "..."
secrets:
  GH_TOKEN: "..."
```

Note: the `prompt:` block appears in both `app.yaml` and `worker.yaml` with different meaning — app's is about structuring the prompt (goal / output_rules / language / allow_worker_rules), worker's is about appending rules (`extra_rules`). Same name is intentional: both sides contribute to the same final prompt, each owning their segment.

### Load Flow Split

Per-scope load helpers in each module's `config/` package:

```go
// app/config/load.go
func BuildAppKoanf(cmd *cobra.Command, configPath string) (*Config, *koanf.Koanf, *koanf.Koanf, DeltaInfo, error)
func LoadAndStash(cmd *cobra.Command, configPath string) error
func Validate(cfg *Config) error
func RunPreflight(cfg *Config) (map[string]any, error)
func AppDefaultsMap() map[string]any
func AppEnvOverrideMap() map[string]any

// worker/config/load.go — symmetric
```

**Pure helpers live in `shared/configloader/`** (no type dependency on AppConfig / WorkerConfig):
- `PickParser(path)` — yaml/yml/json selector
- `ResolveConfigPath(in)` — expand `~/`, absolute path
- `AtomicWrite(path, data, mode)` — temp file + rename
- `WalkYAMLPathsKeyOnly(t, prefix, out, mapKeys)` — for warnUnknownKeys
- `WarnUnknownKeys(k)` — generic
- `SaveConfig(kSave, path, prompted, delta)` — delta-write logic

**Connectivity helpers live in `shared/connectivity/`**:
- `CheckGitHubToken(token)` — via go-github
- `CheckSlackToken(token)` — via slack-go
- `CheckRedis(addr, password, db, tls)` — ping
- `VerifySecretBeacon(redisClient, key)` — for worker startup

Env var allocation:

| Env | AppConfig | WorkerConfig |
|---|:-:|:-:|
| `SLACK_BOT_TOKEN`, `SLACK_APP_TOKEN`, `MANTIS_API_TOKEN` | ✓ | |
| `ACTIVE_AGENT`, `PROVIDERS` | | ✓ |
| `GITHUB_TOKEN`, `REDIS_ADDR`, `REDIS_PASSWORD`, `SECRET_KEY`, `AGENTDOCK_SECRET_*` | ✓ | ✓ |

`AppEnvOverrideMap()` and `WorkerEnvOverrideMap()` each emit only their scope's env keys.

CLI flags get sorted the same way: app subcommand owns Slack-related flags, worker subcommand owns agent/worker-count flags, and a small set of shared flags (e.g. `--redis-addr`) are added to both subcommands by their own registrar.

### Inmem Mode Handling

**The inmem orchestration lives in `cmd/agentdock/app.go`, not in `app/app.go`** — this respects the `app ✗ worker` rule (app module cannot import worker module).

The `app` subcommand gains `--worker-config <path>` flag, used only when `Queue.Transport != "redis"`. Default value: **the `worker.yaml` sitting next to the app config file** (e.g. if `-c /etc/agentdock/app.yaml`, default is `/etc/agentdock/worker.yaml`). This covers K8s ConfigMap layouts where both files mount in the same directory.

```go
// cmd/agentdock/app.go (dispatcher layer, imports both modules)
func runAppCmd(cmd *cobra.Command, args []string) error {
    appCfg, err := appconfig.LoadAndStash(cmd, appConfigPath)
    if err != nil { return err }

    // app.Run does NOT own inmem embedding; it only handles app-native work
    // (Slack handler, workflow, watchdog, result listener).
    appDeps, err := app.Run(appCfg, assembleAppDeps(appCfg))
    if err != nil { return err }

    if appCfg.Queue.Transport != "redis" {
        workerCfgPath := resolveWorkerConfigForInmem(appConfigPath, workerConfigFlag)
        wcfg, err := workerconfig.Load(workerCfgPath)
        if err != nil {
            return fmt.Errorf(
                "inmem mode requires worker configuration, but none found\n"+
                "  tried: %s (not found)\n"+
                "  run: agentdock init worker\n"+
                "  or:  agentdock app --worker-config /path/to/worker.yaml",
                workerCfgPath)
        }
        if err := workerpool.StartLocal(wcfg, appDeps.Buses); err != nil {
            return err
        }
    }
    return appDeps.Wait()
}
```

Key moves:
- `LocalAdapter` code migrates from `cmd/agentdock/local_adapter.go` → `worker/pool/local.go`
- `cmd/agentdock/adapters.go` stays in root module (it's the dispatcher-level type-bridge between app's dependency interfaces and worker's pool input)
- `app.Run` signature: takes `AppConfig` and `AppDeps` (buses, clients, loggers assembled by cmd layer), returns `RunHandle` that caller can `Wait()` on or receive `Buses` to hand to worker pool in inmem case
- Redis mode: `app.Run` does everything app-native; `app` binary subcommand on worker machines runs `worker.Run` which owns the pool

### Manual Config Rebuild (replaces Migration Tool)

**No `agentdock migrate` command.** Users rebuild `app.yaml` and `worker.yaml` manually, either:
- Via `agentdock init app -i` and `agentdock init worker -i` (interactive), or
- By copy-editing example yaml files from `docs/configuration-app.md` / `docs/configuration-worker.md`

**Binary startup behavior**:
- No active detection of legacy `~/.config/agentdock/config.yaml`
- If `-c` not passed and default path (`~/.config/agentdock/app.yaml` for app, `~/.config/agentdock/worker.yaml` for worker) doesn't exist, return standard "config file not found" error with a hint:
  ```
  Error: config file not found: /home/user/.config/agentdock/app.yaml
  Run `agentdock init app -i` to create one, or pass -c /path/to/app.yaml
  ```

**`docs/MIGRATION-v2.md` content**:
- Side-by-side field mapping (legacy `config.yaml` key → `app.yaml` or `worker.yaml` new location)
- Interactive init walkthrough
- Notes on `worker.yaml` schema flattening (legacy `worker.count` → new `count` at top level)
- K8s ConfigMap update steps (split into `app-config` and `worker-config` mounts)

### `agentdock init` Restructure

Cobra subcommand tree:
- `agentdock init app [-c app.yaml] [-i]` — app starter config; interactive asks Slack tokens + GitHub token + Redis + SecretKey
- `agentdock init worker [-c worker.yaml] [-i]` — worker starter config; interactive asks GitHub token + Redis + SecretKey + Providers + ActiveAgent
- `agentdock init` (no sub-command) — **displays cobra help listing the two sub-commands** (standard cobra behavior; zero implicit action)
- `agentdock init --all` (optional convenience) — runs both in sequence; deferred as nice-to-have, not Phase 5 requirement

### Docs Restructure

Top-level `README.md` keeps the overall introduction (product positioning, "what is AgentDock", architecture diagram) and links to sub-READMEs. `app/` and `worker/` each get their own `README.md` covering only that module — **there is no content overlap**, just cross-links where relevant.

| File | Action |
|---|---|
| `README.md` / `.en.md` | Keep intro and architecture only; link to `app/README.md` and `worker/README.md` |
| `app/README.md` / `.en.md` | New — app-specific setup, K8s deployment, configuration pointer |
| `worker/README.md` / `.en.md` | New — worker-specific setup, local usage, configuration pointer |
| `docs/configuration.md` / `.en.md` | Split into `configuration-app.md` and `configuration-worker.md`; original becomes index |
| `docs/MIGRATION-v2.md` / `.en.md` | New — manual rebuild guide with field mapping table, K8s ConfigMap split steps, interactive init walkthrough |
| `CLAUDE.md` | Update Landmines to reflect module structure and import rules |

### Testing Strategy

1. **Unit tests travel with source**: `internal/config/config_test.go` splits into `app/config/*_test.go` and `worker/config/*_test.go`; assertions unchanged, only imports updated. Same for `internal/bot/*_test.go`, `internal/worker/*_test.go`, etc.
2. **Import direction test** (Phase 6): `shared/test/import_direction_test.go` implements **whitelist enforcement** using `go/packages.Load`. Pure stdlib + `go/packages`, zero external dependency. Fails with file:line and disallowed-import path on violation.
3. **New `test.yml` workflow** (Phase 1): runs on every PR with:
    ```yaml
    - run: go test ./...                  # root module
    - run: (cd app && go test ./...)
    - run: (cd worker && go test ./...)
    - run: (cd shared && go test ./...)
    - run: go vet ./...
    ```
    Script `script/test-all.sh` provides the same sequence for local dev. If multi-module `go test ./...` behaves cleanly via `go.work`, the subshell invocations may collapse into one — verified during Phase 1 spike.

No migrate golden-file tests (migrate tool cancelled).

## Implementation Phases

Breaking changes (Phase 4) must ship together with Phase 5 as one PR to avoid broken main — unless main-broken-between-phases is acceptable (which per project status memory, it is). Phases 1~3 and Phase 6 each ship as independent PRs.

### Phase 1 — Shared module + CI test workflow + feasibility spike (low risk)

```
commits:
  0.  spike(build): verify goreleaser+replace build works with multi-module setup
      (creates minimal 3-module hello-world, runs `goreleaser release --snapshot --clean --skip=publish`,
       verifies single agentdock binary dispatches to app/worker sub-commands — if fails,
       pivot to go.work-only or single-module design before further work)
  1.  chore(go): rename module agentdock → github.com/Ivantseng123/agentdock
  2.  chore(ci): add test.yml workflow (initial: go test ./... on root only)
  3.  feat(shared): introduce shared module with replace directive
  4.  refactor(shared): move internal/queue → shared/queue (includes SkillPayload type)
  5.  refactor(shared): move internal/crypto → shared/crypto
  6.  refactor(shared): move internal/logging → shared/logging
  7.  refactor(shared): move internal/github → shared/github
  8.  feat(shared): introduce shared/configloader and shared/connectivity with pure helpers extracted from internal/config + cmd/agentdock/preflight
  9.  chore(ci): update test.yml to also run tests in shared module
```

End of Phase 1: `agentdock` binary behavior identical; shared module active; test workflow running.

### Phase 2 — Worker module

```
commits:
  10. feat(worker): introduce worker module
  11. refactor(worker): move internal/worker/{pool,executor} → worker/pool
  12. refactor(worker): move cmd/agentdock/local_adapter.go → worker/pool/local.go
  13. refactor(worker): move internal/bot/agent.go → worker/agent/runner.go
  14. refactor(worker): move internal/worker/prompt.go → worker/prompt/builder.go
  15. refactor(worker): move internal/config/builtin_agents.go → worker/config/
  16. refactor(worker): expose worker.Run(cfg, deps) entry; wire cmd/agentdock/worker.go to call it
  17. chore(ci): update test.yml to run tests in worker module
```

End of Phase 2: worker code isolated in its module; Config struct still legacy.

### Phase 3 — App module

```
commits:
  18. feat(app): introduce app module
  19. refactor(app): move internal/slack → app/slack
  20. refactor(app): move internal/mantis → app/mantis
  21. refactor(app): move internal/metrics → app/metrics
  22. refactor(app): move internal/bot/{workflow,result_listener,retry_handler,status_listener,parser,enrich,skill_provider} → app/bot
  23. refactor(app/bot): remove dead agentRunner field from Workflow struct (was assigned but never read)
  24. refactor(app): move internal/skill/{loader,watcher,npx,validate} → app/skill
  25. refactor(app): expose app.Run(cfg, deps) entry; wire cmd/agentdock/app.go (redis mode path)
  26. chore(ci): update test.yml to run tests in app module
```

End of Phase 3: all four modules co-exist; Config struct still legacy; workflow.go dead code cleaned.

### Phase 4 — Config split (high risk, ships with Phase 5)

```
commits:
  27. feat(app/config): introduce AppConfig struct + applyAppDefaults + AppDefaultsMap + AppEnvOverrideMap
  28. feat(worker/config): introduce WorkerConfig struct (flat schema: no worker: nest) + applyWorkerDefaults + WorkerDefaultsMap + WorkerEnvOverrideMap
  29. feat(app/config): implement buildAppKoanf + LoadAndStash + Validate + RunPreflight using shared/configloader
  30. feat(worker/config): implement buildWorkerKoanf + LoadAndStash + Validate + RunPreflight using shared/configloader
  31. refactor(app): app.Run signature takes *AppConfig
  32. refactor(worker): worker.Run signature takes *WorkerConfig
  33. feat(cmd): --worker-config flag for inmem mode; cmd/agentdock/app.go orchestrates dispatch
  34. chore(cleanup): remove internal/config (legacy Config struct no longer needed; buildKoanf, mergeBuiltinAgents etc. replaced by per-module versions)
```

End of Phase 4 (before PR-4 lands): legacy config.yaml no longer readable.

### Phase 5 — User-facing cutover + docs (ships with Phase 4 in same PR)

```
commits:
  35. refactor(cmd): default config paths → app.yaml / worker.yaml; init splits into init app / init worker / init (no sub = help)
  36. docs: MIGRATION-v2.md with manual rebuild guide + field mapping table
  37. docs: split configuration.md → configuration-app.md + configuration-worker.md
  38. docs: README.md intro only; new app/README.md + worker/README.md
  39. chore(release): v2.0.0 release notes with BREAKING CHANGE trailer for release-please
```

End of Phase 5: user-facing v2 release ready; breaking change announced.

### Phase 6 — Cleanup + enforcement

```
commits:
  40. chore: remove remaining legacy internal/ directories (internal/bot, internal/worker, etc. — already emptied by Phase 2~3, this just removes stale dirs)
  41. test(shared): add shared/test/import_direction_test.go with whitelist enforcement
  42. docs(CLAUDE.md): update Landmines for new module structure and import rules
  43. docs(README): point to new v2 flow
```

End of Phase 6: boundary enforced; all docs updated.

### PR Breakdown

| PR | Phases | Approx commits |
|---|---|---|
| PR-1 | 1 | 10 (incl. spike commit) |
| PR-2 | 2 | 8 |
| PR-3 | 3 | 9 |
| **PR-4** | 4 + 5 | 13 |
| PR-5 | 6 | 4 |

PR-4 is large but PR-4 is the atomic unit of user-facing breaking change.

## Risks & Mitigation

| Risk | Likelihood | Impact | Mitigation |
|---|:-:|:-:|---|
| User forgets to rebuild config after upgrade | Medium | Medium | Standard "config not found" error includes `init` hint; `MIGRATION-v2.md` field-mapping table is visible in release notes; only 4 machines pre-launch so support burden is minimal |
| K8s production deployment breaks | Low (pre-launch) | Low (pre-launch) | MIGRATION-v2.md gives explicit ConfigMap split steps; rollback is `image: agentdock:v1.x` revert; `.v1.bak` not needed |
| Goreleaser + replace directive incompat | Medium | High | Phase 1 commit 0 is a dedicated spike validating `goreleaser release --snapshot` works; fallback options in spike notes (go.work-only / single-module) |
| Import path churn causes CI failure | Medium | Medium | `go mod tidy` per module after each phase; `test.yml` workflow catches regressions per PR |
| Inmem mode breaks | Medium | Medium | End-to-end manual validation in DoD; clear error message when `--worker-config` missing |
| Scope creep into issue #63 or other features | Medium | Medium | Issue #63 explicitly deferred; phase gates prevent feature additions during refactor |
| Release-please confused by branching | Low | Low | All PRs target main directly; each merge triggers release-please once; BREAKING CHANGE trailer on Phase 5 final commit triggers major bump |

## Success Criteria (DoD)

1. `test.yml` workflow green — `go test ./...` and `go vet ./...` pass in root, `app/`, `worker/`, `shared/`.
2. `release-validate.yml` snapshot build green.
3. `shared/test/import_direction_test.go` green (whitelist enforcement works; no violations).
4. End-to-end manual validation:
    - Redis mode: `agentdock app -c app.yaml` + `agentdock worker -c worker.yaml` complete one triage round-trip.
    - Inmem mode: `agentdock app -c app.yaml --worker-config worker.yaml` completes one triage round-trip.
5. A user with a typical v1 `config.yaml` can follow `docs/MIGRATION-v2.md` and reach a working v2 setup (verified by running through the guide on the 3 worker machines + 1 app pod).

## Follow-up (Not in this spec)

- Issue #63 `extra_args`: adds `WorkerConfig.Agents.<name>.ExtraArgs` field and a `{extra_args}` placeholder in `BuiltinAgents` args. Estimated 1-2 days post-refactor.
- `mergeBuiltinAgents` all-or-nothing partial-override fix (original #63 scope): deferred; may be revisited if users hit the trap.
- Agent CLI environment isolation (XDG / HOME redirect) for plugin-pollution defense: deferred as low-frequency concern; needs per-agent-CLI feasibility study.
- Issue #54 sub-issue #62 (worker takes over output / Slack response text): independent spec.
- Future git repository split (separate repos for app and worker): deferred until team or release cadence diverges.
- `agentdock init --all` convenience command: deferred as nice-to-have.
- Goreleaser `gomod.proxy` enablement (after module rename to full URL): can be turned on in Phase 6 or later; improves reproducible build but not required.
