# 設定

[English](configuration.en.md)

跑 `agentdock init -c /tmp/sample.yaml` 可產生含所有欄位的範本（加 `-i` 進入互動式填入）。完整 schema 見下方：

```yaml
log_level: info                       # console / stderr 輸出層級：debug | info | warn | error（預設 info）

auto_bind: true

channel_defaults:
  branch_select: true
  default_labels: ["from-slack"]

# Agent 設定
agents:
  claude:
    command: claude
    args: ["--print", "--output-format", "stream-json", "-p", "{prompt}"]
    timeout: 15m
    skill_dir: ".claude/skills"
    stream: true                      # 啟用即時事件追蹤
  opencode:
    command: opencode
    args: ["run", "{prompt}"]
    timeout: 5m
    skill_dir: ".opencode/skills"

active_agent: claude
providers: [claude, opencode]

# Queue 設定
queue:
  capacity: 50                        # queue 上限
  transport: inmem                    # inmem | redis
  job_timeout: 20m                    # watchdog: 最大 job 生命週期
  agent_idle_timeout: 5m              # stream-json: 無 event 超時
  prepare_timeout: 3m                 # clone/setup 超時
  status_interval: 5s                 # worker 回報狀態頻率

worker:
  count: 3                            # worker pool 大小
  prompt:
    extra_rules:                      # worker 層執行時規則
      - "列出所有相關的檔案名稱與完整路徑"

# Redis 設定（transport: redis 時使用）
# redis:
#   addr: redis:6379
#   password: ""
#   db: 0

# Secret 加密（Redis 模式必填）
secret_key: "64字元hex編碼的32-byte AES key"
secrets:
  GH_TOKEN: "ghp_xxx"
  K8S_TOKEN: "your-k8s-token"
  # key = 環境變數名稱，value = 明文值

channel_priority:
  # C_INCIDENTS: 100                  # production incidents 優先
  default: 50

prompt:
  language: "繁體中文"
  goal: "使用 /triage-issue skill 調查 codebase 並產出結構化分類結果"
  output_rules: []                    # app 層輸出規則（預設空，不渲染此段落）
  allow_worker_rules: true            # 是否讓 worker.prompt.extra_rules 生效
```

## Log 層級

兩個獨立的 log 層級，分別控制 console 與檔案輸出：

| 欄位 | 去哪 | 預設 |
|---|---|---|
| `log_level` | console / stderr（`./agentdock app` 輸出） | `info` |
| `logging.level` | 滾動檔案 `logs/YYYY-MM-DD.jsonl` | `debug` |

支援值：`debug` / `info` / `warn` / `error`。

### 三種調法

```yaml
# 1. YAML（持久）
log_level: debug
```

```bash
# 2. CLI flag（一次性）
./agentdock app -c ./config.yaml --log-level debug
```

```bash
# 3. 環境變數
LOG_LEVEL=debug ./agentdock app -c ./config.yaml   # 若有設 env mapping
```

### 什麼時候開 debug

- 診斷 prompt 組裝：worker 端印出「Prompt XML 內容」完整 XML；app 端印出「Prompt context 詳細內容」結構化 context
- 追 Slack 附件下載細節
- 排查 skill 載入問題

Debug log 量大，平常跑 `info` 就夠用。檔案端預設 `debug`（jsonl 可用 `jq -r` 事後翻查），console 端預設 `info` 讓你看得清重點。

## Secret 管理

Redis 模式下，app 集中管理 secrets 並加密傳給 worker。

### 運作方式

1. App config 設定 `secret_key`（AES-256 加密金鑰）和 `secrets`（key-value pairs）
2. App 啟動時將 beacon 寫入 Redis，用於 worker 驗證金鑰一致性
3. 每個 job 提交時，`secrets` 用 AES-256-GCM 加密後放入 `Job.EncryptedSecrets`
4. Worker 解密後注入為子進程的環境變數（如 `GH_TOKEN`、`K8S_TOKEN`）

### 設定

**App config（必填）：**
```yaml
secret_key: "0123456789abcdef..."   # 64 字元 hex（32 bytes）
secrets:
  GH_TOKEN: "ghp_xxx"
  K8S_TOKEN: "eyJhb..."
```

**Worker config（必填 `secret_key`，`secrets` 可選覆蓋）：**
```yaml
secret_key: "跟 app 同一把"
secrets:
  GH_TOKEN: "ghp_worker_override"   # 選填，會覆蓋 app 給的值
```

**環境變數注入：**
- `SECRET_KEY` → 覆蓋 `secret_key`
- `AGENTDOCK_SECRET_<NAME>` → 注入 `secrets["<NAME>"]`（例如 `AGENTDOCK_SECRET_K8S_TOKEN=xxx`）

### 互動式啟動

首次執行 `agentdock app` 或 `agentdock worker` 時，如果 `secret_key` 未設定：
- **App**：可選擇自動產生金鑰，產生後會印出並寫入 config
- **Worker**：提示貼入 app 的金鑰，並立即驗證與 app 的 beacon 是否匹配

### 優先級

Secret 的套用順序（後者覆蓋前者）：

1. `github.token`（自動合併為 `secrets["GH_TOKEN"]`）
2. App config 的 `secrets`
3. `AGENTDOCK_SECRET_*` 環境變數
4. Worker config 的 `secrets`（覆蓋 app 給的值）

## Agent Stream 模式

Claude 支援 `--output-format stream-json`，啟用後可即時追蹤：
- 目前在用什麼 tool（Read, Bash, Grep...）
- 已讀了幾個檔案
- 已生成多少文字
- 花了多少錢（cost_usd, input/output tokens）

不支援 stream 的 agent（opencode, codex）只追蹤 PID + 存活狀態。

## Agent Skills

Skills 隨 Job 發送給 worker（`Job.Skills` 欄位），worker 在 clone 的 repo 裡寫入 skill 檔案（支援完整目錄樹：SKILL.md + examples + references），agent CLI 啟動時自動載入。不需要手動安裝。

```
agents/
  skills/
    triage-issue/
      SKILL.md           # triage skill — agent 分析 codebase 回傳結構化結果
  setup.sh               # local 開發：建 symlink（run.sh 自動呼叫）
```

### 動態 Skill 加載（NPX）

除了 baked-in skills，可透過獨立的 `skills.yaml` 設定從 npm registry 動態加載 skills：

```yaml
# skills.yaml（透過 k8s ConfigMap 掛載）
skills:
  triage-issue:
    type: local
    path: agents/skills/triage-issue

  code-review:
    type: remote
    package: "@team/skill-code-review"
    version: "latest"

cache:
  ttl: 5m    # NPX skill 的 cache 有效期
```

在 `config.yaml` 中指定路徑：
```yaml
skills_config: "/etc/agentdock/skills.yaml"
```

**特性：**
- **TTL cache + singleflight**：避免重複 fetch，cache 過期才重新拉取
- **兩層 fallback**：npx 失敗 → 用 cache 舊版 → 用 baked-in → 跳過
- **Negative cache**：失敗的 skill 在 TTL 內不重試
- **啟動預熱**：App 啟動時預先 fetch 所有 npx skills
- **Hot reload**：fsnotify 監控 skills.yaml，ConfigMap 更新後自動 reload
- **同名衝突 fail fast**：local 和 remote skill 同名時立即報錯，避免 silent override
- **檔案驗證**：單一 skill < 1MB，Job 總量 < 5MB，副檔名白名單，path traversal 防護

**NPM package convention：**
```
node_modules/@team/skill-code-review/
  skills/
    code-review/
      SKILL.md           # 必要
      examples/           # 選用
      references/         # 選用
```

私有 registry 需另行配置 `.npmrc`（透過 k8s Secret 掛載到 `/home/node/.npmrc`）。
