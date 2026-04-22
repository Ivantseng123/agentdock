---
date: 2026-04-21
status: approved
owners: Ivantseng123
---

# Bot 訊息在 Thread Context 正確保留

## Problem

目前 thread context 讀取時，外部 bot（GitHub / Jira / webhook / integration）發的訊息整批被濾掉，agent 看不到。使用者回報：「訊息如果是另外一個 bot 發的，會直接讀不到。」

## Root Cause

兩處獨立缺陷疊加：

1. `app/bot/workflow.go:400` 將 `botUserID` 寫死為 `""`，從未把真正的 bot 身份傳進 `FetchThreadContext`。
2. `app/slack/client.go:481` 的過濾條件：

   ```go
   if m.BotID != "" || m.User == botUserID {
       continue
   }
   ```

   前半段殺光所有 `BotID` 非空的訊息（包含外部 bot）；後半段配空字串等於空砲。

實際行為等同於「所有 bot 訊息一律濾除」，而設計原意是「只濾掉自己」。

## Goals

- 外部 bot 訊息（其他 integration / webhook）進入 thread context，agent 可讀取。
- 自己這個 bot 發的訊息（status update、selector、result）仍然不進 context，避免 feedback loop。
- Bot 訊息的文字內容（常見於 `attachments` / `blocks`，`m.Text` 為空）能正確擷取。
- Bot 顯示名（`GitHub`、`Jira` 等）在 context 裡被識別並明確標記為 bot。

## Non-Goals

- 不提供 allowlist / denylist 來選擇性納入 bot。所有非自己的 bot 一律保留。
- 不處理人類訊息的 Slack unfurl preview（attachments 副本）— 範圍控制在 bot 訊息。
- 不動 worker 或 shared queue 模組；不新增 config。

## Design

### 1. Bot 身份透過 preflight 結果取得

`cmd/agentdock/app.go:26` 啟動時已呼叫 `RunPreflight`，preflight 內 `connectivity.CheckSlackToken` 已經打過一次 `auth.test` 並在**成功時拿到 `user_id` 與 `bot_id`**。目前 `CheckSlackToken` 只回傳 `userID` 並把值印在 stderr 就丟掉。我們擴充這條路徑，讓 identity 沿用 preflight 結果，不做第二次 `auth.test`。

**改動 `shared/connectivity/slack.go`**：

```go
type SlackIdentity struct {
    UserID string
    BotID  string
}

// CheckSlackToken verifies the bot token via Slack auth.test API.
// Returns SlackIdentity on success (both user_id and bot_id).
func CheckSlackToken(token string) (SlackIdentity, error)
```

既有呼叫處（`cmd/agentdock/init.go:201`、`app/config/preflight.go:60 & 82`）調整為接新回傳，印出時仍可只用 `UserID`（維持現有 UI 訊息內容），但把完整 `SlackIdentity` 存進 preflight 的 `prompted` 結果 map（key e.g. `slack.bot_user_id`、`slack.bot_id`）。

**改動 `app/config/preflight.go:RunPreflight`**：`prompted` map 結果額外寫入 `slack.bot_user_id` 與 `slack.bot_id`。

**改動 `cmd/agentdock/app.go`**：目前 `if _, err := appconfig.RunPreflight(appCfg); err != nil {` 把 `prompted` 丟棄。改為接住，然後從 map 取出 `slack.bot_user_id` / `slack.bot_id` 組成 `bot.Identity`，作為新增參數傳入 `app.Run(cfg, identity)`。

**改動 `app/app.go:Run`**：簽章增加 `identity bot.Identity` 參數；**移除** `app/app.go:215-218` 的 `api.AuthTest()` 呼叫與 `botUserID` 本地變數推導（原本只為 AutoBind 使用；改從 `identity.UserID` 取）。`identity` 同時傳入 `NewWorkflow(..., identity)`。

**新增 `app/bot/identity.go`**：

```go
type Identity struct {
    UserID string // auth.test 的 user_id
    BotID  string // auth.test 的 bot_id
}
```

`Workflow` struct 增加 `identity Identity` 欄位；`NewWorkflow` 多接一個 `Identity` 參數。`workflow.go:400` 改為：

```go
rawMsgs, err := w.slack.FetchThreadContext(
    pt.ChannelID, pt.ThreadTS, pt.TriggerTS,
    w.identity.UserID, w.identity.BotID,
    w.cfg.MaxThreadMessages,
)
```

**Failure mode**：若 preflight 拿不到 identity（理論上不會發生，因為 preflight 失敗 app 就不會啟動），Identity 是 zero value，filter 條件 `botUserID != "" && ...` / `botID != "" && ...` 會讓所有自我識別條件不成立，回退到「不濾任何東西」的行為。此路徑是 dead code 的保險，不應被正常流程觸發。

### 2. Filter 條件修正

`filterThreadMessages` signature 與邏輯：

```go
func filterThreadMessages(
    messages []slack.Message,
    triggerTS, botUserID, botID string,
) []ThreadRawMessage {
    var result []ThreadRawMessage
    for _, m := range messages {
        if m.Timestamp >= triggerTS {
            continue
        }
        // 只濾掉自己
        if botUserID != "" && m.User == botUserID {
            continue
        }
        if botID != "" && m.BotID == botID {
            continue
        }
        // 其他 bot、webhook、integration 一律保留
        text := extractMessageText(m)
        if m.BotID != "" && text == "" {
            // Bot 訊息三者皆空（純互動訊息、reaction 等），丟棄避免在 prompt 產生空殼
            continue
        }
        user := m.User
        if m.BotID != "" {
            if name := resolveBotDisplayName(m); name != "" {
                user = "bot:" + name
            }
        }
        result = append(result, ThreadRawMessage{
            User:      user,
            Text:      text,
            Timestamp: m.Timestamp,
            Files:     m.Files,
        })
    }
    return result
}
```

### 重要語義

**雙重自我識別**：`m.User == botUserID` 和 `m.BotID == botID` 都要檢查 — 多數情況兩者都命中，但 `chat.postMessage` 帶 custom `username`、thread_broadcast、新 block API 等邊緣路徑會導致其中一欄位不一致。雙條件 OR 保險。

**Empty bot 訊息丟棄**：Bot 訊息若三者（`Text` + `Attachments` + `Blocks`）皆空，通常是純互動按鈕或 reaction，對 triage agent 零 value。丟棄可避免 prompt 產出 `<message user="bot:X" ts="..."></message>` 空殼浪費 token。人類訊息無此規則（人類空訊息罕見，但保留不礙事）。

**Bot 身份前綴 `bot:`**：`User` 欄位對 bot 訊息塞 `"bot:GitHub"` / `"bot:Jira"`。prompt builder 的 XML 會渲染成 `<message user="bot:GitHub" ts="...">...</message>`，讓 agent 一眼辨別 bot 通知與人類發言。不動 `ThreadRawMessage`/`ThreadMessage` struct schema，下游零改動。

### 3. Text 擷取（Text > Blocks > Attachments）

新增 `extractMessageText` helper：

```go
// extractMessageText returns m.Text if non-empty, otherwise reconstructs
// text from bot blocks/attachments. Returns "" only if the message has
// no renderable text content.
func extractMessageText(m slack.Message) string {
    if strings.TrimSpace(m.Text) != "" {
        return m.Text
    }
    if s := extractFromBlocks(m.Blocks.BlockSet); s != "" {
        return s
    }
    if s := extractFromAttachments(m.Attachments); s != "" {
        return s
    }
    return ""
}
```

**優先序 Text > Blocks > Attachments（二擇一）**：

- **Text**：人類訊息與大多數簡單 bot 訊息的主體。
- **Blocks**：新版 API（Slack Workflow Builder、modern GitHub bot、modern integrations）的主要內容載體。**相容模式**下（bot 同時塞 blocks + attachments）通常 blocks 較完整，attachments 只放短 fallback，因此 blocks 優先。
- **Attachments**：舊版 API（Datadog、PagerDuty、Sentry、Jira 舊版、多數 webhook）的主要內容。作為 blocks 缺席時的 fallback。

**Blocks 擷取**：遍歷 `SectionBlock` / `HeaderBlock` / `ContextBlock` / `RichTextBlock`，抓 `TextBlockObject.Text`（`plain_text` 與 `mrkdwn` 皆處理）。其他 block type（`ImageBlock` / `ActionBlock` / `DividerBlock`）略過。片段以 `\n` 串接。

**Attachments 擷取**：依序串接 `Pretext`、`Title`、`Text`、`Fallback`；若有 `Fields`，渲染成 `*Title*: Value`；多個 attachment 以空行分隔。

### 4. `ResolveUser` 防禦性 early-return

`app/slack/client.go:181-191` 的 `ResolveUser` 假設 `userID` 一定是 Slack user id 格式，傳非此格式字串會 `GetUserInfo` 失敗並觸發 `c.logger.Warn`。雖然本 spec 設計已經把 bot 身份前綴 `"bot:"` 送入下游（`"bot:GitHub"`），但仍要加 early-return guard：

```go
var slackUserIDPattern = regexp.MustCompile(`^[UW][A-Z0-9]+$`)

func (c *Client) ResolveUser(userID string) string {
    if !slackUserIDPattern.MatchString(userID) {
        return userID  // 非 Slack user id 格式（例如 "bot:GitHub"），不打 API
    }
    user, err := c.api.GetUserInfo(userID)
    if err != nil {
        c.logger.Warn("使用者名稱解析失敗", "phase", "失敗", "user_id", userID, "error", err)
        return userID
    }
    if user.Profile.DisplayName != "" {
        return user.Profile.DisplayName
    }
    return user.RealName
}
```

理由：Slack user id 固定是 `U` 或 `W` 開頭全大寫字母數字。非此 pattern 打 API 本來就無意義；事前 guard 避免 `"bot:GitHub"` 等字串進到 API layer 噴 warn log。這是 `ResolveUser` 自身的缺陷修正，與 bot filter 鬆綁獨立，但本次一起做。

## Testing

### 擴充 `TestFilterThreadMessages`

新增案例：

- 外部 bot 訊息（`BotID != ""`, `User == ""`, `Text != ""`）→ 保留，`User` 前綴 `bot:`
- 自己 bot（`User == botUserID`）→ 濾掉
- 自己 bot（`BotID == botBotID` 但 `User` 對不上）→ 濾掉
- Bot 訊息 `Text` 空但 blocks 有內容 → `ThreadRawMessage.Text` 非空
- Bot 訊息 `Text` 空但 attachments 有內容 → `ThreadRawMessage.Text` 非空
- Bot 訊息三者皆空 → **濾掉**（不是保留空殼）
- `BotProfile.Name` / `Username` / `BotID` 三選一優先序各一案例
- `botUserID == ""` 且 `botID == ""`（保險 fallback）→ 全部保留

### 新增獨立測試

- `TestExtractMessageText`：
  - `Text` 非空（即使 blocks / attachments 亦有內容）直接回 `Text`
  - 僅 blocks（SectionBlock、HeaderBlock、ContextBlock、RichTextBlock 各一）
  - 僅 attachments（含 `Fields` 渲染 `*Title*: Value`）
  - Blocks + Attachments 都有 → 取 blocks（相容模式優先 blocks）
  - 三者皆空 → 回 `""`
- `TestResolveBotDisplayName`：三段優先序各一案例。
- `TestResolveUser_NonSlackID_EarlyReturn`：傳入 `"bot:GitHub"`、`"ivan"` 等非 Slack ID 格式，verify 未打 `GetUserInfo`（via stub 計數器），回傳原字串。
- `TestCheckSlackToken_ReturnsBotID`：`shared/connectivity/slack_test.go` 新增 mock server 回 `user_id + bot_id`，verify `SlackIdentity` 填滿。

### 介面簽章調整

以下因 `botID` 新增或 `CheckSlackToken` 回傳型別變更而更新：

- `app/slack/client.go:439` `FetchThreadContext`
- `app/bot/workflow.go:38` `slackAPI` interface
- `app/bot/workflow_test.go:115` `stubSlack.FetchThreadContext`
- `shared/connectivity/slack.go` `CheckSlackToken`
- `cmd/agentdock/init.go:201`、`app/config/preflight.go:60 & 82` 之呼叫點

## Files Changed

| 檔案 | 動作 |
|------|------|
| `shared/connectivity/slack.go` | `CheckSlackToken` 回傳改為 `SlackIdentity{UserID, BotID}` |
| `shared/connectivity/slack_test.go` | 新增 `TestCheckSlackToken_ReturnsBotID` |
| `cmd/agentdock/init.go` | `CheckSlackToken` 呼叫點解構新回傳；UI 訊息仍印 user_id |
| `app/config/preflight.go` | `CheckSlackToken` 呼叫點解構；`prompted` map 寫入 `slack.bot_user_id` + `slack.bot_id` |
| `app/slack/client.go` | `FetchThreadContext` 多接 `botID`；`filterThreadMessages` 改過濾與擷取邏輯；新增 `extractMessageText` / `extractFromBlocks` / `extractFromAttachments` / `resolveBotDisplayName`；`ResolveUser` 加 Slack ID pattern early-return |
| `app/slack/client_test.go` | 擴充 `TestFilterThreadMessages`；新增 `TestExtractMessageText`、`TestResolveBotDisplayName`、`TestResolveUser_NonSlackID_EarlyReturn` |
| `app/bot/identity.go`（新檔） | 定義 `Identity` struct |
| `app/bot/workflow.go` | `slackAPI.FetchThreadContext` 加 `botID`；`Workflow` 加 `identity Identity`；`NewWorkflow` 多接 `Identity`；第 400 行改用 `w.identity` |
| `app/bot/workflow_test.go` | `stubSlack.FetchThreadContext` 對齊；`NewWorkflow` 呼叫補 zero-value `Identity` |
| `cmd/agentdock/app.go` | 接住 `RunPreflight` 回傳的 `prompted` map，取 `slack.bot_user_id` / `slack.bot_id` 組 `bot.Identity` 傳入 `app.Run` |
| `app/app.go` | `Run` 簽章加 `identity bot.Identity` 參數；移除第 215-218 行的 `api.AuthTest()`；AutoBind 改用 `identity.UserID`；傳入 `NewWorkflow` |

無 config 變更；import direction 零影響（改動皆在 `app/`、`cmd/`、`shared/connectivity/` 內）。

## Out of Scope

- Allowlist / denylist 選擇性保留 bot（目前一律保留非自己的 bot）。
- 人類訊息的 Slack unfurl 擷取（本次僅 bot 訊息）。
- Mantis 連結擷取 refactor（另一份 spec：`2026-04-21-mantis-agent-skill-design.md`）。
