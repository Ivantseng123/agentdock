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
| `active_agent` | `active_agent` | **worker.yaml** |
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

Inmem 模式下 app pod 需要同時掛載 worker.yaml：

```yaml
volumeMounts:
  - name: app-config
    mountPath: /etc/agentdock/app.yaml
    subPath: app.yaml
  - name: worker-config
    mountPath: /etc/agentdock/worker.yaml
    subPath: worker.yaml
args:
  - "app"
  - "-c"
  - "/etc/agentdock/app.yaml"
  - "--worker-config"
  - "/etc/agentdock/worker.yaml"
```

Redis 模式下 worker deployment：

```yaml
args: ["worker", "-c", "/etc/agentdock/worker.yaml"]
```

## Worker 機（本地啟動）

```bash
brew upgrade agentdock         # 或其他升級方式
agentdock init worker -i
agentdock worker               # 預設讀 ~/.config/agentdock/worker.yaml
```

## 常見問題

- **Q：啟動報 `config file not found: ~/.config/agentdock/app.yaml`** → 執行 `agentdock init app -i` 重建。
- **Q：`unsupported queue.transport`** → v2.1 起 inmem mode 被移除，`queue.transport` 只接受 `redis`。把兩邊 config 的 `queue.transport` 都改成 `redis` 並填 `redis.addr`。
- **Q：worker 啟動時 preflight 報 `secret_key 與 app 不匹配`** → `secret_key` 值跟 app 不一樣。從 app pod 拿 secret_key 貼到 worker.yaml。
- **Q：看到 `未知設定鍵 key=worker.count`** → schema 扁平化，把 `worker.count` 改成 `count`、`worker.prompt.extra_rules` 改成 `prompt.extra_rules`。
