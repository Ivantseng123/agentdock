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

互動式會問 GitHub token、Redis addr、secret_key（必須與 app 一致）、providers。Built-in agents（claude / codex / opencode）會自動寫進 `agents:` 區塊。

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
