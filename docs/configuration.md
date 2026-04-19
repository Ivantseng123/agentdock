# 設定

[English](configuration.en.md)

AgentDock v2 的 config 拆成兩個檔案：

- [App 設定（configuration-app.md）](configuration-app.md) — Slack bot、channels、rate limit、Mantis、prompt 指示
- [Worker 設定（configuration-worker.md）](configuration-worker.md) — agents、providers、worker count、repo cache

如果你從 v1 升級，請看 [MIGRATION-v2.md](MIGRATION-v2.md)。

## 快速開始

```bash
agentdock init app -i       # 建立 ~/.config/agentdock/app.yaml，互動式問 Slack/GitHub/Redis
agentdock init worker -i    # 建立 ~/.config/agentdock/worker.yaml，問 GitHub/Redis/secret/providers
```

然後分兩個 process 啟動：

```bash
agentdock app -c ~/.config/agentdock/app.yaml
agentdock worker -c ~/.config/agentdock/worker.yaml
```

兩邊的 `queue.transport` 必須一致（目前僅支援 `redis`），兩邊的 `secret_key` 也必須相同。
