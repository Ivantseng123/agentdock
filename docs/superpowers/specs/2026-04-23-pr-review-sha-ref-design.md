# PR Review uses head SHA + worker fetch-retries unknown refs + worker logs git stderr

**Date:** 2026-04-23
**Status:** Draft

## Problem

A PR Review trigger died with `:x: 分析失敗: workdir prepare failed` shortly after the underlying PR was merged. Three distinct issues converged:

1. **Stale ref name.** `app/workflow/pr_review.go:260` puts `pr.Head.Ref` (the head **branch name**, e.g. `feat/prompt-response-schema`) into `Job.Branch`. Worker passes that string straight to `git worktree add --detach <path> <ref>` (`shared/github/repo.go:204`). When the PR is merged, GitHub deletes the head branch; the next worker `git fetch --all --prune` removes the matching remote ref locally; `git worktree add` then fails with `invalid reference`.

   The PR API response already returns an immutable `head.sha` (`shared/github/pr_types.go:19`) which is unaffected by branch deletion.

2. **Unreachable SHA.** Even if `Job.Branch` carried the head SHA, a `git worktree add --detach <SHA>` would still fail in the merged-and-deleted case: `git fetch --all --prune` only fetches refs reachable from `refs/heads/*` and `refs/tags/*`, not `refs/pull/*`. After branch deletion, the head commit is reachable on GitHub only via `refs/pull/<N>/head`. The local bare cache never receives the object.

   Verified: `git clone --bare https://github.com/Ivantseng123/agentdock.git && git -C agentdock.git cat-file -t 456f43543082cc162bb742f4797443c33fd5e8dc` → `could not get object info`. But `git fetch origin 456f43543082cc162bb742f4797443c33fd5e8dc` succeeds — GitHub enables `uploadpack.allowReachableSHA1InWant`, so a direct fetch-by-SHA pulls the object even when no ref points to it locally.

3. **Silent worker.** When `provider.Prepare` fails, `worker/pool/executor.go:97` wraps the error and returns it to the app, but never writes the underlying `git` stderr (returned by `RepoCache.AddWorktree` as `git worktree add: <err>\n<stderr>`) to the worker log. The worker pod's stdout shows only `工作完成 ... status=failed` — operators have no breadcrumb.

## Goals

1. **PR Review jobs reference the head commit by SHA**, not by branch name, so a merged-and-deleted head branch does not break worktree preparation.
2. **`RepoCache.AddWorktree` recovers from unknown-ref failures** by fetching the ref directly from `origin` and retrying. Applies to all workflows; covers both deleted branches and unreachable SHAs.
3. **Worker logs the wrapped Prepare error at `error` level**, including git stderr, so operators can diagnose without re-running.

## Non-Goals

- Changing how Issue / Ask workflows resolve refs at submit time. They take user-supplied branch names; if a user picks a deleted branch the failure is on them. Goal 2 makes the worker resilient at execution time, but the workflow itself does not pre-resolve to SHA.
- Changing `EnsureRepo`'s default fetch refspec to include `refs/pull/*/head`. Goal 2's on-demand fetch covers the failure case without polluting every clone with full PR history.
- Touching the cancelled-watchdog "取消狀態逾時，補發 cancelled result" loop seen in the same incident. Tracked separately.
- Changing the user-facing failure message (`:x: 分析失敗: ...`). The wrapped string still surfaces as-is when fetch retry also fails; the worker log gains a parallel breadcrumb. Friendlier text mapping was considered and deferred — see "Future".
- Adding a SHA field to `PromptContext`. The agent's cwd is already `git checkout`-ed at the head commit; `git rev-parse HEAD` covers any traceability need without expanding the cross-workflow `PromptContext` schema.

## Design

### 1. PR Review carries head SHA in `Job.Branch`

**`app/workflow/pr_review.go`** — extend `prReviewState`:

```go
type prReviewState struct {
    URL      string
    Owner    string
    Repo     string
    Number   int
    HeadRepo string
    HeadRef  string
    HeadSHA  string // NEW — pr.Head.SHA, immutable across branch deletion
    BaseRef  string
}
```

`validateAndBuild` (around line 155) populates the new field:

```go
state := &prReviewState{
    URL:      urlStr,
    Owner:    parts.Owner,
    Repo:     parts.Repo,
    Number:   parts.Number,
    HeadRepo: pr.Head.Repo.FullName,
    HeadRef:  pr.Head.Ref,
    HeadSHA:  pr.Head.SHA, // NEW
    BaseRef:  pr.Base.Ref,
}
```

`BuildJob` (around line 260) sends the SHA to git, but keeps `HeadRef` in the prompt context (humans read prompts; SHAs are noise):

```go
job := &queue.Job{
    // ...
    Repo:     st.HeadRepo,
    Branch:   st.HeadSHA, // CHANGED — was st.HeadRef; SHA survives branch deletion
    CloneURL: cloneURL,
    // ...
    PromptContext: &queue.PromptContext{
        Branch: st.HeadRef, // unchanged — agent-facing display value
        // ...
    },
}
```

Rationale for the asymmetry: `Job.Branch` is the **git ref** consumed by `git worktree add` in `shared/github/repo.go:204`. `PromptContext.Branch` is **prose** rendered into the agent's prompt by `worker/prompt`. The git layer needs identifier stability; the prompt layer needs human readability. Splitting these two concerns into one field at the source workflow means no churn in `shared/queue` or `worker/`.

### 2. `RepoCache.AddWorktree` retries with on-demand fetch

**`shared/github/repo.go`** — extend `AddWorktree` (around line 196) with a fallback that catches the unknown-ref case:

```go
func (rc *RepoCache) AddWorktree(barePath, branch, worktreePath string) error {
    pruneCmd := exec.Command("git", "-C", barePath, "worktree", "prune")
    _, _ = pruneCmd.CombinedOutput() // best-effort

    ref := "HEAD"
    if branch != "" {
        ref = branch
    }
    addErr := tryAddWorktree(barePath, worktreePath, ref)
    if addErr == nil {
        return nil
    }
    // First add failed — likely an unknown ref. Fetch directly from origin
    // (works for both deleted branches and unreachable SHAs because
    // GitHub enables allowReachableSHA1InWant), then retry once.
    fetchCmd := exec.Command("git", "-C", barePath, "fetch", "origin", ref)
    fetchOut, fetchErr := fetchCmd.CombinedOutput()
    if fetchErr != nil {
        // tryAddWorktree's returned err already inlines its stderr, so addErr
        // alone carries the first-attempt git output; no need to capture it
        // separately.
        return fmt.Errorf("git worktree add: %w; git fetch origin %s also failed: %v\n%s",
            addErr, ref, fetchErr, fetchOut)
    }
    if retryErr := tryAddWorktree(barePath, worktreePath, ref); retryErr != nil {
        return fmt.Errorf("git worktree add (after fetch retry): %w", retryErr)
    }
    return nil
}

// tryAddWorktree runs `git worktree add --detach` once and returns the wrapped
// error including stderr when it fails, or nil on success.
func tryAddWorktree(barePath, worktreePath, ref string) error {
    cmd := exec.Command("git", "-C", barePath, "worktree", "add", "--detach", worktreePath, ref)
    if out, err := cmd.CombinedOutput(); err != nil {
        return fmt.Errorf("git worktree add: %w\n%s", err, out)
    }
    return nil
}
```

Notes:
- The fetch uses `origin` directly. `EnsureRepo` already configures `origin` with the appropriate token via `resolveURLWithToken` (`shared/github/repo.go:45-64`), so the AddWorktree signature does not need a token argument.
- Fallback fires on **any** first-attempt failure, not just `invalid reference`. Cheaper than parsing stderr; the only realistic alternative failure (worktree path collision) gets retried, fails again identically, and produces the same error a non-retry path would have produced.
- Only one retry. Two-phase fetch+add covers GitHub's behaviour; further retry adds latency without recovery surface.

### 3. Worker logs prepare-failure detail

**`worker/pool/executor.go`** — extend the existing branch at line 95-98:

```go
provider := selectProvider(job, deps.repoCache, ghToken)
repoPath, err := provider.Prepare(job)
if err != nil {
    logger.Error("Repo 準備失敗", "phase", "失敗", "branch", job.Branch, "error", err.Error()) // NEW
    return classifyResult(job, startedAt, fmt.Errorf("workdir prepare failed: %w", err), "", ctx, deps.store)
}
```

`err.Error()` already contains the git stderr because `RepoCache.AddWorktree` returns `fmt.Errorf("...%w\n%s", err, out)` (Design §2 above), and after Goal 2 also includes the fetch-retry stderr if that path was taken. No new error-wrapping needed.

The log line:
- Uses `phase=失敗` per `shared/logging/GUIDE.md` taxonomy
- Mirrors the structured `branch` field from the existing prepare-start Info log at `executor.go:85`, so operators can grep `branch=<X>` to find all failures involving a given ref
- Is the only `Error`-level log on this code path; current code only emits `Info` then silently returns the error to the app

## Testing

### Unit — `app/workflow/pr_review_test.go`

Extend the existing `TestPRReviewWorkflow_*` table:

- `TestBuildJob_UsesHeadSHAAsJobBranch` — given a `prReviewState` with `HeadRef: "feat/x"`, `HeadSHA: "abc123..."`, assert `job.Branch == "abc123..."` and `job.PromptContext.Branch == "feat/x"`.
- `TestValidateAndBuild_PopulatesHeadSHA` — given a fake GitHub client returning `pr.Head.SHA = "deadbeef"`, drive `validateAndBuild` and assert the `Pending`'s `*prReviewState` has `HeadSHA == "deadbeef"`.

### Unit — `shared/github/repo_test.go`

Add to the existing `TestRepoCache_AddWorktree_*` family (which already uses real git binary + `file://` remotes):

- `TestAddWorktree_RetriesAfterFetchOnUnknownRef` — set up a `file://` remote with `uploadpack.allowAnySHA1InWant=true`, clone bare, capture a feature commit's SHA, then `git update-ref -d refs/remotes/origin/feature` in the bare to simulate a deleted branch whose object would not be locally reachable. Call `AddWorktree(bare, <SHA>, wt)` and assert: returns `nil`, worktree exists at HEAD = `<SHA>`.
- `TestAddWorktree_PropagatesErrorWhenFetchAlsoFails` — call `AddWorktree(bare, "deadbeef0000000000000000000000000000beef", wt)` against a remote that does not have that SHA. Assert: returns non-nil error whose `.Error()` contains both `git worktree add` stderr and `git fetch origin` stderr.

### Out of Scope

- Worker-side test for `executor.go` log emission. Capturing `slog` output is achievable but the line under test is one literal `logger.Error` call with a fixed message — coverage cost > value.
- End-to-end test that re-creates a merged-and-deleted PR scenario in CI. The unit tests above cover the data path (workflow → SHA in `Job.Branch`) and the recovery path (`AddWorktree` fetch retry) independently.
- Manual verification step: in a dev environment, submit any job referencing a non-existent branch. Expect (a) worker pod log shows `ERROR ... Repo 準備失敗 ... branch=<X> ... error="git worktree add: ... git fetch origin <X>: ..."` and (b) Slack still shows `:x: 分析失敗: workdir prepare failed: ...`. Use as smoke test, not gating CI.

## Migration / Rollout

- Single deploy. No config change, no schema change, no Redis-format change.
- `Job.Branch` field type is unchanged (`string`); a SHA is still a valid git ref, so any code reading `job.Branch` (logging, metrics, HTTP `/jobs` endpoint) keeps working. Logged values become opaque hashes for PR Review jobs only — Issue / Ask continue to log human-readable branch names.
- In-flight queued PR Review jobs (already-serialised with `Branch: <branch-name>`) continue to use the branch-name path post-deploy. They will succeed if the branch is still alive at execution time (typical), and even if the branch is deleted Goal 2's fetch retry will recover them — assuming the SHA is still on the GitHub server, which is true for any merged PR. Acceptable given queue depth at deploy time is typically zero (project still pre-launch as of 2026-04-19 memory).

## Future

If the user-facing failure message becomes a friction point — e.g. `:x: 分析失敗: workdir prepare failed: git worktree add: invalid reference: ...` confusing non-technical users — wrap the worker error in a per-workflow-aware prettifier in `executor.go` or `app/.../HandleResult`. Deferred until non-PR workflows demonstrate need; the current design's worker log + raw user message is operator-friendly without needing translation.

`EnsureRepo` could one day fetch `refs/pull/*/head` proactively to avoid the on-demand fetch in Goal 2. Out of scope here because the on-demand fetch keeps the bare cache lean and only pays cost when a job actually needs an unreachable ref.

## Open Questions

None. All design decisions confirmed during 2026-04-23 grill-me session.
