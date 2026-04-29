# Issue Workflow: Multi-Repo Reference Support (1 Primary + N Refs)

**Date:** 2026-04-29
**Status:** Design ratified after grill-me iteration, ready for impl
**Issue:** #217 (Issue scope)
**Parent:** #215 (umbrella, 1 primary + N refs model)
**Sibling, shipped:** #216 / PR #220 + PR #222（Ask 已上線；本 spec 大量引用 Ask spec `2026-04-29-ask-multi-repo-design.md`，以下簡稱 *Ask spec*）

---

## 1. Premise Check

接受 *Ask spec* §1 的全部前提：1 primary + N refs、寫權限只給 primary、加法式 schema、`<primary>-refs/<owner>__<repo>/` sibling 布局、序列 single-select UX、SKILL.md → output_rules → post-execute guard 三層防線。

**Issue 對 Ask 的關鍵差異點與 grill 收斂結論**（後三條為 grill-me 過程中發現的 spec hole 反轉）：

1. **Post-execute guard 採 strict 策略**（推翻 Ask 的 lenient）。理由：Issue 的 output 是要 push 到 GitHub 留永久 trace 的 ticket，cost-of-error 比 Ask 答案高一個量級。ref 違規 = prompt 不可信 = body 內容不可信，整 job fail 比建錯 issue 好。
2. **砍掉「LLM 自我判斷 root cause 在 ref」+「警示應改在 X 開立」的設計**（推翻 #217 issue 草案）。理由：「issue 該開哪個 repo」是 routing 決策，由 LLM 拍板會製造 false confidence；不確定時 agent 偏向亂標，比沒標還糟。
3. **Worker 在 push issue 之前自動補 `## Related repos`**（NEW，Ask 沒有對應機制）。理由：「issue body 必須含 ref 引用」這個約束不能依賴 LLM 紀律。
4. **Worker 純回報、App 決策**（grill Q2 反轉）。原 spec 把 strict guard / sentinel 偵測 / auto-fill 三件事都放 worker——但 codebase verify 顯示 `IssueCreator.CreateIssue` 在 app side（`app/workflow/issue.go:410`），worker 不送 issue。Strict 觸發點、sentinel 偵測、auto-fill 全部移到 app `createAndPostIssue`。Worker 只透過新增的 `JobResult.RefViolations []string` 回報事實。
5. **砍 PR #220 的 `RefViolationCallback` 抽象**（grill Q9 反轉）。Q2 把 strict 行為移走後，callback 唯一責任剩 warn-log，已不構成抽象的合理性。`runRefGuard` 簡化為 inline log + return slice，相關 type / func 全砍。Pre-launch 階段，無 production 衝擊。
6. **Metric 遷移為 unified label-based**（grill Q7）。`AskRefWriteViolationsTotal{repo}` 砍，新增 `RefWriteViolationsTotal{workflow, repo}`。**上報點從 worker 搬到 app side `result_listener`**——worker 不認 task semantics。

## 2. Problem

跨 repo bug 的 issue 假裝是 single-repo：前端報錯 issue 開在 frontend repo，但 root cause 在 backend schema 變動。Assignee 拿到 issue 看不到 backend 線索，只能猜或自己重新 trigger。Issue body 不暴露 ref repo 等於白給。

## 3. Goals / Non-Goals

### Goals

1. Issue 可在 primary 之外掛 N 個 ref repo 作為唯讀脈絡（mirror Ask AC-3、AC-4）。
2. 產出的 issue body 必含 `## Related repos` 段，列出每個 ref repo（worker 反推時用 `reference context` placeholder；agent 可在 body 中改寫成更精確的 role 描述，例如「schema 變動疑似關聯」）。
3. ref 寫入違規 → app 不送 issue 到 GitHub，回 Slack 失敗 banner（strict guard，由 app side 觸發）。
4. 不掛 ref 時行為與現在 byte-for-byte 一致（regression-free）。
5. 共用 Ask 已驗證的 schema / workdir / Slack UX / prompt 機制，不為 Issue 重新發明。

### Non-Goals (本 spec 不做)

- LLM 自我判斷「root cause 在哪個 ref」並警示應改開 issue 在 X repo（→ §1 反轉理由）。
- Slack 端讓 user 標 ref 角色的多選 phase（會打掉 AC-I11 訊息行數約束；role 由 agent 在 body 中描述、worker 漏寫時補 `reference context` placeholder）。
- 跨 repo 自動 cross-link（在 ref repo 也開個輕量 issue 互指）— 寫權限約束直接擋。
- 多 primary（一次建 N 個 issue 在不同 repo）— 違反 "1 primary" 模型。
- Issue body 結構化驗證做到 schema 強度（如 yaml frontmatter）— `## Related repos` heading 字面 regex 比對就夠。
- per-ref token / 獨立 auth scope — 沿用 worker 級 PAT。
- ref repo 共享 cache 優化 — 與 Ask 共命運。
- PR Review 多 repo — #215 已決議砍。
- 保留 PR #220 的 `RefViolationCallback` 抽象 — Q9 已決議砍。

## 4. Design

### 4.1 Job Schema 與 JobResult

**Job 端**完全 reuse Ask spec §4.1（已 land：`Job.RefRepos`、`PromptContext.RefRepos`、`PromptContext.UnavailableRefs`）。

**JobResult 端新增**（grill Q7 落實）：

```go
type JobResult struct {
    // ...existing fields unchanged...
    RefViolations []string `json:"ref_violations,omitempty"`  // owner/name list
}
```

`omitempty` → 不掛 ref 的 job 結果序列化形狀完全不變。Worker task-agnostic 寫入；Ask 路徑只用來上 metric（lenient），Issue 路徑用來 fail-fast（strict）。

### 4.2 Workdir Layout

完全 reuse Ask spec §4.2。`<primary>-refs/<owner>__<repo>/` sibling worktree、`prepareRefs` / `cleanupRefs` 已 land 在 `worker/pool/workdir.go`。Issue 路徑零差異。

### 4.3 Worker Prepare Flow

Reuse Ask spec §4.3 的 prepare / cleanup 流程。**唯一差異在 post-execute guard 改寫**（grill Q9 落實）。

#### `runRefGuard` 退化為純回報（callback 抽象砍除）

```go
// worker/pool/executor.go (after refactor)
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

// executeJob (after refactor):
violations := runRefGuard(refContexts, logger)
return &queue.JobResult{
    // ...
    RefViolations: violations,
}
```

砍除：
- `RefViolationCallback` type
- `askLenientRefViolation` func
- `runRefGuard` 既有的 `onViolation` 參數

理由：`worker` 不認 task semantics（user memory「App/Worker 區分」精神延伸）；callback 抽象唯一合理性是 strict 注入點，已挪到 app side（§4.7）。

### 4.4 Slack UX

完全 mirror Ask spec §4.4 的序列 single-select + chat.update 模式。**phase action_id 全部換成 `issue_*` 前綴**：

```
issue_repo_select          → 原有
issue_branch_select        → 原有

issue_ref_decide           → NEW   "加入參考 repo？" 是 / 否
issue_ref_pick             → NEW   單選 ref repo
issue_ref_continue         → NEW   "再加一個 / 開始建 issue"
issue_ref_branch           → NEW   per-ref branch（跳過規則同 primary）

issue_description_prompt   → 原有
```

`issueState` 加四個欄位 mirror `askState`：

```go
type issueState struct {
    // ...existing fields unchanged...

    AddRefs          bool
    RefRepos         []queue.RefRepo
    RefBranchIdx     int
    BranchTargetRepo string
}
```

DRY refactor（grill Q8 落實）：抽 `app/workflow/refstate.go::refExclusionsFor(primary, refs) []string` 共用 helper；其他全部 mirror duplicate（state fields、phase methods、`BuildJob` output_rules append、`BranchSelectedRepo`）。理由：`refExclusionsFor` 是純函數合理 helper；其餘抽出來會引入 hoisting / generics 抽象，比 duplicate 更難維護。

`refExclusionsFor` 簽章：

```go
// app/workflow/refstate.go
package workflow

import "github.com/Ivantseng123/agentdock/shared/queue"

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

兩 state struct 各自實作 `RefExclusions()` 一行 inline call、`BranchSelectedRepo()` 各自實作 `return s.BranchTargetRepo`。

`app/app.go BlockSuggestion` 路由加 `issue_ref` / `issue_ref_branch` cases — 與 PR #222 加入的 `ask_ref` / `ask_ref_branch` 對稱，handler reuse `HandleRefRepoSuggestion` / `HandleBranchSuggestion`（已支援 ref pending）。

`maybeAskRefStep` / `refPickStep` / `refContinueStep` / `nextRefBranchStep` 四個 helper 從 `ask.go` 對應方法直接 mirror，phase 名稱換 `issue_ref_*`、prompt 字串改成 issue 語境（"建 issue" 替 "問問題"），其餘邏輯零差異。

候選 0 跳過、dedup、`BranchTargetRepo` 切換邏輯與 Ask AC-10/AC-11/AC-12/AC-13 完全等價，本 spec 不重列。

### 4.5 Prompt Context

完全 reuse Ask spec §4.5。`<ref_repos>` / `<unavailable_refs>` block 已 land 在 `worker/prompt/builder.go`，Issue 路徑零差異。

### 4.6 Skill + Output Rules（三層防線，Issue 版）

#### Layer 1（軟引導）：`app/agents/skills/triage-issue/SKILL.md`

新增 "Reference repos" 段（內容**結構同 Ask 的 ask-assistant SKILL.md** 的 read-only contract、絕對路徑、citation 格式），但加上 Issue 獨有規則（grill Q3、Q5、Q6 落實）：

```markdown
**Issue body 對 ref 的硬規則**

1. **`## Related repos` 段是 hard requirement**

   When `<ref_repos>` is non-empty, your generated issue body MUST contain
   a `## Related repos` H2 section. Heading spelling must be exactly
   `## Related repos` (lowercase repos, no plural). Format:

   ```
   ## Related repos

   - `frontend/web@main` — primary（issue 開立目標）
   - `backend/api@release-2026q2` — schema 變動疑似關聯
   ```

   Each ref entry follows: `` `<owner/name>@<branch>` — <role> ``. The
   `<role>` field is your judgment based on the question (e.g. "schema
   變動疑似關聯"). If you don't have a confident role, write
   `reference context` instead of guessing.

   If you forget this section, the worker will auto-generate a minimal
   version. Prefer writing your own — you have richer context.

2. **Critical-unavailable sentinel**

   If `<unavailable_refs>` lists a repo that is **critical** to producing
   a meaningful issue (i.e. you fundamentally need its code to write a
   useful body), insert this single-line HTML comment anywhere in the
   body (位置不限):

   ```
   <!-- AGENTDOCK:CRITICAL_REF_UNAVAILABLE -->
   ```

   The worker detects this marker and refuses to push the issue to
   GitHub — the user gets a Slack notification instead. Do not best-
   effort patchwork an issue body when a critical ref is missing.

3. **Non-critical unavailable**

   If the unavailable ref is non-critical, still produce a complete body,
   and mark the unavailable ref's role as `unavailable` in `## Related
   repos`. Do not insert the sentinel.
```

#### Layer 2（硬約束）：`output_rules` 動態注入

`app/workflow/issue.go::BuildJob` 在 `RefRepos` 非空時 append 三條 `output_rules`（前兩條與 Ask 結構相似但 Issue 版指令不同；第三條為 Issue 獨有）：

```
不可寫入、修改、刪除 <ref_repos> 列出之任何 path 之下的檔案；refs 為唯讀脈絡。
若 <unavailable_refs> 含關鍵 ref，必須在 issue body 中加入 HTML comment：<!-- AGENTDOCK:CRITICAL_REF_UNAVAILABLE --> （位置不限，存在即 fail-fast）；worker 偵測到此 marker 即不送 issue 到 GitHub。不要 best-effort 拼湊內容。
Issue body 必須包含 `## Related repos` 段，heading spelling 為 `## Related repos`（lowercase repos），列出 <ref_repos> 中每個 repo 與其角色（含 primary）。Role 不確定時寫 `reference context`。
```

第二、三條與 Layer 1 SKILL.md 對應規則做雙保險——`output_rules` 是 prompt 末端的硬約束，即使 SKILL.md 被忽略也能擋。

#### Layer 3（觀察）：Worker post-execute guard

`worker/pool/executor.go::runRefGuard` 對每個成功 prepare 的 ref worktree 跑 `git status --porcelain`：
- 輸出空 → 不做任何事
- 輸出非空 → warn log（含 ref + diff preview）+ 累積 repo name 到 violations slice
- `git status` 自身失敗 → debug log 跳過

Function 返回 violations slice，由 `executeJob` 寫入 `JobResult.RefViolations` 回 app。**Workflow 語義（Ask lenient warn-only / Issue strict fail-fast）的差異全在 app side 處理**（§4.7）。

不再有 callback 抽象——PR #220 既有的 `RefViolationCallback` type 與 `askLenientRefViolation` 在本 spec 砍除。

### 4.7 App-side Body Normalization Pipeline（Layer 4，Issue 獨有）

**位置**：`app/workflow/issue.go::createAndPostIssue`，在現有 `stripTriageSection` / `Redact` / `CreateIssue` 三步之間加四個新步驟。完整 pipeline 順序（grill Q4 落實）：

```
parsed.Body
  ↓
[s1] check result.RefViolations 非空 → fail Slack, return
  ↓
[s2] detect critical sentinel → fail Slack, return
  ↓
[s4] (existing) stripTriageSection (degraded mode)
  ↓
[s3] hasRelatedReposSection? 否 → prepend
  ↓
[s5] (existing) logging.Redact (secrets)
  ↓
[s6] (existing) w.github.CreateIssue
```

Step 順序的設計理由（grill Q4）：
- **s1 first**：RefViolations 是 worker 客觀回報的 fact，與 body 內容無關。早 fail 早收工。
- **s2 在 s4 之前**：sentinel 在 body 上偵測，必須先於任何改寫。如果 stripTriageSection 跑在前、agent 把 sentinel 寫在 advanced triage section 裡 + degraded 把那段 strip 掉 → sentinel 消失 → push 廢 issue。
- **s3 在 s4 之後**：避免 stripTriageSection 邏輯誤打到 prepend 加進的 `## Related repos` heading。先讓既有 strip 跑完再 prepend。
- **s5 Redact 在 s3 之後**：worker prepend 的內容也過一道 secret redaction（defense in depth）。

#### s1: RefViolations 偵測（strict guard 觸發點）

```go
if len(r.RefViolations) > 0 {
    for _, repo := range r.RefViolations {
        metrics.RefWriteViolationsTotal.WithLabelValues("issue", repo).Inc()
    }
    msg := fmt.Sprintf(
        ":no_entry: 無法建立 issue：agent 違規寫入 ref repo `%s`，job 結果不可信。請重 trigger。",
        strings.Join(r.RefViolations, ", "))
    w.updateStatus(job, msg)
    metrics.WorkflowCompletionsTotal.WithLabelValues("issue", "rejected_ref_violation").Inc()
    return nil
}
```

#### s2: Critical sentinel 偵測（grill Q6 落實）

```go
const criticalSentinel = "<!-- AGENTDOCK:CRITICAL_REF_UNAVAILABLE -->"

if strings.Contains(body, criticalSentinel) {
    repoList := strings.Join(job.PromptContext.UnavailableRefs, ", ")
    msg := fmt.Sprintf(
        ":no_entry: 無法建立 issue：以下 ref repo 不可達，agent 判定關鍵脈絡缺失\n- %s\n\n請確認 worker GH_TOKEN 對這些 repo 有讀權後重 trigger。",
        repoList)
    w.updateStatus(job, msg)
    metrics.WorkflowCompletionsTotal.WithLabelValues("issue", "rejected_critical_ref").Inc()
    return nil
}
```

Sentinel 規格：`<!-- AGENTDOCK:CRITICAL_REF_UNAVAILABLE -->`，固定字串、不帶 repo name、位置不限。Repo 列表從 `Job.PromptContext.UnavailableRefs` 取（事實 of record），sentinel 不負責列舉。Detection = `strings.Contains`，dead simple。

#### s3: `## Related repos` auto-fill（grill Q3、Q5 落實）

```go
var relatedReposHeadingRE = regexp.MustCompile(`(?im)^#{1,4}\s+related\s+rep`)

func hasRelatedReposSection(body string) bool {
    return relatedReposHeadingRE.MatchString(body)
}

func ensureRelatedRepos(body string, job *queue.Job) string {
    if len(job.RefRepos) == 0 {
        return body  // 不掛 ref → 不作為
    }
    if hasRelatedReposSection(body) {
        return body  // agent 已寫 → 尊重 agent 版
    }
    return prependRelatedRepos(body, job)
}

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

Detection regex 設計（grill Q3）：
- `(?im)` — case-insensitive + multi-line
- `^#{1,4}\s+` — H1 到 H4 markdown heading
- `related\s+rep` — 抓 `Related repos` / `Related Repos` / `Related Repositories` / `Related repo`（單複數通吃）
- 故意不抓中文 heading / `**bold**` inline / 純文字無 `#` prefix —— 那些 case 視為「agent 違規格式」，重複 prepend 把 agent 拉回 spec 寫死 spelling

Role 邏輯（grill Q5）：
- agent 寫對 → 用 agent 版（含精確 role 描述）
- agent 漏寫 → worker prepend：primary 標 `primary（issue 開立目標）`、ref 標 `reference context`（誠實 placeholder，不假裝知道 role）

### 4.8 ref repo 候選來源

完全 reuse Ask spec §4.7。同邏輯：channel 配置決定 static button vs external_select；濾 primary；dedup 已選 ref；候選 0 跳過 `issue_ref_decide`。`refExclusionsFor` helper 兩 workflow 共用（§4.4）。

## 5. Acceptance Criteria

繼承 Ask spec AC-1 ~ AC-14（換 issue 路徑），新增 Issue 獨有：

- [ ] **AC-I1（Schema regression）** Job 不帶 `RefRepos` 時，Issue 行為與現在 byte-for-byte 一致；`JobResult.RefViolations` omitempty 序列化無破壞。
- [ ] **AC-I2（單 ref e2e）** 帶 1 ref 走完 Slack flow → worker prepare → agent 出 body → push GitHub。GitHub issue body 含 `## Related repos` 段，列出 primary + ref。
- [ ] **AC-I3（多 ref e2e）** N=3 ref，AC-I2 對所有 ref 成立；issue body `## Related repos` 段列出全部 4 個 repo（1 primary + 3 ref）。
- [ ] **AC-I4（agent 寫了 Related repos）** Mock agent 出帶 `## Related repos` 的 body，worker 不修改、原樣 push。
- [ ] **AC-I5（agent 漏寫 Related repos）** Mock agent 出不含 `## Related repos` 的 body，worker prepend minimal 段（含正確 primary + refs + branches，role 為 `reference context`）。
- [ ] **AC-I6（不掛 ref 時零作為）** Job 無 `RefRepos`，body normalization 完全略過，body byte-for-byte 不變。
- [ ] **AC-I7（strict guard，app side）** e2e 故意叫 agent 寫進 ref，job 走 `worker → JobResult.RefViolations 非空 → app s1 fail-fast → 不 push、不送 IssueCreator → Slack 失敗 banner 列出違規 repo`、metric `ref_write_violations_total{workflow="issue", repo=...}` += 1、`workflow_completions_total{workflow="issue", outcome="rejected_ref_violation"}` += 1。
- [ ] **AC-I8（partial fail，non-critical）** 故意給壞 CloneURL ref 但非 critical，agent 在 `## Related repos` 段把該 ref 標 `unavailable` role，body 仍完整，issue push 成功。
- [ ] **AC-I9（output_rules 三條注入）** Issue 帶 ref 時 prompt 末端 output_rules 含三條規則；不帶 ref 時零注入。
- [ ] **AC-I10（heading 偵測 case-insensitive + 變體）** Body 含 `## Related Repos` / `## Related Repositories` / `### Related repo` / `# RELATED REPOS` 任一變體，worker 不重複 prepend。
- [ ] **AC-I11（Slack UX 行數）** 同 Ask AC-5：N ≥ 3 時 ref 流程在 thread 永久新增訊息行數 ≤ 2。
- [ ] **AC-I12（critical sentinel fail-fast）** Mock agent 在 body 中放 `<!-- AGENTDOCK:CRITICAL_REF_UNAVAILABLE -->`，issue **沒**被 push 到 GitHub、Slack banner 列出 `Job.PromptContext.UnavailableRefs`、metric `workflow_completions_total{workflow="issue", outcome="rejected_critical_ref"}` += 1。
- [ ] **AC-I13（Ask metric 遷移）** `AskRefWriteViolationsTotal` 砍除後，Ask 路徑的違規透過 `result_listener` 上 `RefWriteViolationsTotal{workflow="ask", repo=...}`，行為等價（Ask 不 fail，metric 仍上）。

## 6. Testing Strategy

### Unit tests

- `app/workflow/issue_test.go` — 新 phase 流轉（mirror `ask_test.go`）：`issue_ref_decide` / pick / continue / branch、`BranchTargetRepo` 切換、候選 0 skip、dedup、`BuildJob` 三條 output_rules 注入、`refExclusionsFor` helper 行為。
- `app/workflow/issue_test.go` — `createAndPostIssue` 五步 pipeline 各 case：
  - s1: `result.RefViolations` 非空 → 不呼叫 `IssueCreator.CreateIssue`、Slack banner 內含違規 repo
  - s2: body 含 sentinel → 不呼叫 `CreateIssue`、Slack banner 列 `UnavailableRefs`
  - s3: agent 寫對 / agent 漏寫 / regex 各種變體（H2/H3/單複數/case 變體）
  - s6: 不掛 ref 時 pipeline 完全略過所有新步驟
- `app/workflow/refstate_test.go` — `refExclusionsFor` 三組 case：empty primary、empty refs、both populated。
- `worker/pool/executor_test.go` — `runRefGuard` 改寫後行為：detect-violation（return non-nil + warn log）、clean-worktree（return nil + 無 warn）、empty-refs（return nil）、git-status-fail（debug log + 跳過）。原 callback-related test（`*_NilCallback_NoOp`）刪除。
- `app/bot/result_listener_test.go` — Ask 路徑 RefViolations 上 metric 不 fail；Issue 路徑 RefViolations 觸發 fail path。

### Integration tests

- `worker/integration/queue_redis_integration_test.go` — submit 帶 `RefRepos` 的 Issue job：
  - Happy path：agent 寫對 body，issue push 含 `## Related repos`。
  - Strict guard：fake agent 在 ref 寫檔，job result `RefViolations` 非空、app s1 fail、issue 沒 push、metric 上報。
  - Critical sentinel：fake agent 出帶 sentinel body，app s2 fail、issue 沒 push、metric 上報。
  - Auto-fill：fake agent 出不含 Related repos 的 body，push 到（fake）GitHub 的 body 含 worker prepend 段。

### Slack UX 手動驗收

開測試 channel 配 3 個 repo，跑 issue flow 三次：(0 ref, 1 ref, 3 ref)。重點：
- 截圖訊息序列，驗 AC-I11 永久訊息數量。
- 真開到 GitHub 的 issue 截圖，驗 AC-I2 / AC-I3 body 格式。
- 故意給壞 ref 觸發 AC-I7 strict fail / AC-I12 critical sentinel，驗 Slack banner 文案與 metric。

## 7. Boundaries

### Always do

- Issue 走的所有 schema / workdir / prompt / Slack UX 模式都直接引用 Ask spec，不重發明。
- Worker 完全 task-agnostic：`runRefGuard` 純回報、`JobResult.RefViolations` 由 app 決定怎麼用。
- Strict guard 觸發點、sentinel 偵測、auto-fill 全部在 app `createAndPostIssue` 五步 pipeline 中執行。
- Auto-fill heading regex 雙保險：output_rules 寫死 `## Related repos` spelling、worker 用 loose regex 偵測變體。
- Slack 失敗 banner 點明原因（ref 違規 vs critical-unavailable）：user 知道為什麼 fail。
- ref 候選 mirror primary 來源、濾 primary、dedup；候選 0 跳過 ref decide。
- DRY 邊界：抽 `refExclusionsFor` 共用 helper，其餘 mirror duplicate。

### Ask first

- 是否要把 strict guard 範圍縮小到「`.git/` 之外的 working tree changes」（例如 agent 跑 `git fetch` 修改 `.git/refs/` 不算違規）— 等 PR 1 發出來看實際 false-positive rate 再決定。
- 「reference context」placeholder 文案要不要 i18n / 改成更精準的詞 — 等使用者第一次看到 GitHub 上的成品再 review。
- Sentinel false-positive（agent 在 markdown 範例中包含 sentinel 字串會被誤判）— 預期極低頻，先靠 SKILL.md 警告處理；若 e2e 觀察到再加 escape 機制。

### Never do

- 用 LLM 自我判斷 "root cause 在 X repo" 並建議 routing — §1 已禁。
- 在 ref repo 自動建 cross-link issue — 寫權限只在 primary，硬約束。
- 砍 §4.7 s3（worker auto-fill）退回靠 prompt 紀律 — 那是治本機制。
- 把 §4.7 s1 從 strict 退回 lenient — Issue 的 cost-of-error 不允許。
- 在 push 後再 update issue body — 一次到位，避免 race 與 audit 混亂。
- 復活 `RefViolationCallback` 抽象 — Q9 已決議砍，重啟需要新 issue 評估。
- 用 LLM 標 ref role 為硬約束 — Q5 已決議 hybrid，agent 寫對用 agent 版、漏寫用 placeholder。

## 8. Assumptions to Validate (during impl)

| 假設 | 驗法 | 不成立時的退路 |
| --- | --- | --- |
| Strict guard 不會誤殺合法 case（agent 跑 `git log` / `git fetch` 等讀操作不會在 working tree 留 dirty bit） | `runRefGuard` 既有的 `git status --porcelain` 行為確認；e2e 跑 agent 真的讀 ref，看會不會有 false positive | 縮小判定到 working tree changes，排除 `.git/` 內變動 |
| Worker auto-fill 的 prepend 位置（body 最前）不會打架 issue templates / agent 自己加的 frontmatter | `parsed.Body` 已 codebase verify 是純 markdown 無 frontmatter；mock 各種 body 形狀測；e2e 看 GitHub 上呈現 | 改成 append 到 body 末（assignee 滑下面也看得到，但 routing hint 弱化） |
| LLM 漏寫 `## Related repos` 的頻率夠低，s3 是少見保底而非常態 | 上線後觀察一段：worker 補 vs agent 寫的比例（可加 metric） | 比例倒過來時加強 SKILL.md 範例、考慮在 prompt 直接給格式範本 |
| Heading regex 抓得住 LLM 95% 的變體 | unit test 覆蓋常見變體；e2e 觀察 false-negative 率 | 擴 regex 加中文 / bold inline / 純文字 prefix |
| Sentinel `strings.Contains` 不被 agent 範例內容誤觸發 | unit test mock agent 在 markdown code block 中包含 sentinel 字串；e2e 看是否觀察到 | SKILL.md 警告 agent 「不要在範例 / quote 中複製這個 sentinel」；極端 case 改用更獨特的字串（加 nonce） |

## 9. Out of Scope (本 spec 不做)

- **PR Review 多 repo** — #215 已決議砍。
- **LLM 自我判斷 root-cause routing** — §1、§3、§7 三處皆禁。
- **跨 repo cross-link**（在 ref repo 也開輕量 issue）— 寫權限約束擋掉。
- **多 primary**（一次建 N 個 issue）— 違反模型。
- **ref repo 共享 cache 優化** — 與 Ask 同步推遲。
- **per-ref token / 獨立 auth scope** — 沿用 worker 級 PAT。
- **Issue body schema 強制驗證**（yaml frontmatter 等）— heading regex 比對足矣。
- **同 repo 不同 branch 同時當 ref** — 禁止。
- **Slack 端 user 標 ref role phase** — Q5 已決議砍，role 由 agent 在 body 中描述。
- **保留 `RefViolationCallback` 抽象** — Q9 已決議砍。
- **保留 `AskRefWriteViolationsTotal` metric 名稱** — Q7 已決議遷移到 unified label-based metric。

## 10. Implementation Order

**2 顆 PR**（mirror Ask spec PR 拆分模式）：

| PR | 範圍 | 中間態 |
|---|---|---|
| **PR 1 — backend + Ask 路徑遷移** | (1) `shared/queue/job.go`：`JobResult.RefViolations []string` (omitempty) <br> (2) `shared/metrics/metrics.go`：砍 `AskRefWriteViolationsTotal`、新增 `RefWriteViolationsTotal{workflow, repo}` <br> (3) `worker/pool/executor.go`：砍 `RefViolationCallback` type、`askLenientRefViolation` func；`runRefGuard(refs, logger) []string` 簽章改、inline warn log；`executeJob` 把 violations 寫入 `JobResult` <br> (4) `worker/pool/executor_test.go`：刪除 callback-related test（`*_NilCallback_NoOp`），改寫 detect/clean/empty 三 case <br> (5) `app/workflow/refstate.go`：新增 `refExclusionsFor` helper <br> (6) `app/workflow/ask.go::RefExclusions()` 改 1 行 inline call <br> (7) Ask 路徑 metric 上報點搬到 `app/bot/result_listener.go`（讀 `RefViolations` 上 metric，**不 fail**） | Ask 行為 byte-for-byte 不變（lenient 仍 lenient、metric 仍上、只是上報點搬家）。Issue 沒 user-facing 改動。Worker 已準備好接 Issue ref-aware job，但 app 還沒產出 → safe to land alone. |
| **PR 2 — Issue frontend + body normalization + e2e** | (1) `app/workflow/issue.go`：issueState 4 個 ref 欄位、`RefExclusions` / `BranchSelectedRepo` 各 1 行 method、4 個 ref-flow phase method（mirror `ask.go`）、`BuildJob` 三條 ref output_rules append <br> (2) `app/workflow/issue.go::createAndPostIssue` 五步 pipeline（s1 RefViolations、s2 sentinel、s3 ensureRelatedRepos、既有 stripTriageSection / Redact / CreateIssue 順序調整） <br> (3) `app/app.go`：BlockSuggestion 路由加 `issue_ref` / `issue_ref_branch` cases <br> (4) `app/agents/skills/triage-issue/SKILL.md`：Reference repos + Related repos 段 + sentinel 教學 <br> (5) `app/workflow/issue_test.go` mirror `ask_test.go` ref 那批 + 五步 pipeline 各 case test <br> (6) Slack 手動 e2e + GitHub 真開 issue 截圖到 PR description | 完整 Issue ref e2e 可跑，AC-I1 ~ AC-I13 全部可驗。 |

**為什麼 2 顆而不是 1 顆**：
- PR 1 改動全在 worker / shared / Ask 既有路徑，跟 Issue UX 解耦。Reviewer 焦點：worker 行為 + Ask regression test 仍綠。
- PR 2 改動集中在 app / Issue 路徑與 prompt。Reviewer 焦點：UX + body 格式 + prompt 行為。
- 中間態乾淨：PR 1 land 後沒有 user-facing 改動；PR 2 land 後 e2e 可跑。

**PR 大小估**：PR 1 ~120 行（含 tests）、PR 2 ~900 行（含 tests + SKILL.md）。

**Land 後驗收順序**：
1. PR 1：`go test ./...` 全綠；Ask regression test 對 ref violation 走新 metric 上報路徑通過；`test/import_direction_test.go` 通過。
2. PR 2：`go test ./...` 全綠；Slack 手動 3 ref e2e + strict fail + critical sentinel 各跑一次，截圖到 PR description；真 GitHub 開一個帶 ref 的 issue，截圖 body 格式。
