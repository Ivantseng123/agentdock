# Workflow Types — Design

**Date:** 2026-04-20
**Status:** Revised after grill-me review (2026-04-21); PR Review details re-aligned with `2026-04-21-github-pr-review-skill-design.md` (2026-04-21)

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
3. **Wire-level discriminator**: `Job.TaskType` (existing field, currently unused) becomes the source of truth **for app-side dispatch only**. The worker does not read or validate it — adding a new workflow never requires a worker code or config change.
4. **Backwards-compatible triggers**: the legacy `@bot <repo>` and `@bot <repo>@<branch>` continue to mean "create issue", so existing channel users see no change.
5. **A new D-selector path**: `@bot` with no recognised verb prompts the user with `[ Issue / Ask / PR Review ]` buttons in-thread.
6. **Worker stays workflow-agnostic** at the structural level — the only worker-side change is supporting an empty work directory when a job has no clone target. Per-workflow behavior is encoded in the prompt the app sends, not in worker code.
7. **PR Review uses agent skill, not app code.** The bot does not learn the GitHub PR review API; the `github-pr-review` skill teaches the agent to call it via the REST API + curl. The app only consumes the agent's summary.
8. **Per-workflow config**: `prompt.{issue,ask,pr_review}.{goal,output_rules}` replaces the current flat `prompt.goal` / `prompt.output_rules`, with the flat form aliased to `prompt.issue.*` for backwards compatibility.
9. **Unified workflow metrics**: replace the Issue-centric `IssueCreatedTotal` / `IssueRejectedTotal` / `IssueRetryTotal` with `WorkflowCompletionsTotal{workflow, status}` so a single Grafana panel covers all three.

## Non-goals

- Splitting Slack manifest / event subscription. Trigger is still `app_mention`; bot parses the verb itself.
- Per-channel default workflow (e.g. "this channel is always Q&A"). Out of scope for v1; revisit if needed.
- Agent-side intent classification (option C from brainstorming, rejected). All workflow selection is explicit (verb in mention) or interactive (D-selector).
- A `Job.WorkflowMeta` generic key/value store rich enough to model arbitrary workflows. v1 uses a small `Job.WorkflowArgs map[string]string` for the few keys we need (`pr_url`, `pr_number`).
- Per-workflow skill manifest in `Job.Skills`. v1 ships the full skill set with every job; agent decides what to load based on prompt. Exception: Ask explicitly skips skill mount to reduce the empty-dir surface area (see §Worker side changes).
- Smaller / cheaper LLM for Ask. Same agent CLI, same model selection. Optimisation deferred.
- Migration tool for in-flight production jobs. Pre-launch; no in-flight production jobs to migrate.
- Enterprise GitHub support for PR Review. v1 only accepts `github.com` URLs; enterprise hosts revisit in v2 via `pr_review.allowed_hosts` config.
- A new external tool dependency for PR Review beyond the `agentdock` binary itself. The `github-pr-review` skill's helper logic ships as an `agentdock pr-review-helper` subcommand, which is necessarily present on any worker (Docker image or colleague's laptop). No `gh`, `jq`, curl-JSON assembly, or scripting-language runtime required. See `2026-04-21-github-pr-review-skill-design.md`.
- Per-phase retry for PR Review. v1 rejects retry universally once the job has been dispatched — even for pre-agent failures — to keep the failure path simple. Users re-trigger manually with `@bot review <URL>`. A `JobResult.FailurePhase` field is future work (see §Open questions).
- Automatic recovery if GitHub API is unreachable during URL validation. App refuses the trigger ("GitHub 不可達") and the user re-mentions later.

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

The `registry.Get(taskType)` call is the **natural enforcement point**: an unknown or empty `TaskType` short-circuits with "unknown task type" failure. No separate validator — the dispatcher pattern makes the contract self-enforcing.

#### Component placement notes

- **`IssueCreator` interface** (currently in `app/bot/result_listener.go`) moves to `app/workflow/ports.go` because `IssueWorkflow.HandleResult` is the only consumer after the refactor. `app/bot/` no longer references GitHub.
- **`RetryHandler`** (currently in `app/bot/retry_handler.go`, invoked by the `retry_job` action handler in `cmd/agentdock/app.go`) **stays in `app/bot/`** as shared infrastructure. The retry button itself, its action wiring, and the dedup re-arming are cross-cutting. `IssueWorkflow.HandleResult` decides *whether* to attach the retry button (first failure: yes, retry exhausted: no); the click handler still routes through `RetryHandler`, which re-submits via the dispatcher just like a fresh trigger, carrying `TaskType` and `WorkflowArgs` unchanged. `AskWorkflow` and `PRReviewWorkflow` simply never attach the button.
- **Modal opener** (`OpenDescriptionModal` in `app/slack/client.go`) generalises to `OpenTextInputModal(triggerID, title, label, inputName, metadata string)`. Description modal and PR URL modal both use this primitive.

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

    // BuildJob converts the completed pending state into a queue.Job plus
    // the status-message text to post alongside the "排隊中 → 處理中" lifecycle.
    // Sets TaskType, PromptContext, CloneURL, Repo/Branch, WorkflowArgs.
    BuildJob(ctx context.Context, p *Pending) (job *queue.Job, statusText string, err error)

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

- **Parse the mention text** after the bot tag. Accept `<verb> <args>`, `<args>` only, or empty.
- **Verb matching is case-insensitive** (`@bot ASK`, `@bot Ask`, `@bot ask` all equivalent).
- **Slack auto-wrapping of URLs** (e.g. `<https://github.com/foo/bar/pull/123>`) is stripped from args before further parsing.
- **Verb mapping**:
  - `issue` → `IssueWorkflow.Trigger`
  - `ask` → `AskWorkflow.Trigger`
  - `review` → `PRReviewWorkflow.Trigger`
  - **No verb but args look repo-shaped** (`owner/repo[@branch]`) → `IssueWorkflow.Trigger` (legacy compat)
  - **No verb, no recognisable args** → post D-selector with three buttons
  - **Unknown verb** (e.g. `@bot askme`, `@bot reveiw`) → post D-selector with a message `:warning: 不認得 <verb>，請選一個` (avoids silently mis-routing to Issue legacy path)
- **Partial PR URLs are rejected** — PR Review only accepts complete `https://github.com/.../pull/N`. Shortened forms (`github.com/...`, `owner/repo#123`) respond with "請貼完整 PR URL".
- D-selector buttons reuse the same Selection/Trigger plumbing — clicking `[ Issue ]` is equivalent to a synthetic `@bot issue` event.

### Per-workflow behaviour

#### Issue (refactor, behaviour preserved)

- **Trigger forms**: `@bot issue [<repo>[@<branch>]]`, `@bot <repo>[@<branch>]`, D-selector `[ Issue ]`.
- **Wizard**: repo selector → branch selector (when channel `branch_select` enabled and >1 branch) → "需要補充說明嗎？" modal → submit. Identical to today.
- **Status text** (replaces "正在排入處理佇列"): `:mag: 分析 codebase 中...`
- **`Job` fields**:
  - `TaskType = "issue"`
  - `Repo`, `Branch`, `CloneURL` from selection
  - `PromptContext.Goal` — from `prompt.issue.goal` (config) or hardcoded default
  - `PromptContext.OutputRules` — from `prompt.issue.output_rules` (config) or hardcoded default; agent emits `===TRIAGE_RESULT===`
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
  - Cancel button always present on status message (consistent with Ask / PR Review)

#### Ask

- **Trigger forms**: `@bot ask [<question text>]`, D-selector `[ Ask ]`.
- **Wizard** (short):
  1. If args carry question text, store it in pending; else use thread context only.
  2. Post `:question: 要附加 repo context 嗎？` `[ 附加 / 不用 ]`.
  3. `[ 不用 ]` → submit immediately.
  4. `[ 附加 ]` → repo selector → on selection, submit.
  5. Skip branch selection. Skip description modal.
- **Status text**: `:thinking_face: 思考中...`
- **`Job` fields**:
  - `TaskType = "ask"`
  - `Repo`, `Branch`, `CloneURL` empty when no repo attached; populated when attached
  - `PromptContext.Goal` — from `prompt.ask.goal` (config) or hardcoded default: "Answer the user's question using the thread, and (if a codebase is attached) the repo. Output `===ASK_RESULT===` followed by JSON `{\"answer\": \"<markdown>\"}`."
  - `PromptContext.OutputRules` — from `prompt.ask.output_rules` (config) or hardcoded defaults: Slack-friendly markdown, no title/labels, fenced code blocks for code references, **≤ 30000 characters** (app enforces a hard truncate at 38000 chars as a backstop).
  - **`Skills` intentionally empty** for Ask (defensive — the agent CLI's skill-discovery behaviour in a non-repo work directory is validated by a spike test in PR 4 before the rest of Ask ships; until the spike passes, not mounting skills removes the risk entirely).
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
  - On `completed`: parse, post answer as a bot message in the thread (replacing the status message via `UpdateMessage`). If the answer exceeds 38000 chars, truncate and append `…(已截斷)`.
  - On `failed`: post `:x: 思考失敗：<reason>`. **No retry button** (short job; user can re-mention).
  - Cancel button on status message (consistent).
  - No GitHub side-effect.

#### PR Review

- **Trigger forms**: `@bot review <PR-URL>` (URL required), D-selector `[ PR Review ]`.
- **Feature flag**: gated behind `pr_review.enabled` in `app.yaml` (default `false`). When disabled, the dispatcher responds to a PR Review trigger with `:warning: PR Review 尚未啟用，請聯絡管理員`. Enables operator-controlled rollout before the `github-pr-review` skill package is finished.
- **URL acceptance scope**:
  - **Host**: only `github.com` (enterprise hosts deferred to v2 via `pr_review.allowed_hosts`).
  - **PR state**: closed, merged, and draft PRs all accepted (review of historical PRs is a legitimate use case; agent skill handles "cannot approve a merged PR" edge cases).
  - **Fork / cross-repo PRs**: accepted. The worker clones `head.repo.full_name @ head.ref` (which may be a fork). Base ref is not propagated in `WorkflowArgs` — the `agentdock pr-review-helper` subcommand fetches PR metadata (including base ref) directly via `GET /pulls/:n/files` when needed.
- **A-path** (URL in mention):
  1. Validate URL — regex `https://github.com/{owner}/{repo}/pull/{number}` and GitHub API `GET /repos/{owner}/{repo}/pulls/{number}` using the bot's `GH_TOKEN`.
  2. On 404 → `:x: 找不到 PR`. On 403 → `:x: 沒權限存取 PR`. On format failure → `:x: URL 格式錯誤`. On connection error → `:x: GitHub 不可達，請稍後重試`.
  3. On success: capture `head.repo.full_name`, `head.ref`, `head.sha`, `base.ref`; submit.
- **D-path** (button click):
  1. Scan thread messages for any GitHub PR URL using regex match.
  2. If found: post `:eyes: 找到 owner/repo#N，review？` `[ 是 / 改貼 URL ]`.
  3. If not found, or user clicks `[ 改貼 URL ]`: open a Slack modal (`OpenTextInputModal`) asking for the URL.
  4. URL acquired → same validation as A-path step 1.
- **Status text**: `:eyes: Reviewing owner/repo#N...`
- **`Job` fields**:
  - `TaskType = "pr_review"`
  - `Repo = head.repo.full_name` (may be a fork; not necessarily the base repo)
  - `Branch = head.ref`
  - `CloneURL` derived from `Repo`
  - `PromptContext.Goal` — from `prompt.pr_review.goal` (config) or hardcoded default: "Review the PR at `<URL>`. Use the `github-pr-review` skill to analyze the diff and post line-level comments and a summary review back to the PR. Output `===REVIEW_RESULT===` followed by JSON per the skill's contract (status enum POSTED | SKIPPED | ERROR; see `2026-04-21-github-pr-review-skill-design.md` §Result marker contract)."
  - `Skills` includes whatever the app provides (PR Review does not deliberately strip skills — clone path, known-safe).
  - `WorkflowArgs = { "pr_url": "<URL>", "pr_number": "<N>" }` — `pr_url` used by `HandleResult` to format the Slack result message; `pr_number` for `owner/repo#N` display. Head SHA is **not** captured here — the `agentdock pr-review-helper` on the worker re-resolves it via `git rev-parse HEAD` at post time so the review is tied to the exact commit the agent read. Base ref is fetched by the helper from `GET /pulls/:n/files` if needed. Not surfaced directly into the agent prompt (the URL is already in `Goal`).
- **Result marker**: `===REVIEW_RESULT===`
- **Result JSON (three-state, per `2026-04-21-github-pr-review-skill-design.md` §Result marker contract)**:
  ```json
  // POSTED — helper successfully submitted the review to GitHub
  {
    "status": "POSTED",
    "summary": "<review body, same text posted to GitHub>",
    "comments_posted": 12,
    "comments_skipped": 3,
    "severity_summary": "clean|minor|major"
  }

  // SKIPPED — agent short-circuited (lockfile / vendored / generated)
  {
    "status": "SKIPPED",
    "summary": "<explanation>",
    "reason": "lockfile_only|vendored|generated|pure_docs|pure_config"
  }

  // ERROR — helper failed (auth / 422 / rate-limit); nothing posted
  {
    "status": "ERROR",
    "error": "<one-liner reason>",
    "summary": "<agent's intended review text; not posted>"
  }
  ```
  The GitHub review `event` is always `COMMENT` (see skill spec §Goals — the bot does not auto-approve or auto-request-changes). `severity_summary` carries the bot's assessment informationally and drives the Slack message tone, but does **not** touch GitHub's merge-gating flow.
- **HandleResult** (dispatch on `status`):
  - `POSTED` → post `:white_check_mark: Review 完成 (severity: <severity_summary> · <comments_posted> comments, <comments_skipped> skipped) on <PR URL>\n> <summary first 200 chars>`.
  - `SKIPPED` → post `:information_source: Review 跳過 (<reason>): <summary first 200 chars>`.
  - `ERROR` → post `:x: Review 失敗：<error>`. **No retry button** — the agent may have already posted comments in a prior attempt; retrying would double-write. User re-mentions `@bot review <URL>` manually.
  - On `cancelled` job: post `:white_check_mark: 已取消。已貼的 comments 保留於 PR <URL>` (the agent may have posted partial comments before cancellation).
  - Cancel button on status message (consistent).

### Wire schema changes

#### `Job` (`shared/queue/job.go`)

| Field | Change | Notes |
|-------|--------|-------|
| `TaskType string` | **Activated** (existing field, previously unused) | Required. Values: `issue`, `ask`, `pr_review`. **App-side dispatch key only** — the worker does not read or validate it. Adding a new workflow never requires a worker change; the worker acts on `CloneURL`, `Skills`, `PromptContext`, `WorkflowArgs` alone. The natural enforcement point is `registry.Get(taskType)` on the app side — an unknown / empty value short-circuits with "unknown task type" failure in `ResultListener`. |
| `WorkflowArgs map[string]string` | **New** | Per-workflow KV. v1 keys: `pr_url`, `pr_number`. Head SHA and base ref are re-resolved worker-side (git / GitHub API) — not threaded here. |
| `Repo`, `Branch`, `CloneURL` | Semantic widening | All three may be empty for `ask` without repo; PR Review fills from URL+API (`Repo = head.repo.full_name`, `Branch = head.ref`). |
| `Skills` | No change in shape | App still ships skill set; `AskWorkflow.BuildJob` deliberately sets this empty while the empty-dir skill-mount spike test (PR 4) is in flight. |

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

### Result parser error handling

Each workflow owns its parser. Failure modes and their Slack UX:

| Failure mode | Slack message | Log |
|--------------|---------------|-----|
| Marker missing | `:x: Agent 未照 contract 輸出` | RawOutput first 2K |
| Wrong marker for workflow (e.g. Ask receives `===TRIAGE_RESULT===`) | `:x: Agent 輸出類型錯誤 (期望 X，收到 Y)` | RawOutput first 2K |
| Marker present but JSON syntax broken | `:x: 解析失敗：<err>` | RawOutput first 2K |
| Required field empty (e.g. Ask `answer=""`, PR Review `summary=""`) | `:x: Agent 回傳內容為空` | RawOutput first 2K |
| Multiple marker blocks | Take the last one (`strings.LastIndex`); log warning about extras | Count of extras |

Parser does not attempt best-effort recovery — failing loudly is preferable to half-correct output.

### Config schema changes (`app.yaml`)

`PromptConfig` in `app/config/config.go` evolves from a flat single-goal shape to per-workflow sections:

```yaml
prompt:
  language: "正體中文"
  allow_worker_rules: true

  issue:
    goal: "Use the /triage-issue skill to investigate and produce a triage result."
    output_rules:
      - "Title must be concise..."
      - "Body must include Root Cause Analysis..."

  ask:
    goal: "Answer the user's question using the thread, and (if a codebase is attached) the repo. Output ===ASK_RESULT=== followed by JSON {\"answer\": \"<markdown>\"}."
    output_rules:
      - "Slack-friendly markdown, ≤30000 chars"
      - "No title / labels"
      - "Use fenced code blocks for code references"

  pr_review:
    goal: "Review the PR. Use github-pr-review skill to analyze the diff and post line-level comments plus a summary. Output ===REVIEW_RESULT=== with status (POSTED|SKIPPED|ERROR) + summary + severity_summary."
    output_rules:
      - "Focus on correctness, security, style"
      - "Summary ≤ 2000 chars"

pr_review:
  enabled: false   # feature flag; operator enables when the skill package is ready
```

**Backwards compatibility**: the legacy flat `prompt.goal` / `prompt.output_rules` keys, if present, are aliased to `prompt.issue.goal` / `prompt.issue.output_rules`. Operators can upgrade to the nested form at their own pace. Each workflow falls back to a hardcoded default if its config section is absent — zero config is a valid state.

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

#### Empty-dir skill-mount spike test (blocks PR 5)

Skill discovery behaviour in a non-repo directory is not guaranteed across all agent CLIs (claude, codex, gemini, opencode). PR 4 includes a spike test: mount a fake skill into `<emptyDir>/.claude/skills/` (and each provider's configured skill dir), run the dummy agent runner, and assert the skill is visible to the agent.

Outcomes:

- **All green** — Ask workflow is free to mount skills later (PR 5 can lift the `Skills = nil` defensive stance if desired).
- **Any red** — Ask ships with `Skills = nil` as designed; the failing agent(s) gain a fallback handled by one of:
  - Run `git init` on the empty dir (cheap, retains the "no real repo" property).
  - Mount skills under `$HOME/<skill_dir>` instead of the workdir (global, risks cross-job pollution; only if other fallbacks fail).

The fallback path is not pre-implemented — it ships only if the spike forces it.

#### Skills, prompt builder, agent invocation, result emission

- **Skills**: no structural change. App ships skills in `Job.Skills`; worker mounts whatever is there. `AskWorkflow` deliberately sets `Skills = nil` until the PR 4 spike test passes.
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

    r.recordMetrics(state, result)  // common, emits WorkflowCompletionsTotal etc.

    if state.Status == queue.JobCancelled || result.Status == "cancelled" {
        // Cancellation message content is workflow-specific (PR Review mentions
        // partial comments), so delegate to the workflow after common bookkeeping.
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

`handleFailure` (current method, with retry-button logic) becomes part of `IssueWorkflow.HandleResult`. `AskWorkflow` and `PRReviewWorkflow` post a plain failure line with no retry button. Cancellation UX text is workflow-specific (PR Review mentions partial comments), so workflows own the cancellation message too — `ResultListener` only sets the Job status + clears attachments.

### Metrics

**Removed** (Issue-centric, do not survive the refactor):

- `IssueCreatedTotal{confidence, degraded}`
- `IssueRejectedTotal{reason}`
- `IssueRetryTotal{outcome}`

**Added / updated** (unified across workflows):

- `WorkflowCompletionsTotal{workflow, status}` — `workflow` ∈ `issue|ask|pr_review`; `status` ∈ `success|rejected|error|cancelled|parse_failed`.
- `WorkflowRetryTotal{workflow, outcome}` — `outcome` ∈ `attempted|exhausted`. Currently only Issue emits this; field is present on all three for forward compat.
- `QueueJobDuration{status}` → `QueueJobDuration{workflow, status}` — adds `workflow` label.
- `AgentExecutionsTotal{provider, status}` → `AgentExecutionsTotal{provider, workflow, status}` — adds `workflow` label so per-workflow cost / tool-call analysis is straightforward.

Operator Grafana dashboards are rebuilt against the new schema. Pre-launch, so no production dashboards to migrate.

### Slack UX summary

- **D-selector**: posted only when `@bot` has no verb and no repo-shaped args, or when the verb is unknown.
  ```
  :point_right: 你想做什麼？
  [ 📝 建 Issue ]  [ ❓ 問問題 ]  [ 🔍 Review PR ]
  ```
  Subject to the existing 1-minute `pendingTimeout` (`app/bot/workflow.go:22`); after expiry the user is asked to re-mention the bot. After a button is clicked, the selector message is updated to `:white_check_mark: 已選：<workflow>` (consistent with the existing repo-selector UX).
- **Issue**: identical to today. Status text: `:mag: 分析 codebase 中...`.
- **Ask**: 1 confirmation button (attach repo? yes/no) → optional repo selector → submit. Status text: `:thinking_face: 思考中...`. Result is the bot's answer, posted by editing the status message.
- **PR Review**: URL via mention or auto-detection from thread; modal fallback. Validation → submit. Status text: `:eyes: Reviewing owner/repo#N...`. Result line includes severity summary (clean/minor/major), comment count (posted + skipped), PR URL, and a summary excerpt. Status is always `COMMENT` on GitHub — the bot never auto-approves or auto-requests-changes (see `2026-04-21-github-pr-review-skill-design.md` §Goals).
- **Cancel button**: present on all three workflows' status messages for consistency. Cancellation UX text is workflow-specific (Ask is a silent "已取消"; PR Review explains that already-posted comments are preserved on the PR).
- **Failure**:
  - Issue: retry button on first failure (current behaviour).
  - Ask: plain failure line, no retry.
  - PR Review: plain failure line, no retry (avoid double-writing GitHub).

## Testing

- **Per-workflow unit tests**: `app/workflow/{issue,ask,pr_review}_test.go` cover trigger parsing, wizard transitions, `BuildJob` field population, and `HandleResult` behaviour using mocked `SlackPort`, mocked `IssueCreator`, mocked GitHub client.
- **Dispatcher tests**: parse `@bot ask <q>`, `@bot ASK <q>` (case-insensitive), `@bot review <URL>`, `@bot review <https://github.com/...>` (Slack-wrapped), `@bot review github.com/...` (partial, rejected), `@bot foo/bar`, `@bot foo/bar@dev`, `@bot` (empty), `@bot unknownverb` (→ D-selector with warning) → assert routing and response.
- **D-selector tests**: button click on each of the three buttons → assert correct workflow's `Trigger` is invoked with synthetic event; selector message is updated with check mark.
- **PR Review URL validator tests**: fixture HTTP server returning 200/404/403/connection-error → assert friendly error for each; fork PR fixture → assert `Repo = head.repo.full_name`.
- **Parser failure mode tests**: feed malformed / missing / wrong-marker / empty-required-field / multi-marker `RawOutput` into each workflow's parser → assert expected Slack text.
- **Config alias tests** (`app/config/config_test.go`): `prompt.goal` → `prompt.issue.goal`; explicit nested section overrides alias.
- **Wire schema tests** (`shared/queue/job_test.go`): JSON marshal/unmarshal includes `TaskType` and `WorkflowArgs`; old `JobResult` fields are absent on the wire.
- **Worker `WorkDirProvider` tests** (`worker/pool/*_test.go`): empty dir is created and cleaned for `CloneURL == ""`; existing repo-clone path unchanged.
- **Empty-dir skill spike test** (`worker/pool/*_test.go`, PR 4 must land before PR 5): mount a fake skill into an `EmptyDirProvider` dir, run a dummy agent runner for each provider (claude, codex, gemini, opencode), assert the skill is discoverable.
- **Metrics tests**: assert removed metrics are no longer registered; assert `WorkflowCompletionsTotal{workflow="ask", status="success"}` fires for an Ask success path; similarly for other labels.
- **Refactor period**: `app/bot/result_listener_test.go` and `app/bot/parser_test.go` move to `app/workflow/issue_test.go` with assertions intact (the behaviour they verify is now owned by `IssueWorkflow`).
- **Module boundary**: `test/import_direction_test.go` automatically catches accidental `app/workflow → worker/*` imports.
- **Manual smoke**: after wiring, run all three workflows end-to-end against a staging Slack channel + a fixture GitHub repo.

## Implementation order

Eight PRs, each independently reviewable. The end state is the polymorphic design from §Design; the staging keeps each PR small.

1. **Skeleton** — Create `app/workflow/` package with the `Workflow` interface, `Pending` envelope, `NextStep` types, dispatcher shell, registry, and stub implementations of all three workflows that return "not implemented". Build passes; no behaviour change yet.
2. **`IssueWorkflow` refactor + config alias** — Move logic from `app/bot/workflow.go`, `parser.go`, and the issue-specific portion of `result_listener.go` into `app/workflow/issue.go`. Introduce nested `prompt.issue.*` config with `prompt.goal` alias. Keep public Slack handler entry in `app/bot/` but route through dispatcher → registry → `IssueWorkflow`. All existing tests pass after import-path edits.
3. **`JobResult` cleanup + `ResultListener` thinning + metrics overhaul** — Remove Issue-specific fields from `JobResult`. Make `ResultListener.handleResult` a dispatcher. `IssueWorkflow.HandleResult` owns retry button logic. Remove old `IssueCreatedTotal` / `IssueRejectedTotal` / `IssueRetryTotal`; introduce unified `WorkflowCompletionsTotal{workflow, status}` etc. Behaviour unchanged from a user's perspective.
4. **Worker `WorkDirProvider` + empty-dir skill spike** — Add the abstraction and `EmptyDirProvider`. Wire `executeJob` through `selectProvider`. **Add the skill-mount spike test for every agent runner.** If any fail, implement the chosen fallback (`git init` or HOME-mount) before moving on. Existing repo-clone path verified by tests.
5. **`AskWorkflow`** — New file `app/workflow/ask.go`, wizard, `BuildJob` (with `Skills = nil` by default), `HandleResult`, parser. Add Slack UX (the optional repo button). Add `prompt.ask.*` config. Tests cover both with-repo and without-repo paths, plus the 38K truncate fallback.
6. **`PRReviewWorkflow` + feature flag** — New file `app/workflow/pr_review.go`. URL parser + validator (regex + GitHub API, including fork head detection). Wizard for both A-path and D-path, generalised `OpenTextInputModal`. `BuildJob` populates `Repo`, `Branch`, `WorkflowArgs` (`pr_url`, `pr_number`). Parses three-state `===REVIEW_RESULT===` (POSTED / SKIPPED / ERROR). Add `pr_review.enabled` config flag (default `false`) — dispatcher rejects triggers when disabled. The `github-pr-review` skill and its `agentdock pr-review-helper` subcommand are a separate deliverable tracked by `2026-04-21-github-pr-review-skill-design.md`; this PR merely wires the workflow and trusts the skill to exist in the repo's baked-in set.
7. **D-selector + dispatcher integration** — Wire the three buttons. Confirm `@bot foo/bar` still goes to Issue (legacy compat). Confirm verb routing, case-insensitivity, Slack-URL-strip, and unknown-verb warnings.
8. **End-to-end smoke** — Run all three workflows against staging (PR Review only if its skill is ready; otherwise test with feature flag disabled). Tag and ship.

## Open questions / future work

- **Per-channel default workflow.** If usage shows that a channel almost always wants Q&A, channel config could default `task_type: ask` so users can skip the verb. Not in v1.
- **Per-workflow skill manifest.** Currently every job ships every skill. Ask jobs in particular pay the full skill-payload wire cost despite often not needing any of them. Acceptable for v1 (skills are small text files); if the skill set grows large or Ask volume dominates, add a `Workflow.Skills() []string` filter so each job only ships what it needs.
- **`github-pr-review` skill package.** Tracked as a separate deliverable: baked-in skill at `agents/skills/github-pr-review/` plus an `agentdock pr-review-helper` subcommand in `cmd/agentdock/` backed by `shared/prreview/`. Ships as one implementation plan derived from `2026-04-21-github-pr-review-skill-design.md`. PR 6 here ships the workflow behind the feature flag so orchestration can be reviewed independently of skill progress. Once the skill is merged, operators flip `pr_review.enabled: true` in `app.yaml`; no app-side code change needed.
- **Line-level vs summary-only review.** v1 can ship with a stub goal that asks the agent for a summary-only review (safer while the skill is immature). Upgrade to line-level is a pure goal-text change once the skill is stable.
- **`JobResult.FailurePhase`.** For a future iteration of PR Review retry, worker could populate a `FailurePhase` enum (`prepare` / `clone` / `running` / `cleanup`) so the app can safely retry pre-agent failures without risk of double-writing. Adds a wire-level field; revisit after observing real-world failure distribution.
- **Enterprise GitHub.** `pr_review.allowed_hosts: [github.com, github.example.com]` unlocks enterprise URLs; not a priority until an enterprise deployment appears.
- **Smaller / cheaper LLM for Ask.** Q&A latency would benefit from a faster model; out of scope for v1, revisit after adoption.
- **Result UI for very long answers.** Ask answers may exceed Slack message limits. v1 truncates at 38K; v2 could thread-split or attach as a snippet.
