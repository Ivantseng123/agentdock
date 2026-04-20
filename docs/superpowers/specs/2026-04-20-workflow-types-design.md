# Workflow Types — Design

**Date:** 2026-04-20
**Status:** Draft

## Problem

AgentDock today treats every Slack mention as the same action: spawn an agent to triage a thread, then create a GitHub issue. That single shape is hard-coded across the pipeline:

- `app/bot/workflow.go` always walks `repo → branch → description → submit`.
- `app/bot/parser.go` only understands the `===TRIAGE_RESULT===` JSON used for issue creation.
- `app/bot/result_listener.go` always calls `IssueCreator.CreateIssue`.

Two real use cases don't fit:

1. **Quick Q&A.** A user wants the bot to read a thread and answer a question in Slack. Filing an issue would be noise.
2. **Pull request review.** A user wants the bot to review a specific PR and post line-level + summary comments on GitHub, with a Slack summary back.

The current architecture cannot accommodate either without forking the pipeline, and the configuration can't even express which kind of work a `@bot` mention should perform.

## Goals

1. Introduce **three first-class workflow types**: `issue` (current), `ask`, `pr_review`. Each owns its own wizard, prompt, result schema, and post-processing.
2. **Polymorphic dispatch** via a `Workflow` interface and a registry, so a fourth workflow type later costs one new file, not surgery on shared structs.
3. **Wire-level discriminator**: `Job.TaskType` (existing field, currently unused) becomes the source of truth across app, worker, and result handler.
4. **Backwards-compatible triggers**: the legacy `@bot <repo>` and `@bot <repo>@<branch>` continue to mean "create issue", so existing channel users see no change.
5. **A new D-selector path**: `@bot` with no recognised verb prompts the user with `[ Issue / Ask / PR Review ]` buttons in-thread.
6. **Worker stays workflow-agnostic** at the structural level — the only worker-side change is supporting an empty work directory when a job has no clone target. Per-workflow behavior is encoded in the prompt the app sends, not in worker code.
7. **PR Review uses agent skill, not app code.** The bot does not learn the GitHub PR review API; the `github-pr-review` skill teaches the agent to call it. The app only consumes the agent's summary.

## Non-goals

- Splitting Slack manifest / event subscription. Trigger is still `app_mention`; bot parses the verb itself.
- Per-channel default workflow (e.g. "this channel is always Q&A"). Out of scope for v1; revisit if needed.
- Agent-side intent classification (option C from brainstorming, rejected). All workflow selection is explicit (verb in mention) or interactive (D-selector).
- A `Job.WorkflowMeta` generic key/value store rich enough to model arbitrary workflows. v1 uses a small `Job.WorkflowArgs map[string]string` for the few keys we need (`pr_url`, `pr_number`).
- Per-workflow skill manifest in `Job.Skills`. v1 ships the full skill set with every job; agent decides what to load based on prompt.
- Smaller / cheaper LLM for Ask. Same agent CLI, same model selection. Optimisation deferred.
- Migration tool for in-flight production jobs. Pre-launch; no in-flight production jobs to migrate.

## Design

### Architecture

A new `app/workflow/` package (peer to `app/bot/`) owns the per-workflow logic. `app/bot/` shrinks to: receive Slack events, manage the pending state map, and forward to the dispatcher.

```
app/workflow/
  workflow.go         # Workflow interface + Pending envelope + NextStep types
  dispatcher.go       # parse trigger verb → choose workflow + D-selector wiring
  registry.go         # map[string]Workflow with Register / Get
  issue.go            # IssueWorkflow (relocated from app/bot/workflow.go + parser.go)
  ask.go              # AskWorkflow
  pr_review.go        # PRReviewWorkflow
  ports.go            # SlackPort / GitHubPort / IssueCreator interfaces (so workflow tests can mock)
  *_test.go
```

`app/bot/result_listener.go` becomes a thin dispatcher that looks up the workflow by `state.Job.TaskType` and delegates `HandleResult`. Cancellation, metrics, dedup-clear, and attachment cleanup remain at the listener level (cross-cutting concerns).

#### Component placement notes

- **`IssueCreator` interface** (currently in `app/bot/result_listener.go`) moves to `app/workflow/ports.go` because `IssueWorkflow.HandleResult` is the only consumer after the refactor. `app/bot/` no longer references GitHub.
- **`RetryHandler`** (currently in `app/bot/retry_handler.go`, invoked by the `retry_job` action handler in `cmd/agentdock/app.go`) **stays in `app/bot/`** as shared infrastructure. The retry button itself, its action wiring, and the dedup re-arming are cross-cutting. `IssueWorkflow.HandleResult` decides *whether* to attach the retry button (first failure: yes, retry exhausted: no); the click handler still routes through `RetryHandler`, which re-submits via the dispatcher just like a fresh trigger. `AskWorkflow` and `PRReviewWorkflow` simply never attach the button.

### `Workflow` interface

```go
type Workflow interface {
    // Type returns the discriminator stored in Job.TaskType.
    // Values: "issue", "ask", "pr_review".
    Type() string

    // Trigger handles the entry point for a new mention.
    // ev carries channel/thread/user; args is the remainder after the verb.
    // Returns the next wizard step (post selector, open modal, submit, ...).
    Trigger(ctx context.Context, ev TriggerEvent, args string) (NextStep, error)

    // Selection handles a follow-up button click or modal submit.
    // p is the workflow-typed pending state stored under selectorMsgTS.
    Selection(ctx context.Context, p *Pending, value string) (NextStep, error)

    // BuildJob converts the completed pending state into a queue.Job.
    // Sets TaskType, PromptContext, CloneURL, Repo/Branch, WorkflowArgs as needed.
    BuildJob(ctx context.Context, p *Pending) (*queue.Job, error)

    // HandleResult is called by ResultListener when the worker returns a result
    // for a job whose TaskType matches this workflow. The workflow owns parsing,
    // Slack posting, optional GitHub side-effects, retry-button decisions, and
    // dedup-clear.
    HandleResult(ctx context.Context, job *queue.Job, result *queue.JobResult) error
}
```

`NextStep` is a discriminated union: `{ kind: postSelector | openModal | submit | error, payload: ... }`. The dispatcher executes it against the injected `SlackPort`.

`Pending` is a common envelope (channel, thread, user, requestID, timestamps, selector-msg TS) plus a per-workflow `State any` field. Each workflow casts to its own state struct; `Pending` itself does not enumerate workflow types.

### Dispatch

`HandleTrigger` (entry from `app/bot/`) calls `dispatcher.Dispatch(ev)`:

- Parse the mention text after the bot tag. Accept `<verb> <args>`, `<args>` only, or empty.
- Verb mapping:
  - `issue` → `IssueWorkflow.Trigger`
  - `ask` → `AskWorkflow.Trigger`
  - `review` → `PRReviewWorkflow.Trigger`
  - **No verb but args look repo-shaped** (`owner/repo[@branch]`) → `IssueWorkflow.Trigger` (legacy compat)
  - **No verb, no recognisable args** → post D-selector with three buttons
- D-selector buttons reuse the same Selection/Trigger plumbing — clicking `[ Issue ]` is equivalent to a synthetic `@bot issue` event.

### Per-workflow behaviour

#### Issue (refactor, behaviour preserved)

- **Trigger forms**: `@bot issue [<repo>[@<branch>]]`, `@bot <repo>[@<branch>]`, D-selector `[ Issue ]`.
- **Wizard**: repo selector → branch selector (when channel `branch_select` enabled and >1 branch) → "需要補充說明嗎？" modal → submit. Identical to today.
- **`Job` fields**:
  - `TaskType = "issue"`
  - `Repo`, `Branch`, `CloneURL` from selection
  - `PromptContext.Goal` — the configured issue-triage goal from `app.yaml`
  - `PromptContext.OutputRules` — current rules; agent emits `===TRIAGE_RESULT===`
  - `WorkflowArgs` empty
- **Result marker**: `===TRIAGE_RESULT===`
- **Result JSON** (unchanged):
  ```json
  {
    "status": "CREATED|REJECTED|ERROR",
    "title": "...",
    "body": "...",
    "labels": ["bug"],
    "confidence": "high|medium|low",
    "files_found": 5,
    "open_questions": 0,
    "message": "..."
  }
  ```
- **HandleResult** (logic relocated, behaviour identical):
  - `REJECTED` → low-confidence Slack message
  - `ERROR` → failure with retry button
  - `CREATED` with low confidence or 0 files / ≥5 questions → degraded issue (strip triage section)
  - `CREATED` otherwise → full issue + Slack URL
  - On `failed` from worker: retry button on first failure, no button after retry exhausted

#### Ask

- **Trigger forms**: `@bot ask [<question text>]`, D-selector `[ Ask ]`.
- **Wizard** (short):
  1. If args carry question text, store it in pending; else use thread context only.
  2. Post `:question: 要附加 repo context 嗎？` `[ 附加 / 不用 ]`.
  3. `[ 不用 ]` → submit immediately.
  4. `[ 附加 ]` → repo selector → on selection, submit.
  5. Skip branch selection. Skip description modal.
- **`Job` fields**:
  - `TaskType = "ask"`
  - `Repo`, `Branch`, `CloneURL` empty when no repo attached; populated when attached
  - `PromptContext.Goal` — workflow-specific: "Answer the user's question using the thread, and (if a codebase is attached) the repo. Output `===ASK_RESULT===` followed by JSON `{\"answer\": \"<markdown>\"}`."
  - `PromptContext.OutputRules` — Slack-friendly markdown, no title/labels, fenced code blocks for code references
  - `WorkflowArgs` empty
- **Result marker**: `===ASK_RESULT===`
- **Result JSON**:
  ```json
  {
    "answer": "<markdown>",
    "confidence": "high|medium|low"
  }
  ```
- **HandleResult**:
  - On `completed`: parse, post answer as a bot message in the thread (replacing the status message via `UpdateMessage`).
  - On `failed`: post `:x: 思考失敗：<reason>`. **No retry button** (short job; user can re-mention).
  - No GitHub side-effect.

#### PR Review

- **Trigger forms**: `@bot review <PR-URL>` (URL required), D-selector `[ PR Review ]`.
- **A-path** (URL in mention):
  1. Validate URL — regex `https://github.com/{owner}/{repo}/pull/{number}` and GitHub API `GET /repos/{owner}/{repo}/pulls/{number}` using the bot's `GH_TOKEN`.
  2. On 404 / 403 / format failure: post friendly error and abort.
  3. On success: capture PR head ref + sha; submit.
- **D-path** (button click):
  1. Scan thread messages for any GitHub PR URL using regex match.
  2. If found: post `:eyes: 找到 owner/repo#N，review？` `[ 是 / 改貼 URL ]`.
  3. If not found, or user clicks `[ 改貼 URL ]`: open a Slack modal asking for the URL.
  4. URL acquired → same validation as A-path step 1.
- **`Job` fields**:
  - `TaskType = "pr_review"`
  - `Repo` = `owner/repo` (parsed from URL)
  - `Branch` = PR's `head.ref` (from API call)
  - `CloneURL` derived from `Repo`
  - `PromptContext.Goal` — workflow-specific: "Review the PR at `<URL>`. Use the `github-pr-review` skill to post line-level comments and a summary comment on the PR. Output `===REVIEW_RESULT===` followed by JSON `{\"summary\": \"...\", \"comments_posted\": <int>, \"verdict\": \"approve|comment|request_changes\"}`."
  - `Skills` must include `github-pr-review`
  - `WorkflowArgs = { "pr_url": "<URL>", "pr_number": "<N>" }` — used by `HandleResult` to format the Slack message; not surfaced to the agent prompt (the URL is already in `Goal`).
- **Result marker**: `===REVIEW_RESULT===`
- **Result JSON**:
  ```json
  {
    "summary": "<markdown>",
    "comments_posted": 12,
    "verdict": "approve|comment|request_changes"
  }
  ```
- **HandleResult**:
  - On `completed`: parse, post `:white_check_mark: Review 完成 (verdict · N comments) on <PR URL>\n> <summary first 200 chars>`.
  - On `failed`: post `:x: Review 失敗：<reason>`. **No retry button** — the agent may have already posted comments; retrying would double-write.

### Wire schema changes

#### `Job` (`shared/queue/job.go`)

| Field | Change | Notes |
|-------|--------|-------|
| `TaskType string` | **Activated** (existing field, previously unused) | Required. Values: `issue`, `ask`, `pr_review`. |
| `WorkflowArgs map[string]string` | **New** | Per-workflow KV. v1 keys: `pr_url`, `pr_number`. |
| `Repo`, `Branch`, `CloneURL` | Semantic widening | All three may be empty for `ask` without repo; PR Review fills from URL+API. |
| `Skills` | No change | App ships full skill set; worker mounts all. |

#### `JobResult` (`shared/queue/job.go`)

**Removed** (Issue-specific, leaked into the wire format):

- `Title`, `Body`, `Labels`, `Confidence`, `FilesFound`, `Questions`, `Message`

These move into the per-workflow parser inside `app/workflow/issue.go`. The wire `JobResult` shrinks to common fields only:

- `JobID`, `Status`, `RawOutput`, `Error`, `StartedAt`, `FinishedAt`
- `CostUSD`, `InputTokens`, `OutputTokens`
- `RepoPath`, `PrepareSeconds` (local-only, `json:"-"`)

#### Result markers

Each workflow's `Goal` instructs the agent which marker to emit:

| Workflow | Marker |
|----------|--------|
| Issue | `===TRIAGE_RESULT===` (preserved) |
| Ask | `===ASK_RESULT===` |
| PR Review | `===REVIEW_RESULT===` |

Marker → workflow lookup is implicit: `ResultListener` already knows the workflow from `Job.TaskType`, so the marker name is only a parser-internal validation hint.

### Worker side changes

The worker remains workflow-agnostic. The only structural change is supporting jobs with no clone target.

#### `WorkDirProvider` abstraction

```go
type WorkDirProvider interface {
    Prepare(job *queue.Job) (path string, err error)
    Cleanup(path string)
}
```

Two implementations:

- `RepoCloneProvider` — wraps current `RepoCache.Prepare(CloneURL, Branch, token)` + `RemoveWorktree`.
- `EmptyDirProvider` — `os.MkdirTemp("", "ask-*")` + `os.RemoveAll`.

`executeJob` chooses via a single helper:

```go
provider := selectProvider(job)  // CloneURL == "" → EmptyDir; else RepoClone
workDir, err := provider.Prepare(job)
defer provider.Cleanup(workDir)
```

Skill mount, attachment write, and agent `cwd` all remain identical — they take a path and don't care how it was produced.

#### Skills, prompt builder, agent invocation, result emission

- **Skills**: no change. App ships the full skill set in `Job.Skills`; worker mounts all of them. Mounting an unused skill is cheap and keeps worker workflow-agnostic for v1.
- **Prompt builder** (`worker/prompt/builder.go`): no signature or behaviour change. The per-workflow goal text is what differentiates output; that text already lives in `PromptContext.Goal`, which the app sets in `BuildJob`.
- **Agent invocation**: same `runner.Run(ctx, workDir, prompt, opts)`.
- **Result emission**: worker continues to ship `RawOutput` unparsed. App-side workflow parser does the rest.

#### Failure / cancellation

`failedResult` and `cancelledResult` are common. The worker does not branch on `TaskType` for these. Per-workflow retry policy is decided at `HandleResult` time on the app side.

### `ResultListener` after refactor

```go
func (r *ResultListener) handleResult(ctx context.Context, result *queue.JobResult) {
    if r.alreadyProcessed(result.JobID) { return }

    state, err := r.store.Get(result.JobID)
    if err != nil { /* log + return */ }

    r.recordMetrics(state, result)  // common

    if state.Status == queue.JobCancelled || result.Status == "cancelled" {
        r.handleCancellation(state.Job, state, result)  // common
        r.attachments.Cleanup(ctx, result.JobID)
        return
    }

    workflow, ok := r.registry.Get(state.Job.TaskType)
    if !ok {
        r.failUnknownTaskType(state.Job, result)
        r.attachments.Cleanup(ctx, result.JobID)
        return
    }

    if err := workflow.HandleResult(ctx, state.Job, result); err != nil {
        r.logger.Error("workflow.HandleResult failed", "err", err)
    }
    r.attachments.Cleanup(ctx, result.JobID)
}
```

`handleFailure` (current method, with retry-button logic) becomes part of `IssueWorkflow.HandleResult`. `AskWorkflow` and `PRReviewWorkflow` post a plain failure line with no retry button.

### Slack UX summary

- **D-selector**: posted only when `@bot` has no verb and no repo-shaped args.
  ```
  :point_right: 你想做什麼？
  [ 📝 建 Issue ]  [ ❓ 問問題 ]  [ 🔍 Review PR ]
  ```
  Subject to the existing 1-minute `pendingTimeout` (`app/bot/workflow.go:22`); after expiry the user is asked to re-mention the bot.
- **Issue**: identical to today.
- **Ask**: 1 confirmation button (attach repo? yes/no) → optional repo selector → submit. Result is the bot's answer, posted by editing the status message.
- **PR Review**: URL via mention or auto-detection from thread; modal fallback. Validation → submit. Result line includes verdict, comment count, PR URL, and a summary excerpt.
- **Failure**:
  - Issue: retry button on first failure (current behaviour).
  - Ask: plain failure line, no retry.
  - PR Review: plain failure line, no retry (avoid double-writing GitHub).

## Testing

- **Per-workflow unit tests**: `app/workflow/{issue,ask,pr_review}_test.go` cover trigger parsing, wizard transitions, `BuildJob` field population, and `HandleResult` behaviour using mocked `SlackPort` and mocked `IssueCreator` / GitHub client.
- **Dispatcher tests**: parse `@bot ask <q>`, `@bot review <URL>`, `@bot foo/bar`, `@bot foo/bar@dev`, `@bot` (empty), `@bot unknown verb` → assert routing and D-selector trigger conditions.
- **D-selector tests**: button click on each of the three buttons → assert correct workflow's `Trigger` is invoked with synthetic event.
- **Wire schema tests** (`shared/queue/job_test.go`): JSON marshal/unmarshal includes `TaskType` and `WorkflowArgs`; old `JobResult` fields are absent on the wire.
- **Worker `WorkDirProvider` tests** (`worker/pool/*_test.go`): empty dir is created and cleaned for `CloneURL == ""`; existing repo-clone path is unchanged.
- **Refactor period**: `app/bot/result_listener_test.go` and `app/bot/parser_test.go` move to `app/workflow/issue_test.go` with assertions intact (the behaviour they verify is now owned by `IssueWorkflow`).
- **Module boundary**: `test/import_direction_test.go` automatically catches accidental `app/workflow → worker/*` imports.
- **Manual smoke**: after wiring, run all three workflows end-to-end against a staging Slack channel + a fixture GitHub repo.

## Implementation order

Eight PRs, each independently reviewable. The end state is the polymorphic design from §Design; the staging keeps each PR small.

1. **Skeleton** — Create `app/workflow/` package with the `Workflow` interface, `Pending` envelope, `NextStep` types, dispatcher shell, registry, and stub implementations of all three workflows that return "not implemented". Build passes; no behaviour change yet.
2. **`IssueWorkflow` refactor** — Move logic from `app/bot/workflow.go`, `parser.go`, and the issue-specific portion of `result_listener.go` into `app/workflow/issue.go`. Keep public Slack handler entry in `app/bot/` but route through dispatcher → registry → `IssueWorkflow`. All existing tests pass after import-path edits.
3. **`JobResult` cleanup + `ResultListener` thinning** — Remove Issue-specific fields from `JobResult`. Make `ResultListener.handleResult` a dispatcher. `IssueWorkflow.HandleResult` owns retry button logic. Behaviour unchanged from a user's perspective.
4. **Worker `WorkDirProvider`** — Add the abstraction and `EmptyDirProvider`. Wire `executeJob` through `selectProvider`. Existing repo-clone path verified by tests; empty-dir path verified by new test using a fixture job with `CloneURL == ""`.
5. **`AskWorkflow`** — New file `app/workflow/ask.go`, wizard, `BuildJob`, `HandleResult`, parser. Add Slack UX (the optional repo button). Tests cover both with-repo and without-repo paths.
6. **`PRReviewWorkflow`** — New file `app/workflow/pr_review.go`. URL parser + validator (regex + GitHub API). Wizard for both A-path and D-path. `BuildJob` populates `Repo`, `Branch`, `WorkflowArgs`. Drop a draft `github-pr-review` skill into the skill payload directory.
7. **D-selector + dispatcher integration** — Wire the three buttons. Confirm `@bot foo/bar` still goes to Issue (legacy compat). Confirm verb routing works.
8. **End-to-end smoke** — Run all three workflows against staging. Tag and ship.

## Open questions / future work

- **Per-channel default workflow.** If usage shows that a channel almost always wants Q&A, channel config could default `task_type: ask` so users can skip the verb. Not in v1.
- **Per-workflow skill manifest.** Currently every job ships every skill. Ask jobs in particular pay the full skill-payload wire cost despite often not needing any of them. Acceptable for v1 (skills are small text files); if the skill set grows large or Ask volume dominates, add a `Workflow.Skills() []string` filter so each job only ships what it needs.
- **PR Review without skill.** The `github-pr-review` skill is a separate deliverable. Until it exists, the workflow can ship but its agent prompt will need a stub instructing the agent to post a single review comment via `gh pr comment`.
- **Smaller / cheaper LLM for Ask.** Q&A latency would benefit from a faster model; out of scope for v1, revisit after adoption.
- **Result UI for very long answers.** Ask answers may exceed Slack message limits. v1 truncates; v2 could thread-split or attach as a snippet.
