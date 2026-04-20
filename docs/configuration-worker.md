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

nickname_pool: ["小明", "Alice", "Gary"]  # 可選：每個 worker 啟動時隨機抽一個當 Slack 顯示名

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

## Worker Nicknames（選用）

`nickname_pool` 是一個字串池，worker process 啟動時從中隨機抽 `count` 個不重複的當 Slack 狀態顯示的暱稱。

- 池 **≥** count：每個 worker 各抽一個不重複條目（Fisher–Yates）。
- 池 **<** count：池裡的每個都會被用到一次，剩下的 worker 回退到 `worker-0` / `worker-1` 的機械名。
- 池為空或省略：全部 worker 都顯示 `worker-N`（跟現行行為一致）。
- 每個條目 1–32 runes，前後空白會自動 trim，**允許重複**（池裡填兩個 `"小明"` 就有機會兩個 worker 都叫小明）。
- 暱稱裡的 `<`、`>`、`&` 會在渲染到 Slack 時自動 escape，所以把 `<@U123>` 塞進池不會意外 ping 到人。

Slack 狀態訊息會從冷冰冰的 `:gear: 準備中 · worker-0` 改為擬人化版本：

- 準備階段：`:toolbox: 小明 正在暖機...`
- 執行中：`:fire: 小明 開工啦！(claude) · 奮鬥 1m23s`
- 統計行：`小明 已經敲了 15 次工具、翻了 8 份檔`

沒設暱稱時 `worker-N` 還是會套用同樣的句型（robot-worker 人設）。

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
