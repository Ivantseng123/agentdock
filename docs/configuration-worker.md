# Worker 設定 (`worker.yaml`)

[English](configuration-worker.en.md)

`agentdock worker` 讀取的 YAML。預設位置：`~/.config/agentdock/worker.yaml`。跑 `agentdock init worker -i` 可自動產生。

> **v2 變更**：schema 扁平化。`worker.count` → `count`，`worker.prompt.extra_rules` → `prompt.extra_rules`。worker.yaml 本身已經在 worker scope，不需要再包一層。

## Schema

```yaml
log_level: info                       # console / stderr 層級

logging:
  dir: logs
  level: debug
  retention_days: 30
  agent_output_dir: logs/agent-outputs

github:
  token: ghp-...                      # REQUIRED：agent clone / push 時使用

agents:
  claude:
    command: claude
    args: ["--print", "--output-format", "stream-json", "-p", "{prompt}"]
    timeout: 15m
    skill_dir: .claude/skills
    stream: true                      # 啟用即時事件追蹤
  codex:
    command: codex
    args: ["exec", "--skip-git-repo-check", "--color", "never", "{prompt}"]
    timeout: 15m
    skill_dir: .agents/skills         # Codex 讀 .agents/skills，不是 .codex/skills
  opencode:
    command: opencode
    args: ["run", "{prompt}"]
    timeout: 15m
    skill_dir: .opencode/skills

active_agent: claude                  # 單一 agent 模式
providers: [claude, codex, opencode]  # fallback chain（依序嘗試）

count: 3                              # worker goroutine 數（扁平！舊是 worker.count）

prompt:
  extra_rules:                        # worker 端補上的規則（扁平！舊是 worker.prompt.extra_rules）
    - "列出所有相關的檔案名稱與完整路徑"
    - "不要猜測，不要虛構"

repo_cache:
  dir: /var/cache/agentdock/repos     # 必須是絕對路徑
  max_age: 10m

queue:
  capacity: 50
  transport: redis
  job_timeout: 20m
  agent_idle_timeout: 5m
  prepare_timeout: 3m
  cancel_timeout: 60s
  status_interval: 5s

redis:
  addr: redis:6379
  password: ""
  db: 0
  tls: false

secret_key: 跟-app-同一把             # REQUIRED：從 app.yaml 複製過來

secrets:
  GH_TOKEN: ghp_worker_override       # 可選：覆蓋 app 給的值
```

## Agent Stream

Claude 支援 `--output-format stream-json`，開啟 `stream: true` 可即時追蹤：
- 目前在用什麼 tool（Read, Bash, Grep...）
- 已讀多少檔案、已生成多少字
- cost_usd / input tokens / output tokens

不支援 stream 的 agent（opencode, codex）只追蹤 PID + 存活狀態。

## Agent Skills

Skills 隨 job 發送過來（`Job.Skills`），worker 在 clone 的 repo 裡寫入 skill 檔（SKILL.md + examples + references），agent CLI 啟動時自動載入 — 不用手動安裝。Skill 目錄取自 `agents[provider].skill_dir`。

## Preflight

`agentdock worker` 啟動時會跑 preflight：

1. `github.token` 是否有效（`GET /user`）
2. `redis.addr` 是否可連
3. `secret_key` 是否與 app 的 beacon 一致
4. `providers` 內的每個 agent CLI 是否可執行（`<cmd> --version`）

Preflight 失敗直接拒啟動；`--log-level debug` 會印細節。

## Secrets

- `github.token` 會自動 merge 進 `secrets["GH_TOKEN"]`
- `AGENTDOCK_SECRET_<NAME>` 環境變數會進 `secrets["<NAME>"]`
- Job 跑起來時，解密後的 `secrets` 注入為 agent 子進程的 env var
