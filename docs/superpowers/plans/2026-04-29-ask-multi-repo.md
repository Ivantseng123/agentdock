# Ask Multi-Repo Reference Support — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development` (recommended) or `superpowers:executing-plans` to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement Ask workflow multi-repo reference support per spec — primary repo + N read-only ref repos, with refs cloned to an external path co-located with the primary worktree, sequential clone with partial-success, three-layer write-protection (skill prompt + output_rules injection + post-execute guard callback).

**Architecture:** Two-PR delivery aligned with module ownership.
- **PR 1 (backend, ~300 lines):** schema, prompt builder, worker prepare flow, post-execute guard. Lands without user-facing impact — produces no new behavior until PR 2 emits ref-bearing jobs.
- **PR 2 (frontend + e2e, ~400 lines):** app workflow phases, BuildJob output_rules injection, SKILL.md update, e2e verification.

**Tech Stack:** Go 1.25, three modules (`shared/`, `app/`, `worker/`), git CLI, slack-go. Test framework: stdlib `testing`. No new dependencies.

**Spec:** `docs/superpowers/specs/2026-04-29-ask-multi-repo-design.md`

---

## File Structure

| Path | Module | PR | Responsibility |
|---|---|---|---|
| `shared/queue/job.go` | shared | 1 | `Job.RefRepos`, `RefRepo`, `PromptContext.RefRepos`, `PromptContext.UnavailableRefs`, `RefRepoContext` |
| `shared/queue/job_test.go` | shared | 1 | JSON round-trip; old-job (no `RefRepos`) backwards-compat |
| `worker/prompt/builder.go` | worker | 1 | `<ref_repos>` / `<unavailable_refs>` rendering between `<issue_context>` and `<response_language>` |
| `worker/prompt/builder_test.go` | worker | 1 | Render presence/absence by field state |
| `worker/pool/executor.go` | worker | 1 | `RepoProvider.PrepareAt` interface; `executeJob` ref orchestration; post-execute guard with `RefViolationCallback` |
| `worker/pool/adapters.go` | worker | 1 | `RepoCacheAdapter.PrepareAt` impl |
| `worker/pool/workdir.go` | worker | 1 | `RepoCloneProvider.Cleanup` extended to also rm refs root dir |
| `worker/pool/workdir_test.go` | worker | 1 | `fakeRepoProvider.PrepareAt` + ref cleanup ordering test |
| `worker/pool/executor_test.go` | worker | 1 | Post-execute guard callback unit test (fake `git status`) |
| `worker/pool/pool_test.go` | worker | 1 | `mockRepo.PrepareAt` extension to satisfy interface |
| `worker/integration/queue_redis_integration_test.go` | worker | 1 | Multi-repo Ask job e2e — primary + refs + partial-fail; cleanup invariants |
| `worker/integration/queue_integration_test.go` | worker | 1 | `fakeRepo.PrepareAt` extension to satisfy interface |
| `shared/metrics/metrics.go` | shared | 1 | New counter `ask_ref_write_violations_total` |
| `app/workflow/ask.go` | app | 2 | `askState` extensions; new phases `ask_ref_decide` / `ask_ref_pick` / `ask_ref_continue` / `ask_ref_branch`; `BuildJob` ref + output_rules injection |
| `app/workflow/ask_test.go` | app | 2 | Phase transitions; candidate filter (primary + dedup); 0-candidate skip; per-ref branch flow; output_rules injection |
| `app/agents/skills/ask-assistant/SKILL.md` | app | 2 | Add "Reference repos" subsection in §5 with critical-fail-fast rule |

---

## PR 1 — Backend

### Task 1: Schema additions in `shared/queue`

**Files:**
- Modify: `shared/queue/job.go`
- Test: `shared/queue/job_test.go`

**Background:**

`Job` and `PromptContext` need additive fields with `omitempty` so old jobs serialize byte-for-byte unchanged. `RefRepoContext.Path` is filled by worker at runtime (post-prepare); the JSON tags exist because PromptContext is the carrier from worker-prepare to worker-build-prompt — they happen to round-trip through JSON nowhere on the wire (Job is the wire format), but the tags are kept for symmetry with other PromptContext fields and to allow optional debug serialization.

**Steps:**

- [ ] **1.1 Add `RefRepo` type + `Job.RefRepos` field.**

In `shared/queue/job.go`, between existing `AttachmentMeta` and `ThreadMessage` (matching grouping of other request-side types):

```go
// RefRepo is a read-only reference repo attached alongside the primary repo.
// Repo is owner/name (mirrors Job.Repo); CloneURL is the worker's clone target;
// Branch empty = default branch.
type RefRepo struct {
    Repo     string `json:"repo"`
    CloneURL string `json:"clone_url"`
    Branch   string `json:"branch,omitempty"`
}
```

In `Job` struct, append (omitempty):

```go
RefRepos []RefRepo `json:"ref_repos,omitempty"`
```

- [ ] **1.2 Add `RefRepoContext` type + `PromptContext.RefRepos` / `UnavailableRefs` fields.**

Below `ThreadMessage`:

```go
// RefRepoContext is one ref repo as seen by the prompt builder. Path is the
// absolute on-disk path the agent should grep/read; filled by worker after
// PrepareAt succeeds. Empty Branch = default branch.
type RefRepoContext struct {
    Repo   string `json:"repo"`
    Branch string `json:"branch,omitempty"`
    Path   string `json:"path"`
}
```

In `PromptContext`, append:

```go
RefRepos        []RefRepoContext `json:"ref_repos,omitempty"`
UnavailableRefs []string         `json:"unavailable_refs,omitempty"`
```

- [ ] **1.3 Round-trip + backwards-compat tests.**

In `shared/queue/job_test.go`, add:

```go
func TestJob_RefRepos_RoundTrip(t *testing.T) {
    in := Job{
        ID: "abc", Repo: "foo/bar", CloneURL: "https://example/foo/bar.git",
        RefRepos: []RefRepo{
            {Repo: "frontend/web", CloneURL: "https://example/frontend/web.git", Branch: "main"},
            {Repo: "backend/api", CloneURL: "https://example/backend/api.git"},
        },
    }
    raw, err := json.Marshal(in)
    if err != nil { t.Fatal(err) }
    var out Job
    if err := json.Unmarshal(raw, &out); err != nil { t.Fatal(err) }
    // Compare RefRepos field-by-field; full deepequal would also need other
    // unset fields to align, which adds noise.
    if !reflect.DeepEqual(in.RefRepos, out.RefRepos) {
        t.Fatalf("RefRepos mismatch: in=%+v out=%+v", in.RefRepos, out.RefRepos)
    }
}

func TestJob_OldJob_NoRefRepos_StillParses(t *testing.T) {
    // Old job shape — no ref_repos key at all; must unmarshal cleanly with
    // RefRepos == nil.
    raw := []byte(`{"id":"abc","repo":"foo/bar","clone_url":"https://example/foo/bar.git"}`)
    var out Job
    if err := json.Unmarshal(raw, &out); err != nil { t.Fatal(err) }
    if out.RefRepos != nil {
        t.Fatalf("expected nil RefRepos on old-job parse, got %+v", out.RefRepos)
    }
}
```

Same pattern for `PromptContext.RefRepos` / `UnavailableRefs` round-trip.

**Verification:**
- [ ] `go test ./shared/queue/...` passes.
- [ ] No diff in serialized form for any existing test job.

**Estimated scope:** S (1-2 files, ~80 LOC + tests).

---

### Task 2: Prompt builder rendering

**Files:**
- Modify: `worker/prompt/builder.go`
- Test: `worker/prompt/builder_test.go`

**Background:**

`BuildPrompt` currently renders blocks in a fixed order. Per spec §4.5, the new blocks slot after `<issue_context>` and before `<response_language>`. The `<ref_repos>` block lists each ref's repo, branch, and absolute path; `<unavailable_refs>` is a parallel block listing failed refs by `owner/name`. Both blocks render only when their corresponding slice is non-empty (mirrors `<extra_description>` / `<prior_answer>` patterns already in the function).

The path attribute is rendered raw (after `xmlEscape`) — agents need the literal path to `Read` / `Grep`. Branch is omitted when empty so default-branch refs don't get an empty `branch=""` attribute.

**Steps:**

- [ ] **2.1 Add `<ref_repos>` rendering after `<issue_context>` close.**

In `BuildPrompt`, after the `</issue_context>\n\n` write:

```go
if len(ctx.RefRepos) > 0 {
    b.WriteString("<ref_repos>\n")
    for _, r := range ctx.RefRepos {
        if r.Branch != "" {
            fmt.Fprintf(&b,
                "  <ref repo=\"%s\" branch=\"%s\" path=\"%s\"/>\n",
                xmlEscape(r.Repo), xmlEscape(r.Branch), xmlEscape(r.Path),
            )
        } else {
            fmt.Fprintf(&b,
                "  <ref repo=\"%s\" path=\"%s\"/>\n",
                xmlEscape(r.Repo), xmlEscape(r.Path),
            )
        }
    }
    b.WriteString("</ref_repos>\n\n")
}

if len(ctx.UnavailableRefs) > 0 {
    b.WriteString("<unavailable_refs>\n")
    for _, repo := range ctx.UnavailableRefs {
        fmt.Fprintf(&b, "  <repo>%s</repo>\n", xmlEscape(repo))
    }
    b.WriteString("</unavailable_refs>\n\n")
}
```

- [ ] **2.2 Tests for both render conditions.**

In `worker/prompt/builder_test.go`:

```go
func TestBuildPrompt_RefReposRendered(t *testing.T) {
    ctx := queue.PromptContext{
        Goal: "g", Channel: "c", Reporter: "r",
        RefRepos: []queue.RefRepoContext{
            {Repo: "frontend/web", Branch: "main", Path: "/tmp/refs/frontend__web"},
            {Repo: "backend/api", Path: "/tmp/refs/backend__api"}, // default branch
        },
    }
    out := BuildPrompt(ctx, nil, nil)
    if !strings.Contains(out, `<ref repo="frontend/web" branch="main" path="/tmp/refs/frontend__web"/>`) {
        t.Errorf("missing branch-attr ref render; got:\n%s", out)
    }
    if !strings.Contains(out, `<ref repo="backend/api" path="/tmp/refs/backend__api"/>`) {
        t.Errorf("missing default-branch ref render; got:\n%s", out)
    }
}

func TestBuildPrompt_UnavailableRefsRendered(t *testing.T) {
    ctx := queue.PromptContext{
        Goal: "g", Channel: "c", Reporter: "r",
        UnavailableRefs: []string{"broken/repo"},
    }
    out := BuildPrompt(ctx, nil, nil)
    if !strings.Contains(out, "<unavailable_refs>") || !strings.Contains(out, "<repo>broken/repo</repo>") {
        t.Errorf("missing unavailable_refs render; got:\n%s", out)
    }
}

func TestBuildPrompt_NoRefRepos_NoBlock(t *testing.T) {
    ctx := queue.PromptContext{Goal: "g", Channel: "c", Reporter: "r"}
    out := BuildPrompt(ctx, nil, nil)
    if strings.Contains(out, "<ref_repos>") || strings.Contains(out, "<unavailable_refs>") {
        t.Errorf("expected no ref blocks for empty fields; got:\n%s", out)
    }
}
```

**Verification:**
- [ ] `go test ./worker/prompt/...` passes.
- [ ] `TestBuildPrompt_*` existing tests still pass (regression).

**Dependencies:** Task 1 (uses new types).

**Estimated scope:** S (2 files, ~50 LOC + tests).

---

### Task 3: `RepoProvider.PrepareAt` interface + adapter

**Files:**
- Modify: `worker/pool/executor.go` (interface)
- Modify: `worker/pool/adapters.go` (RepoCacheAdapter impl)
- Test extension: `worker/pool/workdir_test.go::fakeRepoProvider`
- Test extension: `worker/pool/pool_test.go::mockRepo`
- Test extension: `worker/integration/queue_integration_test.go::fakeRepo`

**Background:**

The existing `RepoProvider.Prepare(cloneURL, branch, token) (string, error)` returns a worktree path under `RepoCache.WorktreeDir()`. For refs, the caller must specify the target path (under `<primary worktree>-refs/<owner>__<repo>/`). Adding a new method `PrepareAt(cloneURL, branch, token, targetPath string) error` keeps the existing signature untouched and avoids breaking the `Prepare` caller.

`RepoCacheAdapter.PrepareAt` re-uses `EnsureRepo` for the bare clone and `AddWorktree(barePath, branch, targetPath)` for the worktree creation — same pattern as `Prepare` but with caller-supplied target. The mkdir of the parent dir is the caller's responsibility (worker creates `<primary>-refs/` once before looping refs).

All three test fakes (`fakeRepoProvider` / `mockRepo` / `fakeRepo`) must implement `PrepareAt` to satisfy the interface, but their behavior is allowed to be a no-op + record-call for tests that don't exercise refs.

**Steps:**

- [ ] **3.1 Add `PrepareAt` to `RepoProvider` interface in `executor.go`.**

```go
type RepoProvider interface {
    Prepare(cloneURL, branch, token string) (string, error)
    PrepareAt(cloneURL, branch, token, targetPath string) error  // NEW
    RemoveWorktree(worktreePath string) error
    CleanAll() error
    PurgeStale() error
}
```

- [ ] **3.2 Implement on `RepoCacheAdapter` in `adapters.go`.**

```go
// PrepareAt clones into targetPath rather than the cache's default worktree dir.
// Used for ref repos so worker can co-locate them with the primary worktree.
func (a *RepoCacheAdapter) PrepareAt(cloneURL, branch, token, targetPath string) error {
    barePath, err := a.Cache.EnsureRepo(cloneURL, token)
    if err != nil {
        return err
    }
    // git worktree add requires the target dir not to exist.
    if err := os.RemoveAll(targetPath); err != nil {
        return fmt.Errorf("clear ref target %s: %w", targetPath, err)
    }
    return a.Cache.AddWorktree(barePath, branch, targetPath)
}
```

- [ ] **3.3 Extend test fakes.**

In `worker/pool/workdir_test.go::fakeRepoProvider`, add:

```go
func (f *fakeRepoProvider) PrepareAt(cloneURL, branch, token, targetPath string) error {
    f.prepareAtCalls = append(f.prepareAtCalls, fakePrepareAtCall{
        cloneURL: cloneURL, branch: branch, target: targetPath,
    })
    if f.prepareAtErr != nil {
        return f.prepareAtErr
    }
    // Create the target dir so caller's mkdir-then-prepare flow looks real.
    return os.MkdirAll(targetPath, 0755)
}
```

Same shape on `mockRepo` (in `pool_test.go`) and `fakeRepo` (in `queue_integration_test.go`); behavior should default to "succeed and create the dir" so existing tests pass without modification.

**Verification:**
- [ ] `go build ./worker/...` succeeds (interface implemented everywhere).
- [ ] `go test ./worker/pool/...` and `go test ./worker/integration/...` pass.

**Dependencies:** None (pure infrastructure).

**Estimated scope:** S (5 files, ~80 LOC).

---

### Task 4: `executeJob` ref orchestration + cleanup

**Files:**
- Modify: `worker/pool/executor.go`
- Modify: `worker/pool/workdir.go`
- Test: `worker/pool/workdir_test.go`
- Test: `worker/integration/queue_redis_integration_test.go`

**Background:**

After `provider.Prepare(job)` returns the primary worktree path, executeJob must:

1. If `job.RefRepos` is non-empty AND `job.CloneURL != ""` (refs without a primary make no sense; defensive skip), compute refs root = `<primary path> + "-refs"` and `MkdirAll` it.
2. Sequentially `provider.PrepareAt(ref.CloneURL, ref.Branch, token, target)` for each ref where `target = <refs root>/<owner>__<repo>` (slashes in `Repo` become `__`).
3. Successful refs append to `[]queue.RefRepoContext` (Repo, Branch, Path) and to `successfulRefPaths []string` (for guard + cleanup).
4. Failed refs append to `[]string` of `owner/name` form.
5. Mutate `job.PromptContext.RefRepos = successful` and `job.PromptContext.UnavailableRefs = failed` BEFORE the `BuildPrompt` call.
6. Cleanup ordering: ref worktrees (reversed) → refs root dir → primary (existing `provider.Cleanup`).

The cleanup extension must survive primary-prepare success + ref-prepare any-state, AND not break the cancellation path (which already routes through `classifyResult` + the existing cleanup deferred chain). Cleanest implementation: add a slice `[]string` of "extra paths to remove" on `executeJob`'s local scope, defer their cleanup right after `provider.Prepare` returns success.

**Steps:**

- [ ] **4.1 Add helper `prepareRefs` in `worker/pool/workdir.go`.**

```go
// prepareRefs clones each ref into <primaryPath>-refs/<owner>__<repo>/ in
// sequence. Returns successful contexts (with absolute paths), unavailable
// refs (owner/name form), the refs-root dir to clean up, and an error only
// if the refs-root mkdir fails (per-ref errors are collected as unavailable).
func prepareRefs(provider RepoProvider, primaryPath, token string, refs []queue.RefRepo, logger *slog.Logger) (
    successful []queue.RefRepoContext,
    successfulPaths []string,
    unavailable []string,
    refsRoot string,
    err error,
) {
    if len(refs) == 0 {
        return nil, nil, nil, "", nil
    }
    refsRoot = primaryPath + "-refs"
    if err := os.MkdirAll(refsRoot, 0755); err != nil {
        return nil, nil, nil, "", fmt.Errorf("mkdir refs root: %w", err)
    }
    for _, r := range refs {
        target := filepath.Join(refsRoot, refDirName(r.Repo))
        if perr := provider.PrepareAt(r.CloneURL, r.Branch, token, target); perr != nil {
            logger.Warn("ref clone failed; continuing with partial context",
                "phase", "處理中", "ref", r.Repo, "branch", r.Branch, "error", perr)
            unavailable = append(unavailable, r.Repo)
            continue
        }
        successful = append(successful, queue.RefRepoContext{
            Repo: r.Repo, Branch: r.Branch, Path: target,
        })
        successfulPaths = append(successfulPaths, target)
    }
    if len(refs) >= 5 {
        logger.Info("multi-ref ask with high count", "phase", "處理中", "count", len(refs))
    }
    return successful, successfulPaths, unavailable, refsRoot, nil
}

// refDirName flattens owner/name → owner__name. GitHub disallows __ in repo
// names, so collisions are impossible.
func refDirName(repo string) string {
    return strings.ReplaceAll(repo, "/", "__")
}
```

- [ ] **4.2 Wire into `executeJob` after `provider.Prepare`.**

```go
// existing:
provider := selectProvider(job, deps.repoCache, ghToken)
repoPath, err := provider.Prepare(job)
// ... existing prepareSeconds + isEmptyRepoReference handling ...

// NEW: ref handling — only if primary is real and refs are requested.
var refContexts []queue.RefRepoContext
var successfulRefPaths []string
var unavailableRefs []string
var refsRootDir string
if job.CloneURL != "" && len(job.RefRepos) > 0 {
    var refErr error
    refContexts, successfulRefPaths, unavailableRefs, refsRootDir, refErr = prepareRefs(
        provider, repoPath, ghToken, job.RefRepos, logger,
    )
    if refErr != nil {
        return classifyResult(job, startedAt, fmt.Errorf("refs prepare: %w", refErr), repoPath, ctx, deps.store)
    }
}
defer cleanupRefs(provider, successfulRefPaths, refsRootDir)

// existing PromptContext nil check stays here ...

// NEW: mutate PromptContext fields before BuildPrompt.
job.PromptContext.RefRepos = refContexts
job.PromptContext.UnavailableRefs = unavailableRefs
```

- [ ] **4.3 Add `cleanupRefs` helper in `workdir.go`.**

```go
// cleanupRefs removes ref worktrees (reverse order) then the refs-root dir.
// Best-effort: failures are logged at debug, not surfaced — primary cleanup
// happens via provider.Cleanup regardless.
func cleanupRefs(provider RepoProvider, paths []string, refsRoot string) {
    for i := len(paths) - 1; i >= 0; i-- {
        _ = provider.RemoveWorktree(paths[i])
    }
    if refsRoot != "" {
        _ = os.RemoveAll(refsRoot)
    }
}
```

- [ ] **4.4 Unit test for `prepareRefs` with mixed success/failure.**

`worker/pool/workdir_test.go`:

```go
func TestPrepareRefs_PartialSuccess(t *testing.T) {
    primary := t.TempDir() + "/triage-repo-abc"
    if err := os.MkdirAll(primary, 0755); err != nil { t.Fatal(err) }

    fake := &fakeRepoProvider{
        prepareAtBehavior: func(target string) error {
            if strings.Contains(target, "broken__repo") {
                return fmt.Errorf("simulated clone fail")
            }
            return os.MkdirAll(target, 0755)
        },
    }
    refs := []queue.RefRepo{
        {Repo: "frontend/web", CloneURL: "u1", Branch: "main"},
        {Repo: "broken/repo",  CloneURL: "u2"},
        {Repo: "backend/api",  CloneURL: "u3", Branch: "release"},
    }
    successful, successfulPaths, unavailable, refsRoot, err := prepareRefs(
        fake, primary, "token", refs, slog.Default(),
    )
    if err != nil { t.Fatal(err) }
    if len(successful) != 2 || len(unavailable) != 1 {
        t.Fatalf("partial-success counts wrong: ok=%d fail=%d", len(successful), len(unavailable))
    }
    if unavailable[0] != "broken/repo" {
        t.Errorf("unavailable mismatch: %v", unavailable)
    }
    if !strings.HasSuffix(refsRoot, "-refs") {
        t.Errorf("refs root naming wrong: %s", refsRoot)
    }
    if !strings.HasSuffix(successful[0].Path, "frontend__web") {
        t.Errorf("ref dir naming wrong: %s", successful[0].Path)
    }
    if len(successfulPaths) != 2 {
        t.Errorf("successfulPaths length mismatch: %d", len(successfulPaths))
    }
}

func TestPrepareRefs_EmptyRefs_NoOp(t *testing.T) {
    successful, _, unavailable, refsRoot, err := prepareRefs(
        &fakeRepoProvider{}, "/tmp/p", "t", nil, slog.Default(),
    )
    if err != nil || len(successful) != 0 || len(unavailable) != 0 || refsRoot != "" {
        t.Fatalf("nil refs should no-op: ok=%v fail=%v root=%q err=%v",
            successful, unavailable, refsRoot, err)
    }
}
```

- [ ] **4.5 Integration test for multi-repo Ask flow.**

In `worker/integration/queue_redis_integration_test.go`, add a test that submits an Ask job with 2 refs (one valid, one with bogus CloneURL), verifies:
- worktree directory structure (`<primary>` exists, `<primary>-refs/<repo1>__<repo2>` exists)
- prompt receives `<ref_repos>` for the successful ref
- prompt receives `<unavailable_refs>` for the bogus one
- after job completes, both directories are removed (cleanup invariant)

Reuse existing `fakeRepo` infrastructure; add a `prepareAtFailFor` map to the fake to selectively fail a clone URL.

**Verification:**
- [ ] `go test ./worker/pool/...` passes including new tests.
- [ ] `go test ./worker/integration/...` passes.
- [ ] `go test ./test/...` (import-direction test) still passes.
- [ ] No new top-level lint warnings.

**Dependencies:** Tasks 1 + 2 + 3.

**Estimated scope:** M (3-4 files, ~200 LOC + ~120 LOC tests).

---

### Task 5: Post-execute guard with `RefViolationCallback`

**Files:**
- Modify: `worker/pool/executor.go`
- Modify: `shared/metrics/metrics.go`
- Test: `worker/pool/executor_test.go`

**Background:**

After `runner.Run` returns, walk each successful ref worktree and run `git status --porcelain`. Non-empty output = agent wrote into the ref. The callback receives the violation; Ask installs a lenient callback that increments a metric and warn-logs. Issue (#217) will install a strict callback that fails the job — keep the abstraction clean enough that #217 only swaps the callback.

The metric is a counter labeled by the ref `repo` for ops visibility. No alert rule is added in this PR — that's an SRE follow-up after the metric has data.

**Steps:**

- [ ] **5.1 Define callback type + lenient impl in `executor.go`.**

```go
// RefViolationCallback is invoked when post-execute guard finds modifications
// in a ref worktree. Ask installs a lenient impl (warn + metric); Issue (#217)
// will install a strict impl that fails the job.
type RefViolationCallback func(refContext queue.RefRepoContext, diffPreview string, logger *slog.Logger)

// askLenientRefViolation is the Ask-workflow callback: warn-log + metric, no
// job-fail. Worktree cleanup happens regardless, so writes have no persistent
// effect — the guard is observability, not enforcement.
func askLenientRefViolation(ref queue.RefRepoContext, diff string, logger *slog.Logger) {
    logger.Warn("agent wrote into ref repo (lenient: not failing job)",
        "phase", "處理中",
        "ref", ref.Repo,
        "diff_preview", truncateDiff(diff),
    )
    metrics.AskRefWriteViolationsTotal.WithLabelValues(ref.Repo).Inc()
}

func truncateDiff(s string) string {
    s = strings.TrimSpace(s)
    if len(s) > 200 {
        return s[:200] + "…(truncated)"
    }
    return s
}
```

- [ ] **5.2 Add `runRefGuard` helper that runs `git status --porcelain` per ref.**

```go
// runRefGuard walks successful ref contexts, runs `git status --porcelain` in
// each, invokes onViolation when the output is non-empty.
func runRefGuard(refs []queue.RefRepoContext, onViolation RefViolationCallback, logger *slog.Logger) {
    if onViolation == nil {
        return
    }
    for _, ref := range refs {
        out, err := exec.Command("git", "-C", ref.Path, "status", "--porcelain").Output()
        if err != nil {
            // git missing or worktree corrupt is operationally noisy; skip rather
            // than escalate — guard is best-effort observability.
            logger.Debug("ref guard: git status failed; skipping",
                "phase", "處理中", "ref", ref.Repo, "error", err)
            continue
        }
        if diff := strings.TrimSpace(string(out)); diff != "" {
            onViolation(ref, diff, logger)
        }
    }
}
```

- [ ] **5.3 Wire `runRefGuard` into `executeJob` after `runner.Run`.**

```go
// existing:
output, err := deps.runner.Run(ctx, repoPath, promptXML, opts)
if err != nil { ... }

// NEW: only when refs were prepared, and only on the success path (failures
// route through classifyResult earlier; running guard after a failed agent
// run is noise).
runRefGuard(refContexts, askLenientRefViolation, logger)
```

(Note: when #217 lands, the callback selection will move into a per-task-type lookup; for now Ask is the only consumer so hardcoding is OK.)

- [ ] **5.4 Add metric registration in `shared/metrics/metrics.go`.**

```go
AskRefWriteViolationsTotal = prometheus.NewCounterVec(
    prometheus.CounterOpts{
        Name: "ask_ref_write_violations_total",
        Help: "Number of times an Ask-workflow agent wrote into a ref repo (lenient: violation does not fail the job).",
    },
    []string{"repo"},
)
```

Register in the `init()` block alongside the other counters.

- [ ] **5.5 Unit test guard with stubbed worktree state.**

`worker/pool/executor_test.go`:

```go
func TestRunRefGuard_DetectsViolation(t *testing.T) {
    // Create a real git worktree, modify a tracked file, expect callback fired.
    dir := t.TempDir()
    run(t, dir, "git", "init")
    run(t, dir, "git", "-c", "user.email=t@t", "-c", "user.name=t", "commit",
        "--allow-empty", "-m", "init")
    if err := os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0644); err != nil {
        t.Fatal(err)
    }

    var fired []string
    cb := func(ref queue.RefRepoContext, diff string, logger *slog.Logger) {
        fired = append(fired, ref.Repo)
    }
    runRefGuard([]queue.RefRepoContext{{Repo: "foo/bar", Path: dir}}, cb, slog.Default())
    if len(fired) != 1 || fired[0] != "foo/bar" {
        t.Fatalf("expected violation fired for foo/bar; got %v", fired)
    }
}

func TestRunRefGuard_CleanWorktree_NoCallback(t *testing.T) {
    dir := t.TempDir()
    run(t, dir, "git", "init")
    run(t, dir, "git", "-c", "user.email=t@t", "-c", "user.name=t", "commit",
        "--allow-empty", "-m", "init")

    var fired int
    cb := func(_ queue.RefRepoContext, _ string, _ *slog.Logger) { fired++ }
    runRefGuard([]queue.RefRepoContext{{Repo: "foo/bar", Path: dir}}, cb, slog.Default())
    if fired != 0 {
        t.Fatalf("clean worktree should not fire callback, fired=%d", fired)
    }
}
```

(`run` helper exists in `shared/github/repo_test.go`; if not importable here, copy the 6-line helper into `executor_test.go`.)

**Verification:**
- [ ] `go test ./worker/pool/...` passes.
- [ ] `go test ./shared/metrics/...` passes (metric registration smoke).
- [ ] Counter visible at `:metrics-port/metrics` after a manual ask with refs.

**Dependencies:** Task 4 (refs already prepared by the time guard runs).

**Estimated scope:** S (3 files, ~80 LOC + ~40 LOC tests).

---

### Checkpoint: PR 1 ready

- [ ] All `shared/queue/`, `worker/prompt/`, `worker/pool/`, `worker/integration/`, `shared/metrics/` tests pass.
- [ ] `go build ./...` clean.
- [ ] `go test ./test/...` (import-direction) still passes.
- [ ] Old jobs (no `RefRepos` field) parse and execute byte-for-byte unchanged — verified by Task 1.3 backwards-compat test + an integration run with a no-ref Ask.
- [ ] PR description includes a manual log capture from a single-ref + multi-ref worker run showing the new prepare/guard flow logs.
- [ ] Open PR titled `feat(worker): Ask multi-repo backend (refs schema, workdir, prompt, guard)` referencing `#216`.
- [ ] **Wait for CI green + user review before merging** (per memory: PR merge authorization).

---

## PR 2 — Frontend + e2e

### Task 6: `askState` extensions + `BranchTargetRepo` plumbing

**Files:**
- Modify: `app/workflow/ask.go`
- Test: `app/workflow/ask_test.go`

**Background:**

`askState` gains four fields per spec §4.4. `BranchSelectedRepo()` switches from reading `SelectedRepo` to reading `BranchTargetRepo`; the workflow is responsible for setting `BranchTargetRepo` before each branch-select phase (primary OR ref). This keeps `BranchStateReader` interface untouched and `HandleBranchSuggestion` (in app/bot) ignorant of refs.

**Steps:**

- [ ] **6.1 Extend `askState` struct.**

```go
type askState struct {
    // ...existing fields unchanged...

    AddRefs          bool
    RefRepos         []queue.RefRepo // accumulated as user picks; Branch filled later
    RefBranchIdx     int             // current ref index during ask_ref_branch loop
    BranchTargetRepo string          // set before each branch select phase
}
```

- [ ] **6.2 Update `BranchSelectedRepo` to read `BranchTargetRepo`.**

```go
func (s *askState) BranchSelectedRepo() string {
    if s == nil {
        return ""
    }
    return s.BranchTargetRepo
}
```

- [ ] **6.3 Set `BranchTargetRepo = SelectedRepo` when entering primary branch select.**

In `afterRepoSelectedStep` (line ~287), at the point before returning the `ask_branch_select` NextStep:

```go
st.BranchTargetRepo = st.SelectedRepo
p.Phase = "ask_branch_select"
```

This preserves existing behavior: primary branch selection still works exactly as today.

- [ ] **6.4 Test: existing `BranchSelectedRepo` callers see the expected value.**

In `app/workflow/ask_test.go`, add:

```go
func TestAskState_BranchSelectedRepo_PrimaryPhase(t *testing.T) {
    st := &askState{SelectedRepo: "foo/bar", BranchTargetRepo: "foo/bar"}
    if got := st.BranchSelectedRepo(); got != "foo/bar" {
        t.Errorf("primary phase: expected foo/bar, got %s", got)
    }
}

func TestAskState_BranchSelectedRepo_RefPhase(t *testing.T) {
    st := &askState{
        SelectedRepo:     "foo/bar",
        BranchTargetRepo: "frontend/web",
        RefRepos:         []queue.RefRepo{{Repo: "frontend/web"}},
    }
    if got := st.BranchSelectedRepo(); got != "frontend/web" {
        t.Errorf("ref phase: expected frontend/web, got %s", got)
    }
}
```

**Verification:**
- [ ] `go test ./app/workflow/...` passes.
- [ ] No existing test fails (regression).

**Dependencies:** Task 1 (uses `queue.RefRepo`).

**Estimated scope:** S (2 files, ~30 LOC + tests).

---

### Task 7: New phases — `ask_ref_decide` / `ask_ref_pick` / `ask_ref_continue` / `ask_ref_branch`

**Files:**
- Modify: `app/workflow/ask.go`
- Test: `app/workflow/ask_test.go`

**Background:**

Four new phases form the ref loop. All of them re-use the existing selector-message ts via `chat.update` so the thread doesn't accumulate rows. `ask_ref_continue` is the loop pivot — user picks "再加一個" → back to `ask_ref_pick`; "開始問問題" → into `ask_ref_branch` (which itself loops over `RefRepos` until idx exhausts, then drops into the existing prior-answer/description flow).

**Pre-check: 0-candidate skip.** Before posting `ask_ref_decide`, evaluate the candidate pool (channel repos minus primary minus already-picked refs). If empty, skip the entire ref flow and route directly to `priorAnswerOrDescriptionStep`.

**Candidate filtering:** for each `ask_ref_pick` invocation, the option list is `(channelRepos OR external_select) − {primary} − {already-picked refs}`. dedup is by `Repo` field (one repo, one ref — spec §4.7).

**Per-ref branch:** mirrors primary's `afterRepoSelectedStep` rules: `branch_select` off → skip; ≤ 1 branch → use that one; else → external_select with type-ahead. Each ref makes the decision independently.

**Steps:**

- [ ] **7.1 Insert ref decision step after primary branch select / skip.**

`Selection` cases for `ask_repo_select` (skip-attach branch) and `ask_branch_select` currently hand off to `priorAnswerOrDescriptionStep`. Inject a new wrapper `maybeAskRefStep(p)` that:

1. If `len(refCandidates(p)) == 0` → call `priorAnswerOrDescriptionStep(p)` (existing path).
2. Else → set `p.Phase = "ask_ref_decide"`, return the decide selector.

Replace `return w.priorAnswerOrDescriptionStep(p), nil` in those two cases with `return w.maybeAskRefStep(p), nil`.

- [ ] **7.2 Implement `refCandidates` helper.**

```go
// refCandidates returns the channel-allowed repo list minus primary and
// already-picked refs. Returns nil when the channel uses external search
// (caller must dispatch to type-ahead pickup instead of static list).
func (w *AskWorkflow) refCandidates(p *Pending) (list []string, useExternalSearch bool) {
    st, _ := p.State.(*askState)
    cc := w.cfg.ChannelDefaults
    if c, ok := w.cfg.Channels[p.ChannelID]; ok {
        cc = c
    }
    repos := cc.GetRepos()
    if len(repos) == 0 {
        return nil, true // external search will run; primary still excluded by suggestion handler
    }
    picked := make(map[string]bool, len(st.RefRepos))
    for _, r := range st.RefRepos {
        picked[r.Repo] = true
    }
    for _, r := range repos {
        if r == st.SelectedRepo || picked[r] {
            continue
        }
        list = append(list, r)
    }
    return list, false
}
```

- [ ] **7.3 `ask_ref_decide` selector.**

```go
func (w *AskWorkflow) refDecideStep(p *Pending) NextStep {
    return NextStep{
        Kind: NextStepSelector,
        Selector: &SelectorSpec{
            Prompt:   ":books: 加入參考 repo 嗎？(唯讀脈絡)",
            ActionID: "ask_ref_decide",
            Options: []SelectorOption{
                {Label: "加入", Value: "add"},
                {Label: "不用", Value: "skip"},
            },
        },
        Pending: p,
    }
}
```

In `Selection`, add `case "ask_ref_decide":`:
- `value == "skip"` → `priorAnswerOrDescriptionStep`
- `value == "add"` → `st.AddRefs = true`; transition to `ask_ref_pick`.

- [ ] **7.4 `ask_ref_pick` selector (single-select with current candidates, OR external search).**

```go
func (w *AskWorkflow) refPickStep(p *Pending) NextStep {
    list, externalSearch := w.refCandidates(p)
    p.Phase = "ask_ref_pick"
    if externalSearch {
        return NextStep{
            Kind: NextStepSelector,
            Selector: &SelectorSpec{
                Prompt:         ":point_right: 選參考 repo:",
                ActionID:       "ask_ref",
                Searchable:     true,
                Placeholder:    "Type to search repos...",
                CancelActionID: "ask_ref_cancel",
                CancelLabel:    "← 返回 (不加 ref)",
            },
            Pending: p,
        }
    }
    options := make([]SelectorOption, 0, len(list)+1)
    for _, r := range list {
        options = append(options, SelectorOption{Label: r, Value: r})
    }
    options = append(options, SelectorOption{Label: "← 返回 (不加 ref)", Value: "back_to_decide"})
    return NextStep{
        Kind: NextStepSelector,
        Selector: &SelectorSpec{
            Prompt:   ":point_right: 選參考 repo:",
            ActionID: "ask_ref",
            Options:  options,
        },
        Pending: p,
    }
}
```

`Selection` case `ask_ref_pick`:
- `value == "back_to_decide"` or `value == "ask_ref_cancel"` → reset `st.AddRefs = false`, route to `priorAnswerOrDescriptionStep`.
- otherwise → append `queue.RefRepo{Repo: value, CloneURL: cleanCloneURL(value)}` to `st.RefRepos`; transition to `ask_ref_continue`.

- [ ] **7.5 `ask_ref_continue` selector.**

```go
func (w *AskWorkflow) refContinueStep(p *Pending) NextStep {
    p.Phase = "ask_ref_continue"
    list, externalSearch := w.refCandidates(p)
    options := []SelectorOption{
        {Label: "開始問問題", Value: "done"},
    }
    if len(list) > 0 || externalSearch {
        options = append([]SelectorOption{{Label: "再加一個 ref", Value: "more"}}, options...)
    }
    return NextStep{
        Kind: NextStepSelector,
        Selector: &SelectorSpec{
            Prompt:   fmt.Sprintf(":heavy_plus_sign: 已加 %d 個 ref。", len(p.State.(*askState).RefRepos)),
            ActionID: "ask_ref_continue",
            Options:  options,
        },
        Pending: p,
    }
}
```

`Selection` case `ask_ref_continue`:
- `value == "more"` → `refPickStep`.
- `value == "done"` → start ref-branch loop: `st.RefBranchIdx = 0`, call `nextRefBranchStep(p)`.

- [ ] **7.6 `ask_ref_branch` per-ref loop.**

```go
func (w *AskWorkflow) nextRefBranchStep(p *Pending) NextStep {
    st := p.State.(*askState)
    if st.RefBranchIdx >= len(st.RefRepos) {
        return w.priorAnswerOrDescriptionStep(p)
    }
    target := st.RefRepos[st.RefBranchIdx].Repo

    // Mirror primary branch-select skip rules.
    cc := w.cfg.ChannelDefaults
    if c, ok := w.cfg.Channels[p.ChannelID]; ok {
        cc = c
    }
    if !cc.IsBranchSelectEnabled() {
        // No branch select for any ref — leave Branch empty, advance.
        st.RefBranchIdx++
        return w.nextRefBranchStep(p)
    }

    var branches []string
    if len(cc.Branches) > 0 {
        branches = cc.Branches
    } else if w.repoCache != nil {
        ghToken := ""
        if w.cfg.Secrets != nil {
            ghToken = w.cfg.Secrets["GH_TOKEN"]
        }
        if rp, err := w.repoCache.EnsureRepo(target, ghToken); err == nil {
            if lb, lerr := w.repoCache.ListBranches(rp); lerr == nil {
                branches = lb
            }
        }
    }
    if len(branches) <= 1 {
        if len(branches) == 1 {
            st.RefRepos[st.RefBranchIdx].Branch = branches[0]
        }
        st.RefBranchIdx++
        return w.nextRefBranchStep(p)
    }

    st.BranchTargetRepo = target
    p.Phase = "ask_ref_branch"
    options := make([]SelectorOption, 0, len(branches)+1)
    for _, b := range branches {
        options = append(options, SelectorOption{Label: b, Value: b})
    }
    options = append(options, SelectorOption{Label: "取消", Value: "取消"})
    return NextStep{
        Kind: NextStepSelector,
        Selector: &SelectorSpec{
            Prompt:   fmt.Sprintf(":point_right: 選 `%s` 的 branch?", target),
            ActionID: "ask_ref_branch",
            Options:  options,
        },
        Pending: p,
    }
}
```

`Selection` case `ask_ref_branch`:
- `value == "取消"` → `NextStepCancel`.
- otherwise → `st.RefRepos[st.RefBranchIdx].Branch = value`; `st.RefBranchIdx++`; recurse `nextRefBranchStep(p)`.

- [ ] **7.7 Tests for new phases.**

`app/workflow/ask_test.go`:

```go
func TestAsk_RefDecide_AddPath(t *testing.T) { ... select "add" → next phase = ask_ref_pick }
func TestAsk_RefDecide_SkipPath(t *testing.T) { ... select "skip" → no RefRepos in BuildJob output }
func TestAsk_RefPick_DedupPrimary(t *testing.T) { ... primary "foo/bar" excluded from candidate list }
func TestAsk_RefPick_DedupAlreadyPicked(t *testing.T) { ... can't pick same ref twice }
func TestAsk_RefContinue_NoMoreCandidates(t *testing.T) { ... only "開始問問題" appears }
func TestAsk_RefBranch_SkipsWhenBranchSelectDisabled(t *testing.T)
func TestAsk_RefBranch_PerRefIndependent(t *testing.T) { ... ref1 has 2 branches → picker; ref2 has 1 → skip }
func TestAsk_RefDecide_ZeroCandidates_PhaseSkipped(t *testing.T) { ... channel has only 1 repo == primary → maybeAskRefStep routes straight to description prompt }
```

(Each test sets up an askWorkflow with a fixture config + invokes Selection with the expected sequence; assert phase progression and final state.)

**Verification:**
- [ ] `go test ./app/workflow/...` passes including new tests.
- [ ] `go test ./...` (full suite) clean.

**Dependencies:** Task 6.

**Estimated scope:** L (1 file + 1 test file, ~250 LOC + ~250 LOC tests). **Justification for L (not split further):** the four new phases form one indivisible state machine — splitting them across tasks creates dead-code intermediate states.

---

### Task 8: `BuildJob` ref + `output_rules` injection

**Files:**
- Modify: `app/workflow/ask.go`
- Test: `app/workflow/ask_test.go`

**Background:**

`BuildJob` populates `Job.RefRepos` from `st.RefRepos` and conditionally appends two output rules to `PromptContext.OutputRules` when refs are non-empty. The two rules correspond to spec §4.6 Layer 2: read-only enforcement + critical-fail-fast directive.

The CloneURL on each ref is set during `ask_ref_pick` via `cleanCloneURL`; here it just flows through.

**Steps:**

- [ ] **8.1 Populate `Job.RefRepos` in `BuildJob`.**

After the existing `cloneURL` derivation for primary:

```go
// Pass through ref repos as captured during the ref loop.
job.RefRepos = st.RefRepos // nil-safe: zero-len slice marshals as empty/omitted
```

- [ ] **8.2 Conditionally inject `output_rules`.**

```go
outputRules := w.cfg.Workflows.Ask.Prompt.OutputRules
if len(st.RefRepos) > 0 {
    outputRules = append(outputRules,
        "不可寫入、修改、刪除 <ref_repos> 列出之任何 path 之下的檔案；refs 為唯讀脈絡。",
        "若 <unavailable_refs> 含關鍵 ref，必須在答案開頭聲明「無法取得 X repo 脈絡，無法回答」並停手；不要 best-effort 拼湊。",
    )
}
// then wire outputRules into PromptContext below.
```

- [ ] **8.3 Tests.**

```go
func TestBuildJob_NoRefs_NoOutputRulesInjection(t *testing.T) {
    // primary only — output_rules length == config-defined length
}
func TestBuildJob_WithRefs_TwoRulesAppended(t *testing.T) {
    // RefRepos non-empty → output_rules len == config-defined + 2
    // verify exact wording on the two appended rules
}
func TestBuildJob_RefRepos_Pasthrough(t *testing.T) {
    // Job.RefRepos === st.RefRepos
}
```

**Verification:**
- [ ] `go test ./app/workflow/...` passes.

**Dependencies:** Tasks 6 + 7.

**Estimated scope:** S (2 files, ~25 LOC + ~50 LOC tests).

---

### Task 9: SKILL.md update

**Files:**
- Modify: `app/agents/skills/ask-assistant/SKILL.md`

**Background:**

Add a "Reference repos" subsection to §5 (Action boundaries). Includes the critical-fail-fast directive that mirrors the second injected output_rule.

**Steps:**

- [ ] **9.1 Insert subsection in §5.**

After the existing "**Read-only on the repo**" / "**No ticketing, no reviewing**" / etc. groupings, before "These rules apply even when..." trailer, add:

```markdown
**Reference repos (absolute paths in `<ref_repos>`)**

If the prompt has a `<ref_repos>` block, those directories are mounted at the
absolute paths listed for read-only context. Rules:

- You CAN: grep, read, follow imports, run `git log --oneline` in them.
- You CANNOT: write, commit, edit, mv, rm any file under those paths.
- Refs are physically OUTSIDE your cwd; use the absolute paths exactly as
  listed in `<ref_repos>`. Don't try to compute relative paths from cwd.

If `<unavailable_refs>` lists a repo:

- If the unavailable ref is **critical** to answering this question
  (i.e., the question fundamentally requires that repo's code), you must
  state plainly at the answer's opening: "無法取得 X repo 脈絡，這題我答不了 — 請確認 PAT 對該 repo 有讀權後重 trigger"
  and stop. Do not best-effort patchwork an answer from the available refs.
- If the unavailable ref is non-critical, mention it in the answer
  ("以下回答僅基於 Y 與 Z，X repo 無法取得") and continue.
```

**Verification:**
- [ ] Markdown renders correctly (manual check).
- [ ] No skill-loader test breakage.

**Dependencies:** None (pure docs change, can land independently — but bundles with PR 2 for atomicity).

**Estimated scope:** XS (1 file, ~30 lines).

---

### Task 10: e2e Slack walk-through + AC verification

**Files:**
- Update PR 2 description with screenshots / log captures
- Possibly minor test additions if e2e surfaces gaps

**Background:**

Manual verification against AC-1 to AC-14. Particularly important is AC-5 (chat.update message-row count), which can only be confirmed by visual inspection of a real Slack thread.

**Steps:**

- [ ] **10.1 Configure a test channel with 3 repos.**

Pick three small repos the test PAT has access to. Update channel config locally.

- [ ] **10.2 Run three flows: 0-ref, 1-ref, 3-ref.**

For each:
- Trigger with `@bot ask`.
- Walk through phases.
- Capture a screenshot when the ask completes.
- Count permanent message rows (not counting attachments / replies).

- [ ] **10.3 Force ref clone failure.**

Add a ref with a bogus clone URL (e.g., a private repo the PAT can't see). Verify:
- Job completes.
- Answer mentions "無法取得 X repo 脈絡".
- Worker log shows ref-failure warn.

- [ ] **10.4 Force write violation.**

Use a primary + 1 ref. In the ask, explicitly instruct the agent: "Write `test` to `<ref absolute path>/foo.txt`". Verify:
- Worker log shows `agent wrote into ref repo (lenient...)`.
- `ask_ref_write_violations_total{repo="..."} > 0`.
- User still receives an answer.

- [ ] **10.5 0-candidate skip.**

Configure a channel with only 1 repo. Run `@bot ask` and pick that repo as primary. Verify the thread does NOT contain the "加入參考 repo？" message.

- [ ] **10.6 Document AC results in PR description.**

Map screenshot / log evidence to each AC. AC-1 through AC-14.

**Verification:**
- [ ] All 14 ACs evidenced in PR description.
- [ ] No regression in single-repo Ask (compare against pre-PR baseline screenshot).

**Dependencies:** Tasks 6 + 7 + 8 + 9 (the full feature must be deployed to test channel).

**Estimated scope:** M (no code change ideally; ~1-2 hours of manual e2e + writing).

---

### Checkpoint: PR 2 ready

- [ ] All ACs (AC-1 to AC-14) evidenced in PR description.
- [ ] `go test ./...` clean.
- [ ] Manual e2e showing N=3 ref scenario produces ≤ 2 new permanent rows in Slack thread.
- [ ] SKILL.md change visible to agents (verified by inspecting prompt logs from a test run).
- [ ] PR description titled `feat(workflow/ask): multi-repo support (frontend + e2e)` referencing `#216`.
- [ ] **Wait for CI green + user review before merging.**

---

## Risks and Mitigations

| Risk | Impact | Mitigation |
|---|---|---|
| `multi_static_select` reconsideration after seeing real UX (could push back to spec) | Med | Spec §1 explicitly closed this; if e2e screenshots reveal sequential UX is too clicky, redirect rather than retro-fitting. Keep the door open: §10 leaves room for v2 modal. |
| Sequential ref clone hits `prepare_timeout` at N ≥ 5 in production | Med | Spec §8 assumption; metric `worker_prepare_seconds` watched. If it bites, refactor `RepoCache.EnsureRepo` to per-repo lock — separate scope, not blocking this PR. |
| Agent CLIs interpret absolute paths inconsistently across `claude` / `opencode` / `codex` / `gemini` | Low | All four use absolute path natively in their `Read`/`read_file`/equivalent tools. Manual e2e covers `claude` (default agent); other CLIs verified later via the same prompt schema (no per-agent code path). |
| `RemoveAll` on refs root races a still-running git process inside a ref worktree | Low | Worker only calls cleanup after `runner.Run` returns; agents don't fork persistent git processes. If observed, add a 100ms grace before `RemoveAll`. |

## Open Questions

None — all resolved during spec grilling (Q1-Q14). Re-open only on production data.
