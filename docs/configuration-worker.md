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

## Agent 覆寫（選用）

建議省略 `agents:` 區塊 — 啟動時 `mergeBuiltinAgents` 自動填入內建預設值。只有在需要覆寫特定欄位時才需要寫：

| 欄位 | 型別 | 說明 |
|---|---|---|
| `command` | string | 執行檔名稱或路徑 |
| `args` | []string | CLI 引數；`{prompt}` 會被替換為 job 的 prompt 內容；`{extra_args}` 會被下方 `extra_args` 展開 |
| `extra_args` | []string | 注入到 `args` 中 `{extra_args}` 位置的額外 flag（見下節） |
| `timeout` | duration | 單一 job 的 wall-clock 上限（例如 `30m`） |
| `skill_dir` | string | Repo 內寫入 skill 檔的相對目錄 |
| `stream` | bool | 啟用即時 JSON 事件解析（僅 claude 支援）。**注意**：bool 無法區分「沒寫」vs「寫 false」，所以只寫 `stream: false` 在內建 agent 的 partial override 區塊裡**不會生效**；要關掉內建的 stream，請同時覆寫 `command` + `args`（整段 block 重寫）。 |

只需寫要覆寫的欄位；其他欄位沿用 `BuiltinAgents`。範例：

```yaml
agents:
  opencode:
    timeout: 30m    # 只改 timeout，command/args/skill_dir 沿用內建
  claude:
    skill_dir: .claude/custom-skills
```

### `extra_args`（每個 agent 追加 flag）

想給內建 agent 塞一兩個 flag（例如幫 opencode 鎖定 model）不必整段 `args` 複製一份。內建 `args` 都預留了 `{extra_args}` placeholder，`extra_args` 會在 runtime 展開成 0~N 個引數：

```yaml
agents:
  opencode:
    extra_args: ["-m", "opencode/claude-opus-4-7"]
  codex:
    extra_args: ["--sandbox", "read-only"]
```

對應到實際啟動命令：

- `opencode run --pure -m opencode/claude-opus-4-7 "{prompt}"`
- `codex exec --skip-git-repo-check --color never --sandbox read-only "{prompt}"`
- `claude --print --output-format stream-json <extra_args...> -p "{prompt}"`

**為什麼要這個：** 原本唯一加 flag 的方法是整段覆寫 `agents.opencode`，一旦 binary 升級、內建 `args` 變了（例如 `--pure` 是 v2.2 才加的），你的 snapshot 就落後。`extra_args` 讓你繼續吃內建預設值，只疊自己的 flag。

**Placeholder 位置（已由 binary 固定，operator 不用管）：**

- `claude`: `{extra_args}` 在 `stream-json` 之後、`-p` 之前（claude 要求 flags 都要在 `-p` 前）
- `codex`: 在 `--color never` 之後、`{prompt}` 之前
- `opencode`: 在 `--pure` 之後、`{prompt}` 之前（`-m`、`--agent`、`--variant` 等都走這個位置）

**Precedence（優先順序）：**

1. 純寫 `extra_args`（不動 `command` / `args`）→ 維持內建 `args`、疊上你的 flag。推薦做法。
2. 同時寫完整 `args` 覆寫 + `extra_args`：**完整覆寫勝出**，`extra_args` 被丟掉；啟動時會 emit 一行 `extra_args 被忽略` 的 warn log。想同時保留兩者，就在你自己的 `args` 裡手動加 `"{extra_args}"` token。
3. `extra_args` 為空（nil 或 `[]`）→ `{extra_args}` 槽位直接消失，不會在 CLI 引數裡留下空字串。

**留意：** `extra_args` 是 operator 自己選的 flag，worker 不會預設幫你加任何東西。如果你寫了 `--dangerously-skip-permissions` 之類的開關，那是你的選擇，也是你的風險（worker 可能跑在你的筆電上，而不是 pod 裡）。

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
