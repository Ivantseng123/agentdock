# AgentDock v2 Migration Guide

[English](MIGRATION-v2.en.md)

**TL;DR**：單一 `config.yaml` 拆成 `app.yaml` + `worker.yaml`，手動重建一次就好。現有 3 台 worker + 1 個 app pod 各自升級。

## 為什麼要拆

App 和 Worker 在 v2 成為完全獨立的 Go module。App 負責 Slack、submit job；Worker 負責 agent CLI 執行。兩邊的 config schema 因此也拆開，以便未來獨立演化（甚至拆 repo）。

## 步驟總覽

1. 升級 binary 到 v2.0.0（K8s image、brew 等）
2. 手動重建 `app.yaml` 和 `worker.yaml`
3. 更新部署（K8s ConfigMap、worker 機啟動指令）

## 重建 Config

### App

```bash
agentdock init app -c ~/.config/agentdock/app.yaml -i
```

互動式會問 Slack bot/app token、GitHub token、Redis addr、secret_key（Redis 模式時可自動產生 32-byte 金鑰並印出，複製到 worker.yaml）。

### Worker

```bash
agentdock init worker -c ~/.config/agentdock/worker.yaml -i
```

互動式會問 GitHub token、Redis addr、secret_key（必須與 app 一致）、providers。Built-in agents（claude / codex / opencode）由 worker 啟動時自動補入，不再寫進 `agents:` 區塊（詳見 〈v2.6 → v2.7〉 段落）。

## 欄位對照表

舊 `config.yaml` → 新 `app.yaml` 或 `worker.yaml`：

| 舊欄位 | 新欄位 | 檔案 |
|---|---|---|
| `slack.*` | `slack.*` | app.yaml |
| `channels.*` | `channels.*` | app.yaml |
| `channel_defaults.*` | `channel_defaults.*` | app.yaml |
| `auto_bind` | `auto_bind` | app.yaml |
| `max_thread_messages` | `max_thread_messages` | app.yaml |
| `max_concurrent` | **（移除）**，之前已 deprecated | — |
| `rate_limit.*` | `rate_limit.*` | app.yaml |
| `mantis.*` | `mantis.*` | app.yaml |
| `channel_priority.*` | `channel_priority.*` | app.yaml |
| `prompt.goal` / `prompt.output_rules` / `prompt.language` / `prompt.allow_worker_rules` | 同名 | app.yaml |
| `skills_config` | `skills_config` | app.yaml |
| `attachments.*` | `attachments.*` | app.yaml |
| `server.port` | `server.port` | app.yaml |
| `agents.*` | `agents.*` | **worker.yaml** |
| `active_agent` | **（v2.5 移除）** 改用 `providers: [<name>]`；舊 yaml 中的 `active_agent:` 欄位會被 loader 忽略並 log warn | worker.yaml |
| `providers` | `providers` | **worker.yaml** |
| `worker.count` | **`count`**（扁平） | worker.yaml |
| `worker.prompt.extra_rules` | **`prompt.extra_rules`**（扁平） | worker.yaml |
| `github.token`, `redis.*`, `logging.*`, `repo_cache.*`, `queue.*`, `secret_key`, `secrets.*` | 同名 | **app.yaml 和 worker.yaml 都要有**（各自獨立） |

重點：

- **`max_concurrent` 移除**。它原本就是 deprecated（要用 `workers` / `worker.count`），v2 直接砍掉。
- **Worker schema 扁平化**：`worker.count` → `count`，`worker.prompt.extra_rules` → `prompt.extra_rules`。`worker.yaml` 檔案本身已經在 worker scope，不需要再包一層。
- **`github.token` / `redis.*` / `secret_key` 要同時寫在兩個檔案**。v2 沒有共享 config；app 和 worker 各自讀自己的檔案。

## K8s ConfigMap 拆分

舊：

```yaml
volumeMounts:
  - name: config
    mountPath: /etc/agentdock/config.yaml
    subPath: config.yaml
args: ["app", "-c", "/etc/agentdock/config.yaml"]
```

新：

```yaml
volumeMounts:
  - name: app-config
    mountPath: /etc/agentdock/app.yaml
    subPath: app.yaml
args: ["app", "-c", "/etc/agentdock/app.yaml"]
```

Worker deployment：

```yaml
args: ["worker", "-c", "/etc/agentdock/worker.yaml"]
```

> v2.1 起 inmem transport 被移除，app pod 不再需要同時掛載 `worker.yaml`，worker 一律獨立 deployment。詳見下面 〈v2.0 → v2.2 後續變更〉。

## Worker 機（本地啟動）

```bash
brew upgrade agentdock         # 或其他升級方式
agentdock init worker -i
agentdock worker               # 預設讀 ~/.config/agentdock/worker.yaml
```

## 常見問題

- **Q：啟動報 `config file not found: ~/.config/agentdock/app.yaml`** → 執行 `agentdock init app -i` 重建。
- **Q：worker 啟動時 preflight 報 `secret_key 與 app 不匹配`** → `secret_key` 值跟 app 不一樣。從 app pod 拿 secret_key 貼到 worker.yaml。
- **Q：看到 `未知設定鍵 key=worker.count`** → schema 扁平化，把 `worker.count` 改成 `count`、`worker.prompt.extra_rules` 改成 `prompt.extra_rules`。

## v2.0 → v2.2 後續變更

v2.0 是 config 拆檔的大改版。之後的 2.x 小版本大多是加法，只有一件真正的 breaking：

### v2.0 → v2.1：inmem transport 移除（**breaking**）

v2.0 還支援 `queue.transport: inmem`（app 和 worker 跑在同一個 process）。v2.1 整段砍掉，只剩 `redis`。升級步驟：

1. 兩邊（`app.yaml` / `worker.yaml`）都把 `queue.transport` 改成 `redis`。
2. 兩邊都填 `redis.addr`、`secret_key`（同一把）。
3. Worker 改成獨立 deployment / 獨立 process 啟動。

啟動時若還留著 `queue.transport: inmem` 會跳 `unsupported queue.transport`。

其他變更（additive，舊 yaml 照跑）：

- `nickname_pool`（worker.yaml）— 隨機暱稱池。
- `worker.github.token` 變成可選 — app 透過 `secrets["GH_TOKEN"]` 發給 worker 就夠。

### v2.1 → v2.2：無 breaking

新增 `github-pr-review` skill 和 `agentdock pr-review-helper` subcommand。v2.2 發佈當下 PR Review workflow 要用 `pr_review.enabled: true` 才會啟用（v2.3.x 之後預設改為開啟，要關改寫 `enabled: false`）。

### v2.2 + workflow-types（PR #124）：prompt schema 重組 + PR Review feature flag

三個動詞（`issue` / `ask` / `review`）+ 各自 prompt。改動都是 additive，舊 app.yaml 不改也能跑，但建議遷移。

1. **Prompt 改成 per-workflow nested**：

   舊（flat，還支援但視為 legacy alias）：

   ```yaml
   prompt:
     goal: "Use the /triage-issue skill ..."
     output_rules: [...]
   ```

   新：

   ```yaml
   prompt:
     language: 繁體中文
     allow_worker_rules: true
     issue:
       goal: "..."
       output_rules: []
     ask:
       goal: "..."
       output_rules: [...]
     pr_review:
       goal: "..."
       output_rules: [...]
   ```

   載入時 `prompt.goal` / `prompt.output_rules`（flat）會被拷到 `prompt.issue.*`（前提是 `prompt.issue.*` 沒設）。每個 workflow 任何欄位留空都會 fallback 到 `app/config/defaults.go` 的 hardcoded 預設，所以整個 `prompt:` 區塊留空也 OK。

2. **PR Review feature flag（預設停用）**：

   ```yaml
   pr_review:
     enabled: false
   ```

   打開前先確認 worker 那側有 `github-pr-review` skill 可用、`agentdock pr-review-helper` subcommand 可執行、`secrets.GH_TOKEN` 有在目標 PR 留 review 的權限。

3. **Slack `/triage` slash command 變成 fallback**：實際觸發改成 `@bot <verb>`（`issue` / `ask` / `review`）。舊的 `/triage` 仍註冊，被呼叫會回一條提示叫使用者改用 `@bot`。不需要改 Manifest 就能繼續運作，但建議把 slash command 的 `description` 更新成 legacy 說明。

欄位完整說明：[configuration-app.md](configuration-app.md#workflow-specific-prompts)、[configuration-app.md](configuration-app.md#pr-review-啟用)。

### v2.2 → v2.3：`workflows:` 升格為頂層、`prompt_defaults:` 分出（additive，有 legacy alias）

**動機**（issue #126）：舊 schema 有兩個結構問題——`prompt:` 同時當 prompt 容器與 workflow 容器（層級反了），而同一個 workflow 的屬性被拆到 `prompt.pr_review` 和頂層 `pr_review` 兩個 block。新 schema 把 workflow 升格為頂層、prompt 縮進到 workflow 底下，跨 workflow 共用的 `language` / `allow_worker_rules` 獨立成 `prompt_defaults:`。

**新 shape（推薦）**：

```yaml
workflows:
  issue:
    prompt:
      goal: "..."
      response_schema: "..."
      output_rules: []
  ask:
    prompt:
      goal: "..."
      output_rules: [...]
  pr_review:
    enabled: false          # PR Review workflow feature flag 現在住這裡
    prompt:
      goal: "..."
      output_rules: [...]

prompt_defaults:
  language: 繁體中文
  allow_worker_rules: true
```

**Legacy alias（自動，沒遷移也能跑）**：

| 舊寫法 | 映射到 | 備註 |
|---|---|---|
| `prompt.goal` / `prompt.response_schema` / `prompt.output_rules`（Old-A，v2.1 以前的 flat 寫法） | `workflows.issue.prompt.*` | 只有在 `workflows.issue.prompt.*` 還沒設時才會被搬 |
| `prompt.issue.*` / `prompt.ask.*` / `prompt.pr_review.*`（Old-B，v2.2 中間型） | `workflows.issue.prompt.*` / `workflows.ask.prompt.*` / `workflows.pr_review.prompt.*` | 同樣只在新欄位空著時搬 |
| `prompt.language` / `prompt.allow_worker_rules` | `prompt_defaults.language` / `prompt_defaults.allow_worker_rules` | 兩個 legacy 版本都支援 |
| 頂層 `pr_review.enabled`（Old-B） | `workflows.pr_review.enabled` | |

**混用規則**：新舊欄位同時出現在一份 yaml 時，以 `workflows:` / `prompt_defaults:` 為準，啟動時會 log 一條 `component=config phase=載入` warning 建議移除 legacy 區塊。

**建議遷移步驟**：

1. `prompt.issue.*` → `workflows.issue.prompt.*`（ask / pr_review 同理）。
2. 頂層 `pr_review.enabled` → `workflows.pr_review.enabled`。
3. `prompt.language` / `prompt.allow_worker_rules` → `prompt_defaults.*`。
4. 刪掉舊的 `prompt:` / 頂層 `pr_review:` 區塊。

最省事：`agentdock init app -c app-new.yaml --force`（會直接產出新 shape），再把你自訂的 goal / output_rules 搬進去。

### v2.5 → v2.6：`queue.store` 新欄位（**預設 redis，升級後行為會變**）

v2.6 把 `RedisJobStore`（#145 / PR #147）接進 App（#146），並把 `queue.store` 預設設為 `redis`，讓生產部署預設就能在 app 重啟後 resume in-flight job（#123）。

**⚠️ 升級影響（既有 yaml 沒設 `queue.store` 的情況）**

- **行為改變**：app 重啟從「掉所有 in-flight state（`:hourglass:` 卡住）」變成「從 Redis 讀回來、正確完成 Slack thread」。生產端是看的是 bug 修好。
- **Redis 流量增加**：多 ~0.6 × N QPS（N = active worker 數），來自 ResultListener / StatusListener 每個 StatusReport 週期的 `store.Get` + 必要時的 `UpdateStatus`。Sizing Redis 時納入考量，對一般規模 Redis 可忽略。
- **TTL 預設 1h**：每次寫入 refresh；若 job 跑超過 1h 可能被 TTL evict，請把 `queue.store_ttl` 設得比最長預期 job runtime 大。

**想保留 v2.5 的 in-process 行為**（local dev / 單 pod 測試、不想依賴 Redis 持久化），在 `app.yaml` 明確寫 `mem`：

```yaml
queue:
  # ... 其他欄位不變 ...
  store: mem            # 明確 opt-out，回到 v2.5 行為：app 重啟時 in-flight job state 會掉
```

只影響 app，worker.yaml 不用動（worker 不讀 JobStore，只產 JobResult / StatusReport）。

**啟動行為**：app 走 `redis` 路徑時會 `ListAll()` 一次並 log `rehydrated in-flight jobs from previous instance`（數量 = 非 terminal state 筆數）。Terminal state 讓 TTL evict，不主動刪。

背景：[#123](https://github.com/Ivantseng123/agentdock/issues/123) incident——app 重啟時 in-flight Slack thread 被 orphan（`:hourglass:` 停留不會消失），即使 worker 把 result 推出來也沒人接。

### v2.6 → v2.7：`init worker` 不再凍結 BuiltinAgents 快照

**行為變化**：`agentdock init worker` 產出的 `worker.yaml` 不再包含 `agents:` 區塊。Worker 啟動時改由 `mergeBuiltinAgents` 從當下 binary 補齊所有內建值（claude / codex / opencode）。

**已有 `agents:` 區塊的 user（非 breaking）**：現有 yaml 仍正常運作。`mergeBuiltinAgents` 只補缺少的 entry，不覆寫已存在的。如果你的 yaml 裡有舊版的 opencode 設定（例如缺少 `--pure`），worker 仍會沿用舊設定。

**取得最新內建設定的步驟**：

```bash
# 刪掉 agents: block（或整個 agents: 段落），重啟即可
# 若只想更新特定 agent，刪掉該 agent 的 entry 就好
```

具體例子：PR #108 為 opencode 加入 `--pure` 旗標。沒有刪 `agents.opencode` 的 user 在升級後仍不會拿到 `--pure`；刪了就能自動生效。

**想覆寫特定欄位**：只寫需要改的部分即可；未寫的欄位由 BuiltinAgents 補齊：

```yaml
agents:
  opencode:
    timeout: 30m  # 只改 timeout，command/args/skill_dir 沿用內建
```
