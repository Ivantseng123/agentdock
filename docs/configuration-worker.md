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

# agents: block 可省略。省略時 worker 啟動自動以當下 binary 的 BuiltinAgents
# 填入 claude / codex / opencode 預設值。只有要覆寫特定欄位時才需要寫。
# 升級 binary 後，刪掉（或不寫）agents: block 即可取得最新內建預設值。
#
# agents:
#   opencode:
#     timeout: 30m    # 範例：只覆寫 timeout，其餘欄位沿用內建預設
#     extra_args: ["-m", "opencode/claude-opus-4-7"]
#     # 範例：用 extra_args 插入 model 等旗標，args 裡的 {extra_args}
#     # 會被展開成這裡填的 tokens（不用複製整組 args）

providers: [claude, codex, opencode]  # fallback chain（依序嘗試）；單一 agent 模式：providers: [claude]

count: 3                              # worker goroutine 數（扁平！舊是 worker.count）

nickname_pool: ["小明", "Alice", "Gary"]  # 可選：每個 worker 啟動時隨機抽一個當 Slack 顯示名

prompt:
  extra_rules:                        # worker 端補上的規則（扁平！舊是 worker.prompt.extra_rules）
    - "Do not guess, do not invent"

repo_cache:
  dir: /var/cache/agentdock/repos     # 必須是絕對路徑
  max_age: 10m

queue:
  capacity: 50
  transport: redis
  job_timeout: 35m
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

## extra_args — 注入額外旗標

`extra_args` 讓你只加旗標，**不用複製整組 `args`**。Builtin agent 的 `args` 裡有 `{extra_args}` token；runtime 會把你的 extra_args slice splice 進去。

### 範例

```yaml
agents:
  opencode:
    extra_args: ["-m", "opencode/claude-opus-4-7"]    # 指定 model

  claude:
    extra_args: ["--dangerously-skip-permissions"]     # 跳過 permission prompt（沙箱環境）

  codex:
    extra_args: ["--sandbox", "off"]                  # 關掉 sandbox
```

### 注入位置

| Agent      | `{extra_args}` 位置                            | 原因                                       |
|------------|-----------------------------------------------|--------------------------------------------|
| `claude`   | `stream-json` 後、`-p` 前                      | claude CLI 要求所有 option flag 必須在 `-p` 前 |
| `codex`    | 固定旗標後、`{prompt}` 前                        | `codex exec` 接受 option 在 positional 前   |
| `opencode` | `--pure` 後、`{prompt}` 前                      | `-m`、`--variant` 等旗標都放在 positional 前  |

### 優先序

如果你同時寫了完整的 `args` override **和** `extra_args`，但 `args` 裡沒有 `{extra_args}` token，`extra_args` 會被忽略，啟動時會印 warn log：

```
agent has both args override and extra_args; extra_args ignored
```

建議：**只寫 `extra_args`，不要複製整組 args** — 這樣之後 builtin 更新旗標時你會自動拿到。

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

## Agent 覆寫（選用）

建議省略 `agents:` 區塊 — 啟動時 `mergeBuiltinAgents` 自動填入內建預設值。只有在需要覆寫特定欄位時才需要寫：

| 欄位 | 型別 | 說明 |
|---|---|---|
| `command` | string | 執行檔名稱或路徑 |
| `args` | []string | CLI 引數；`{prompt}` 會被替換為 job 的 prompt 內容 |
| `timeout` | duration | 單一 job 的 wall-clock 上限（例如 `30m`） |
| `skill_dir` | string | Repo 內寫入 skill 檔的相對目錄 |
| `stream` | bool | 啟用即時 JSON 事件解析（僅 claude 支援） |

只需寫要覆寫的欄位；其他欄位沿用 `BuiltinAgents`。範例：

```yaml
agents:
  opencode:
    timeout: 30m    # 只改 timeout，command/args/skill_dir 沿用內建
  claude:
    skill_dir: .claude/custom-skills
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
