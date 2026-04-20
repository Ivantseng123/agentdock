# Worker Nicknames — Slack 顯示用暱稱池

- **Date**: 2026-04-20
- **Status**: Design approved, pending plan
- **Scope**: 新功能，worker 端 config 擴充 + Slack 顯示層微調；不破壞既有部署。

## 1. 背景

目前 Slack 狀態訊息把 worker 顯示為 `worker-0` / `worker-1`（`app/bot/status_listener.go:193` 的 `shortWorker(r.WorkerID)`，把 `hostname/worker-0` 截成 `worker-0`）。對 reporter 來說這是冷冰冰的 index，沒有辨識度；對運維來說想「幫特定 worker 取個名」（例如依地區、部門、實驗分組）也沒有機制。

本案在 worker config 加一個 `nickname_pool`，worker process 啟動時從池中隨機分配給每個 slot，Slack 顯示改成暱稱（沒暱稱則維持原 `worker-N`）。純使用者體驗功能，不改動任何 ID 語意。

## 2. 目標

- `worker/config/config.go` 新增 `NicknamePool []string`，使用者於 `worker.yaml` 填 `nickname_pool: ["小明","Gary",...]`。
- Worker process 啟動時用 Fisher–Yates 從池抽 `count` 個（池夠大時不重複抽；不夠則用掉整池，剩下 slot 保持空字串）。
- Nickname 寫入 `shared/queue.WorkerInfo.Nickname`（Redis 註冊欄位）與 `shared/queue.StatusReport.WorkerNickname`（跨 pod 傳遞用）。
- `app/bot/status_listener.go` 的 `renderStatusMessage` 有 nickname 則顯示 nickname，沒有則維持 `worker-N`。
- `agentdock init worker` 產出的 template 預設包含 `nickname_pool: []`，使用者可直接填。

## 3. 非目標

- **不改 WorkerID 本體**：`{hostname}/worker-{N}` 維持不變。Redis key、ProcessRegistry、metrics labels、logs 的 `worker_id` 屬性、dashboard query 全部不動。
- **不改 logs**：logs 的 `worker_id` 整數 index 是 debug 用的程式辨識，不是人類讀的。想知道哪個 id 對哪個暱稱去 `ListWorkers` 對。
- **不改 metrics / Grafana**：dashboard 穩定性優先於命名趣味。
- **不做 admin CLI 列 workers**：`WorkerInfo.Nickname` 欄位是未來的基礎，但本案不實作消費端。
- **不做唯一性檢查**：允許使用者在池裡填重複名字（他的選擇）。
- **不做 Slack 以外的 human-facing surface**：目前沒有，也不新增。
- **不做 runtime 重分配**：一個 process 的 nickname 在啟動時決定，生命週期結束才會消失；重啟會重抽。

## 4. 關鍵決策

| # | 決策 | 理由 |
|---|---|---|
| Q1 | **池獨立於 count，不做交叉驗證** | 池大可小可等於 count 都合法；池小於 count 發 warn log 但不 fail，給運維選擇空間。 |
| Q2 | **A 方案：不重複抽（池夠大時），池不夠剩餘 slot 空字串** | 「挑 count 出來」的自然中文語意；池夠大時每 worker 名字不同、最容易辨識。 |
| Q3 | **重複允許由 pool 填寫者承擔** | 使用者在池裡填兩個「小明」就會有兩個 worker 都叫小明；系統不攔。 |
| Q4 | **Nickname 透過 `StatusReport` piggyback 傳到 app** | vs. app 端查 `ListWorkers` — piggyback 耦合低、不用多一個 Redis round trip。 |
| Q5 | **Slack 只顯示 nickname，沒設就 fallback `worker-N`**（不顯示雙 label） | `worker-0 (小明)` 對 reporter 是 noise；reporter 不 care 技術 ID，運維要對照去 Redis/logs。 |
| Q6 | **omitempty 兩個新欄位（`WorkerInfo.Nickname`、`StatusReport.WorkerNickname`）** | 向後相容：舊 worker/舊 Redis 資料沒這欄，app 端收到空字串 = 無暱稱 = 現有行為。 |

## 5. Architecture

### 檔案動線

```
worker/config/config.go        +NicknamePool []string
worker/config/validate.go      +驗證每個條目（1–32 Unicode char、非純空白）
worker/config/defaults.go      +NicknamePool: nil（空池、功能關閉）
worker/pool/nickname.go        +pickNicknames(pool, count, rng) []string  [新檔]
worker/pool/pool.go            +Nicknames []string in Config；Register/statusAccumulator 塞值
worker/pool/status.go          +statusAccumulator 帶 nickname；toReport() 輸出
worker/worker.go               +呼叫 pickNicknames，傳進 pool.Config
shared/queue/interface.go      +WorkerInfo.Nickname、StatusReport.WorkerNickname
shared/queue/redis_jobqueue.go +Register/ListWorkers 序列化 Nickname
app/bot/status_listener.go     +formatWorkerLabel(workerID, nickname)
cmd/agentdock/init.go          （無動；yaml serializer 自動輸出 nickname_pool: []）
docs/configuration-worker.md   +nickname_pool 段落
docs/configuration-worker.en.md +nickname_pool 段落
```

### 資料流

1. `worker.Run` 啟動 → 讀 `cfg.NicknamePool`，用 `rand.New(rand.NewSource(time.Now().UnixNano()))` 呼叫 `pickNicknames`，得到長度為 `cfg.Count` 的 `[]string`（空字串代表該 slot 無暱稱）。
2. 結果塞進 `pool.Config.Nicknames`，傳入 `pool.NewPool`。
3. `pool.workerHeartbeat` 建立 `WorkerInfo` 時填 `Nickname: nicknames[i]`，寫入 Redis。
4. `pool.executeWithTracking` 建立 `statusAccumulator` 時把 `nicknames[workerIndex]` 一起帶。
5. `statusAccumulator.toReport()` 輸出 `StatusReport{WorkerNickname: ...}`。
6. Redis StatusBus → app pod → `StatusListener.maybeUpdateSlack` → `renderStatusMessage`。
7. `renderStatusMessage` 以 `formatWorkerLabel(r.WorkerID, r.WorkerNickname)` 取代 `shortWorker(r.WorkerID)`。

### 核心函式

**`pickNicknames`** (`worker/pool/nickname.go`，新檔)：

```go
func pickNicknames(pool []string, count int, rng *rand.Rand) []string {
    out := make([]string, count)
    if len(pool) == 0 || count <= 0 {
        return out
    }
    perm := rng.Perm(len(pool))
    n := count
    if n > len(pool) {
        n = len(pool)
    }
    for i := 0; i < n; i++ {
        out[i] = pool[perm[i]]
    }
    return out
}
```

- 池 ≥ count：`perm[:count]` 取前 count 個（Fisher–Yates 打亂後前 N 項，不重複）。
- 池 < count：用掉整池，`out[len(pool):]` 保持空字串。
- 池 = 0 或 count ≤ 0：全空字串。

**`formatWorkerLabel`** (`app/bot/status_listener.go`，新增)：

```go
func formatWorkerLabel(workerID, nickname string) string {
    if nickname != "" {
        return nickname
    }
    return shortWorker(workerID)
}
```

`renderStatusMessage` 三處 `worker := shortWorker(r.WorkerID)` 改為 `worker := formatWorkerLabel(r.WorkerID, r.WorkerNickname)`。

### 驗證規則

`worker/config/validate.go` 對 `NicknamePool` 每個條目：

- `utf8.RuneCountInString(strings.TrimSpace(s))` 在 `[1, 32]` 範圍內。
- `strings.TrimSpace(s) != ""`（拒純空白）。
- 無字元集白名單（任意 Unicode 合法）。
- 允許重複條目（不去重、不報錯）。

不做的驗證：
- `len(pool)` 跟 `count` 的關係（獨立，池不夠時在 `worker.Run` 發 warn log）。
- 跨條目唯一性（重複本案視為合法）。

### 啟動警示

`worker/worker.go` 在 `pickNicknames` 前：

```go
if n := len(cfg.NicknamePool); n > 0 && n < cfg.Count {
    appLogger.Warn("nickname 池小於 worker 數，部份 worker 將無暱稱",
        "phase", "處理中", "pool_size", n, "worker_count", cfg.Count)
}
```

池為空（含缺省）靜默，因為「沒啟用」是合法狀態。

### 向後相容

| 情境 | 行為 |
|---|---|
| 舊 worker + 新 app | `StatusReport.WorkerNickname` 缺省 = 空字串 = Slack 顯示 `worker-N`（現有行為）。 |
| 新 worker + 舊 app | 舊 app 不讀 `WorkerNickname` 欄位（JSON decode 自動忽略），Slack 顯示 `worker-N`。 |
| 舊 worker 的 `WorkerInfo` 留在 Redis | `Nickname` 欄位缺省 = 空字串。 |
| 升級部署未填 `nickname_pool` | 行為完全不變（空池 → 全 slot 空字串 → 現有顯示邏輯）。 |

`omitempty` 雙方護航，不需要版本 gate。

## 6. YAML 範例

```yaml
# worker.yaml
count: 5
nickname_pool: ["小明", "小華", "Gary", "Alice", "Bob", "Charlie", "Delta"]
```

- 啟動時從 7 個抽 5 個不重複，每個 worker slot 得一個。
- 重啟後重抽，順序會變。

```yaml
# 不啟用：
count: 5
nickname_pool: []   # 或整個 key 省略
```

```yaml
# 池比 count 小：
count: 5
nickname_pool: ["小明", "小華"]
# 啟動 log: "nickname 池小於 worker 數，部份 worker 將無暱稱"
# 結果：2 個 worker 隨機拿到 小明/小華，其他 3 個顯示 worker-2/worker-3/worker-4
```

## 7. 測試策略

### 單元測試

- `worker/pool/nickname_test.go`（新檔）：
  - 池 > count：回傳長度 count、全部來自池、無重複 index。
  - 池 = count：每個條目都被用到一次。
  - 池 < count：前 `len(pool)` 個非空且來自池，其餘空字串。
  - 池 = 0：全空字串。
  - count = 0：回傳空 slice。
  - 用 `rand.New(rand.NewSource(42))` 保證測試可重現。
- `worker/config/validate_test.go`：
  - 空池、空字串條目、純空白條目、超長（33 字元）、剛好 32 字元、Unicode 條目。
  - 重複條目合法。

### 整合

- `app/bot/status_listener_test.go`：
  - 有 `WorkerNickname` → 渲染 `:gear: 準備中 · 小明`。
  - 無 `WorkerNickname` → 渲染 `:gear: 準備中 · worker-0`（既有 snapshot 不變）。
- `worker/integration/queue_redis_integration_test.go`：
  - `Register` 帶 `Nickname` → `ListWorkers` 讀回同值。

### 手動

- `count: 3`, `nickname_pool: ["Alice","Bob"]` → 本機跑、觸 triage、Slack 看兩個 worker 掛 Alice/Bob、第三個是 `worker-2`。

## 8. 風險

- **Slack 暱稱混淆使用者**：有人把 pool 填重複名字造成兩個 worker 都叫小明。Slack 無法分辨哪個進度屬誰。緩解：文件明說這是使用者的選擇；工程上不攔。
- **重啟後暱稱重抽**：reporter 若在 thread 裡提「小明剛才說會議卡住」，worker 重啟後找不到原本的小明。緩解：agentdock job 壽命短（通常分鐘級），跨重啟追暱稱本來就不合理；文件提醒。
- **JSON schema 新增欄位對舊 consumer**：`WorkerInfo` / `StatusReport` 加欄位的相容性已由 `omitempty` + JSON decode 寬容性保證。

## 9. Migration

- **無 migration 工具**：`nickname_pool: []` 是合法預設，使用者無需改任何現有 config。
- **新使用者**：`agentdock init worker` 產生的 template 自帶 `nickname_pool: []`（Config struct 不加 `omitempty`，yaml marshaller 就會輸出空陣列讓人看到這個 key 的存在）。

## 10. 文件更新

- `docs/configuration-worker.md` 與 `.en.md`：新增 `nickname_pool` 段落，說明隨機抽、池大小語意、Slack 顯示行為。
- `README.md`：不動（此功能是細節 UX，不值得出現在 overview）。
- `CLAUDE.md`：不動（沒有新的 landmine）。
