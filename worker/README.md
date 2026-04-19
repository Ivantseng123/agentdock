# worker/

[English](README.en.md)

AgentDock 的 agent 執行端。`agentdock worker` 的程式碼 module，獨立成 Go module（`github.com/Ivantseng123/agentdock/worker`）。

## 責任範圍

- 從 queue 拉 job
- Clone / cache repo，掛 skill 檔到 agent 可見的目錄
- 呼叫 agent CLI（claude / codex / opencode / gemini）並傳 prompt
- 追 PID + 心跳狀態，回報給 status bus
- 接 kill 指令（command bus）
- 解密 app 送過來的 secrets，注入為子進程 env var

Worker **不直接跟 Slack 或 GitHub issue 打交道**；那是 app 的責任。

## Prerequisites

Host 需安裝至少一個 agent CLI：

- `claude`（[Claude CLI](https://github.com/anthropics/claude-code)） — 支援 stream-json
- `codex` — non-streaming
- `opencode`（[OpenCode](https://github.com/opencode-ai/opencode)）
- 其他也行，只要能吃 `{prompt}` placeholder

PATH 裡找不到 agent binary 的話，preflight 會直接失敗。

## 設定

詳細 schema 見 [docs/configuration-worker.md](../docs/configuration-worker.md)。

快速建立：

```bash
agentdock init worker -i -c ~/.config/agentdock/worker.yaml
```

> `secret_key` 必須跟 app 的一致。Beacon 驗證失敗會拒啟動。

啟動：

```bash
agentdock worker -c ~/.config/agentdock/worker.yaml
```

## 模式

Worker 作為獨立 process 跑，透過 `queue.transport` 指定的 backend 與 app 溝通（目前只支援 `redis`）。

## 測試

```bash
(cd worker && go test ./... -race)
```

## 依賴

- `shared/` — queue、github、logging、crypto、prompt、connectivity
- 不依賴 `app/`（worker ✗ app 邊界強制）

## 相關文件

- [頂層 README](../README.md)
- [app/README.md](../app/README.md)
- [docs/configuration.md](../docs/configuration.md)
- [docs/MIGRATION-v2.md](../docs/MIGRATION-v2.md)
