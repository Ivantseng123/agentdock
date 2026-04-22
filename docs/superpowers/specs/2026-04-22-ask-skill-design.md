---
date: 2026-04-22
status: approved
owners: Ivantseng123
---

# Ask Assistant — Local Skill for `@bot ask`

## Problem

`@bot ask` 目前靠 workflow goal prompt + output_rules 以外，沒有任何行為規範：

- Agent 拿到 thread + optional repo 之後「自由發揮」——想挖 code 就挖、想改 code 也沒人擋、想開 issue 也沒人擋、想偏題回答閒聊也 OK。
- Issue 跟 PR Review 有各自的 skill（`triage-issue`、`github-pr-review`）做為「行為護城河」：告訴 agent 用什麼流程、做什麼 / 不做什麼、輸出長什麼樣。Ask 少了這層，結果就是 Ask 的回答品質跟直接去 claude.ai 問差不多——**用這個 bot 就沒意義了**。
- 使用者陳述：「純 thread 問問題自由發揮，就不需要這個 bot 了。Skill 應該某部分限制範圍邊界。」

結論：Ask 需要一個 local skill 把「邊界」跟「SOP」釘死，讓 Ask 變成一個「範圍明確、拒絕越權、會指路」的對話助手。

## Root Reasoning

- **Ask 的產品定位是「結構化答題」，不是「萬能助手」**：skill 是結構的承載媒介。
- **Skill 形式已經是既有模式**：`agents/skills/<name>/SKILL.md` + `skills.yaml` 註冊；app 的 `submitJob` 把所有 skills mount 給 worker；agent 靠 skill 的 `description` 自動挑。Ask skill 只是再加一個——零新 infra。
- **Ask workflow 程式碼不需改**：`submitJob` 已經把所有 loaded skills 塞進 job（`ask.go` 裡 `Skills: nil` 的那行其實是 dead code，被 `app.go:286` `job.Skills = loadSkills(...)` 覆蓋）。加 skill 就是純資源檔動作。

## Goals

- 新增 local skill `ask-assistant`，位置 `agents/skills/ask-assistant/SKILL.md`。
- 把使用者確認的 **動作邊界** 跟 **主題邊界** 寫進 skill body。
- 註冊到 `skills.yaml`。
- `ask-assistant` 的 `description` 清楚到只有 Ask workflow 的 prompt 會觸發 agent 使用它（Issue / PR Review agent 不會誤挑）。
- 把 `ask.go` BuildJob 裡誤導的 `Skills: nil` 註解 / 指派刪掉（dead code，讓檔案更誠實）。

## Non-Goals

- **不改 Ask workflow 的程式碼結構**：phase 流程、prompt goal、output_rules 都不動。Skill 是行為指引，不是流程改造。
- **不改其他 skills**：`triage-issue` / `github-pr-review` / `mantis` 維持現狀。
- **不為 Ask 開 scripts/ 子資料夾**：Ask 的邊界都是政策性規則，不需執行邏輯；未來若需要分類判斷類腳本再補。
- **不硬性禁止偏題問題的回答**：使用者確認策略是「儘量回答 + 附一句建議改用其他 workflow」，只有純偏題（閒聊/翻譯/代寫）才直接婉拒。
- **不做 output format 邊界**：已經由 workflow 的 output_rules 處理（Slack mrkdwn、≤30000 chars、ASK_RESULT JSON）。

## Design

### 1. Skill 檔案位置 + frontmatter

路徑：`agents/skills/ask-assistant/SKILL.md`

```yaml
---
name: ask-assistant
description: Use when answering a general question in a Slack thread triggered by `@bot ask` — covers architecture/behavior/design questions, thread summaries, concept clarifications, tradeoff analysis, and code walkthroughs (with or without an attached repo). Enforces strict read-only boundaries and redirects bug triage to `@bot issue` and PR reviews to `@bot review`. Do NOT use for filing issues, posting PR reviews, committing code, or off-topic queries like translation / creative writing / casual chat.
---
```

`description` 的關鍵字（`@bot ask` / `thread` / `architecture` / `tradeoff` / `read-only` / `redirect`）幫 agent 在有 Issue / PR Review 同場的 skill list 時做出正確選擇。結尾的 `Do NOT use for …` 明確排除干擾。

### 2. Body 大綱

Skill body 分五節，大致 150–220 行：

**#1. Input**
說明 agent 會收到什麼 prompt 內容（thread_context、可能的 repo、extra_description、channel/reporter、language、output_rules），一段話結束。

**#2. Classification（分類決定走哪條 SOP）**
教 agent 分三類：

- *Pure-thread*：無 repo（或 repo 不相關），問題是對話摘要、概念釐清、建議、tradeoff 分析 → 走 §3a。
- *Codebase*：有 repo，問 code / 架構 / 行為 / 「X 在哪」 → 走 §3b。
- *Punt-worthy*：命中 §5 主題邊界規則的 → 走 §5。

**#3a. Pure-thread SOP**
- 從 thread_context 提取關鍵事實，不擅自臆造。
- 若 thread 資訊不足：直接承認「thread 沒有足夠線索判斷 X」，不硬答。
- 結構化回覆：*簡答* → *依據* → *延伸*（只在有真貨時才寫延伸）。

**#3b. Codebase SOP**
- **允許的輕量指令**：`git log` / `git show` / `git diff` / `grep` / `rg` / `find` / `cat` / `ls`。
- **禁止**：`go test` / `npm test` / `make` / build / `go run` / 任何會修改狀態的指令。
- 引用一律 `path/to/file.ext:LINE` 格式；> 3 處用清單。
- 不確定就講不確定——不補編故事。

**#4. 動作邊界（A 類，hard no）**
- 只讀：不 `git commit` / `push` / 開 branch / 改任何檔案（包括 temp）。
- 不開票：不 `gh issue create` / `gh pr create` / `gh pr review`。
- 不跑耗時指令：> 10 秒預期或需要網路抓資料的指令都不跑；真的要深度分析就走 §5 punt。
- 不碰 secrets：不讀 `.env*`、不 `printenv`、不把 env var value 印出來。
- 不外連：不 `curl` 外部 URL、不發 webhook；skill（如 mantis）提供的受控通道除外。

**#5. 主題邊界（B 類，軟性引導 + 純偏題婉拒）**

*策略：先盡力回答可見層面，結尾加一句建議。*

| 觸發條件 | 建議 closing 一句 |
|----------|-------------------|
| 有 stack trace / 明講「壞了」/ 需要追 root cause | 「想追完整 root cause + TDD fix plan 請改用 `@bot issue`」|
| 貼了 PR URL 要求檢查 | 「想要 line-level review 請改用 `@bot review <url>`」|
| 想要我改 code / 寫 patch | 「實際改動請開 issue 讓 worker 正式處理」|

*純偏題*（例外情境，直接婉拒，不硬答）：

- 閒聊（天氣、食物、星座、八卦）
- 翻譯、代寫（email、履歷、情書、行銷文案）
- 通用 LLM 挑戰題（haiku、謎題、roleplay）

→ 統一回：「這超出我的職責範圍（工程/專案相關問題為主）。需要一般對話協助請使用一般 LLM。」

**#6. Output 對齊**
引用 workflow 已經設定的 output_rules：Slack mrkdwn / 無 heading marker / `ASK_RESULT` 包 JSON / ≤30000 chars。Skill 不重複這些規則，只強調「不要破壞」。

### 3. `skills.yaml` 註冊

追加一段：

```yaml
skills:
  # ... existing ...
  ask-assistant:
    type: local
    path: agents/skills/ask-assistant
```

### 4. `ask.go` 清理 dead code

BuildJob 內的：

```go
// Skills intentionally nil — Ask flow defensive until empty-dir skill
// spike (Phase 4) observed-safe for a release cycle.
Skills: nil,
```

直接移除。理由：`app/app.go:286` 的 `job.Skills = loadSkills(ctx, skillLoader, appLogger)` 會覆蓋所有 workflow 的 `Skills` 欄位，這個 `nil` 根本沒生效。保留只會讓下一個讀 code 的人誤以為 Ask 沒載 skill。

## 驗收

- `@bot ask` 附 repo 問「X 在哪裡」→ 回答含 `path:line` 引用
- `@bot ask` 貼 stack trace 說「這壞了」→ agent 給初步推論 + 結尾附「建議 `@bot issue`」
- `@bot ask 翻譯這段英文` → agent 婉拒並指引一般 LLM
- `@bot ask 剛才我們討論的結論是什麼？` → agent 摘要 thread，結構化輸出
- worker log 可見 agent 有讀取 `ask-assistant` skill
- agent 沒在 Ask 任務裡執行 `go test` / 寫檔 / `gh issue create`

## 部署

1. 新增 skill 檔（`agents/skills/ask-assistant/SKILL.md`）
2. 更新 `skills.yaml`
3. 刪除 `ask.go` BuildJob 的 `Skills: nil` 指派 + 舊註解
4. 重啟 app（skill loader 會 warm up + 啟動 watcher 自動 reload）
5. 手動跑驗收 4 個 case

## Rollback

Skill 是純資源檔，rollback 直接：

- 從 `skills.yaml` 拿掉 `ask-assistant` 那段 → 下次 warm-up 後 agent 就看不到
- 如果需要完整移除，刪 `agents/skills/ask-assistant/` 資料夾

不涉及 schema / runtime / wire change，沒有 migration 成本。
