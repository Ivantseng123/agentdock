# Session Handoff — 2026-04-03

## 本次 Session 做了什麼

這是一個大型重構 + 產品定位 session，從分析 OpenHarness 架構開始，最終完成了 react2issue 的核心重構和開源。

### 時間軸

1. **分析 OpenHarness 架構** → 找到 harness 模式的優化機會
2. **Agent Loop 重構** (branch: `agent-loop-diagnosis`)
   - 設計 spec → 寫 plan → subagent-driven development 執行
   - 把原本 4-step hardcoded pipeline 改成 LLM-driven agent loop
   - 新增 `ConversationProvider` 介面（取代舊的 `Provider.Diagnose()`）
   - 4 個 provider 全部改寫：Claude (native tool use), OpenAI (function calling), CLI/Ollama (JSON-in-text)
   - 新增 6 個 tool：grep, read_file, list_files, read_context, search_code, git_log
3. **修 bug 三輪**
   - CLI provider 的 tool 不可見 → 加上 `CLIToolPromptSuffix`
   - LLM 讀到本地 CLAUDE.md 以為是目標 repo → prompt 加 "REMOTE repository"
   - JSON 格式不匹配 → prompt 和 parser 對齊到 `{"tool": "...", "args": {...}}`
4. **品質調校**
   - 加入 pre-grep（原始關鍵字先 grep）解決非英文搜尋問題
   - 修改 prompt：不重複列檔案、方向不給 code、reference not instructions
5. **產品定位討論** → 確認「結構化工具」定位
   - 只有 confidence=low 才 reject
   - files=0 或 questions>=5 → 建 issue 但不附 triage
6. **Message Enrichment**
   - Slack 附件：文字檔下載 inline，圖片附上 permalink
   - Mantis URL：解析 issue ID，抓 title + description
7. **Description Modal**
   - 選完 repo/branch 後可以補充說明
   - Slack modal (trigger_id + view_submission + private_metadata)
8. **開源**
   - 建立 GitHub repo: `Ivantseng123/react2issue` (public)
   - README 中文 + 英文版
   - 移除所有絕對路徑

### Commits（按順序）

```
b3577d2 feat: implement ConversationProvider.Chat() for all 4 LLM providers
a84d11c feat: add MaxTurns, MaxTokens, CacheTTL to DiagnosisConfig
cc03d30 feat: 6 diagnosis tools
202e82a feat: agent loop system prompt with tool descriptions
a6c7a38 feat: in-memory diagnosis response cache with TTL
42557d1 feat: agent loop with turn limit, forced finish, token budgeting
7c7cc3e refactor: rewrite engine with agent loop, remove old pipeline
85e22f9 feat: wire agent loop engine with ChatFallbackChain and progress message
d8124df docs: update CLAUDE.md and README.md for agent loop diagnosis
183c0a0 feat: pre-grep, CLI tool-use fixes, prompt tuning, README zh/en
1dbaf30 feat: only reject on confidence=low, skip triage for weak results
f3c96de docs: update rejection mechanism to reflect graceful degradation
e038517 docs: replace issue output example with generic content
f198a9c docs: update CLAUDE.md with react2issue positioning and current architecture
6ae3363 fix: remove hardcoded absolute paths
8c4907c docs: generalize CLI provider description
2713add docs: add extra_rules explanation with examples
5f819fd docs: rewrite README with engineer-oriented tone
5516af5 feat: enrich messages with Slack attachments and Mantis issues
d0773f9 feat: optional description input after repo/branch selection
```

## 當前狀態

- **Branch**: `main`（agent-loop-diagnosis 已 merge）
- **GitHub**: https://github.com/Ivantseng123/react2issue
- **Tests**: 76 tests passing (`go test ./...`)
- **Bot**: 可能還在背景跑（port 8180），用 `lsof -i :8180 -t | xargs kill` 關掉
- **Log**: `/private/tmp/slack-issue-bot-agent-loop.log`

## 待測試 / 待確認

1. **Description Modal** — 最後加的功能，user 還沒測試確認
   - 選完 repo + branch 後出現「補充說明」和「跳過」按鈕
   - 「補充說明」打開 modal → 文字附加到 message → 進入 diagnosis
   - 「跳過」直接進 diagnosis
   - 需確認 Slack App 有開 Interactivity（Socket Mode 應該自帶）

2. **Mantis Integration** — config 裡有設定但需要實際 Mantis 環境測試
   - `config.yaml` 的 `integrations.mantis` 區塊
   - 支援 API token 或 basic auth

## 關鍵檔案快速導覽

| 檔案 | 角色 | 改動量 |
|------|------|--------|
| `internal/diagnosis/loop.go` | Agent loop 主邏輯 | **新增** |
| `internal/diagnosis/tools.go` | 6 個 tool 實作 | **新增** |
| `internal/llm/provider.go` | ConversationProvider 介面 | **大改** |
| `internal/llm/cli.go` | CLI provider + JSON-in-text | **大改** |
| `internal/llm/prompt.go` | System prompt + tool schema | **大改** |
| `internal/bot/workflow.go` | 流程加入 description phase | **中改** |
| `internal/bot/enrich.go` | Message enrichment | **新增** |
| `internal/mantis/client.go` | Mantis REST API client | **新增** |
| `internal/slack/client.go` | 附件下載 + modal | **中改** |

## 下一步建議

- 確認 description modal 在 Slack 正常運作
- 考慮加入更多 enrichment 來源（目前只有 Mantis + Slack 附件）
- 如果要支援更多通訊軟體（Teams, Discord），`internal/slack/` 需要抽象化
- Config 的 `extra_rules` 是可以對外推廣的功能，讓不同團隊自訂 prompt
