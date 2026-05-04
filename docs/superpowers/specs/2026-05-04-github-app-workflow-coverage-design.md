# GitHub App Coverage for Repo-Bound Workflow Entrypoints

**Date:** 2026-05-04
**Status:** Draft
**Issue:** TBD
**Builds on:** [`2026-05-02-github-app-auth-design.md`](./2026-05-02-github-app-auth-design.md)

## Objective

Close the remaining gaps between "GitHub App auth exists" and "all repo-bound
workflow entrypoints behave correctly without a PAT when the target repo is
covered by the App installation."

Success means:

1. App-covered repos work end-to-end for `issue`, `ask`, and `pr_review`
   without relying on `secrets["GH_TOKEN"]` or `github.token`.
2. Repos outside the App installation use PAT only as an explicit fallback,
   not as an implicit hidden dependency.
3. Pre-dispatch app-side GitHub calls use the same repo-aware auth decision as
   dispatch/retry.
4. Operators get clear signals when configuration will override or bypass
   app-minted installation tokens.

## Assumptions

1. `origin/main` is the source of truth for current behavior; the local
   checkout may lag and may carry unrelated uncommitted edits.
2. The 2026-05-02 GitHub App work already landed for the core token-source,
   dispatch, retry, and app-internal client paths.
3. The desired end state is **App first, PAT fallback**, not "remove PAT
   support entirely."
4. Worker processes must continue to receive plain `GH_TOKEN` strings only;
   private keys remain app-only.

## Problem

As of `origin/main`, the system is in a partially converged state:

- Core app-side auth construction already uses `githubapp.TokenSource`.
- Dispatch and retry already mint per-job `GH_TOKEN` bundles and support
  cross-installation PAT fallback via `dispatch.ChooseJobSource(...)`.
- Worker-side repo preparation still correctly consumes only the encrypted
  per-job token bundle.

But the remaining repo-bound entrypoints are inconsistent:

1. **PR Review pre-submit validation is not routed through repo-aware auth
   selection.**
   `app/workflow/pr_review.go` validates the PR URL by calling
   `GetPullRequest(...)` before job submission. This path currently depends on
   the app-wide PR client, not the same repo-aware source choice that dispatch
   and retry use.

2. **Interactive app-side repo access can still hit App-only blind spots.**
   `issue` / `ask` branch listing uses `RepoCache.EnsureRepo(...)` before job
   dispatch. Those paths already avoid stale `cfg.Secrets["GH_TOKEN"]`, but
   they still need an explicit per-repo fallback decision when a selected repo
   is outside the App installation.

3. **Operator-facing config signals still imply PAT is required even in pure
   App deployments.**
   Init/config comments and some defaults still reflect the older PAT-first
   mental model.

4. **Worker secret overlay remains a deployment landmine.**
   Any `worker.yaml` value for `secrets.GH_TOKEN` overrides the app-minted job
   token because worker secrets win during merge.

The practical consequence is that "PAT removed from secrets" can look like the
root cause, even when the real issue is a pre-dispatch auth-routing gap or an
entrypoint-level transport timeout.

## Non-Goals

- Redesigning the `githubapp.TokenSource` abstraction.
- Moving GitHub App private keys or JWT signing into worker code.
- Removing PAT fallback support.
- Solving multi-installation routing across multiple orgs.
- Replacing the current encrypted secret transport between app and worker.
- Reworking workflow UX beyond what is required to make auth behavior correct
  and diagnosable.

## Current Commands

Build:

```bash
go test ./workflow ./dispatch ./githubapp
```

From `shared/`:

```bash
go test ./github
```

From `worker/`:

```bash
go test ./config ./pool
```

Repo-wide targeted grep while implementing:

```bash
git grep -n "cfg.GitHub.Token\|cfg.Secrets\\[\"GH_TOKEN\"\\]\|GetPullRequest\|EnsureRepo" origin/main -- app shared worker
```

## Project Structure

- `app/githubapp/`
  App-side GitHub App token source, minting, preflight, accessible-repo cache.
- `app/dispatch/`
  Per-job source choice and encrypted secret bundle construction.
- `app/workflow/`
  Workflow entrypoints that may touch GitHub before job dispatch.
- `shared/github/`
  Repo cache, PR client, issue client, discovery client.
- `worker/`
  Repo preparation and secret overlay behavior on the execution side.
- `docs/superpowers/specs/`
  Design artifacts used to drive issue creation and follow-up plans.

## Code Style

Prefer small auth-routing helpers over introducing another generic framework.
The target shape is:

```go
source, fallback, err := dispatch.ChooseJobSource(cfg.GitHub.Token, tokenSource, repo)
if err != nil {
    return NextStep{Kind: NextStepError, ErrorText: "..."} , nil
}

client := ghclient.NewClient(source.Get)
pr, err := client.GetPullRequest(ctx, owner, repo, number)
```

Key conventions:

- Keep repo-aware auth choice near the workflow entrypoint that knows the repo.
- Reuse existing `ChooseJobSource(...)` semantics rather than creating a second
  fallback policy.
- Use per-call token overrides for `RepoCache.EnsureRepo(...)` where possible
  instead of adding more cache variants.

## Design

### 1. Define the exact surface area

This spec covers **repo-bound app-side GitHub operations**, meaning any app
operation that:

- knows the target `owner/repo`, and
- touches GitHub before worker execution, or
- resolves repo access on behalf of a workflow before dispatch.

In-scope examples:

- PR URL validation in `pr_review`
- app-side branch listing / repo ensure in `issue` and `ask`
- retry-time source selection

Out-of-scope examples:

- GitHub App preflight against installation metadata
- worker-side git fetch after the job already has a minted token

### 2. Unify repo-aware auth choice for pre-dispatch flows

All app-side repo-bound flows must use the same source decision that dispatch
already uses:

- if the App installation covers the repo: use App token source
- else if PAT is configured: use PAT fallback
- else: fail loudly with a repo/install-specific error

No app-side repo-bound path may directly assume that the global app client is
sufficient for every repo.

This is the canonical rule for this repo: **any known-target repo-bound
workflow entrypoint must share the same App-first, PAT-fallback routing
semantics as dispatch.**

### 3. PR Review: fix the pre-submit gap

`app/workflow/pr_review.go` must stop treating PR URL validation as a generic
GitHub call. It is repo-bound and must choose auth based on the PR's base repo.

Implementation direction:

- parse the PR URL first
- run repo-aware source selection for `owner/repo`
- build a short-lived PR client from the selected source
- validate with that client

This keeps behavior aligned with dispatch/retry and avoids a separate fallback
policy just for `pr_review`.

For forked PRs, the auth decision is intentionally split:

- **pre-submit validation** routes against the PR's **base repo**
- **dispatch / worker execution** routes against the PR's **head repo**

These are different repository concepts and must not be collapsed into a
single "PR repo" term.

### 4. Ask / Issue: make branch-listing fallback explicit

`issue` and `ask` already pass `""` into `EnsureRepo(...)` to avoid stale
dispatch-time secrets. That is correct for App-covered repos, but it is not
sufficient for repos outside the installation.

Implementation direction:

- before `EnsureRepo(...)`, resolve whether the selected repo requires PAT
  fallback
- if App-covered: keep per-call token empty and let `RepoCache.tokenFn` use
  App auth
- if PAT fallback required: pass the PAT string as the per-call token override
- if neither App coverage nor PAT exists: fail with a user-safe repo access
  error

This keeps `RepoCache` unchanged at the abstraction level and uses the
per-call token escape hatch that already exists.

Implementation should use a small shared workflow helper that wraps the
existing `dispatch.ChooseJobSource(...)` policy and returns the correct
per-call token override / failure result for repo access. The helper is a
call-site deduplication aid, not a second auth policy abstraction.

This failure is deterministic, not transient. The interactive workflow must
fail loudly as soon as the target repo is known to be unreachable, rather than
letting the user finish the wizard and only failing at submit time.

User-facing Slack wording should be explicit but safe, for example:

`無法存取 repo <owner/repo>：GitHub App 未涵蓋此 repo，且未設定 PAT fallback`

### 5. App-side GitHub transport resilience

The current incident should not be treated as a "10s timeout only" bug. Auth
routing and transport resilience are separate concerns, but the user-facing
failure sits at their intersection, so this spec keeps them in one
implementation round.

This transport work applies to **app-side GitHub REST entrypoints** that run
before or around workflow dispatch, including:

- PR validation / metadata fetch
- issue creation
- repo discovery / installation repo listing
- App preflight metadata fetches

Required hardening:

- replace the current PR-only 10s assumption with transport settings that fit
  real GitHub latency
- classify `context deadline exceeded` / `Client.Timeout exceeded while
  awaiting headers` as transient GitHub unavailability
- make timeout / retry / error-shaping behavior consistent across app-side
  GitHub REST clients where the user or operator would otherwise see divergent
  failure modes for the same underlying outage

Retry classes are explicitly:

- `429`
- `403` only when the body indicates secondary rate limit / abuse detection
- `5xx`
- network / timeout failures

Fail-fast classes are explicitly:

- ordinary `401`
- ordinary permission `403`
- `404`
- ordinary `422`

This scope still excludes worker-side git clone/fetch transport and does not
attempt a repo-wide networking redesign outside app-side GitHub REST paths.

This is an explicit same-round implementation decision: workflow-facing and
non-workflow app-side GitHub REST paths must converge on one transport
resilience policy in this change, rather than fixing `pr_review` alone and
leaving preflight / discovery / installation refresh on a separate model.

The policy primitives should live in `shared/github/`, extending the existing
GitHub HTTP plumbing layer beyond token injection to also cover timeout,
retry classification, and retry-capable client construction. Callers in
`app/githubapp/`, workflow-facing clients, and discovery/preflight paths
consume that shared policy rather than carrying independent retry logic.

The shared policy exposes a small fixed set of named profiles rather than
letting each caller invent its own timing model:

- `interactive`
- `background`
- `preflight`

Each profile defines:

- per-attempt timeout
- retry delays
- max wall time

For the `interactive` profile, transient retries remain invisible to Slack
users. Retry detail belongs in logs and metrics; user-facing workflow text only
surfaces the final success or the final user-safe failure once the wall-time
budget is exhausted.

### 6. Guardrails for worker secret override

`worker.yaml` `secrets.GH_TOKEN` is not inherently invalid, but in App mode it
is dangerous because it overrides app-minted job tokens.

Required guardrail:

- when App mode is enabled, worker preflight must fail if `secrets.GH_TOKEN`
  is set
- in PAT-only deployments, worker preflight may warn but must not fail
- docs must state that this is only acceptable for PAT-only deployments or
  deliberate worker-local override scenarios

This keeps App-mode semantics strict without breaking legitimate PAT-only
deployments.

### 7. Operator-facing config/docs cleanup

Pure App deployment must stop looking incomplete in generated config comments.

Required cleanup:

- `init`, generated config comments, and preflight prompts must stop implying
  `github.token` is always required
- docs must distinguish:
  - App-covered repo path
  - PAT fallback path
  - no-PAT / no-installation failure path

This round does **not** change the legacy config default that auto-merges
`github.token` into `secrets["GH_TOKEN"]`; that compatibility behavior remains
in place and is handled through clearer warnings/docs rather than a semantic
change.

### 8. PR Review fork-specific failure contract

Forked PRs use different repositories for validation and execution:

- validation/auth-to-review-target uses the **PR base repository**
- clone/auth-to-execution-source uses the **PR head repository**

When validation succeeds against the base repo but execution cannot proceed
because the head repo is outside the installation and no PAT fallback exists,
the system must report this explicitly as a **head repo access failure**.

Slack/log messaging must not collapse this into generic GitHub unavailability.
The operator-facing remediation must name both valid fixes:

1. install the App for the head-repo owner
2. configure PAT fallback

### 9. Logging requirements

`pr_review` pre-submit validation must log which auth source was chosen:

- `phase=validation`
- `repo=<base owner/repo>`
- `auth_source=app|pat_fallback`

This is required so operators can diagnose incidents where pre-submit behavior
and dispatch behavior diverge.

## Testing Strategy

Unit/integration coverage should prove auth routing, not just token minting.

### Required tests

1. `pr_review` validation:
   - App-covered repo uses App source
   - repo outside installation + PAT uses PAT fallback
   - repo outside installation + no PAT fails before submit

2. `issue` / `ask` branch-listing path:
   - App-covered repo branch listing succeeds without PAT
   - PAT fallback path succeeds when repo is not App-covered
   - no-PAT fallback path yields explicit repo access error

3. PR client resilience:
   - timeout-class errors map to `GitHub 不可達，請稍後重試`
   - longer timeout is pinned by a focused test

4. App-side non-workflow transport:
   - preflight / installation refresh / repo discovery use the same timeout /
     retry policy family as workflow-facing app-side GitHub REST clients

5. Worker guardrail:
   - App mode + `secrets.GH_TOKEN` set => preflight fail
   - PAT-only mode + `secrets.GH_TOKEN` set => preflight warn

6. Forked `pr_review` failure shape:
   - base repo accessible + head repo inaccessible + no PAT fallback =>
     explicit head-repo access failure

7. Logging:
   - pre-submit `pr_review` validation logs `auth_source=app|pat_fallback`
   - interactive retry activity is observable in logs/metrics, not Slack thread

### Verification commands

From `app/`:

```bash
go test ./workflow ./dispatch ./githubapp
```

From `shared/`:

```bash
go test ./github
```

From `worker/`:

```bash
go test ./config ./pool
```

## Boundaries

### Always do

- Reuse existing `dispatch.ChooseJobSource(...)` semantics.
- Keep private key handling app-only.
- Keep worker token delivery through encrypted per-job secrets.
- Add focused tests for each new repo-aware path.

### Ask first

- Changing worker secret merge precedence.
- Removing PAT fallback support.
- Turning worker `secrets.GH_TOKEN` warning into a hard startup failure.
- Broadening timeout/retry behavior beyond the PR validation path.

### Never do

- Introduce a second, competing App-vs-PAT routing policy.
- Read GitHub App private keys from worker code.
- Mutate `cfg.Secrets` in place per request.
- Hide cross-installation failures behind generic "GitHub error" wording.

## Success Criteria

1. A pure App deployment can run `issue`, `ask`, and `pr_review` against an
   App-covered private repo without PAT.
2. Any repo outside the installation uses PAT only through explicit fallback
   selection, never via accidental legacy dependency.
3. `pr_review` no longer has a unique pre-submit auth-routing path that can
   diverge from dispatch/retry.
4. Worker-side `secrets.GH_TOKEN` override risk is visible to operators before
   a production incident.
5. Generated config/docs no longer imply that PAT is mandatory for App-capable
   deployments.
6. Forked PR failures distinguish base-repo validation success from head-repo
   execution-source access failure.
7. App-side GitHub REST paths no longer split into unrelated timeout/retry
   behaviors between workflow-facing and non-workflow code.

## Open Questions

None at this stage. The remaining work is implementation planning and task
breakdown, not unresolved product or terminology decisions.
