# 設定

[English](configuration.en.md)

AgentDock v2 的 config 拆成兩個檔案：

- [App 設定（configuration-app.md）](configuration-app.md) — Slack bot、channels、rate limit、Mantis、prompt 指示
- [Worker 設定（configuration-worker.md）](configuration-worker.md) — agents、providers、worker count、repo cache

外部系統設定步驟：
- [Slack App 設定](slack-setup.md) — 建 Slack App、scopes、socket mode
- [GitHub App 設定](github-app-setup.md) — 建 GitHub App、permissions、private key（PAT 也可）

如果你從 v1 升級，請看 [MIGRATION-v2.md](MIGRATION-v2.md)。從 PAT 切換到 GitHub App 請看 [MIGRATION-github-app.md](MIGRATION-github-app.md)。

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
