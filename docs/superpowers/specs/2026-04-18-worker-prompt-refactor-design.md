# Worker Prompt 組裝重構（XML + app 控制 Goal/OutputRules/rules）

- **Issue**: [#61](https://github.com/Ivantseng123/agentdock/issues/61)
- **Parent**: [#54](https://github.com/Ivantseng123/agentdock/issues/54)
- **Siblings**: [#62](https://github.com/Ivantseng123/agentdock/issues/62)（依賴本案）、[#63](https://github.com/Ivantseng123/agentdock/issues/63)（獨立）
- **Date**: 2026-04-18
- **Status**: Design approved, pending plan

## 1. 背景

目前 prompt 組裝由 app 執行：`internal/bot/prompt.go` 的 `BuildPrompt` 接收 `PromptInput`，吐 markdown-like 字串，`internal/bot/workflow.go:429` 把字串塞進 `Job.Prompt`，worker 收到後幾乎原封不動餵給 agent CLI。

這個安排有三個結構問題：

1. **App/Worker 界線糊掉** — 組 prompt 本質是「怎麼跟 LLM 講話」，這是 agent 執行細節，屬於 worker 的決策。放在 app 意味著 app 需要知道 agent 的 prompt 格式，違反 CLAUDE.md 的 landmine：「每次改動必須區分 app vs worker 角色，不可混用」。
2. **格式彈性不足** — 寫死 markdown headers，換格式要改 app；Goal / Output 這種需要 per-flow 自訂的段落也沒有 plumbing。
3. **配置位置錯位** — `cfg.Prompt.ExtraRules` 在頂層，但實際只有 worker 會用（agent 跑在 worker 端），app 層是資料搬運工。

本案把 prompt 組裝徹底搬到 worker 端，改用 XML 格式，並把 Goal / OutputRules / Rules 拆成 app 控制 / worker 控制兩側，讓職責對齊。

## 2. 目標

- Prompt 組裝邏輯完全搬到 `internal/worker/prompt.go`，app 不再 import prompt builder。
- App 傳**結構化 context** 給 worker（thread messages、channel、reporter、branch、extra desc、language、goal、output rules、allow_worker_rules 開關），不再傳 raw prompt 字串。
- Prompt 格式從 markdown-like 改 XML。
- App config 的 `prompt.extra_rules` 移除；worker config 新增 `worker.prompt.extra_rules`。
- YAML top-level `workers:` rename 為 `worker:`，`count` 併入同一 section（breaking，跟本 PR 其他 breaking 一起吃）。
- App 透過 job payload `allow_worker_rules: bool` 決定 worker 端的 ExtraRules 要不要生效。
- App config 支援 `prompt.goal`（string）與 `prompt.output_rules`（string array），允許運維覆寫預設的 Goal 與 Output 指示；`Goal` 有 hardcoded default、`OutputRules` default 為 `[]`（不渲染 section）。
- 舊測試從 `internal/bot/prompt_test.go` 搬到 `internal/worker/prompt_test.go` 並改斷 XML。

## 3. 非目標

- Issue 建立與 Slack 回文仍由 app 處理（留給 #62）。
- Agent CLI command 自訂（已有 #63 處理）。
- Memory-safe secret handling（解密後殘留在 heap 的風險）是獨立問題，本案不處理。
- Prompt 的 multi-flow（多種 triage 變體）override — 目前只有一個 flow，config 層的 Goal / OutputRules 就夠；多 flow 時再回來擴 job payload。

## 4. 關鍵決策

Brainstorm + grill 後的結論：

| # | 決策 | 理由 |
|---|---|---|
| Q1 | **Drain queue + cut**，不做 schema backward compat | Queue 消化速度快，deploy 停幾十秒 drain 成本低；dual-decode 會留 dead code。 |
| Q2 | **分側放 config**：app 側 `Language / Goal / OutputRules / AllowWorkerRules`；worker 側 `ExtraRules` | App 控制「要做什麼、怎麼回報」，worker 控制「執行時怎麼自律」，職責清楚。 |
| Q3 | **XML 直切**，不留 markdown fallback flag | 跟 Q1 一致，兩套 render code 並存會永遠刪不掉。 |
| Q4 | **Secret 不進 prompt**，agent 透過 env var (`$GH_TOKEN`) + skill 文件知道能力 | 避免 token 進 LLM log；agent 的 skill 文件本來就該講這些。 |
| Q5 | **Prompt builder 放 `internal/worker/prompt.go`** | App/worker 界線最硬；`AppendAttachmentSection` 一起搬進來（目前只有 `executor.go:107` 在呼叫）。 |
| G1 | **`OutputRules` default 為 `[]`** | 避免啟用預設值後改變現有 agent 行為（跟 `/triage-issue` skill 的 JSON output schema 衝突）。運維要才自己填。 |
| G2 | **`AllowWorkerRules` default `true`** | 升級平滑：搬完 `extra_rules` 行為照舊；忘搬還有 `warnUnknownKeys` 警告當安全網。 |
| G3 | **YAML rename `workers` → `worker`**，`count` 併入 | 避免 `workers`/`worker` 單複數並存的永久 smell；跟本 PR 其他 breaking 改動一起承擔一次升級成本。 |
| G4 | **特判 migration warn** for 舊 key（`prompt.extra_rules`、`workers.count`） | 通用 `warnUnknownKeys` 訊息不告訴運維該搬去哪；特判一次省後續一大堆 support。 |

## 5. Architecture

### 角色分工

**App（`internal/bot/workflow.go`）**
- 殺掉 `BuildPrompt` 呼叫（workflow.go 約 429 行）。
- 從 Slack thread 抓 rawMsgs（經 Mantis enrich）、modal extra、channel config，組成 `queue.PromptContext{}`。
- 把 `PromptContext` 塞進 `job.PromptContext`（新欄位），`job.Prompt string`（舊欄位）刪除。
- App 不再知道 agent 的 prompt 長什麼樣。

**Worker（`internal/worker/executor.go`）**
- 收到 job，解附件、解 secret（流程不變）。
- 在呼叫 `runner.Run` 之前呼叫 `worker.BuildPrompt(job.PromptContext, cfg.Worker.Prompt.ExtraRules, attachmentInfos)`，拿到完整 XML 字串。
- `AppendAttachmentSection` 併入 `BuildPrompt` 內部邏輯（不再是 workflow 之外的二段式組裝）。

**Wire protocol（`internal/queue/job.go`）**
- 新 field：`Job.PromptContext *PromptContext`
- 刪除 field：`Job.Prompt string`
- Q1 drain & cut，沒有 dual schema 支援。

**Secret flow（不變）**
- `Job.EncryptedSecrets` → worker 解密 → `opts.Secrets` map → `cmd.Env` 注入給 agent subprocess。
- 完全不經過 prompt 字串，維持現狀。

## 6. Components & Config

### 檔案異動

| 動作 | 路徑 | 說明 |
|---|---|---|
| 新增 | `internal/worker/prompt.go` | `BuildPrompt`、XML 渲染、attachment 渲染 |
| 新增 | `internal/worker/prompt_test.go` | 所有測試案例 |
| 刪除 | `internal/bot/prompt.go` | 整檔 |
| 刪除 | `internal/bot/prompt_test.go` | 整檔（內容搬到 worker） |
| 修改 | `internal/queue/job.go` | 新增 `PromptContext`、`ThreadMessage`；刪除 `Job.Prompt` 欄位；新增 `Job.PromptContext` 欄位 |
| 修改 | `internal/worker/executor.go` | 改呼叫 `worker.BuildPrompt`；`deps` 加 `workerPromptConfig` |
| 修改 | `internal/bot/workflow.go` | 刪掉 `BuildPrompt` 呼叫；改組 `queue.PromptContext{}` 塞進 job |
| 修改 | `internal/config/config.go` | `PromptConfig` 擴 Goal/OutputRules/AllowWorkerRules，刪 ExtraRules；rename `Workers` → `Worker`，`Count` 併入，新增 `Prompt WorkerPromptConfig`；特判舊 key migration warn |
| 修改 | 所有 `cfg.Workers.Count` 呼叫點 | rename 為 `cfg.Worker.Count`（全專案 grep 改） |

### App config（`internal/config/config.go`）

```go
type PromptConfig struct {
    Language         string   `yaml:"language"`
    Goal             string   `yaml:"goal"`              // 新增，default hardcoded
    OutputRules      []string `yaml:"output_rules"`      // 新增，default: []（不輸出 section）
    AllowWorkerRules bool     `yaml:"allow_worker_rules"` // 新增，default true（升級平滑）
    // ExtraRules []string ← 刪除
}
```

**Default 套用策略**（於 `applyDefaults(cfg *Config)`）：

| 欄位 | Default | 理由 |
|---|---|---|
| `Goal` | `"Use the /triage-issue skill to investigate and produce a triage result."` | 必須非空（XML 一定渲染 `<goal>`），無值等於 agent 沒指令 |
| `OutputRules` | `[]`（空陣列） | 現況 prompt 無 output 段，default 保持現況行為；空陣列時 `<output_rules>` 整段不渲染（optional section 規則） |
| `AllowWorkerRules` | `true` | 升級時搬完 rules 繼續生效（lossless） |

### Worker config（`internal/config/config.go`，rename + 擴充）

**Breaking change**：把既有的 `Workers WorkersConfig`（plural）rename 成 `Worker WorkerConfig`（singular），`Count` 併進去同時新增 `Prompt` 子區塊：

```go
type Config struct {
    // ... existing ...
    // Workers WorkersConfig `yaml:"workers"` ← 刪除
    Worker WorkerConfig `yaml:"worker"` // 每個 worker 的規模與行為都放這
}

type WorkerConfig struct {
    Count  int                `yaml:"count"`  // 從舊 WorkersConfig 搬來
    Prompt WorkerPromptConfig `yaml:"prompt"` // 新增
}

type WorkerPromptConfig struct {
    ExtraRules []string `yaml:"extra_rules"`
}
```

程式碼內所有 `cfg.Workers.Count` 同時 rename 成 `cfg.Worker.Count`。

### YAML 升級範例

```diff
-workers:
-  count: 3
+worker:
+  count: 3
+  prompt:
+    extra_rules:
+      - "no guessing"

 prompt:
   language: zh-TW
-  extra_rules:
-    - "no guessing"
+  # 以下可省略，走 hardcoded default 或不渲染；範例展示如何覆寫：
+  # goal: "..."
+  # output_rules:
+  #   - "..."
+  allow_worker_rules: true
```

### Migration warning（特判）

`applyDefaults` 或 `validate` 階段額外檢查舊位置的 key：

```go
// pseudocode - 看 koanf raw key 是否存在
if k.Exists("prompt.extra_rules") {
    slog.Warn("prompt.extra_rules 已搬到 worker.prompt.extra_rules，本設定忽略",
        "phase", "設定", "migration", "prompt-refactor")
}
if k.Exists("workers.count") && !k.Exists("worker.count") {
    slog.Warn("workers.count 已 rename 為 worker.count，本設定忽略",
        "phase", "設定", "migration", "prompt-refactor")
}
```

- **WARN only，不 remap**：運維自己改 YAML。跟其他 breaking change 一致。
- **不 fail-fast**：避免把只是想跑新 binary 的運維卡死。
- **通用 `warnUnknownKeys` 仍會 fire**：這兩個特判提供更具體的指引，和通用 warn 並存不衝突。

## 7. Wire Protocol：`PromptContext`

```go
// internal/queue/job.go

type PromptContext struct {
    // Thread 內容（核心）
    ThreadMessages []ThreadMessage `json:"thread_messages"`

    // Modal 補充說明
    ExtraDescription string `json:"extra_description,omitempty"`

    // Thread 來源 metadata
    Channel  string `json:"channel"`
    Reporter string `json:"reporter"`
    Branch   string `json:"branch,omitempty"`

    // App 控制的 prompt 段落
    Language         string   `json:"language"`
    Goal             string   `json:"goal"`
    OutputRules      []string `json:"output_rules"`
    AllowWorkerRules bool     `json:"allow_worker_rules"`
}

type ThreadMessage struct {
    User      string `json:"user"`
    Timestamp string `json:"timestamp"`
    Text      string `json:"text"`
}

type Job struct {
    // ... existing fields ...
    // Prompt      string  `json:"prompt"`  ← REMOVED
    PromptContext *PromptContext `json:"prompt_context"` // ADDED
}
```

**欄位來源對應**：

| 欄位 | 來源 |
|---|---|
| `ThreadMessages` | Slack API 抓下來的 rawMsgs，經 Mantis enrich + `ResolveUser` |
| `ExtraDescription` | Slack modal 的備註輸入 |
| `Channel` | `pt.ChannelName` |
| `Reporter` | `pt.Reporter`（Slack display name） |
| `Branch` | `pt.SelectedBranch`（可能為空） |
| `Language` | `cfg.Prompt.Language` |
| `Goal` | `cfg.Prompt.Goal`（有 default） |
| `OutputRules` | `cfg.Prompt.OutputRules`（有 default） |
| `AllowWorkerRules` | `cfg.Prompt.AllowWorkerRules`（default true） |

附件走另一條：`job.Attachments []AttachmentMeta`（既有）保留不動。Worker 下載後才把 `AttachmentInfo` slice 傳給 `BuildPrompt`，attachment 不進 `PromptContext`（因為是 runtime 解析的產物，不是 app 傳的 context 本體）。

## 8. XML Template

Worker `BuildPrompt()` 吐的字串長這樣：

```xml
<goal>Use the /triage-issue skill to investigate and produce a triage result.</goal>

<thread_context>
  <message user="Alice" ts="2026-04-09 10:30">Login page is broken when I click submit twice</message>
  <message user="Bob" ts="2026-04-09 10:32">Same here, Chrome 120</message>
</thread_context>

<extra_description>It happens on the login page after entering wrong password 3 times</extra_description>

<issue_context>
  <channel>general</channel>
  <reporter>Alice</reporter>
  <branch>main</branch>
</issue_context>

<response_language>zh-TW</response_language>

<additional_rules>
  <rule>no guessing</rule>
  <rule>only reference real files</rule>
</additional_rules>

<attachments>
  <attachment path="/tmp/.../screenshot.png" type="image">use your file reading tools to view</attachment>
  <attachment path="/tmp/.../error.log" type="text">read directly</attachment>
</attachments>

<output_rules>
  <rule>一句話整理分析結果</rule>
  <rule>&lt; 100 字</rule>
  <rule>用在回報 slack</rule>
</output_rules>
```

### Rendering 規則

- **Section 順序**：`<goal>` 永遠第一、`<output_rules>` 永遠最後，依 LLM 注意力配置。中間依序為 `<thread_context>` → `<extra_description>` → `<issue_context>` → `<response_language>` → `<additional_rules>` → `<attachments>`。
- **Escape**：所有使用者提供的字串（ThreadMessage text、ExtraDescription、每條 rule）走 `encoding/xml.EscapeString`，`< > & " '` 會轉為 entity。
- **Optional section 省略**：
  - `ExtraDescription == ""` → 整個 `<extra_description>` 元素不輸出
  - `Branch == ""` → `<issue_context>` 裡不輸出 `<branch>` 子元素
  - `AllowWorkerRules == false`，或 `len(ExtraRules) == 0` → 不輸出 `<additional_rules>`
  - 無附件 → 不輸出 `<attachments>`
- **無 root wrapper**：Top-level elements 平鋪。這份 XML 是給 LLM 讀的 context 不是給 parser 吃的，fragment 模式視覺乾淨。
- **ThreadMessage** 用屬性 + 內文：`<message user="X" ts="Y">text</message>`，比展開子元素短。
- **Attachment hint 對應**（`AttachmentInfo.Type` 字串，沿用既有語意）：
  - `"image"` → hint 文字 `use your file reading tools to view`
  - `"text"` → `read directly`
  - `"document"` → `document`
  - 其他 / 未知 → 省略 hint（`<attachment path="..." type="X"/>` 自閉合）

## 9. Error Handling

| 狀況 | 處理 |
|---|---|
| `job.PromptContext == nil`（舊 schema 殘留） | Worker `failedResult("malformed job: missing prompt_context")`，不 retry。Q1 drain 下不該發生，純防禦。 |
| `PromptContext.ThreadMessages` 空 | App assembly 時先檢查，空 thread 根本不該進 queue：`workflow.go` log + `notifyError`，不 submit。 |
| `PromptContext.Goal` 空 | App `applyDefaults` 已套 hardcoded default，worker 信任 app 已填好，不額外驗證。 |
| `PromptContext.OutputRules` 空（default 就是 `[]`） | Worker 完全不輸出 `<output_rules>` 整段（optional section 規則生效）。不是錯，是預期行為。 |
| XML escape edge case（非法 UTF-8、null byte） | `encoding/xml.EscapeString` 會忽略無效序列，最壞情況 agent 收到缺字但不 crash。不 retry、不失敗。 |

## 10. Deploy & Migration

### 部署程序（Q1 drain & cut）

目前 `internal/queue` 無 pause / drain 端點，因此：

1. **停 app pod**（K8s scale to 0 或手動 kill）：立刻停止新 job 進 queue。
2. **等 queue 耗盡**：查 `internal/queue/httpstatus.go` 的 `/status`，確認 `queue_depth == 0` 且沒有 running jobs。
3. **同時 deploy 新 app + 新 worker**（binary + config 一起）。
4. **啟動順序：worker 先、app 後**：避免新 app 送 job 給還未更新的 worker。
5. **恢復流量**：app scale up，觀察第一批 job 成功率。

若 step 2 有 long-running job 卡住：等、或 admin force-kill（`httpstatus.go` 已有機制）。

### Config 升級指引

寫進 `docs/MIGRATION-v1.md` 增補章節或新開 migration 文：

- **必改**：
  - `workers:` → `worker:`（top-level rename）
  - `prompt.extra_rules` → `worker.prompt.extra_rules`
- **可選**（有 default）：`prompt.goal`、`prompt.output_rules`、`prompt.allow_worker_rules`
- **App 啟動 validation**：
  - 偵測到 `prompt.extra_rules` 殘留 → 特判 WARN「已搬到 worker.prompt.extra_rules，本設定忽略」
  - 偵測到 `workers.count` 殘留 → 特判 WARN「已 rename 為 worker.count，本設定忽略」
  - 通用 `warnUnknownKeys` 照常 fire（兩者並存）
  - **不 crash、不 remap**，給 operator 看 log 自己改 YAML

## 11. Testing

### Worker prompt tests（`internal/worker/prompt_test.go`）

| Test | 驗證 |
|---|---|
| `TestBuildPrompt_Basic` | ThreadMessages 渲染為 `<message user="..." ts="...">...</message>`；`<goal>` 在頭、`<output_rules>` 在尾 |
| `TestBuildPrompt_AllSections` | 所有 optional section 都填時，XML 對齊預期 |
| `TestBuildPrompt_OptionalOmitted` | `ExtraDescription=""`、`Branch=""`、無附件、`AllowWorkerRules=false` 時對應 section 不輸出 |
| `TestBuildPrompt_WorkerRulesToggle` | `AllowWorkerRules=false` → 無 `<additional_rules>` 不管 ExtraRules 是否有值；`=true` + 空 rules → 仍無 section；`=true` + 有 rules → 渲染 |
| `TestBuildPrompt_XMLEscaping` | ThreadMessage 含 `<script>alert("x")</script>`、`&`、`'` 等字元 → 全部變 entity |
| `TestBuildPrompt_Attachments` | image / text / document 三種 type 的 hint 文字正確 |
| `TestBuildPrompt_OutputRulesArray` | 多筆 `OutputRules` 渲染為多個 `<rule>` |

### App-side assembly tests（新增或擴充現有 `internal/bot/workflow_test.go`）

| Test | 驗證 |
|---|---|
| `TestAssemblePromptContext_AppliesDefaults` | `cfg.Prompt.Goal == ""` → `PromptContext.Goal` 得到 hardcoded default；`OutputRules == nil` → 維持 nil/empty（空陣列是預期）；`AllowWorkerRules` 預設 true |
| `TestAssemblePromptContext_PassesConfigThrough` | 有填的 config 欄位原封不動進 PromptContext |

### Config migration tests（擴充 `internal/config/config_test.go` 或 `cmd/agentdock/config_test.go`）

| Test | 驗證 |
|---|---|
| `TestConfig_LegacyPromptExtraRules_Warns` | YAML 含 `prompt.extra_rules` → log 含「已搬到 worker.prompt.extra_rules」；值不套用到 `cfg.Prompt`（已被移除的欄位） |
| `TestConfig_LegacyWorkersCount_Warns` | YAML 含 `workers.count` → log 含「已 rename 為 worker.count」；值不套用到 `cfg.Worker.Count` |
| `TestConfig_NewWorkerSection_LoadsCorrectly` | YAML 含 `worker.count` + `worker.prompt.extra_rules` → `cfg.Worker` 正確 populated |

### Integration（`internal/worker/pool_test.go` 擴充）

- Submit Job with 完整 PromptContext → mock runner 收到的 `prompt` 字串含 `<goal>`、`<thread_context>` 等預期 XML 片段
- `PromptContext == nil` 的 Job → worker `failedResult` 且錯誤含 `"missing prompt_context"`

### 刪除

- `internal/bot/prompt_test.go` 整檔（6 個 tests 都搬進 `internal/worker/prompt_test.go` 並改 XML 斷言）

### 不做

- 不用 golden file — case 不多，string assertion 夠
- 不測 XML parser 正確性 — prompt 給 LLM 讀，不是給 parser 吃
- 不測 end-to-end Slack → issue — 超出 #61 範疇，#62 整合時一起做

## 12. References

- Issue #61：本案
- Issue #54：parent，列出原始 prompt example（markdown 版）
- Issue #62：follow-up（worker 接手 GitHub issue 建立 + Slack 回文），依賴本案完成
- Issue #63：sibling（worker config 自訂 agent command），獨立並行
- `internal/bot/prompt.go`：現況 prompt builder（將刪除）
- `internal/bot/workflow.go:428`：現況呼叫點
- `internal/worker/executor.go:100-110`：現況 `AppendAttachmentSection` 呼叫點
- `internal/queue/job.go`：Job schema 變更位置
- `internal/config/config.go:92`：`PromptConfig` 變更位置
- `CLAUDE.md`：App/worker 界線 landmine
