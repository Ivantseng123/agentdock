# Issue Multi-Repo Reference Support — Implementation Plan

> **For agentic workers:** Steps use checkbox (`- [ ]`) syntax for tracking. Implement task-by-task; verify each task's verification block before moving on.

**Goal:** Implement Issue workflow multi-repo reference support per spec — agent sees N read-only ref repos as cross-repo context; produced issue body must contain a `## Related repos` section; ref write violations and critical-unavailable conditions are caught and prevent issue creation.

**Architecture:** Two-PR delivery, mirroring Ask plan structure.
- **PR 1 (backend + Ask path migration, ~120 lines):** `JobResult.RefViolations` schema, metric unification, `runRefGuard` simplification (callback abstraction removed), shared `refExclusionsFor` helper, Ask path metric reporting moved from worker to `result_listener`. Lands without user-facing impact for Issue; Ask behaviour byte-for-byte unchanged.
- **PR 2 (frontend + e2e, ~900 lines):** `issueState` extensions, 4 new ref-flow phases, `BuildJob` `output_rules` injection (3 rules), `createAndPostIssue` 5-step body normalization pipeline (strict guard / sentinel / auto-fill), `triage-issue/SKILL.md` update, full test coverage, Slack e2e + GitHub real issue verification.

**Tech Stack:** Go 1.25, three modules (`shared/`, `app/`, `worker/`), slack-go. Test framework: stdlib `testing`. No new dependencies.

**Spec:** `docs/superpowers/specs/2026-04-29-issue-multi-repo-design.md`

**Sibling reference:** Ask plan `docs/superpowers/plans/2026-04-29-ask-multi-repo.md` (already shipped via PR #220 + #222) — many tasks here mirror that work; cross-reference noted per task.

---

## File Structure

| Path | Module | PR | Responsibility |
|---|---|---|---|
| `shared/queue/job.go` | shared | 1 | `JobResult.RefViolations []string` (omitempty) |
| `shared/queue/job_test.go` | shared | 1 | Round-trip / omitempty regression for `RefViolations` |
| `shared/metrics/metrics.go` | shared | 1 | Drop `AskRefWriteViolationsTotal`; add `RefWriteViolationsTotal{workflow,repo}` |
| `worker/pool/executor.go` | worker | 1 | Drop `RefViolationCallback` type + `askLenientRefViolation` func; rewrite `runRefGuard(refs, logger) []string`; `executeJob` writes violations into `JobResult` |
| `worker/pool/executor_test.go` | worker | 1 | Drop callback-related tests; rewrite three guard cases (detect / clean / empty) |
| `app/workflow/refstate.go` | app | 1 | NEW — `refExclusionsFor(primary string, refs []queue.RefRepo) []string` helper |
| `app/workflow/refstate_test.go` | app | 1 | NEW — three table cases for the helper |
| `app/workflow/ask.go` | app | 1 | `RefExclusions()` body shrinks to one-line `refExclusionsFor` call |
| `app/bot/result_listener.go` | app | 1 | Read `r.RefViolations`, emit `RefWriteViolationsTotal{workflow,repo}` per repo (Ask + Issue paths handled here, no fail-fast for Ask) |
| `app/bot/result_listener_test.go` | app | 1 | Verify metric emission on completed result with `RefViolations`; Ask path does not fail |
| `app/workflow/issue.go` | app | 2 | `issueState` 4 ref fields, `BranchSelectedRepo` / `RefExclusions` methods, 4 ref-flow phase methods, `BuildJob` 3 ref `output_rules` rules, `createAndPostIssue` 5-step pipeline (s1 RefViolations / s2 sentinel / s3 ensureRelatedRepos / regex helper) |
| `app/workflow/issue_test.go` | app | 2 | Phase tests mirroring `ask_test.go`; pipeline tests for s1/s2/s3; regex variants |
| `app/app.go` | app | 2 | `BlockSuggestion` routes `issue_ref` (HandleRefRepoSuggestion) and `issue_ref_branch` (HandleBranchSuggestion); mirrors PR #222's Ask wiring |
| `app/agents/skills/triage-issue/SKILL.md` | app | 2 | NEW "Reference repos" section: read-only contract, `## Related repos` H2 spelling rule, sentinel teaching, role hybrid description |

---

## PR 1 — Backend + Ask Path Migration

### Task 1: `JobResult.RefViolations` schema field

**Files:**
- Modify: `shared/queue/job.go`
- Test: `shared/queue/job_test.go`

**Background:**

Worker emits violations as `[]string` (owner/name list) into the result envelope. App side decides what to do — Ask uses it for metric only, Issue uses it for fail-fast (PR 2). `omitempty` is mandatory so old jobs / non-ref jobs serialize byte-for-byte unchanged.

Spec ref: §4.1, grill Q7.

**Steps:**

- [ ] **1.1 Add `RefViolations` to `JobResult`.**

  In `shared/queue/job.go::JobResult`, append after `OutputTokens`:

  ```go
  // RefViolations lists ref repos (owner/name) where post-execute guard
  // detected agent writes. App side decides how to react — Ask logs metric
  // only; Issue treats non-empty as a fail-fast signal (no GitHub push).
  RefViolations []string `json:"ref_violations,omitempty"`
  ```

- [ ] **1.2 Round-trip test.**

  In `shared/queue/job_test.go`:

  ```go
  func TestJobResult_RefViolations_RoundTrip(t *testing.T) {
      in := JobResult{JobID: "j1", Status: "completed", RefViolations: []string{"foo/bar", "baz/qux"}}
      raw, err := json.Marshal(in)
      if err != nil { t.Fatal(err) }
      var out JobResult
      if err := json.Unmarshal(raw, &out); err != nil { t.Fatal(err) }
      if !reflect.DeepEqual(in.RefViolations, out.RefViolations) {
          t.Fatalf("RefViolations mismatch: in=%v out=%v", in.RefViolations, out.RefViolations)
      }
  }

  func TestJobResult_NoRefViolations_OmitEmpty(t *testing.T) {
      raw, err := json.Marshal(JobResult{JobID: "j1", Status: "completed"})
      if err != nil { t.Fatal(err) }
      if strings.Contains(string(raw), "ref_violations") {
          t.Fatalf("expected ref_violations omitted, got: %s", raw)
      }
  }
  ```

**Verification:**
- [ ] `go test ./shared/queue/...` passes.
- [ ] Existing `JobResult` tests still green (no break).

**Estimated scope:** XS (1 file + test, ~30 LOC).

---

### Task 2: Metric unification

**Files:**
- Modify: `shared/metrics/metrics.go`

**Background:**

PR #220 introduced `AskRefWriteViolationsTotal{repo}` (workflow not in label set). Issue grill Q7 unified metric to `RefWriteViolationsTotal{workflow, repo}` so both workflows report through the same counter. User memory confirms pre-launch state — backwards-incompatible metric rename is acceptable.

Spec ref: §1 condition 6, grill Q7.

**Steps:**

- [ ] **2.1 Drop `AskRefWriteViolationsTotal` declaration + registry.**

  In `shared/metrics/metrics.go` near line 119-126: remove the var block and its `AskRefWriteViolationsTotal,` entry in the registry list at line ~214.

- [ ] **2.2 Add `RefWriteViolationsTotal{workflow,repo}`.**

  In the same file, in the same area:

  ```go
  // RefWriteViolationsTotal counts post-execute guard violations: an
  // agent that wrote into a ref worktree. Labelled by workflow ("ask"
  // or "issue") and repo. Ask path increments without failing the job;
  // Issue path increments and fails (no GitHub push).
  var RefWriteViolationsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
      Namespace: "agentdock",
      Subsystem: "worker",
      Name:      "ref_write_violations_total",
      Help:      "Post-execute guard detected agent writing into ref worktree.",
  }, []string{"workflow", "repo"})
  ```

  Add `RefWriteViolationsTotal,` to the registry list.

**Verification:**
- [ ] `go build ./shared/metrics/...` succeeds.
- [ ] `grep -r AskRefWriteViolationsTotal` returns no hits other than git history.

**Estimated scope:** XS (1 file, ~15 LOC net).

---

### Task 3: Worker `runRefGuard` refactor (callback abstraction removed)

**Files:**
- Modify: `worker/pool/executor.go`
- Test: `worker/pool/executor_test.go`

**Background:**

PR #220's `RefViolationCallback` abstraction was designed for a strict-injection point that grill Q2 moved to app side. With strict logic gone from worker, the callback's only job is "warn log" — the abstraction has nothing to abstract. Q9 ratified the cut. `runRefGuard` becomes inline-log + return `[]string` of violations; `executeJob` writes the slice into `JobResult.RefViolations`.

Note worker is now task-agnostic about ref violations (does not look at `Job.TaskType`); metric emission moves to app side (Task 5).

Spec ref: §4.3, grill Q2 + Q9.

**Steps:**

- [ ] **3.1 Drop the callback abstraction.**

  In `worker/pool/executor.go`, remove:
  - `RefViolationCallback` type declaration (around line 217-223)
  - `askLenientRefViolation` func (around line 225-235)
  - The `metrics.AskRefWriteViolationsTotal.WithLabelValues(...).Inc()` call inside it

- [ ] **3.2 Rewrite `runRefGuard` to return violations.**

  Replace the existing `runRefGuard` (around line 247-266):

  ```go
  // runRefGuard walks successful ref worktrees, runs `git status --porcelain`
  // in each, warn-logs on dirty bits, and returns the owner/name list of
  // violating refs. Caller writes the slice into JobResult.RefViolations;
  // app side decides how to react. `git status` failure is debug-logged and
  // skipped (a corrupt worktree shouldn't escalate; cleanup runs anyway).
  func runRefGuard(refs []queue.RefRepoContext, logger *slog.Logger) []string {
      if len(refs) == 0 {
          return nil
      }
      var violations []string
      for _, ref := range refs {
          out, err := exec.Command("git", "-C", ref.Path, "status", "--porcelain").Output()
          if err != nil {
              logger.Debug("ref guard: git status failed; skipping",
                  "phase", "處理中", "ref", ref.Repo, "error", err)
              continue
          }
          if diff := strings.TrimSpace(string(out)); diff != "" {
              logger.Warn("agent wrote into ref repo",
                  "phase", "處理中", "ref", ref.Repo,
                  "diff_preview", truncateRefDiff(diff))
              violations = append(violations, ref.Repo)
          }
      }
      return violations
  }
  ```

- [ ] **3.3 Wire the slice into `JobResult` from `executeJob`.**

  Around line 200-214 of `executor.go`:

  ```go
  // BEFORE:
  // runRefGuard(refContexts, askLenientRefViolation, logger)
  // return &queue.JobResult{... }

  // AFTER:
  violations := runRefGuard(refContexts, logger)
  return &queue.JobResult{
      JobID:          job.ID,
      Status:         "completed",
      RawOutput:      output,
      RepoPath:       repoPath,
      StartedAt:      startedAt,
      FinishedAt:     time.Now(),
      PrepareSeconds: prepareSeconds,
      RefViolations:  violations,
  }
  ```

- [ ] **3.4 Drop `truncateRefDiff` if it becomes unused — keep otherwise.**

  Verify: `grep truncateRefDiff worker/pool/` after edits. Still referenced inside `runRefGuard`, so keep.

- [ ] **3.5 Rewrite tests.**

  In `worker/pool/executor_test.go`:
  - Delete `TestRunRefGuard_NilCallback_NoOp` (callback is gone).
  - Rewrite `TestRunRefGuard_DetectsViolation` to assert returned slice equals `[]string{"foo/bar"}` and a warn log was emitted (use `slogtest` or capture via `slog.NewTextHandler` to a buffer).
  - Rewrite `TestRunRefGuard_CleanWorktree_NoCallback` → `TestRunRefGuard_CleanWorktree_NoNoise`: assert returned slice is nil and no warn-level log emitted.
  - Rewrite `TestRunRefGuard_EmptyRefs_NoOp`: assert returned slice is nil.

**Verification:**
- [ ] `go test ./worker/pool/...` passes.
- [ ] `grep RefViolationCallback worker/` returns no hits.
- [ ] `grep askLenientRefViolation worker/` returns no hits.
- [ ] Existing `TestExecuteJob_MultiRepo_EndToEnd` still green (now also validates `RefViolations` slice on result).

**Estimated scope:** S (2 files, ~80 LOC net change including test rewrite).

---

### Task 4: Shared `refExclusionsFor` helper

**Files:**
- Create: `app/workflow/refstate.go`
- Create: `app/workflow/refstate_test.go`
- Modify: `app/workflow/ask.go`

**Background:**

Grill Q8 ratified DRY boundary: the only piece of ref-state logic worth extracting between Ask and Issue is the candidate-exclusion calculation (primary + already-picked refs). Phase methods, struct fields, and `BranchSelectedRepo` mirror as duplicates because shared abstraction would force generics or hoisting more painful than the duplication itself.

Spec ref: §4.4, grill Q8.

**Steps:**

- [ ] **4.1 Create `app/workflow/refstate.go`.**

  ```go
  package workflow

  import "github.com/Ivantseng123/agentdock/shared/queue"

  // refExclusionsFor returns repos that should NOT appear as ref candidates:
  // the primary plus any refs already picked. Shared between Ask and Issue
  // workflows; both states call this from their RefExclusions() method.
  func refExclusionsFor(primary string, refs []queue.RefRepo) []string {
      out := make([]string, 0, 1+len(refs))
      if primary != "" {
          out = append(out, primary)
      }
      for _, r := range refs {
          out = append(out, r.Repo)
      }
      return out
  }
  ```

- [ ] **4.2 Create `app/workflow/refstate_test.go` with three table cases.**

  ```go
  package workflow

  import (
      "reflect"
      "testing"

      "github.com/Ivantseng123/agentdock/shared/queue"
  )

  func TestRefExclusionsFor(t *testing.T) {
      cases := []struct {
          name     string
          primary  string
          refs     []queue.RefRepo
          expected []string
      }{
          {"both populated", "foo/bar", []queue.RefRepo{{Repo: "a/b"}, {Repo: "c/d"}}, []string{"foo/bar", "a/b", "c/d"}},
          {"empty primary", "", []queue.RefRepo{{Repo: "a/b"}}, []string{"a/b"}},
          {"empty refs", "foo/bar", nil, []string{"foo/bar"}},
          {"both empty", "", nil, []string{}},
      }
      for _, tc := range cases {
          t.Run(tc.name, func(t *testing.T) {
              got := refExclusionsFor(tc.primary, tc.refs)
              if !reflect.DeepEqual(got, tc.expected) && !(len(got) == 0 && len(tc.expected) == 0) {
                  t.Fatalf("got %v, want %v", got, tc.expected)
              }
          })
      }
  }
  ```

- [ ] **4.3 Inline-call from `askState.RefExclusions()`.**

  In `app/workflow/ask.go`, replace the existing `RefExclusions()` body (around line 80-95) with:

  ```go
  func (s *askState) RefExclusions() []string {
      if s == nil {
          return nil
      }
      return refExclusionsFor(s.SelectedRepo, s.RefRepos)
  }
  ```

  Drop the inline loop and slice construction.

**Verification:**
- [ ] `go test ./app/workflow/...` passes (Ask ref-state tests still green).
- [ ] Helper round-trips three table cases.

**Estimated scope:** XS (3 files, ~50 LOC including test).

---

### Task 5: Ask path metric migration to `result_listener`

**Files:**
- Modify: `app/bot/result_listener.go`
- Test: `app/bot/result_listener_test.go`

**Background:**

Worker no longer emits the metric (Task 3 dropped `askLenientRefViolation`). App side `result_listener` now reads `r.RefViolations` and emits `RefWriteViolationsTotal{workflow, repo}` for each entry. For Ask, this is purely observability — no fail-fast. For Issue (handled later in PR 2 inside `createAndPostIssue`), the same data triggers fail-fast at step s1 of the body normalization pipeline.

Spec ref: §1 condition 6, AC-I13, grill Q7.

**Steps:**

- [ ] **5.1 Identify the dispatch point.**

  In `app/bot/result_listener.go`, locate where a `completed` `JobResult` is dispatched to its workflow (likely a `switch state.Job.TaskType` block or equivalent). Confirm the result envelope (`r *queue.JobResult`) is in scope at that point.

- [ ] **5.2 Add metric emission before dispatch.**

  Once per result, for any non-empty `r.RefViolations`, emit one counter increment per repo:

  ```go
  if len(r.RefViolations) > 0 {
      for _, repo := range r.RefViolations {
          metrics.RefWriteViolationsTotal.WithLabelValues(state.Job.TaskType, repo).Inc()
      }
  }
  ```

  Place this emission **before** the workflow dispatch — it should fire for both Ask (lenient, continues to dispatch) and Issue (strict, dispatches to `createAndPostIssue` which then fail-fasts in PR 2's s1).

- [ ] **5.3 Test Ask path: metric emitted, no fail.**

  In `app/bot/result_listener_test.go`, add:

  ```go
  func TestResultListener_AskRefViolations_EmitsMetricNoFail(t *testing.T) {
      // Setup: Ask job with completed result, RefViolations=["foo/bar"]
      // Assert: counter ref_write_violations_total{workflow="ask",repo="foo/bar"} == 1
      // Assert: workflow.HandleCompletedResult was called (Ask still dispatches)
  }
  ```

  Use the existing fakes in `result_listener_test.go` for the workflow/metric assertions.

- [ ] **5.4 Test Issue path: metric emitted, dispatch reaches createAndPostIssue.**

  Same shape, `state.Job.TaskType == "issue"`. Issue's actual fail-fast is tested in PR 2's Task 10; this test only verifies the metric fires before dispatch.

**Verification:**
- [ ] `go test ./app/bot/...` passes.
- [ ] Manually confirm metric label values via test (`workflow="ask"` not `workflow="askLenient"` etc.).

**Estimated scope:** S (2 files, ~60 LOC including tests).

---

### Checkpoint: PR 1 Ready

- [ ] `go test ./...` passes for `shared/`, `worker/`, `app/`.
- [ ] `go build ./cmd/agentdock/` succeeds.
- [ ] `test/import_direction_test.go` passes (no module-boundary violation).
- [ ] `grep -r AskRefWriteViolationsTotal\|RefViolationCallback\|askLenientRefViolation .` returns 0 hits in code (only git history).
- [ ] Ask flow regression: tests in `app/workflow/ask_test.go` for ref-violation paths still green.
- [ ] Manual sanity: send an Ask job with `RefRepos`, confirm `ref_write_violations_total{workflow="ask"}` increments on violation.
- [ ] Open PR 1; await human review + CI green before starting PR 2.

---

## PR 2 — Issue Frontend + e2e

### Task 6: `issueState` extensions + helper methods

**Files:**
- Modify: `app/workflow/issue.go`

**Background:**

`issueState` mirrors `askState` for the four ref fields and the two interface methods. The phase methods themselves are Task 7. This task is the type / state / interface plumbing only.

Spec ref: §4.4, grill Q8.

**Steps:**

- [ ] **6.1 Extend `issueState` with 4 ref fields.**

  In `app/workflow/issue.go::issueState` (around line 30-35), append:

  ```go
  // Multi-repo (ref) state. Mirrors askState's same fields exactly — see
  // ask.go for full doc. AddRefs is the user's yes/no on the decide prompt;
  // RefRepos accumulates as each ref is picked (Branch is filled in the per-
  // ref branch loop). RefBranchIdx steps the per-ref branch picker forward.
  // BranchTargetRepo is the transient "which repo are we asking branches
  // for right now" — set before each branch select phase (primary OR ref).
  AddRefs          bool
  RefRepos         []queue.RefRepo
  RefBranchIdx     int
  BranchTargetRepo string
  ```

- [ ] **6.2 Update `BranchSelectedRepo` to read `BranchTargetRepo`.**

  Replace the existing one-liner:

  ```go
  func (s *issueState) BranchSelectedRepo() string {
      if s == nil {
          return ""
      }
      return s.BranchTargetRepo
  }
  ```

  Note: existing primary-only paths set `BranchTargetRepo = SelectedRepo` (Task 7 wires this).

- [ ] **6.3 Add `RefExclusions()`.**

  ```go
  // RefExclusions satisfies workflow.RefExclusionReader. Mirrors askState.
  func (s *issueState) RefExclusions() []string {
      if s == nil {
          return nil
      }
      return refExclusionsFor(s.SelectedRepo, s.RefRepos)
  }
  ```

**Verification:**
- [ ] `go build ./app/workflow/...` succeeds.
- [ ] `go test ./app/workflow/...` still green (issueState tests pass; new methods covered in Task 12).

**Estimated scope:** S (1 file, ~30 LOC).

---

### Task 7: 4 ref-flow phase methods + state machine wiring

**Files:**
- Modify: `app/workflow/issue.go`

**Background:**

This is the bulk of PR 2. Each method mirrors the Ask equivalent in shape — only string differences (phase names, prompt text "建 issue" vs "問問題") plus the state-machine transitions hooked into `issue.go`'s existing `Selection` switch. Spec §4.4 explicitly: "phase methods directly mirror; only phase names + prompt text differ".

The state machine entry into the ref flow happens after primary branch is picked (or skipped). The `maybeAskRefStep` gate decides whether to enter or skip based on candidate-pool size (mirrors Ask AC-12 zero-candidate skip).

Spec ref: §4.4, grill Q8 (mirror, not abstract).

**Steps:**

- [ ] **7.1 Add `maybeAskRefStep`.**

  Mirror `app/workflow/ask.go::maybeAskRefStep`. Drop one in `issue.go`:

  ```go
  func (w *IssueWorkflow) maybeAskRefStep(p *Pending) NextStep {
      list, useExternalSearch := w.refCandidates(p)
      if !useExternalSearch && len(list) == 0 {
          return w.descriptionPromptStep(p)  // Issue's post-branch step
      }
      p.Phase = "issue_ref_decide"
      return NextStep{
          Kind: NextStepSelector,
          Selector: &SelectorSpec{
              Prompt:   ":books: 加入參考 repo 嗎？(唯讀脈絡)",
              ActionID: "issue_ref_decide",
              Options: []SelectorOption{
                  {Label: "加入", Value: "add"},
                  {Label: "不用", Value: "skip"},
              },
          },
          Pending: p,
      }
  }
  ```

  Note: `descriptionPromptStep` is Issue's post-branch step. Replaces Ask's `priorAnswerOrDescriptionStep` (Issue has no prior-answer flow). Verify that name in `issue.go` before stamping.

- [ ] **7.2 Add `refCandidates`.**

  Direct mirror — same code as `ask.go::refCandidates`, just the closure receiver changes. The candidate filter logic is workflow-agnostic.

- [ ] **7.3 Add `refPickStep` / `refContinueStep` / `nextRefBranchStep`.**

  Each is a near-byte-for-byte port of the Ask version with `ask_ref_*` action IDs renamed to `issue_ref_*`. The branch-select fallback logic (skip when `branch_select` disabled, auto-fill when ≤ 1 branch) is identical to Ask's primary-branch handling.

- [ ] **7.4 Wire `Selection` switch.**

  In `issue.go::Selection`, add cases for the four new actions. The shape mirrors `ask.go`'s newer cases (PR #222):

  ```go
  case "issue_ref_decide":
      if value == "skip" {
          st.AddRefs = false
          return w.descriptionPromptStep(p), nil
      }
      st.AddRefs = true
      return w.refPickStep(p), nil

  case "issue_ref_pick":
      if value == "back_to_decide" || value == "issue_ref_back" || value == "← 不加 ref" {
          st.AddRefs = false
          return w.descriptionPromptStep(p), nil
      }
      st.RefRepos = append(st.RefRepos, queue.RefRepo{
          Repo:     value,
          CloneURL: cleanCloneURL(value),
      })
      st.RefBranchIdx = len(st.RefRepos) - 1
      return w.nextRefBranchStep(p), nil

  case "issue_ref_continue":
      switch value {
      case "more":
          return w.refPickStep(p), nil
      case "done":
          return w.descriptionPromptStep(p), nil
      default:
          return NextStep{Kind: NextStepError, ErrorText: fmt.Sprintf(":x: unexpected ref_continue value: %q", value)}, nil
      }

  case "issue_ref_branch":
      if value == "取消" {
          return NextStep{Kind: NextStepCancel}, nil
      }
      st.RefRepos[st.RefBranchIdx].Branch = value
      return w.refContinueStep(p), nil
  ```

- [ ] **7.5 Hook `maybeAskRefStep` into the post-primary-branch path.**

  Wherever Issue's existing flow currently calls `descriptionPromptStep(p)` after primary repo+branch settle (search `issue.go::Selection` for `branch_select` and `descriptionPromptStep`), replace those calls with `w.maybeAskRefStep(p)`. The gate skips back to `descriptionPromptStep` when there are no ref candidates, so behaviour for single-repo channels is preserved (AC-I11 / Ask AC-12 equivalent).

- [ ] **7.6 Set `BranchTargetRepo` before each branch picker.**

  Before posting the primary branch picker, set `st.BranchTargetRepo = st.SelectedRepo`. Inside `nextRefBranchStep`, set `st.BranchTargetRepo = st.RefRepos[st.RefBranchIdx].Repo` before posting. Without this, `BranchSelectedRepo()` returns the wrong repo for the type-ahead branch suggestion handler.

**Verification:**
- [ ] `go build ./app/workflow/...` succeeds.
- [ ] Behaviour-level tests in Task 12.

**Estimated scope:** M (1 file, ~280 LOC).

---

### Task 8: `BuildJob` ref-aware `output_rules` injection

**Files:**
- Modify: `app/workflow/issue.go`

**Background:**

When `RefRepos` is non-empty, three rules are appended to `OutputRules`. Two pair with SKILL.md guidance (Layer 1) for double-defence; one is Issue-only (the `## Related repos` H2 spelling lock).

Spec ref: §4.6 Layer 2, grill Q3 + Q6.

**Steps:**

- [ ] **8.1 Locate `BuildJob` and the existing `OutputRules` source.**

  In `issue.go::BuildJob` (around line ~530-600 — verify exact line), find where `Workflows.Issue.Prompt.OutputRules` is read into a local `outputRules` var.

- [ ] **8.2 Append three rules when `len(st.RefRepos) > 0`.**

  ```go
  outputRules := w.cfg.Workflows.Issue.Prompt.OutputRules
  if len(st.RefRepos) > 0 {
      outputRules = append(outputRules,
          "不可寫入、修改、刪除 <ref_repos> 列出之任何 path 之下的檔案；refs 為唯讀脈絡。",
          "若 <unavailable_refs> 含關鍵 ref，必須在 issue body 中加入 HTML comment：<!-- AGENTDOCK:CRITICAL_REF_UNAVAILABLE --> （位置不限，存在即 fail-fast）；worker 偵測到此 marker 即不送 issue 到 GitHub。不要 best-effort 拼湊內容。",
          "Issue body 必須包含 `## Related repos` 段，heading spelling 為 `## Related repos`（lowercase repos），列出 <ref_repos> 中每個 repo 與其角色（含 primary）。Role 不確定時寫 `reference context`。",
      )
  }
  ```

  Pass `outputRules` into the `Job.PromptContext.OutputRules` field as already done.

- [ ] **8.3 Wire `Job.RefRepos` from `st.RefRepos`.**

  Same place — set `Job.RefRepos = st.RefRepos` in the assembled job. Mirror `ask.go::BuildJob`.

**Verification:**
- [ ] `go build ./app/workflow/...` succeeds.
- [ ] Behaviour test in Task 12 covers AC-I9.

**Estimated scope:** S (1 file, ~20 LOC).

---

### Task 9: `app/app.go` BlockSuggestion routing for ref + ref-branch

**Files:**
- Modify: `app/app.go`

**Background:**

PR #222 added `ask_ref` (handled by `HandleRefRepoSuggestion`) and `ask_ref_branch` (reuses `HandleBranchSuggestion`). Issue mirrors with `issue_ref` and `issue_ref_branch`. Both handlers are workflow-agnostic at this layer — they read state via the `RefExclusionReader` / `BranchStateReader` interfaces, which `issueState` now implements (Task 6).

Spec ref: §4.4.

**Steps:**

- [ ] **9.1 Add cases.**

  In `app/app.go::handleSocketEvent`'s `BlockSuggestion` switch (around line ~580-600 — find the existing `ask_ref` case from PR #222):

  ```go
  case "ask_ref", "issue_ref":
      ackSuggestions("Ref repo 搜尋結果", wf.HandleRefRepoSuggestion(cb.Container.MessageTs, cb.Value))
  case "branch_select", "ask_branch", "ask_ref_branch", "issue_branch", "issue_ref_branch":
      ackSuggestions("Branch 搜尋結果", wf.HandleBranchSuggestion(cb.Container.MessageTs, cb.Value))
  ```

  Note `issue_branch` may also need adding if the existing primary-branch picker for Issue uses a different action_id — verify in `issue.go`. If primary path already uses `branch_select` (the catch-all), only add `issue_ref_branch`.

**Verification:**
- [ ] `go build ./cmd/agentdock/` succeeds.
- [ ] e2e in Task 13 confirms the routing wires up.

**Estimated scope:** XS (1 file, ~5 LOC).

---

### Task 10: `createAndPostIssue` 5-step body normalization pipeline

**Files:**
- Modify: `app/workflow/issue.go`
- Test: `app/workflow/issue_test.go` (deferred to Task 12; this task is impl only)

**Background:**

The pipeline runs inside the existing `createAndPostIssue` function, between `parsed.Body` extraction and the `w.github.CreateIssue` call. Order is fixed (grill Q4): s1 RefViolations → s2 sentinel → existing stripTriageSection → s3 prepend → existing Redact → CreateIssue. Each new step is short; the function grows by ~80 LOC.

Spec ref: §4.7, grill Q1/Q3/Q4/Q5/Q6.

**Steps:**

- [ ] **10.1 Add the regex helper for `## Related repos` detection.**

  At package level in `issue.go`:

  ```go
  // relatedReposHeadingRE matches H1-H4 markdown headings whose text starts
  // "Related rep…" (singular, plural, or Repositories), case-insensitive.
  // Spec §4.7 / grill Q3 — loose enough to cover normal LLM variation,
  // strict enough to refuse non-heading mentions (bold inline / Chinese).
  var relatedReposHeadingRE = regexp.MustCompile(`(?im)^#{1,4}\s+related\s+rep`)

  func hasRelatedReposSection(body string) bool {
      return relatedReposHeadingRE.MatchString(body)
  }
  ```

- [ ] **10.2 Add the sentinel constant.**

  ```go
  // criticalSentinel is the HTML-comment marker the agent emits when it
  // judges that a critical ref is unavailable and the issue should not
  // be created. Detection is plain Contains; format is fixed (no repo
  // name) — the worker reads UnavailableRefs for the actual list.
  // Spec §4.7 / grill Q6.
  const criticalSentinel = "<!-- AGENTDOCK:CRITICAL_REF_UNAVAILABLE -->"
  ```

- [ ] **10.3 Add `prependRelatedRepos` helper.**

  ```go
  func prependRelatedRepos(body string, job *queue.Job) string {
      var sb strings.Builder
      sb.WriteString("## Related repos\n\n")
      sb.WriteString(formatRefLine(job.Repo, job.Branch, "primary（issue 開立目標）"))
      for _, ref := range job.RefRepos {
          sb.WriteString(formatRefLine(ref.Repo, ref.Branch, "reference context"))
      }
      sb.WriteString("\n---\n\n")
      sb.WriteString(body)
      return sb.String()
  }

  func formatRefLine(repo, branch, role string) string {
      if branch == "" {
          return fmt.Sprintf("- `%s` — %s\n", repo, role)
      }
      return fmt.Sprintf("- `%s@%s` — %s\n", repo, branch, role)
  }
  ```

- [ ] **10.4 Insert the 5-step pipeline into `createAndPostIssue`.**

  In `issue.go::createAndPostIssue`, immediately after `body := parsed.Body` (currently around line 390):

  ```go
  // [s1] strict guard: ref violations from worker → fail, no push.
  if len(r.RefViolations) > 0 {
      msg := fmt.Sprintf(
          ":no_entry: 無法建立 issue：agent 違規寫入 ref repo `%s`，job 結果不可信。請重 trigger。",
          strings.Join(r.RefViolations, ", "))
      w.updateStatus(job, msg)
      metrics.WorkflowCompletionsTotal.WithLabelValues("issue", "rejected_ref_violation").Inc()
      return nil
  }

  // [s2] critical sentinel: agent flagged unrecoverable critical-ref
  // missing context → fail, no push, list UnavailableRefs in banner.
  if strings.Contains(body, criticalSentinel) {
      repoList := strings.Join(job.PromptContext.UnavailableRefs, ", ")
      msg := fmt.Sprintf(
          ":no_entry: 無法建立 issue：以下 ref repo 不可達，agent 判定關鍵脈絡缺失\n- %s\n\n請確認 worker GH_TOKEN 對這些 repo 有讀權後重 trigger。",
          repoList)
      w.updateStatus(job, msg)
      metrics.WorkflowCompletionsTotal.WithLabelValues("issue", "rejected_critical_ref").Inc()
      return nil
  }

  // [s4 — existing] degraded triage strip (already in code below).

  // ... existing degraded-mode stripTriageSection block stays put ...

  // [s3] auto-fill `## Related repos` if agent didn't write it.
  // Runs after stripTriageSection so degraded mode can't strip the prepend.
  if len(job.RefRepos) > 0 && !hasRelatedReposSection(body) {
      body = prependRelatedRepos(body, job)
  }

  // [s5 — existing] Redact secrets — runs after prepend so worker-added
  // content is also redacted (defense in depth).

  // ... existing Redact / CreateIssue calls stay put ...
  ```

  Verify the order in the rendered diff matches s1 → s2 → s4 → s3 → s5 → s6. The guard for s3 is **`len(job.RefRepos) > 0`** — not `RefRepos != nil` — to handle empty-but-non-nil slices safely.

- [ ] **10.5 Add the `regexp` and (if missing) `strings` imports to `issue.go`.**

**Verification:**
- [ ] `go build ./app/workflow/...` succeeds.
- [ ] Tests in Task 12.

**Estimated scope:** M (1 file + helpers, ~120 LOC).

---

### Task 11: `triage-issue/SKILL.md` update

**Files:**
- Modify: `app/agents/skills/triage-issue/SKILL.md`

**Background:**

SKILL.md is Layer 1 (soft guidance). Three rule blocks added under a new "Reference repos" section: read-only contract (mirrors ask-assistant), `## Related repos` H2 spelling rule with role-hybrid description, critical-unavailable sentinel teaching. Output_rules (Task 8) is Layer 2 hard constraint; SKILL is the agent's first read.

Spec ref: §4.6 Layer 1, grill Q3/Q5/Q6.

**Steps:**

- [ ] **11.1 Append a "Reference repos" section** in the appropriate SKILL.md location.

  Use the spec §4.6 Layer 1 verbatim block as the source of truth (already finalized during grill). Three sub-blocks:
  1. Read-only contract: CAN grep/read; CANNOT write/commit/edit/mv/rm; absolute paths from `<ref_repos>`; citation format.
  2. `## Related repos` is hard requirement: H2 spelling exact (`## Related repos`, lowercase repos); per-entry format `- `<owner/name>@<branch>` — <role>``; role hybrid (agent picks if confident, `reference context` if not).
  3. Critical-unavailable sentinel: when `<unavailable_refs>` contains a critical repo, insert `<!-- AGENTDOCK:CRITICAL_REF_UNAVAILABLE -->` (anywhere in body); do not best-effort patchwork. Non-critical → mark role as `unavailable` in `## Related repos`.

- [ ] **11.2 Cross-check section numbering in `triage-issue/SKILL.md`.**

  Where this section lands depends on existing structure (probably after action-boundaries / before output-format sections). Match the placement style used in `ask-assistant/SKILL.md`'s "Reference repos" subsection (which is in the action-boundaries block).

**Verification:**
- [ ] Spec §4.6 Layer 1 instructions appear verbatim in the file.
- [ ] No conflict with existing SKILL guidance (e.g. don't accidentally contradict an earlier "you may modify any file" rule — search first).
- [ ] e2e (Task 13) confirms agent honours the rules.

**Estimated scope:** S (1 markdown file, ~80 LOC).

---

### Task 12: Tests — phase flow + pipeline coverage

**Files:**
- Test: `app/workflow/issue_test.go`

**Background:**

Direct port of `ask_test.go`'s ref-flow test suite, plus pipeline-specific cases for the s1/s2/s3 normalization. Split into two halves to keep each commit reviewable.

Spec ref: §6, AC-I1 through AC-I12.

**Steps:**

- [ ] **12.1 Phase-flow tests (mirror ask_test.go).**

  Port these tests, swapping `ask_*` names and prompts to `issue_*`:
  - `TestIssueWorkflow_RefFlow_DecidePromptOffered`
  - `TestIssueWorkflow_RefFlow_ZeroCandidatesSkipsDecide` (AC-I11 zero-candidate)
  - `TestIssueWorkflow_RefFlow_DecideAddRoutesToPick`
  - `TestIssueWorkflow_RefFlow_DecideSkipRoutesToDescription` (Issue's post-skip step is description, not prior-answer)
  - `TestIssueWorkflow_RefFlow_PickFiltersPrimary`
  - `TestIssueWorkflow_RefFlow_PickAccumulatesAndContinues`
  - `TestIssueWorkflow_RefFlow_PickDedupAlreadyPicked`
  - `TestIssueWorkflow_RefFlow_ContinueExhaustedPoolHidesMore`
  - `TestIssueWorkflow_RefFlow_ContinueDoneEntersDescriptionStep`
  - `TestIssueWorkflow_RefFlow_PerRefBranchPicker`
  - `TestIssueWorkflow_BuildJob_WithRefs_PopulatesJobAndRules` (AC-I9 — three rules appended)
  - `TestIssueWorkflow_BuildJob_NoRefs_NoRulesInjected` (regression)
  - `TestIssueState_RefExclusions_PrimaryAndPicked`
  - `TestIssueState_BranchSelectedRepo_FollowsBranchTarget`

- [ ] **12.2 Pipeline-specific tests for `createAndPostIssue`.**

  Use a fake `IssueCreator` to capture `CreateIssue(...)` arguments (or assert it isn't called). Cases:

  - `TestCreateAndPostIssue_S1_RefViolations_FailsAndDoesNotPush` (AC-I7) — JobResult with `RefViolations: ["foo/bar"]`, assert `IssueCreator.CreateIssue` not called, `WorkflowCompletionsTotal{outcome="rejected_ref_violation"}` += 1, Slack banner contains "foo/bar".
  - `TestCreateAndPostIssue_S2_CriticalSentinel_FailsAndDoesNotPush` (AC-I12) — body contains `<!-- AGENTDOCK:CRITICAL_REF_UNAVAILABLE -->`, `Job.PromptContext.UnavailableRefs = ["broken/repo"]`, assert `CreateIssue` not called, banner lists `broken/repo`, metric `outcome="rejected_critical_ref"`.
  - `TestCreateAndPostIssue_S3_AgentWroteRelatedRepos_NotPrepended` (AC-I4) — body already contains `## Related repos`, assert `CreateIssue` called with body unchanged (apart from Redact).
  - `TestCreateAndPostIssue_S3_AgentMissed_PrependsMinimal` (AC-I5) — body without the heading, `Job.RefRepos` non-empty; assert pushed body starts with `## Related repos\n\n- `<primary>@<branch>` — primary（issue 開立目標）\n…`.
  - `TestCreateAndPostIssue_S3_NoRefs_NoOp` (AC-I6) — empty `RefRepos`, body lacks heading; assert no prepend; pushed body equals input.
  - `TestHasRelatedReposSection_RegexVariants` (AC-I10) — table-test asserting regex hits all of: `## Related repos`, `## Related Repos`, `## Related Repositories`, `### Related Repo`, `# RELATED REPOS`; misses: `## 相關 repos`, `**Related repos:**`, plain-text mention in a paragraph.

- [ ] **12.3 Sanity: order of pipeline steps.**

  Add one integration-style test:

  ```go
  func TestCreateAndPostIssue_PipelineOrder_S1BeforeS2BeforeS3(t *testing.T) {
      // 1. RefViolations + sentinel both present → s1 wins (rejected_ref_violation, not rejected_critical_ref).
      // 2. Sentinel present + s4 strip-target absent + Related-repos missing → s2 fires before s3 attempts to prepend (s3 should never run on a fail path).
  }
  ```

**Verification:**
- [ ] `go test ./app/workflow/...` all green.
- [ ] Coverage for AC-I1 through AC-I12 traceable to a test.

**Estimated scope:** L (1 file, ~600 LOC including phase mirror + pipeline cases). If the agent finds this too large in one session, split 12.1 and 12.2 into separate commits.

---

### Task 13: Slack e2e + GitHub real-issue verification

**Files:**
- None (manual procedure; outputs go to PR description)

**Background:**

Final smoke test before review. Run against the `ai_trigger_issue_bot` test channel (per user memory `project_slack_bot_identities.md`). Spec §6 Slack UX manual verification.

**Steps:**

- [ ] **13.1 Build and deploy locally.**

  Build app+worker; run worker against the test channel.

- [ ] **13.2 0-ref regression.**

  Trigger `@bot issue` in a single-repo channel (or skip ref decide via channel config); assert flow is unchanged from main; issue body contains no `## Related repos` (AC-I6).

- [ ] **13.3 1-ref happy path, multi-repo channel.**

  In a channel with 2-3 configured repos: decide-add → pick a ref → continue done → primary branch → ref branch (if applicable) → description → submit. Assert:
  - Permanent thread message rows from ref flow ≤ 2 (AC-I11; selector uses `chat.update`).
  - GitHub issue body has `## Related repos` H2 with primary + 1 ref.

- [ ] **13.4 3-ref full flow.**

  Same channel; pick 3 refs. Assert AC-I11 still ≤ 2 rows; issue body lists 4 entries (1 primary + 3 ref); each row matches `` `<owner/name>@<branch>` — <role> `` where roles are agent-judged (or `reference context` if agent omitted).

- [ ] **13.5 Strict guard: force ref write violation.**

  Use a worker hook or a contrived skill prompt that writes a file under one of the ref worktrees during agent execution. Trigger an issue request. Assert:
  - Issue **not** pushed to GitHub (verify via `gh`).
  - Slack banner: `:no_entry: 無法建立 issue：agent 違規寫入 ref repo \`<repo>\`…`.
  - Metric: `ref_write_violations_total{workflow="issue", repo="<repo>"} > 0`.
  - Metric: `workflow_completions_total{workflow="issue", outcome="rejected_ref_violation"} += 1`.

- [ ] **13.6 Critical sentinel: force ref clone failure.**

  Pick a ref the worker's PAT can't read. Phrase the question so the agent considers the ref critical and emits the sentinel. Assert:
  - Issue **not** pushed.
  - Slack banner lists the unavailable ref's owner/name.
  - Metric: `workflow_completions_total{workflow="issue", outcome="rejected_critical_ref"} += 1`.

- [ ] **13.7 Capture screenshots.**

  Slack thread + GitHub issue body shots; paste into PR 2 description.

**Verification:**
- [ ] All 5 manual flows above complete with expected outcomes.
- [ ] Screenshots in PR description (≥ 4 — happy path, 3-ref, strict fail, sentinel fail).

**Estimated scope:** XS LOC (no code), high wall-clock (~1-2 hours real-time validation).

---

### Checkpoint: PR 2 Ready

- [ ] `go test ./...` green.
- [ ] `test/import_direction_test.go` green.
- [ ] AC-I1 through AC-I13 each traceable to a test or e2e step.
- [ ] PR 2 description has Slack + GitHub screenshots.
- [ ] Open PR 2; await human review.

---

## Risks and Mitigations

| Risk | Impact | Mitigation |
|---|---|---|
| Strict guard false-positive (agent's read-only ops dirty `git status`) | Med — would block legit issue creation | `git status --porcelain` ignores `.git/` internals; `git fetch` / `git log` won't trip it. Verify in T13.5; if observed, narrow to working-tree-only via explicit pathspec. |
| Heading regex misses LLM variant (Chinese, bold inline) | Low — worker prepends a duplicate, slightly ugly issue | Spec §4.7 deliberately accepts the duplicate as "agent format violation"; SKILL.md + output_rules pin the spelling. Track real-world variants post-launch. |
| Sentinel substring false-positive (agent quotes the sentinel in a code block as an example) | Low — would erroneously block issue creation | Sentinel is sufficiently unique (`AGENTDOCK:` prefix + brackets). SKILL.md adds an explicit "do not quote the sentinel" warning. If observed, switch to a nonce-suffixed sentinel. |
| `BranchTargetRepo` not set in some Task 7 path → branch picker shows wrong candidates | Med — bad UX, type-ahead lists wrong branches | Cover in T12 (`TestIssueState_BranchSelectedRepo_FollowsBranchTarget` — variant for primary AND each ref). |
| Ask metric migration (T5) breaks Grafana dashboards | Pre-launch → no live dashboards. | User memory `project_deployment_status.md` confirms pre-launch; dashboard rebuild is post-launch concern. |
| Spec creep — someone adds "auto cross-link in ref repo" mid-implementation | High — violates §3 hard constraint (write only to primary) | Spec §7 "Never do" is explicit; reject mid-flight. |

---

## Open Questions

- T9.1: does Issue's existing primary-branch picker use `branch_select` (catch-all in `app/app.go`) or a workflow-specific action_id? Confirm before stamping the routing change. If specific (`issue_branch`), add it to the case list.
- T11.2: where exactly in `triage-issue/SKILL.md` should "Reference repos" land? Mirror `ask-assistant/SKILL.md` placement (under §5 action boundaries) — confirm during implementation.
- T13.5: best mechanism to forcibly write into a ref worktree from the agent — does the test channel allow custom skill prompts, or do we need a dedicated debug harness?

---

## Decision trail to grill-me Q&A

- Q1 + Q6 → T8.2 (rule 2 wording) + T10.2 (constant) + T10.4 (s2) + T11 (SKILL block 3) + T12.2 (sentinel test) + T13.6 (e2e).
- Q2 → T1 (`JobResult.RefViolations` field) + T3 (worker just reports) + T10 (app decides).
- Q3 → T8.2 (rule 3 wording) + T10.1 (regex helper) + T11 (SKILL block 2) + T12.2 (regex variants).
- Q4 → T10.4 (pipeline order s1→s2→s4→s3→s5→s6).
- Q5 → T10.3 (`reference context` placeholder) + T11 (SKILL block 2 role hybrid).
- Q7 → T1 + T2 + T3 (worker task-agnostic) + T5 (Ask metric path migration) + T13.5 (e2e metric label assertion).
- Q8 → T4 (`refExclusionsFor` shared) + T6 (`issueState` mirror, no struct extraction) + T7 (phase methods mirror, not abstracted).
- Q9 → T3 (`RefViolationCallback` removed).
- Q10 → entire 2-PR structure of this plan.
