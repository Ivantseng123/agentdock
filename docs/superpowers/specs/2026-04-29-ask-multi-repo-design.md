# Ask Workflow: Multi-Repo Reference Support (1 Primary + N Refs)

**Date:** 2026-04-29
**Status:** Design ratified, ready for impl
**Issue:** #216 (Ask scope)
**Parent:** #215 (umbrella, 1 primary + N refs model)
**Sibling, deferred:** #217 (Issue scope — depends on this; Ask post-execute guard 抽成 callback 預留 #217 用 strict 策略)

---

## 1. Premise Check (與 #215 的差異點)

接受 #215 的核心：1 primary + N refs，寫權限只給 primary，job schema 加法式擴充，Ask 先做、Issue 後做。

**對 #215 / #216 提案的兩處關鍵反轉，已在設計討論中收斂：**

1. **refs 移出 primary cwd**（推翻 #215「`.refs/<owner>__<repo>/` 在 cwd 之內」）。改成獨立外部路徑、prompt 帶絕對路徑告知 agent。理由：
   - `.claude/skills` 自動發現本來就只服務 primary，refs 不需要。
   - 完全規避 primary `.gitignore` 對 ref grep 的污染風險（#215 自己標 TBD 的那條），不必開 spike。
   - cleanup / 後置 guard 邏輯不必在巢狀樹上 walk。
2. **Slack ref 多選改為「序列 single-select」**（推翻 #216「`multi_static_select` + 完成按鈕」）。理由：
   - Slack message 上的 `multi_static_select` 沒有 atomic submit，要靠伺服端累積 selection state，脆弱。
   - 序列 single-select 100% 重用既有 `PostSmartSelector` 路徑，零新 Slack adapter code。
   - chat.update 重用同一條 selector message，AC「ref 流程在 thread 永久新增訊息行數 ≤ 2」一樣達標。

**Read-only 強度升格為三層防線**：SKILL.md（軟引導）→ `output_rules` 動態注入（硬約束）→ worker post-execute guard（觀察用，可注入策略給 #217 改 strict）。理由：本 case 是「同一 prompt 內的硬規則」，不能只依賴 SKILL.md 軟引導（user memory「Skill vs output_rules 強度差」）。

## 2. Problem

當前 Ask workflow 只能掛單一 repo。跨 repo 問題（典型：前端報錯 + 後端 schema 變動）只能挑一邊問，agent 看不到對面 codebase 就只能猜。

## 3. Goals / Non-Goals

### Goals

1. Ask 可以在 primary repo 之外掛 N 個 ref repo 作為唯讀脈絡。
2. Worker 在 prepare 階段把 refs sequential 解到外部路徑（與 primary worktree 同層、配對命名），agent 透過 prompt 拿到絕對路徑。
3. Slack thread 不會因為 ref UX 爆訊息行數（N 個 ref 永久新增訊息 ≤ 2 條，靠 chat.update 同一條 selector message 改寫）。
4. 不掛 ref 時行為與現在一致（regression-free）。
5. ref clone 失敗不阻塞 primary（partial success；prompt 帶 `<unavailable_refs>` 通知 agent；agent 必要時自我聲明「資訊不足、無法回答」）。
6. 多層防線阻擋 agent 寫入 ref：SKILL.md → `output_rules` 硬約束 → worker post-execute guard（callback 形式，本 spec 用 lenient 策略，#217 換 strict）。

### Non-Goals (本 spec 不做)

- Issue workflow 多 repo（→ #217）
- PR Review 多 repo（明確砍掉）
- ref repo 數量硬上限（N ≥ 5 info-log，無強制 cap）
- ref 的 cache 共享優化（先量再決定）
- 同 repo 不同 branch 同時當 ref（禁止；要比 branch 在 primary 內 `git diff`）
- 多選 widget（`multi_static_select` / modal） — 改序列 single-select
- per-ref token / 獨立 auth 範疇 — 沿用 worker 級 `GH_TOKEN`，治理仍 channel-level

## 4. Design

### 4.1 Job Schema (`shared/queue/job.go`)

加法式擴充：

```go
type Job struct {
    // ...existing fields unchanged...
    RefRepos []RefRepo `json:"ref_repos,omitempty"`
}

// RefRepo 是 primary 之外的唯讀脈絡 repo。Repo 為 owner/name 形式（與 Job.Repo 對齊），
// CloneURL 是 worker 用的 https URL，Branch 是要 checkout 的 branch（空字串 = 預設 branch）。
type RefRepo struct {
    Repo     string `json:"repo"`
    CloneURL string `json:"clone_url"`
    Branch   string `json:"branch,omitempty"`
}
```

`omitempty` 確保舊 job 序列化形狀不變。worker 收到 `RefRepos == nil` 或 `len == 0` 走原路徑。

`PromptContext` 同步擴充（見 §4.5）。

### 4.2 Workdir Layout (`worker/pool/`)

**refs 不在 primary cwd 內。** 與 primary worktree 同層、配對命名：

```
<repo_cache.dir>/worktrees/
  ├── triage-repo-abc123/                ← primary worktree (cwd, 不變)
  │    ├── .git/                         ← primary 的 worktree git ref
  │    ├── .claude/, CLAUDE.md, ...
  │    └── <primary repo files>
  └── triage-repo-abc123-refs/           ← refs root (NEW)
       ├── frontend__web/                ← ref 1
       │    ├── .git/                    ← 自己的 worktree git ref
       │    └── ...
       └── backend__api/                 ← ref 2
            ├── .git/
            └── ...
```

命名規則：refs root path = primary worktree path + `-refs` 後綴。  
ref 子目錄 = `<owner>__<repo>`（slash → 雙底線；GitHub repo 名稱不允許底線連續，碰撞風險為 0）。

**為什麼這個 layout：**

- **同根目錄治理**：refs root 也在 `<repo_cache.dir>/worktrees/` 下，既有 `RepoCache.PurgeStale` / `agentdock worker --clean-cache` 自動覆蓋。
- **debug 友善**：殘留 directory 看路徑就知道是哪個 job 的 ref（同前綴）。
- **物理隔離 grep 風險**：primary 的 `.gitignore` 不會傳到外部 path，agent CLI / ripgrep 行為與單獨對 ref 跑時一致。
- **cleanup 邏輯對稱**：refs root 整個 `os.RemoveAll`，每個 ref 獨立 worktree 也可走 `RepoProvider.RemoveWorktree` 個別清。

### 4.3 Worker Prepare Flow

`executeJob` 在 primary 解開後（不變）依序：

1. 計算 refs root path = primary worktree path + `-refs`，`MkdirAll` 建立。
2. 對 `job.RefRepos` 中**每一個 ref，sequential**：
   - 解到 `<refs root>/<owner>__<repo>/`。
   - 失敗（auth / 404 / branch 不存在 / 網路 timeout / git 任何錯）→ 收集成 `unavailableRefs []string`，繼續下一個（partial success）。
3. 把成功 prepare 的 ref 列表（`[]RefRepoContext`）與 `unavailableRefs` 一起塞進 `PromptContext`（§4.5）給 prompt builder。
4. 跑 agent。
5. **post-execute guard（callback 形式）**：對每個成功掛上的 ref worktree 跑 `git status --porcelain`，違規透過注入的 `OnViolation` callback 處理。本 spec（Ask）的 callback 只升 metric + warn log，不 fail job（lenient）；#217（Issue）會注入 strict callback。
6. **Cleanup**（reversed order）：先 ref worktrees（`RemoveWorktree`），再 refs root 目錄（`os.RemoveAll`），最後 primary worktree（`provider.Cleanup`）。

#### Sequential 不平行的原因

`RepoCache.EnsureRepo` 有 single global `sync.Mutex`，平行呼叫會在 lock 上排隊，wall-clock 完全沒改善。要平行得先 refactor 成 per-repo lock — 不在本 spec 範圍。

#### prepare_timeout

預設 3min 不動。冷快取單 repo ~10-30s、熱快取 < 5s；N=5 全冷最壞 ~150s 仍在 budget 內。觀察 metric `worker_prepare_seconds` 一段時間，若 N ≥ 5 的 job 平均逼近 80% 上限再回頭加。

#### Cancellation 行為

ctx 中斷期間，`executeJob` 走既有 `classifyResult` → `cancelledResult` 路徑。cleanup 由 `provider.Cleanup` 負責 —  refs root 也是 worktree base 下的目錄，整個 cleanup（reversed order）跑完。**不為 cancellation 做特殊處理**。

#### Auth scope

ref clone 用 worker 級 `GH_TOKEN`（與 primary 同），透過既有 `gitAuthEnv` 注入 — 不在 cmdline / .git/config 留下 token。**不引入 per-ref token**；治理仍 channel-level（channel 配置允許的 repo 才會出現在候選池）。如果 PAT 對某 ref 沒讀權，clone 401 → `unavailableRefs` 路徑。

#### `RepoProvider` 介面變更

`worker/pool/executor.go::RepoProvider` 加新 method：

```go
type RepoProvider interface {
    Prepare(cloneURL, branch, token string) (string, error)
    PrepareAt(cloneURL, branch, token, targetPath string) error  // ← NEW
    RemoveWorktree(worktreePath string) error
    CleanAll() error
    PurgeStale() error
}
```

`PrepareAt` 與 `Prepare` 的差別：caller 指定 worktree 落點，不從 cache 預設 `worktrees/<random>` 取。`RepoCacheAdapter.PrepareAt` 內部走 `EnsureRepo` + `AddWorktree(barePath, branch, targetPath)`。

### 4.4 Slack UX (`app/workflow/ask.go`)

#### Phase 序列

primary repo + branch 選完之後插入 ref 流程，**重用同一條 selector message**透過 `chat.update` 改寫：

```
ask_repo_prompt           → 原有
ask_repo_select           → 原有
ask_branch_select         → 原有 (only if branch_select enabled)

ask_ref_decide            → NEW   "加入參考 repo？" 是 / 否
ask_ref_pick              → NEW   單選 ref repo（mirror primary 來源）
ask_ref_continue          → NEW   "再加一個 ref / 開始問問題"
ask_ref_branch            → NEW   選此 ref 的 branch（per-ref，跳過規則同 primary）

ask_prior_answer_prompt   → 原有
ask_description_prompt    → 原有
```

`ask_ref_pick` 與 `ask_ref_continue` 構成 loop：每選完一個 ref 就詢問是否繼續；user 點「再加一個」→ chat.update 回 `ask_ref_pick`；點「開始問問題」→ 進 `ask_ref_branch` 階段（也是 loop，per-ref）。

**`ask_ref_decide` 前置守衛**：進入此 phase 前先評估候選池大小。若濾掉 primary 後**候選 = 0**，跳過整個 ref 流程，直接走 `ask_prior_answer_prompt` / `ask_description_prompt`，thread 不出現「加入參考 repo？」這條沒意義訊息。

#### `askState` 擴充

```go
type askState struct {
    // ...existing fields unchanged...

    AddRefs          bool                // ask_ref_decide 結果
    RefRepos         []queue.RefRepo     // 累積中：pick 階段填 Repo+CloneURL，branch 階段補 Branch
    RefBranchIdx     int                 // ask_ref_branch 用：目前在問第幾個 ref 的 branch
    BranchTargetRepo string              // primary 階段=SelectedRepo；ref 階段=RefRepos[RefBranchIdx].Repo
}

// BranchSelectedRepo 改讀 BranchTargetRepo（既有介面契約不變）。
func (s *askState) BranchSelectedRepo() string { return s.BranchTargetRepo }
```

`BranchStateReader` 介面**不動**，封裝在 askState 內部。`HandleBranchSuggestion` 不需要懂 phase。

#### Branch UX 行數約束（AC）

具體：N ≥ 3 時，ref 流程在 thread 永久新增的訊息行數 **≤ 2**。

實作策略：
- `ask_ref_decide` 用 `UpdateMessage` / `UpdateMessageWithButton` 把先前的「branch 選擇器」訊息改寫成「加入參考 repo？」。
- 若 user 點「不加」→ 同條訊息再 update 成下一個 phase 的問題。
- 若 user 點「加入」→ 同條訊息 update 成 ref repo 選擇器。每一次 ref pick / continue / per-ref branch 都用 `UpdateMessage` 改寫**同一個 ts**，不發新訊息。
- 整個 ref 流程結束 → 同條訊息再 update 成 `ask_description_prompt`。

換句話說，整個 ref 流程**只佔 1 條訊息位置**（chat.update 不斷改寫同一條）。

#### 不擴 `SelectorSpec`

序列 single-select 100% 重用既有 `PostSmartSelector` 路徑（button / static_select / external_select 自動分流照舊）。**`SelectorSpec` 不加 `MultiSelect`，slack adapter 不動。**

### 4.5 Prompt Context (`shared/queue/job.go`)

`PromptContext` 加：

```go
type PromptContext struct {
    // ...existing fields unchanged...
    RefRepos        []RefRepoContext `json:"ref_repos,omitempty"`
    UnavailableRefs []string         `json:"unavailable_refs,omitempty"`  // owner/name 形式
}

type RefRepoContext struct {
    Repo   string `json:"repo"`              // owner/name
    Branch string `json:"branch,omitempty"`  // 空 = 預設 branch
    Path   string `json:"path"`              // 絕對路徑，e.g., /var/cache/.../triage-repo-abc-refs/frontend__web
}
```

`worker/prompt/builder.go::BuildPrompt` 在 `<issue_context>` 之後 render：

```xml
<ref_repos>
  <ref repo="frontend/web" branch="main" path="/.../triage-repo-abc-refs/frontend__web"/>
  <ref repo="backend/api"  branch="release-2026q2" path="/.../triage-repo-abc-refs/backend__api"/>
</ref_repos>

<unavailable_refs>
  <repo>broken-org/missing-repo</repo>
</unavailable_refs>
```

只在非空時 render。

### 4.6 Skill + Output Rules（三層防線）

#### Layer 1（軟引導）：`app/agents/skills/ask-assistant/SKILL.md`

`§5 Action boundaries` 之下新增「Reference repos」段：

```markdown
**Reference repos (絕對路徑於 `<ref_repos>` 中)**

If the prompt has a `<ref_repos>` block, those directories are mounted at the
absolute paths listed for read-only context. Rules:

- You CAN: grep, read, follow imports, run `git log --oneline` in them.
- You CANNOT: write, commit, edit, mv, rm any file under those paths.
- Refs are physically OUTSIDE your cwd; use the absolute paths exactly as
  listed in `<ref_repos>`. Don't try to compute relative paths to them.

If `<unavailable_refs>` lists a repo:
- Treat it as missing context.
- If the unavailable ref is **critical** to answering this question
  (i.e., the question fundamentally requires that repo's code), you must
  state plainly: "無法取得 X repo 脈絡，這題我答不了 — 請確認 PAT 對該 repo 有讀權後重 trigger"
  and stop. Do not best-effort patchwork an answer from the available refs.
- If the unavailable ref is non-critical, mention it in the answer
  ("以下回答僅基於 Y 與 Z，X repo 無法取得") and continue.
```

#### Layer 2（硬約束）：`output_rules` 動態注入

`app/workflow/ask.go::BuildJob` 在 `RefRepos` 非空時，往 `OutputRules` 動態 append 兩條：

```
不可寫入、修改、刪除 <ref_repos> 列出之任何 path 之下的檔案；refs 為唯讀脈絡。
若 <unavailable_refs> 含關鍵 ref，必須在答案開頭聲明「無法取得 X repo 脈絡，無法回答」並停手；不要 best-effort 拼湊。
```

第二條對應 SKILL.md Layer 1 的 fail-fast 規則 — `output_rules` 是 prompt 末端的硬約束，即使 SKILL.md 被忽略也能擋。

#### Layer 3（觀察）：Worker post-execute guard（callback 形式）

```go
// worker/pool/executor.go (sketch)
type RefViolationCallback func(ref RefRepoContext, diff string, logger *slog.Logger)

// Ask 注入 lenient callback：
func askLenientCallback(ref RefRepoContext, diff string, logger *slog.Logger) {
    logger.Warn("agent wrote into ref repo (lenient: not failing job)",
        "phase", "處理中", "ref", ref.Repo, "diff_preview", truncate(diff, 200))
    metrics.AskRefWriteViolationsTotal.WithLabelValues(ref.Repo).Inc()
}

// Issue (#217) 注入 strict callback：
func issueStrictCallback(ref RefRepoContext, diff string, logger *slog.Logger) error {
    return fmt.Errorf("agent wrote into ref repo %s: %s", ref.Repo, truncate(diff, 200))
}
```

guard 邏輯共用：遍歷 successful refs，跑 `git status --porcelain`，輸出非空 → call callback。callback signature 不返 error 是因為 Ask 不需要中斷；#217 的 strict 版本可以 panic-equivalent 或透過另一支 signature。具體 abstraction 等 PR 1 寫完再 polish — 重點是**guard 偵測邏輯一份、行為策略可注入**。

#### 為什麼 lenient 對 Ask、不 strict

- **無 user-visible harm**：worktree 一定被 cleanup，寫入物理上不存在。
- **Ask best-effort 路線**：沒 retry button、parser fail 走 fallback；post-execute fail 跟整體路線衝突。
- **真正治本在 prompt 端**：Layer 2 的 `output_rules` + Layer 1 的 SKILL.md fail-fast 規則才是治本。Layer 3 是觀察用。
- **#217 不同**：Issue 會建 ticket，違規 ref 引用會造成 cross-repo 誤導，guard 該 strict。

### 4.7 ref repo 候選來源

ref 候選池與 primary 同源，**濾掉 primary**：

| Channel 配置 | primary 用 | ref 候選來源 |
| --- | --- | --- |
| `GetRepos()` 非空 | static button list | 同 list 濾掉 primary |
| `GetRepos()` 為空 | external_select type-ahead | 同 type-ahead，suggestion handler 排除 primary |

**不允許同 repo 不同 branch 重複當 ref**：UI 上選第二次同 repo 時被濾掉（依 ref `Repo` 欄位 dedup）。要比 branch 用 primary 內 `git diff` 解。

濾完候選池 = 0 → `ask_ref_decide` 直接 skip（見 §4.4 phase 前置守衛）。

## 5. Acceptance Criteria

- [ ] **AC-1（Schema）** `Job.RefRepos` / `RefRepo` / `PromptContext.RefRepos` / `UnavailableRefs` 正確 omitempty 序列化；舊 job（無此欄位）解析測試通過。
- [ ] **AC-2（regression）** Job 不帶 `RefRepos` 時，整個 Ask 行為與現在 byte-for-byte 一致（unit + integration）。
- [ ] **AC-3（單 ref）** 帶 1 個 ref，worker workdir 出現 `<primary>-refs/<owner>__<repo>/`，prompt 含 `<ref_repos>` block，agent 可 grep 該路徑。
- [ ] **AC-4（多 ref）** 帶 N=3 ref，AC-3 對所有 ref 成立；prepare 時間線性增長（log 觀察，不卡 SLO）。
- [ ] **AC-5（Slack UX 行數）** N ≥ 3 時，ref 流程在 thread 永久新增訊息行數 ≤ 2（chat.update 同條改寫驗證；截圖記錄）。
- [ ] **AC-6（partial fail）** 故意給一個壞 CloneURL ref，job 不 fail；prompt 含 `<unavailable_refs>`；agent 回覆有「無法取得 X」字樣（透過 SKILL.md + output_rules 引導）。
- [ ] **AC-7（critical-ref fail-fast）** 故意把核心 repo 設成壞 CloneURL，agent 回覆開頭聲明「無法取得 X repo 脈絡，無法回答」，不嘗試拼湊答案（驗 output_rules 第二條硬規則生效）。
- [ ] **AC-8（write guard, lenient）** e2e 故意叫 agent 寫進 ref；worker log 有 `agent wrote into ref repo (lenient...)` warn，metric `ask_ref_write_violations_total` += 1，**job 仍 success**。
- [ ] **AC-9（output_rules 動態注入）** 帶 ref 時 prompt 末端 output_rules 含「不可寫入...」與「critical fail-fast」兩行；不帶 ref 時兩行皆無。
- [ ] **AC-10（候選濾掉 primary）** primary 已選 `foo/bar`，ref 候選不出現 `foo/bar`（static list + external_select 兩種來源都驗）。
- [ ] **AC-11（dedup 同 repo）** 已選 `foo/bar` 當 ref，再選 ref 時 `foo/bar` 不出現。
- [ ] **AC-12（候選為 0 跳過 ref decide）** Channel 配置只允許 1 個 repo，primary 選掉它後，ref 流程整個 skip — thread 不出現「加入參考 repo？」訊息。
- [ ] **AC-13（per-ref branch picker）** 每個 ref 跑與 primary 對稱的 branch 跳過邏輯（`branch_select` 關 / branches ≤ 1 → 各自跳過；否則 type-ahead）。
- [ ] **AC-14（Cancellation cleanup）** ref prepare 進行中 user cancel job → refs root 與 primary worktree 全部 cleanup，無殘留 directory。

## 6. Testing Strategy

### Unit tests

- `shared/queue/job_test.go` — `RefRepos` JSON round-trip；舊 job 解析（無此欄位）。
- `worker/prompt/builder_test.go` — `<ref_repos>`、`<unavailable_refs>` 渲染條件（皆空 → 不 render；有值 → 對應形狀）。
- `worker/pool/workdir_test.go` — fake `RepoProvider.PrepareAt` 模擬成功 / 失敗組合，驗 `unavailableRefs` 累積、cleanup 順序、refs root path 計算。
- `worker/pool/executor_test.go` — post-execute guard callback 被呼叫的條件（fake `git status` output）；lenient callback 不返 error 行為。
- `app/workflow/ask_test.go` — 新 phase 流轉（attach=yes, refs=yes/no, ref pick + continue + per-ref branch）；候選 0 時 skip ref decide；dedup 已選 ref；branch UX phase 切換把 `BranchTargetRepo` 設對。

### Integration tests

- `worker/integration/queue_redis_integration_test.go` — submit 帶 `RefRepos` 的 Ask job，驗 worktree 結構（primary + refs root + ref 子目錄），agent 跑完 cleanup 完整。
- 一個 ref 故意設無效 CloneURL，驗 partial-success：job success、prompt 含 `<unavailable_refs>`。

### Slack UX 手動驗收

開測試 channel 配 3 個 repo，跑 ask flow 三次：(0 ref, 1 ref, 3 ref)。重點：
- 截圖訊息序列，驗 AC-5 永久訊息數量。
- 每次 ref pick / continue / branch 點擊都觀察 selector message ts 是否不變（chat.update 確實 in-place）。
- 故意把候選池逼到 0（primary 選掉唯一 repo），驗 AC-12 ref decide 被 skip。

## 7. Boundaries

### Always do

- Job schema additive，不破壞舊 job。
- 新 phase 的 selector 都用 chat.update 重用既有 selector message ts，不發新訊息。
- ref 失敗不阻塞 primary。
- `RefRepos` 非空時自動往 `output_rules` 注入兩條硬約束（read-only + critical fail-fast）。
- worker post-execute 對每個成功 ref 跑 `git status --porcelain`，違規透過注入的 callback 處理（Ask: lenient warn + metric）。
- ref 候選 mirror primary 來源、濾 primary、dedup 已選 ref；候選 0 跳過 ref decide phase。
- cleanup reversed order：refs root → primary worktree。

### Ask first

- ref 數量 hard cap 決策（spec 不設，但若 prepare metric 顯示 N ≥ 5 job 跑爆 timeout，回頭加）。
- 是否要把 refs root 改 read-only mount（host 權限做不到時更嚴格的方案）。
- 是否要 per-ref token / 獨立 auth 範疇（spec 維持 channel-level、worker 級 PAT；若 SRE 想要更細治理另開 issue）。

### Never do

- 用 `--dangerously-skip-permissions` 之類 flag 放開 host sandbox（user memory「Worker 部署環境不可假設」）。
- 把 ref 寫權限從 prompt 放開（即使使用者要求）。
- 在 primary worktree 內掛 nested git worktree（已被 §4.2 改外部 path 解掉，但實作時要守住別走回頭路）。
- 砍 `output_rules` 那兩條動態注入（即使 user-facing 看不到，那是治本的 prompt-level 防線）。

## 8. Assumptions to Validate (during impl)

| 假設 | 驗法 | 不成立時的退路 |
| --- | --- | --- |
| Sequential clone 在 N=3 時仍能在 `prepare_timeout`（預設 3min）內完成 | 觀察 metric `worker_prepare_seconds` | 加 budget；refactor `RepoCache` 成 per-repo lock 啟用平行 |
| ref 寫入違規率夠低，metric 觀察就夠、不需要 fs-level read-only 強制 | 觀察 `ask_ref_write_violations_total` 一段時間 | host 容器化部署時掛 readonly bind mount；laptop 部署只能靠 prompt |
| 既有 channels 配置足以涵蓋多 repo 場景（type-ahead 路徑也 OK） | 與使用者確認既有 channel repos 配置 | 若需要更精細治理，另開 issue 加 ref-only allow list |

> **不需要 spike 的事項**：
> - `.refs/` 與 `.gitignore` 互動 — refs 已移外部 path（§4.2），完全規避此問題。
> - Slack `multi_static_select` + chat.update 行為 — 改用序列 single-select（§4.4），完全規避。

## 9. Out of Scope (本 spec 不做)

- **#217 Issue workflow 多 repo + body 引用** — 等 #216 land 後接著做，沿用本 spec 的 schema、workdir layout、UX；guard callback 改 strict 策略。
- **PR Review 多 repo** — #215 已決議砍掉。
- **ref repo 共享 cache 優化** — 先量再決定。
- **refs root mount 成 read-only fs** — host 權限做不到，留 v2。
- **多選 widget（modal 或 multi_static_select）** — 序列 single-select 解決 UX 需求，無動機回頭。
- **ref 數量硬上限** — 無，N ≥ 5 info-log 觀察。
- **同 repo 不同 branch 同時當 ref** — 禁止。
- **per-ref token / 獨立 auth scope** — 沿用 worker 級 PAT。

## 10. Implementation Order

**2 顆 PR：**

| PR | 範圍 | 中間態 |
| --- | --- | --- |
| **PR 1 — backend** | `shared/queue/job.go`（schema + PromptContext 擴充）、`worker/prompt/builder.go`（`<ref_repos>` / `<unavailable_refs>` 渲染）、`worker/pool/`（`PrepareAt`、`executeJob` 處理 RefRepos、sequential clone、partial success、refs root cleanup、post-execute guard callback w/ Ask lenient impl）、相關 unit + integration tests | 舊行為 byte-for-byte 不破。worker 收到帶 `RefRepos` 的 job 能正確 prepare、跑 agent、cleanup；但因為 app 還沒能產出這種 job，functionally 等於沒上線（safe to land alone）。 |
| **PR 2 — frontend + e2e** | `app/workflow/ask.go`（askState 擴充、新 phase、`BranchTargetRepo` 切換、BuildJob output_rules 動態注入、ref 候選濾 primary + dedup + 0-候選 skip）、`app/agents/skills/ask-assistant/SKILL.md`（Reference repos + critical fail-fast 段落）、ask_test.go 對新 phase 的覆蓋、Slack 手動 e2e 截圖紀錄到 PR description | 完整 user-facing 流程啟用，AC-1 到 AC-14 全部可驗。 |

**為什麼 2 顆而不是 1 顆**：
- PR 1 是 worker / shared 兩個獨立 module 的 atomic 改動，沒碰 app — 對應 user memory「App/Worker 區分」分界。Review 時只看 backend 行為。
- PR 2 是 app + skill prompt + e2e，全是 user-visible 流程。Review 重心是 UX 與 prompt 行為。
- 中間態乾淨：PR 1 land 後沒有 user-facing 改動；PR 2 land 後 e2e 可跑。

**PR 大小估**：PR 1 ~300 行（含 tests）、PR 2 ~400 行（含 tests + SKILL.md）。都在 review 友善區間。
