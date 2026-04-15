# CLI 改用 spf13/cobra + Koanf 設定持久化 — Design

- **Date:** 2026-04-15
- **Status:** Draft (brainstorming complete; awaiting user review then writing-plans)
- **Repo:** Ivantseng123/agentdock
- **Origin:** 對話需求 — `bot worker` 設定改用 cobra，所有可調整配置開 flag、merge 後寫回 `~/.config/agentdock/`
- **Related:** `2026-04-15-worker-interactive-preflight-design.md`（既有 worker preflight spec；本案複用該流程並抽出共用 prompt helpers）

## 摘要

把 AgentDock 的 CLI 從 stdlib `flag` 換成 spf13/cobra，提供 `app` / `worker` / `init` 三個子命令。所有可調整的 scalar 配置開出 flag。Config 載入用 `knadh/koanf/v2` 走四層 provider chain（default → config 檔 → env → flag），merge 後寫回 `~/.config/agentdock/config.yaml`，**env 不持久化**。Binary 名 `bot` → `agentdock`。Breaking change，CHANGELOG / release-please 對應處理。

## 動機

- **現況：** `cmd/bot/main.go:42-43` 與 `cmd/bot/worker.go:20-22` 只有 `-config` flag；其他配置只能改 YAML 或設 8 個 env vars。臨時調整 `workers.count`、`redis.addr` 之類得編 YAML 或前置 env，操作繁瑣。
- **目標：** 把所有 scalar 欄位開出 flag；首次設定後寫回 config 檔，下次啟動就記得；提供 `init` 子命令做一鍵 config 模板（含互動模式）。

## 決策摘要

| # | 決策 | 來源 |
|---|---|---|
| D1 | Merge 順序 `flag > env > --config > default`，env 自成一層、**不被 save-back 持久化** | Q1 |
| D2 | 預設 config 路徑字面 `~/.config/agentdock/config.yaml`，跨平台一致（不走 `os.UserConfigDir()`） | Q2 |
| D3 | bot / worker 共用一份 config 檔（不分檔） | Q3 |
| D4 | Save-back 含 secrets，`chmod 0600` + atomic write | Q4 |
| D5 | `Channels` / `Agents` map 不開 flag，純走 config 檔 | Q5 |
| D6 | 保留 preflight 互動，merge 後仍缺值才 prompt | Q6 |
| D7 | Library: `cobra + knadh/koanf/v2`，hand-written flags | Approach 1 |
| D8 | 命令樹 `agentdock` (help) / `app` / `worker` / `init`；不帶 subcommand 印 help | Section A |
| D9 | 檔案放 `cmd/agentdock/` 平鋪，不開 sub-package | Section A |
| D10 | `init --interactive` 與 worker preflight 共用同一套 prompt helpers（`prompts.go`） | Section A |
| D11 | Binary 名 `bot` → `agentdock` | Section A |

## 命令樹

```
agentdock                         # root，不帶 subcommand → 印 help
├── app                           # 主 Slack bot (was: 原 ./bot 主程式)
├── worker                        # worker pool
└── init [-c PATH] [--force] [-i, --interactive]
                                  # 產 starter config 模板
```

`-h, --help` / `-v, --version` root 與三個 subcommand 都吃。

### Persistent flags（root，三個 subcommand 繼承）

- `-c, --config <path>`（預設 `~/.config/agentdock/config.yaml`）
- `--log-level`
- `--redis-addr`、`--redis-password`、`--redis-db`、`--redis-tls`
- `--github-token`
- `--mantis-base-url`、`--mantis-api-token`、`--mantis-username`、`--mantis-password`
- 所有 `Queue.*` scalar：`--queue-capacity`、`--queue-transport`、`--queue-job-timeout`、`--queue-agent-idle-timeout`、`--queue-prepare-timeout`、`--queue-status-interval`
- 所有 `Logging.*` scalar：`--logging-dir`、`--logging-level`、`--logging-retention-days`、`--logging-agent-output-dir`
- `--repo-cache-dir`、`--repo-cache-max-age`
- `--attachments-store`、`--attachments-temp-dir`、`--attachments-ttl`
- `--workers`（= `Workers.Count`）
- `--active-agent`、`--providers`（comma-separated → `[]string`）
- `--skills-config`

### `app` 專屬 flags

- `--slack-bot-token`、`--slack-app-token`
- `--server-port`
- `--auto-bind`
- `--max-concurrent`、`--max-thread-messages`、`--semaphore-timeout`
- `--rate-limit-per-user`、`--rate-limit-per-channel`、`--rate-limit-window`

### `worker` 專屬 flags

無（全部繼承 root 即可）。

### 不開 flag 的欄位

`Channels map[string]ChannelConfig`、`Agents map[string]AgentConfig`、`Prompt`、`ChannelDefaults`、`ChannelPriority` map — 純走 config 檔。理由：nested map 用 flag 表達會醜（`--channels.<id>.repo=...`）且使用率低；走 YAML / JSON 直接編輯體驗最佳。

## `init` 子命令

```
agentdock init [-c, --config <path>] [--force] [-i, --interactive]
```

### 非互動模式（預設）

dump 一份 starter YAML 到指定 / 預設 path，內容：

- 所有 scalar 用 `applyDefaults` 真實預設值（直接可用）
- `agents:` 預先放 `claude` / `codex` / `opencode` 三個 entry（從目前 `LoadDefaults()` hardcode 搬過來）
- `slack:` / `github:` / `redis:` 留空 + `# REQUIRED` 註解
- `channels:` 註解掉的範例 entry，註明怎麼新增
- 寫檔 `chmod 0600`、atomic（先寫 `.tmp` 再 `os.Rename`）
- 寫完 stderr 印 `config written to <path>; edit secrets then run 'agentdock app'`，exit 0

### 互動模式 `-i`

跑與 worker preflight **同一套 prompt helpers**（`promptLine` / `promptHidden` / `promptYesNo` + `checkRedis` / `checkGitHubToken` / `checkAgentCLI`，搬到 `prompts.go`），問必填：

1. **Slack bot token**（hidden，呼叫 `auth.test` 驗證）— `app` 用，worker 不用但仍寫進共用 config
2. **Slack app token**（hidden，至少驗 `xapp-` 前綴）— 同上
3. **GitHub token**（hidden，`/user` + `/user/repos` 驗）
4. **Redis address**（PING 驗）
5. **Providers**（從 hardcoded agents map 數字選）

填完寫檔，與非互動模式相同的 chmod 0600 + atomic write。

### 衝突處理

| 情境 | 行為 |
|---|---|
| 目標檔存在 + 無 `--force` | exit 1，stderr 印 `config already exists at <path>; pass --force to overwrite` |
| `--force` | 直接覆寫，不備份 |
| 目錄 `~/.config/agentdock/` 不存在 | `os.MkdirAll(dir, 0700)` 自動建 |

### Marshal 方式

用 `gopkg.in/yaml.v3` 直接 marshal，**不**走 koanf — 因要保留註解（`# REQUIRED`、範例 channel 註解等），koanf 標準 marshal 不吐註解。

`init` 一律輸出 YAML（不管 `--config` 副檔名是 `.json`），因為 JSON 不支援註解、starter 模板沒意義。如果 user 真要 JSON config，就 `init` 完手轉一次。

## 檔案結構

```
cmd/agentdock/                   # was cmd/bot/
  main.go                        # ~10 行：func main() { Execute() }
  root.go                        # rootCmd + persistent flags + version vars
  app.go                         # appCmd
  worker.go                      # workerCmd
  init.go                        # initCmd
  flags.go                       # 所有 flag 註冊 helper（addRedisFlags、addLoggingFlags 等）
  config.go                      # koanf load / merge / save-back
  prompts.go                     # 互動 helpers（從 preflight.go 抽出共用部分）
  preflight.go                   # runPreflight（給 app + worker startup 用）
  adapters.go                    # 從原 main.go 拆出 agentRunnerAdapter / repoCacheAdapter / slackPosterAdapter
  local_adapter.go               # 維持

internal/config/
  config.go                      # Config struct + applyDefaults + EnvOverrideMap() + DefaultsMap()
                                 # 移除 Load() / LoadDefaults() / applyEnvOverrides()
```

### 連帶要改

- `Dockerfile` — `./cmd/bot/` → `./cmd/agentdock/`，binary `bot` → `agentdock`，entrypoint `agentdock app`
- `run.sh` — 同上
- `.github/workflows/*.yml` — release 流程裡 binary 名與路徑
- `README.md` — 使用方式 / migration 提示
- CHANGELOG（release-please 自動產，但 commit message 要含 `BREAKING CHANGE:` footer）

### 為什麼 `cmd/agentdock/` 而非直接放 `cmd/`

Go 慣例 `cmd/<binary>/<files>`（`kubectl` / `gh` / `hugo` / `prometheus`）。`go build ./cmd/agentdock/` 產生 `agentdock` binary，名稱與目錄一致。若未來新增第二個 binary（如 admin CLI、migrator），`cmd/<other>/` 路徑現成不用重構。

### 為什麼不開 sub-package（`cmd/agentdock/cmd/`）

cobra 文件預設那樣寫是 multi-binary 大專案的範例。AgentDock 一個 binary 一層 subcommand，平鋪比較好讀，main.go 也省一層 import。

## Config 資料流

### 啟動序列（app / worker 共用）

```
1. cobra.Execute() → 解析 flags + 跑 PersistentPreRunE
2. PersistentPreRunE:
     a. 解析 --config 路徑（含 ~ 展開）
     b. buildKoanf(cmd) → 回 (cfg, kEff, kSave)
     c. preflight.Run(cfg, prompted)（缺值 + interactive 才 prompt）
     d. saveConfig(kSave, path, prompted)（kSave + preflight 結果，**不含 env**）
     e. cfg 塞進 cmd.Context()
3. RunE: 從 ctx 拿 cfg 跑主流程
```

`init` 是另一條短路徑：解析 path → 檢查存在 → (`-i` 才 prompt) → marshal + 寫檔 → exit。

### koanf 兩 instance（pseudo-Go）

```go
kEff  := koanf.New(".")   // effective config（給 runtime）
kSave := koanf.New(".")   // 給 save-back marshal

// 後 Load 蓋前面 → load 順序就是優先序低到高

// L0: defaults — 兩邊都載
kEff.Load(confmap.Provider(DefaultsMap(), "."), nil)
kSave.Load(confmap.Provider(DefaultsMap(), "."), nil)

// L1: --config 檔（YAML 或 JSON 看副檔名）— 兩邊都載
parser := pickParser(path)  // .yaml/.yml→yaml, .json→json
if fileExists(path) {
    kEff.Load(file.Provider(path), parser)
    kSave.Load(file.Provider(path), parser)
}

// L2: env — **只給 kEff**
kEff.Load(confmap.Provider(EnvOverrideMap(), "."), nil)

// L3: cobra flags（只 Changed 過的）→ 走顯式 flag→key 映射表（見下）— 兩邊都載
flagMap := buildFlagOverrideMap(cmd)
kEff.Load(confmap.Provider(flagMap, "."), nil)
kSave.Load(confmap.Provider(flagMap, "."), nil)

// Unmarshal kEff 到 Config struct（用 yaml tag）
kEff.UnmarshalWithConf("", &cfg, koanf.UnmarshalConf{Tag: "yaml"})
```

### Flag → koanf key 映射

posflag.Provider 預設拿 flag name 當 koanf key（`--redis-addr` → `redis-addr`），但 Config struct 用 yaml snake_case tag。`-` 與 `_` 的對應規則必須**逐 flag 控制**，例如：

- `--redis-addr` → `redis.addr`（struct boundary 用 dot）
- `--logging-agent-output-dir` → `logging.agent_output_dir`（boundary dot + snake_case 保留）
- `--log-level` → `log_level`（純 snake）
- `--rate-limit-per-user` → `rate_limit.per_user`

簡單字串替換（`-` → `.`）做不到。改用**顯式映射表** + 手工建 `map[string]any` 灌 `confmap.Provider`：

```go
// flags.go — single source of truth
var flagToKey = map[string]string{
    "redis-addr":               "redis.addr",
    "redis-password":           "redis.password",
    "redis-db":                 "redis.db",
    "redis-tls":                "redis.tls",
    "github-token":             "github.token",
    "logging-dir":              "logging.dir",
    "logging-level":            "logging.level",
    "logging-retention-days":   "logging.retention_days",
    "logging-agent-output-dir": "logging.agent_output_dir",
    "log-level":                "log_level",
    "rate-limit-per-user":      "rate_limit.per_user",
    "rate-limit-per-channel":   "rate_limit.per_channel",
    "rate-limit-window":        "rate_limit.window",
    "queue-capacity":           "queue.capacity",
    "queue-job-timeout":        "queue.job_timeout",
    // ... 每個 flag 一行
}

func buildFlagOverrideMap(cmd *cobra.Command) map[string]any {
    out := map[string]any{}
    cmd.Flags().Visit(func(f *pflag.Flag) {
        key, ok := flagToKey[f.Name]
        if !ok {
            return  // skip --help / --version / --config / --force / --interactive
        }
        // 依 flag type 取對應 typed value
        switch f.Value.Type() {
        case "string":      out[key], _ = cmd.Flags().GetString(f.Name)
        case "int":         out[key], _ = cmd.Flags().GetInt(f.Name)
        case "bool":        out[key], _ = cmd.Flags().GetBool(f.Name)
        case "duration":    out[key], _ = cmd.Flags().GetDuration(f.Name)
        case "stringSlice": out[key], _ = cmd.Flags().GetStringSlice(f.Name)
        }
    })
    return out
}
```

**維護成本：** 每加一個 flag 要同時更新 (1) flag 註冊（`flags.go` 的 helper）跟 (2) `flagToKey` map。換來明確、與 yaml tag 對得起來、不會因為欄位名有底線而錯位。

**Test 涵蓋：** `flags_test.go` 補一個測試 walk Config struct 的 yaml tag、確保每個 flag 對應的 key 真的存在於 struct 路徑（catch 漏字）。

### Path 解析

```
"/abs/path/foo.yaml"           → 原樣
"./relative.yaml"              → filepath.Abs
"~/.config/agentdock/x.yaml"   → 展開 ~ 為 os.UserHomeDir()
未指定                         → ~/.config/agentdock/config.yaml（字面）
```

字面 `~/.config/agentdock` 跨平台一致（Q2 決定）。Windows 下會變 `C:\Users\<u>\.config\agentdock\config.yaml` — 不漂亮但不在目標 OS。

## Save-back

### 觸發

`app` / `worker` 每次成功通過 preflight 都寫一次。`init` 不走這個函式（自己 marshal 含註解）。

### 內容

`kSave.Marshal(parser)` — default + config + flag overrides + preflight 互動填入；**不含 env layer**。

### Preflight 結果寫進 kSave

preflight 跑完後，把它互動填入的欄位 `kSave.Set("redis.addr", v)`、`kSave.Set("github.token", t)` 等，再 marshal。這樣下次啟動 config 檔已含值，preflight 不會再問。

### 寫檔

```go
os.MkdirAll(filepath.Dir(path), 0700)
tmpPath := path + ".tmp"
os.WriteFile(tmpPath, data, 0600)
os.Rename(tmpPath, path)   // atomic 替換
```

每次 save 都重設 mode 為 `0600`（防止外部改成寬權限後仍可讀）。

### 錯誤策略

- save-back 失敗 → `slog.Warn("config save failed", ...)`，**不 fail 啟動**（runtime 已有 in-memory cfg）
- `chmod 0600` 失敗（rare）→ 同上 warn 繼續

### 已知 UX 陷阱：Env-derived 值不持久化

D1 規定 env 不進 kSave。實務上意味著：
- 用 `REDIS_PASSWORD=xxx agentdock worker` 啟動，第一次 OK；下次 unset env 後啟動 → Redis 連不上（password 從未進過 config 檔）
- 用 `GITHUB_TOKEN=xxx` 啟動同理；preflight 不問 token（因 env 已填值），但 token 不寫回，下次沒 env 又會被 preflight 抓出來重問

這是 D1 的有意設計（避免 secrets 因為一次帶 env 就被永久寫進檔），但對使用者要清楚溝通。**README 與 `agentdock --help` 須註明：要永久設定 secrets，請走 `--config` 檔或 `agentdock init -i`，env 只是「本次 session」用。**

## 錯誤處理

| 情境 | 行為 |
|---|---|
| `--config` 沒指定 + 預設路徑檔不存在 | 繼續（用 defaults + env + flags + preflight 跑，啟動後 save-back 創建檔案） |
| `--config` 指定 + 檔不存在 | fail：`config file not found: <path>; run 'agentdock init -c <path>' first` |
| 檔存在但解析失敗 | fail，印 koanf 錯誤訊息（YAML 含 line number） |
| 副檔名不在 `.yaml/.yml/.json` | fail：`unsupported config format: .toml; only .yaml/.yml/.json supported` |
| Env 格式錯誤（如 `PROVIDERS=,,,`） | `EnvOverrideMap()` 內過濾空 token；極端情況 `slog.Warn` 不 fail |
| flag 型別錯誤（如 `--workers abc`） | cobra/pflag 自動 reject 印 usage，exit 2 |
| `~/.config/agentdock/` 目錄無法建（permission） | fail，明確指 path |
| Preflight Ctrl-C / EOF | fail，exit code 130（SIGINT 標準） |
| Signal handling（runtime） | 維持現狀 — `signal.Notify(SIGTERM, SIGINT)` 觸發 graceful shutdown |

## 與現有部署的相容性（Breaking Changes）

### 變更清單

1. Binary `bot` → `agentdock`，子命令必填（`agentdock app` / `agentdock worker`）
2. CLI flag `-config` → `-c, --config`（cobra 慣例不支援單 dash 長名）
3. **Env 優先序變了：** 原本 env 蓋過 YAML（`config.go:269-294`），新版 YAML 蓋過 env（env 已降到 default 之上）
4. 預設 config 路徑：原本當前目錄 `config.yaml`，新版 `~/.config/agentdock/config.yaml`

### 遷移路徑

- 既有 YAML 檔 schema **不變**（同 Config struct + yaml tag），用 `--config /原路徑` 直接吃
- Docker / k8s entrypoint：`./bot -config /etc/agentdock/config.yaml` → `agentdock app -c /etc/agentdock/config.yaml`
- Env-only 部署（沒 YAML）：`agentdock app` + 全 env vars 仍 OK，但要記得 env 不再蓋 YAML（沒 YAML 就無差別）
- release-please：commit message 帶 `BREAKING CHANGE:` footer，自動 bump major（`0.x → ?`，release 政策由維護者決定）

### 不做的相容 hack

- 不留 `bot` 子命令 alias
- 不留 `-config` 單 dash 長名 normalizer
- 不偵測舊路徑自動搬

乾淨斷裂、CHANGELOG / README 講清楚。

## 測試策略

### 新增 test files

```
cmd/agentdock/
  config_test.go         # koanf layering、save-back round-trip、env exclusion
  flags_test.go          # flag 註冊、dashToDot 轉換
  init_test.go           # init 非互動 snapshot、--force 行為
  prompts_test.go        # 互動 helpers（stub stdin/stdout）
  preflight_test.go      # 既有 → 補 cobra integration
  config_path_test.go    # ~ 展開、abs/rel、parser 選擇

internal/config/
  config_test.go         # 既有 → 補 EnvOverrideMap()、DefaultsMap()
```

### 重點覆蓋

1. **Layering 優先序** — 4 層各自獨立 + 兩兩疊加 + 全疊；scalar / bool / duration / `[]string` 各驗代表性欄位
2. **Save-back round-trip** — load → mutate flag → save → reload → 預期相等（**不含 env**）
3. **Env exclusion** — 設 `REDIS_ADDR=10.0.0.1`、無 flag、save、reload without env → 預期得 default 不是 10.0.0.1
4. **Secrets persisted** — `--github-token=ghp_xxx` flag → save → reload → token 在檔
5. **chmod 0600** — save 後 `os.Stat` mode mask = 0600
6. **Atomic write** — mock disk full / 寫到一半失敗，原檔不應損毀
7. **Path resolution** — `~/.config/...` 在不同 `HOME` 環境變數下展開正確
8. **Format detection** — `.yaml` / `.yml` / `.json` 各跑一遍 round-trip
9. **`init` 非互動** — snapshot 比對輸出 YAML（含 `# REQUIRED` 註解）
10. **`init --interactive`** — stub stdin 回應，驗最終 marshal 結果
11. **`init --force`** — 既存檔被覆寫（驗內容 + mode）
12. **Preflight integration** — missing required + interactive → prompt；missing required + non-interactive → fail；all set → skip

### Out-of-scope

- cobra / pflag 自身解析（信任上游）
- koanf 自身 provider 機制（信任上游）
- Slack / GitHub / Redis 連線本身（已有 `checkRedis` / `checkGitHubToken`，整合測由現有 preflight test 涵蓋）

## 實作分階段（給 writing-plans 用的提示）

1. **Refactor 不換語意：** 搬 `cmd/bot/` → `cmd/agentdock/`；抽 `EnvOverrideMap()` / `DefaultsMap()` helpers；保留現有 stdlib flag 路徑能 build 過、test 過
2. **加 cobra 框架：** `root` + `app` + `worker` + `init` 骨架，flags 暫接老 `Load()` — still works
3. **接 koanf：** 取代 `Load()` 為兩 instance 流程；preflight 改 PreRunE
4. **加 `init` 含 `--interactive`，** 共用 prompt helpers
5. **Save-back 串起來：** preflight 結果 → kSave + atomic + chmod
6. **改 Dockerfile / run.sh / workflows / README**
7. **補測試**

每階段都該獨立 build + 跑現有 150 個 test。

## 不在範圍內

- Config hot reload（現有 `skill.watcher` 是另一回事）
- 多 profile / named config 支援（後續可再開單獨 spec）
- `bot` binary alias / 舊 flag normalize 等向後相容 hack
- Schema migration（既有 YAML schema 不變）

## 已決議（非未決，集中 reference）

腦力激盪過程中浮出過、上面已決定的：

- Env vars 處理 → D1 / Q1
- 預設路徑跨平台 → D2 / Q2
- 分檔 vs 共檔 → D3 / Q3
- Secrets persist → D4 / Q4
- Map 欄位 flag 化 → D5 / Q5
- Preflight 去留 → D6 / Q6
- Library 選 → D7 / Approach 1
- Subcommand 命名 → D8（`app` / `worker` / `init`）
- 子 package vs 平鋪 → D9（平鋪在 `cmd/agentdock/`）
- Binary 名 → D11（`agentdock`）

## 真正未決（writing-plans 之前可再問）

- BREAKING CHANGE 走 `0.x → 1.0.0` 還是 `0.2.x → 0.3.0`？由維護者 release 政策決定
- 是否需要 `--config-format` flag 強制覆寫副檔名推斷（目前判斷：少見需求，writing-plans 階段再決定）
