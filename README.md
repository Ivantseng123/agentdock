# Slack Issue Bot

[English](README.en.md)

透過 Slack 表情符號反應，自動建立帶有 AI 程式碼分析的 GitHub Issue。

## 運作方式

1. 有人在 Slack 頻道發佈 bug 回報或功能需求
2. 團隊成員貼上設定的表情符號（例如 `:bug:` 或 `:rocket:`）
3. Bot 在**訊息討論串**中回覆：
   - 顯示 repo 選擇器（支援搜尋或按鈕）
   - 顯示分支選擇器（若啟用）
   - Clone/Pull GitHub repo
   - 執行 AI 診斷引擎分析程式碼
   - **拒絕機制** — 若描述太模糊，回覆要求補充說明而非建立 issue
   - 建立 GitHub Issue（含可點擊的檔案連結）並在討論串回覆 URL

## 功能特色

- **討論串互動** — 所有 bot 訊息都在原始訊息的討論串中
- **多 Repo 支援** — 單一頻道可對應多個 repo，透過按鈕或搜尋下拉選擇
- **分支選擇** — 可選擇要分析的分支
- **跨 Repo 感知** — 讀取 README/CLAUDE.md/agent.md 理解 repo 上下文關係
- **拒絕機制** — 報告太模糊時拒絕建立 issue（找不到相關檔案、不確定項目太多、信心度低）
- **GitHub 檔案連結** — Issue 中的檔案參考為可點擊的原始碼連結
- **LLM 備援鏈** — 多個 provider 支援各自的重試次數和逾時設定
- **CLI Provider** — 串接任何 CLI 工具（claude、opencode 等），使用自己的訂閱，零 API 成本
- **Lite 模式** — 僅 grep 分析，零 LLM 成本
- **頻率限制** — 支援 per-user 和 per-channel 節流
- **自動綁定** — Bot 加入頻道時自動註冊，無需手動設定
- **回應快取** — 相同訊息在 TTL 內直接回傳快取結果

## 前置需求

| 項目 | 取得方式 |
|------|---------|
| Go 1.22+ | [go.dev/dl](https://go.dev/dl/) |
| Slack App | [api.slack.com/apps](https://api.slack.com/apps) |
| GitHub PAT | GitHub Settings > Developer settings > Personal access tokens |
| LLM Provider | CLI 工具 / API key (Anthropic/OpenAI) / Ollama (免費) |

### Slack App 設定

1. 在 [api.slack.com/apps](https://api.slack.com/apps) 建立新 App
2. **OAuth & Permissions** — 加入 Bot Token Scopes：
   - `reaction_read`, `channels:history`, `chat:write`, `users:read`, `channels:read`
   - 私人頻道需額外：`groups:history`, `groups:read`
3. **Socket Mode** — 啟用
4. **Basic Information** — 產生 App-Level Token，scope 選 `connections:write`（取得 `xapp-` token）
5. **Event Subscriptions** — 訂閱 `reaction_added` bot event
   - 自動綁定需額外訂閱：`member_joined_channel`, `member_left_channel`
6. **Install to Workspace** — 複製 `xoxb-` Bot Token

## 快速開始

```bash
cp config.example.yaml config.yaml
# 編輯 config.yaml 填入你的 token

# 執行
go run ./cmd/bot/
# 或
./run.sh
```

## 設定

完整選項請參考 `config.example.yaml`。主要區段：

```yaml
auto_bind: true                       # Bot 加入頻道時自動綁定

channel_defaults:                     # 自動綁定頻道的預設值
  branch_select: true
  default_labels: ["from-slack"]

channels:                             # 靜態頻道設定（可選）
  C05XXXXXX:
    repos:                            # 多個 repo → Slack 按鈕選擇
      - "org/backend"
      - "org/frontend"
    branch_select: true               # 顯示分支選擇
    default_labels: ["from-slack"]

reactions:                            # 表情符號對應
  bug:
    type: "bug"
    issue_labels: ["bug", "triage"]
    issue_title_prefix: "[Bug]"
  rocket:
    type: "feature"
    issue_labels: ["enhancement"]
    issue_title_prefix: "[Feature]"

llm:
  providers:
    - name: "cli"
      command: "claude"               # 任何支援 --print 的 CLI 工具皆可
      args: ["--print", "{prompt}"]
      timeout: 5m                     # CLI 需要較長時間
      max_retries: 3

    - name: "claude"                  # API 備援
      api_key: "sk-ant-..."
      model: "claude-sonnet-4-20250514"
      base_url: "https://api.anthropic.com"
      max_retries: 3
  timeout: 60s                        # 全域預設（各 provider 可覆蓋）

diagnosis:
  mode: "full"                        # "full"（使用 LLM）或 "lite"（僅 grep）
  max_turns: 5                        # Agent loop 最大回合數
  max_tokens: 100000                  # Token 預算上限
  cache_ttl: 10m                      # 回應快取 TTL（0 = 不快取）
  prompt:
    language: "繁體中文"
    extra_rules:
      - "列出所有相關的檔案名稱與完整路徑"
```

### 診斷模式

| 模式 | LLM 成本 | 說明 |
|------|---------|------|
| `full` | 每次觸發消耗 token | Agent loop：LLM 使用工具（grep、read_file 等）進行多回合對話直到診斷完成 |
| `lite` | **0 token** | 僅 grep，建立 issue 附上檔案參考，供工程師自行用 AI 分析 |

### 拒絕與降級機制

此工具的核心價值是**把 Slack 對話結構化成 issue**，降低非工程師建立 issue 的門檻。AI triage 是加分項，不是必要條件。

| 情況 | 行為 |
|------|------|
| AI 分析正常（有相關檔案、信心度足夠） | 建立 issue，包含完整 AI Triage 區段 |
| AI 找不到相關檔案 / 待釐清項目過多，但信心度非 low | 建立 issue，**跳過 AI Triage 區段**（僅保留原始訊息 + Channel + Reporter） |
| `信心度 = low` | **拒絕建立**（可能選錯 repo 或內容完全無關） |

拒絕時 bot 會在討論串回覆，請報告者確認 repo 是否正確或補充描述。

### CLI Provider

串接任何支援 stdin 或 `--print` 模式的 CLI 工具，使用你自己的訂閱，零 API 成本：

```yaml
# 範例：Claude Code CLI
- name: "cli"
  command: "claude"
  args: ["--print", "{prompt}"]
  timeout: 5m

# 範例：其他 CLI 工具（透過 stdin 傳入 prompt）
- name: "cli"
  command: "my-ai-tool"
  args: []
  timeout: 3m
```

`{prompt}` 為 placeholder，prompt < 32KB 時嵌入 args，超過時改走 stdin。若 args 中無 `{prompt}`，一律走 stdin。

### 環境變數覆蓋

```bash
export SLACK_BOT_TOKEN="xoxb-..."
export SLACK_APP_TOKEN="xapp-..."
export GITHUB_TOKEN="ghp_..."
export LLM_CLAUDE_API_KEY="sk-ant-..."
```

## Issue 輸出範例

```markdown
**Channel:** #dev-general | **Reporter:** Alice

> 使用者登入頁面，輸入空白密碼按送出後頁面直接當掉

### AI Triage

登入表單的送出處理缺少空欄位驗證，在呼叫 auth API 前未檢查密碼是否為空

### Related Files

- [`LoginPage.vue`](https://github.com/example/webapp/blob/main/src/pages/LoginPage.vue) — 登入頁面元件，含表單送出邏輯
- [`auth.api.js`](https://github.com/example/webapp/blob/main/src/api/auth.api.js) — 認證 API 呼叫，可能需要加入輸入驗證
- [`validation.js`](https://github.com/example/webapp/blob/main/src/utils/validation.js) — 現有的表單驗證工具，可參考其做法

### Direction

- 在 LoginPage.vue 的表單送出前加入空欄位驗證，可參考 validation.js 的做法
- 確認 auth.api.js 是否有 server-side 驗證作為後備

### Needs Clarification

- 這個問題是否在所有瀏覽器都會發生？
- 當掉時有沒有顯示錯誤訊息，還是頁面直接卡住？
```

## 測試

```bash
go test ./...   # 76 tests
```

## Docker

```bash
docker build -t slack-issue-bot .
docker run -v $(pwd)/config.yaml:/config.yaml slack-issue-bot
```

## 架構

```
Slack 表情反應 → Socket Mode → Handler（去重 + 頻率限制 + 並發控制）
  → Workflow（透過討論串按鈕選擇 repo/分支）
    → 診斷引擎
    → 拒絕檢查（files=0, questions>=5, confidence=low）
    → GitHub Issue（可點擊的檔案連結）→ 在討論串回覆 URL
```

### 診斷引擎

引擎使用 **Agent Loop** — 由 LLM 驅動的多回合對話，模型自行決定使用哪些工具、何時已有足夠資訊產出分析卡。

```
1. Pre-grep（免費，不消耗 LLM）
   從原始 Slack 訊息擷取關鍵字並執行 git grep。
   這能捕捉到非英文詞彙（如中文），避免 LLM 翻譯時遺漏。

2. Agent Loop（最多 max_turns 回合，預設 5）
   LLM 看到 pre-grep 結果 + 可用工具後自行決定：
   ┌──────────────────────────────────────────────────┐
   │  LLM：「我要讀取 sectionInfo.vue」                  │
   │  引擎：執行 read_file，回傳檔案內容               │
   │  LLM：「我需要搜尋 unitno」                        │
   │  引擎：執行 grep，回傳匹配檔案清單                │
   │  LLM：「資訊足夠了 → 產出分析卡」                  │
   └──────────────────────────────────────────────────┘

3. 輸出：分析卡（JSON）
   摘要、相關檔案、方向建議、待釐清項目、信心度
```

**可用工具（6 個）：**

| 工具 | 用途 |
|------|------|
| `grep` | 搜尋哪些檔案提及某個詞彙（廣泛探索） |
| `read_file` | 讀取檔案內容（含行號） |
| `list_files` | 列出 repo 檔案樹（`git ls-files`） |
| `read_context` | 讀取 README.md、CLAUDE.md、agent.md 了解 repo 上下文 |
| `search_code` | 正規表達式搜尋，含前後文行 |
| `git_log` | 查看最近的 commit 記錄 |

**為什麼用 Agent Loop 而非固定流程：**
- LLM 依據每份報告調整策略 — 清楚的報告可能只需 2 回合，模糊的會用完 5 回合
- 非英文訊息自然處理 — LLM 在推理過程中自動翻譯
- Pre-grep 確保原始語言的關鍵字命中不會被遺漏
- Repo 的文件越完整（README、CLAUDE.md），分析結果越精準

## 授權

MIT
