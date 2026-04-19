# 部署

[English](deployment.en.md)

## Local（In-Memory 模式）

```bash
./run.sh
# 或
go build -o bot ./cmd/bot/ && ./bot -config config.yaml
```

## Local（Redis 模式）

```bash
# 啟動 Redis
redis-server --daemonize yes

# App（處理 Slack 事件、建 issue）
./bot -config config.yaml   # config 裡 queue.transport: redis

# Worker（消費 job、跑 agent）— 可以開多個
./bot worker -config worker.yaml
```

## 外部 Worker（同事電腦）

同事不需要任何 config 檔案，只需要 binary + 環境變數：

```bash
# 前置條件：已安裝 agent CLI 並登入（例如 claude login）
REDIS_ADDR=redis.company.com:6379 GITHUB_TOKEN=ghp_xxx ./bot worker
```

自訂 agent：
```bash
REDIS_ADDR=redis.company.com:6379 GITHUB_TOKEN=ghp_xxx PROVIDERS=codex ./bot worker
```

Worker 內建三個 agent 的預設 config（claude/codex/opencode），不需要 YAML。Redis 地址和 token 透過環境變數傳入。

### External Worker 依賴

如果你下載 GitHub Release 附的 binary 在外部機器跑 `bot worker`，**binary 不是 self-contained**。Worker 會 `exec` 以下 CLI，請先自行安裝並確認在 `PATH` 中：

- **至少一個 agent CLI**（config 裡選定的那個）：
  - `@anthropic-ai/claude-code`（npm）
  - `@openai/codex`（npm）
  - `opencode`（見 [anomalyco/opencode](https://github.com/anomalyco/opencode) releases）
  - `gemini`（如有使用）
- **`gh` CLI**（建立 GitHub issue 用）
- **`git`**（clone repo）

若不想自行管理這些依賴，改用 Docker image：`ghcr.io/ivantseng123/agentdock:<version>` 已預裝全部 runtime。

**Windows 備註**：上述 CLI 的 Windows 原生支援由上游廠商提供，若遇相容性問題建議改用 Docker image（需 WSL2 或 Linux VM）。

## Redis 模式架構

```
┌─────────────┐                    ┌─────────────┐
│  App Pod    │                    │ Worker Pod  │
│             │    Redis Streams   │             │
│ Slack ──→   │──── JobQueue ────→│ consume job │
│ Workflow    │                    │ clone repo  │
│             │←── ResultBus ────│ run agent   │
│ create issue│←── StatusBus ────│ report      │
│ post Slack  │──── CommandBus ──→│ kill signal │
└─────────────┘                    └─────────────┘
```

App 不跑 agent。Worker 不需要 Slack token 或 GitHub write token。

## Docker

Image 包含三個 agent CLI：claude、codex、opencode。

> **注意：Docker 容器只能使用 API key 認證，不支援 OAuth 登入。** Agent CLI 的 OAuth（如 `claude login`）綁定本機 keychain，無法移植到容器內。個人電腦使用 OAuth 的場景請用上方的「外部 Worker」方式（native binary）。

```bash
docker build -t agentdock .

# App（Slack 端）
docker run -e SLACK_BOT_TOKEN=xoxb-... \
           -e SLACK_APP_TOKEN=xapp-... \
           -e GITHUB_TOKEN=ghp_... \
           -e REDIS_ADDR=redis:6379 \
           agentdock app

# Worker（獨立消費 job）
docker run -e REDIS_ADDR=redis:6379 \
           -e GITHUB_TOKEN=ghp_... \
           -e PROVIDERS=claude \
           -e ANTHROPIC_API_KEY=sk-ant-... \
           agentdock worker
```

### Agent 認證方式比較

| 執行方式 | 認證方式 | 適用場景 |
|---------|---------|---------|
| Native binary (`./bot worker`) | OAuth 登入（`claude login` 等） | 個人電腦，用自己的 Pro/Max 額度 |
| Docker / k8s | API key（環境變數） | 自動化部署，公司付費的 API 額度 |

### Agent 選擇與 API Key

Worker 透過 `PROVIDERS` 環境變數選擇要使用的 agent（逗號分隔，依序嘗試），不需要修改 config 檔：

```bash
# 用 claude
docker run -e PROVIDERS=claude -e ANTHROPIC_API_KEY=sk-ant-... ...

# 用 codex，fallback 到 claude（依序嘗試）
docker run -e PROVIDERS=codex,claude -e OPENAI_API_KEY=sk-... -e ANTHROPIC_API_KEY=sk-ant-... ...

# 用 opencode
docker run -e PROVIDERS=opencode -e ANTHROPIC_API_KEY=sk-ant-... ...
```

| Agent | API Key 環境變數 | 取得方式 |
|-------|-----------------|---------|
| claude | `ANTHROPIC_API_KEY` | [console.anthropic.com](https://console.anthropic.com) |
| codex | `OPENAI_API_KEY` | [platform.openai.com](https://platform.openai.com) |
| opencode | `ANTHROPIC_API_KEY` | [console.anthropic.com](https://console.anthropic.com) |

只需要傳 `PROVIDERS` 裡列出的 agent 的 API key。

### 所有環境變數

| 環境變數 | 用途 | 必要 |
|---------|------|------|
| `SLACK_BOT_TOKEN` | Slack Bot token | App 模式 |
| `SLACK_APP_TOKEN` | Slack App-Level token | App 模式 |
| `GITHUB_TOKEN` | GitHub token（App: read+write, Worker: read） | 是 |
| `REDIS_ADDR` | Redis 連線地址 | Redis 模式 |
| `REDIS_PASSWORD` | Redis 密碼 | 有密碼時 |
| `PROVIDERS` | Agent provider 順序（逗號分隔） | 否（預設用 config） |
| `ACTIVE_AGENT` | 主要 agent | 否（預設用 config） |
| `CLAUDE_AUTH_TOKEN` | Claude CLI auth | 用 claude 時 |
| `OPENAI_API_KEY` | Codex CLI auth | 用 codex 時 |
| `ANTHROPIC_API_KEY` | OpenCode CLI auth | 用 opencode 時 |

## Kubernetes

使用 Kustomize：

```
deploy/
  base/                          # 通用 deployment（進 repo）
    kustomization.yaml
    deployment.yaml
  overlays/
    example/                     # 範本（進 repo）
      kustomization.yaml.example
      secret.yaml.example
    <your-env>/                  # 實際設定（gitignored）
      kustomization.yaml
      secret.yaml
```

```bash
cp deploy/overlays/example/*.example deploy/overlays/my-env/
vi deploy/overlays/my-env/kustomization.yaml
vi deploy/overlays/my-env/secret.yaml
kubectl apply -k deploy/overlays/my-env/
```

## CI/CD

Automated via [release-please](https://github.com/googleapis/release-please)：

1. 寫 Conventional Commits（`feat:`, `fix:`, `chore:` 等）
2. release-please 自動維護 Release PR（version bump + CHANGELOG）
3. Merge Release PR → 自動建 GitHub Release + tag
4. GHA build Docker image → push 到 `ghcr.io`
