# Repo Re-select Back Button for Slack Triage Flow

**Date:** 2026-04-17
**Status:** Approved
**Issue:** [#82](https://github.com/Ivantseng123/agentdock/issues/82)

## Problem

In the Slack interactive triage flow, after the user picks a repo (`action_id=repo_search` for external search or `repo_select_<N>` for button list), the flow immediately advances to the branch selector and — if needed — the description prompt. There is **no way to correct a mis-clicked repo** without abandoning the current thread and re-triggering `@bot`. Each abandoned flow costs the user thread re-parsing and re-attachment handling.

The user-facing pain is purely about conversational UX, not about AI triage quality. Quoting the issue: the main friction of the triage bot is not AI quality but "對話 UI 不友善".

## Goals

1. When the user has gone through a repo selector, every subsequent selector (branch, description) offers a `← 重新選 repo` button that returns the flow to a fresh repo selector.
2. The back action preserves the existing thread context (Slack messages, attachments resolution, reporter, channel metadata) — re-entering repo selection must not force the user to re-`@bot`.
3. No changes to Redis, job store, or any state beyond the in-memory `pending` map in `internal/bot/workflow.go`. The back action happens strictly before `runTriage` submits a job.
4. When the user triggered via the shortcut `@bot owner/repo[@branch]` or the channel has a single fixed repo (no selector was ever shown), the back button does NOT appear — clicking it would be nonsensical.

## Non-Goals

- Description modal back button. Once the modal opens, Slack's view lifecycle is separate; a back button there would require a different mechanism and is not requested.
- A global "reset" button that works at every step and clears the whole flow (issue's Option C). Scoped out; only the repo re-select flow from the issue.
- Re-selecting branch from the description step independently. If the user wants a different branch, they go back to repo (which re-triggers branch selection) — this keeps the single-axis "back to repo" semantic simple.
- Allowing back after `runTriage` has already submitted the job to the queue. The existing cancel button covers that.

## Design

### 1. Data Model

**`internal/bot/workflow.go`** — add one boolean to `pendingTriage`:

```go
type pendingTriage struct {
    // ... existing fields ...
    RepoWasPicked bool // true once the user has gone through a repo selector
}
```

Set by `HandleSelection` in the `case "repo", "repo_search":` branch immediately after a successful repo pick. Never reset. Governs whether downstream selectors render a back button.

No changes to `Job`, `JobState`, or any persisted struct. This flag lives only in the in-memory `pending` map.

### 2. Slack Client — `PostSelectorWithBack`

**`internal/slack/client.go`** — add a new function that wraps the existing `PostSelector` logic with an optional trailing back button:

```go
// PostSelectorWithBack sends a button selector with an optional trailing back button.
// If backActionID is empty, behaves identically to PostSelector.
func (c *Client) PostSelectorWithBack(
    channelID, prompt, actionPrefix string,
    options []string,
    threadTS string,
    backActionID, backLabel string,
) (string, error)
```

Implementation: reuse the existing `PostSelector` body, but after appending option buttons, if `backActionID != ""` append one more `slack.NewButtonBlockElement(backActionID, backLabel, ...)`. All buttons go in the same `actions` block. Slack's 25-element limit per action block is not at risk (options lists are bounded by channel configs in practice).

`PostSelector` keeps its current signature and body untouched; `PostSelectorWithBack` is a net-new function. Existing callers of `PostSelector` are not modified.

### 3. Workflow — `HandleBackToRepo`

**`internal/bot/workflow.go`** — add:

```go
func (w *Workflow) HandleBackToRepo(channelID, selectorMsgTS string)
```

Behavior, in order:

1. **Lookup & lock.** Acquire `w.mu`, read `pt := w.pending[selectorMsgTS]`. If absent, release and return (a prior path already consumed it — silent no-op, consistent with `HandleSelection`).
2. **Delete old key.** `delete(w.pending, selectorMsgTS)` under the same lock, then unlock.
3. **Resolve channel config.** Same lookup as `HandleSelection` (fallback to `ChannelDefaults` if the channel is not explicitly configured).
4. **Clear carried-over fields.** `pt.SelectedRepo = ""`, `pt.SelectedBranch = ""`, `pt.ExtraDesc = ""`. Keep `RepoWasPicked=true` — the user has now gone through a repo selector twice, back should remain available on the next round.
5. **Post new repo selector FIRST** (via the extracted `postRepoSelector` helper — see §4). If this fails, call `notifyError`, `clearDedup`, and return WITHOUT touching the old message (so the user still sees the original selector state and can retry manually by re-triggering `@bot`).
6. **Freeze old message.** Only after the new selector is posted: `UpdateMessage(channelID, selectorMsgTS, ":leftwards_arrow_with_hook: 已返回 repo 選擇")`.
7. **Register new pending entry.** `w.storePending(newSelectorTS, pt)` starts a fresh 1-minute timeout.

The old selector's 1-minute timeout goroutine will find its key missing and no-op — no extra cleanup needed.

### 4. Helper Extraction — `postRepoSelector`

Factor the repo-selector-posting logic out of `HandleTrigger` into a private method so `HandleBackToRepo` can reuse it:

```go
func (w *Workflow) postRepoSelector(pt *pendingTriage, channelCfg config.ChannelConfig) (string, error)
```

The body mirrors the current `HandleTrigger` branches:
- If `len(repos) > 1` → `pt.Phase = "repo"`, `PostSelector(... "repo_select" ...)`, return TS.
- Else (no channel repos) → `pt.Phase = "repo_search"`, `PostExternalSelector(... "repo_search" ...)`, return TS.

The single-repo case (`len(repos) == 1`) never reaches this helper — it's handled inline in `HandleTrigger` and bypasses selector entirely. `HandleBackToRepo` will never be called in that flow because the gate (§5) ensures no back button is rendered.

`HandleTrigger` is updated to call `postRepoSelector` for the multi-repo / external-search branches; the existing single-repo short-circuit stays.

### 5. Downstream Selectors — Gate on `RepoWasPicked`

**`afterRepoSelected`** (branch selector):

```go
backAction := ""
if pt.RepoWasPicked {
    backAction = "back_to_repo"
}
selectorTS, err := w.slack.PostSelectorWithBack(
    pt.ChannelID,
    fmt.Sprintf(":point_right: Which branch of `%s`?", pt.SelectedRepo),
    "branch_select", branches, pt.ThreadTS,
    backAction, "← 重新選 repo",
)
```

**`showDescriptionPrompt`**:

```go
backAction := ""
if pt.RepoWasPicked {
    backAction = "back_to_repo"
}
selectorTS, err := w.slack.PostSelectorWithBack(
    pt.ChannelID,
    ":memo: 需要補充說明嗎？（補充後可讓分析更精準）",
    "description_action", []string{"補充說明", "跳過"}, pt.ThreadTS,
    backAction, "← 重新選 repo",
)
```

No other call site of `PostSelector` is affected.

### 6. Router Wiring

**`cmd/agentdock/app.go`** — inside the `case slack.InteractionTypeBlockActions:` switch, add one case (ordering doesn't matter — all are mutually exclusive by `ActionID`):

```go
case action.ActionID == "back_to_repo":
    wf.HandleBackToRepo(cb.Channel.ID, selectorTS)
```

### 7. Logging

Follow `internal/logging/GUIDE.md` — component/phase taxonomy, Chinese messages, structured attrs:

- `HandleBackToRepo` entry: `pt.Logger.Info("收到返回 repo 請求", "phase", "接收", "from_selector_ts", selectorMsgTS)`
- Successful re-post: `pt.Logger.Info("已重新顯示 repo 選擇", "phase", "處理中", "new_selector_ts", newSelectorTS)`
- Re-post failure: `pt.Logger.Error("重選 repo 失敗", "phase", "失敗", "error", err)`

## Error Handling

| Scenario | Behavior |
|---|---|
| `pending[selectorTS]` absent (double-click race, timeout-hit-first) | Silent return. Consistent with `HandleSelection`. |
| `postRepoSelector` returns error | `notifyError` + `clearDedup` so user can re-`@bot`. Old selector message is NOT frozen (user still sees the previous step). |
| `UpdateMessage` on old message fails after new selector posted | Log warn, proceed. User has the new selector in hand; a stale-looking old message is acceptable. |
| Channel config changed between trigger and back | Re-read `w.cfg.Channels[pt.ChannelID]` inside `HandleBackToRepo`. Mirrors existing workflow pattern. |
| Shortcut path (`@bot owner/repo[@branch]`) | `RepoWasPicked` stays false; back button never rendered; `HandleBackToRepo` never called. |
| Single-repo channel | Same as above — selector bypassed, flag stays false. |
| Channel with `branch_select: false` | `showDescriptionPrompt` called directly after repo pick; description selector carries back button if `RepoWasPicked=true`. |

## Testing

### Unit — `internal/bot/workflow_test.go` (or new `workflow_back_test.go`)

Using the existing mock Slack client pattern:

- `TestHandleBackToRepo_FromBranchStep` — pending pt in `branch` phase with `RepoWasPicked=true`. Assert: old key deleted, new key present, `SelectedRepo`/`SelectedBranch` cleared, new repo selector posted, old message frozen.
- `TestHandleBackToRepo_FromDescriptionStep` — same with `Phase=description` and `ExtraDesc="existing"`. Assert: `ExtraDesc == ""` after back.
- `TestHandleBackToRepo_PendingNotFound` — empty pending map. Assert: no panic, no Slack calls.
- `TestHandleBackToRepo_PostSelectorFails_NoFreeze` — stub `PostSelector`/`PostExternalSelector` to error. Assert: `UpdateMessage` NOT called, `notifyError` called, `clearDedup` called.
- `TestBranchSelector_HasBackButton_WhenRepoWasPicked` — set `pt.RepoWasPicked=true`, call `afterRepoSelected`. Assert: `PostSelectorWithBack` called with `backActionID="back_to_repo"`.
- `TestBranchSelector_NoBackButton_OnShortcut` — `pt.RepoWasPicked=false`. Assert: `backActionID==""` (or equivalent).
- `TestDescriptionPrompt_BackButtonGate` — both gate variants, same pattern as branch selector tests.

### Unit — `internal/slack/client_test.go`

- `TestPostSelectorWithBack_AppendsBackButton` — inspect the posted blocks payload; last element of the actions block has `action_id == backActionID`.
- `TestPostSelectorWithBack_EmptyBackID_MatchesPostSelector` — payload identical to `PostSelector` when `backActionID==""`.

### Integration / Manual QA

No router-level automated test (existing codebase has none for `cmd/agentdock/app.go` dispatch; not adding one here). PR description includes a Slack manual checklist:

1. Multi-repo channel: `@bot` → search dropdown → pick repo → branch selector shows back button → click back → repo search shows again, state clean.
2. Multi-repo channel: pick repo → pick branch → description prompt shows back button → click back → repo selector again, branch cleared.
3. Single-repo channel: `@bot` → branch selector has NO back button.
4. Shortcut `@bot owner/repo`: branch selector has NO back button.
5. Shortcut `@bot owner/repo@branch`: no selector at all (no regression).
6. Double-click back button: second click silently ignored.
7. Back → wait 1 minute → "已超時" message appears.

## Rollout

Single PR. No config flag — the back button is uniformly beneficial and gated by `RepoWasPicked`, so channels where it shouldn't apply are automatically excluded.

No migration. `pendingTriage` lives in memory; existing in-flight triages unaffected because adding a field defaults to zero.

No version bump beyond the normal release-please patch.
