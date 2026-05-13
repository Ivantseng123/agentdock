# Spec: Opencode via long-running server

## Objective

替換 agentdock worker 對 opencode 的整合方式 — 從「每 job spawn `opencode run --pure --format json`」改成「worker 啟動時跑長住的 `opencode serve`，每 job 透過 HTTP `POST /session/{id}/message` + SSE `/event` 操作」。

**Why**：opencode `run --format json` 有兩個 deterministic bug，silent 丟掉 ask 答案：

- Bug A — provider default `finishReason="other"`，upstream 回空 SSE 不會被當錯
- Bug B — `run.ts` `loop()` fire-and-forget + `bootstrap.ts` finally `disposeInstance()` 砍 in-process server，trailing `message.part.updated` event 丟失

兩 bug 在 opencode HEAD 都還沒修。任何 CLI flag 都救不了 (結構性問題)。換啟用模式是唯一根治路。

**Who**：agentdock operators + Slack ask 使用者。
**Success**：ask path 0 silent answer drop；ask path 對 opencode patch-level 升版有抗性；issue path 後續同樣機制 (本 spec 不涵蓋)。

兩階段交付：

- **Phase 3.1 — POC**：在獨立 feature branch `poc/opencode-server-mode` 寫拋棄式 Go 程式驗證 hidden assumption，**code 不進 main**；只把 `REPORT.md` merge 進 `docs/specs/opencode-server-mode-poc-report.md`，作為 Phase 3.1 → 3.2 的 ship gate 依據。`REPORT.md` 必須把 `server_mode_regression` 與 `provider_retry_transient` 分開記錄
- **Phase 3.2 — Production swap**：worker 加 top-level `opencode:` config block（含 `mode: server | spawn`），spawn legacy 並存，POC server-mode criteria 全綠（provider retry transient 另行記錄）+ prod 監測 ≥ 2 週後 default 翻 `server`

## Tech Stack

- Go 1.x (跟 worker 同 stack)
- stdlib only (`net/http`, `bufio`, `os/exec`)，無新增 `go.mod` 依賴
- opencode 1.14.41 (worker runtime 版本，`Dockerfile.release` 已 pin) + HEAD (cross-version 驗證)
- Reuse `shared/queue/stream.go ReadStreamJSONOpencode` event 解析邏輯

## Version Policy

Server mode 採 **lower-bound 版本門檻**：`MinimumOpencodeVersion` 是 worker 程式碼裡的常數，作為「最低支援版本」floor。worker 在 `mode: server` 啟動時若 `agents.opencode.command` 指向的 binary 版本 **低於** floor → 拒絕啟動（log + non-zero exit）；**等於或高於** floor → 允許啟動，但 **正式 POC backing 只對 floor 自身那個版本成立**，高於 floor 的版本允許跑但**不承諾**。`mode: spawn` 完全不受影響（legacy 路保留，沒有 version 閘門）。

Floor 只會在 POC `-all -replay-count=100` 在更新 build 上重跑、P1–P10 在 classification rules 下全綠時才升級；舊版本在 floor 升級的時點起停止 `server` mode 支援。

**Scope of the check (and what it does not catch).** 這個檢查只是一條單向 lower-bound `version >= MinimumOpencodeVersion`。它抓得到「user 裝的 opencode 太舊、缺我們依賴的 server-mode 行為」這條；它**不**抓「user 裝的是比 floor 新但 upstream 引入 regression」的情況 — 見 POC report P3，opencode 1.14.48 比 floor 新但 server-mode SSE 已壞。對 newer-but-broken 的防線是 per-job 失敗偵測（Bug A detector + `server_mode_regression` 分類 + 不做 auto-fallback，spec C4），worker 大聲 fail job，operator 介入調查 — 不是靠 version check 攔下來。

**Why lower-bound floor instead of exact-match or compat matrix.** Exact-match (`version == MinimumOpencodeVersion`) 在 operator 升 opencode 後立刻拒絕啟動，每個 patch 都要動 worker code，operational pain 大於價值。Compat matrix (「support any version ≥ N + 對每版做相容測試」) 要嘛要 per-version branch、要嘛 silent best-effort，failure surface 變糊。Floor 是中間路：低於 floor 一律 fail-fast，floor 以上交給 per-job 失敗偵測接住；team 只 own floor 自身那個版本，更高版本的 upstream 行為由 operator 自負風險（明示 trade-off）。

**Bumping the floor.** 流程：(a) 裝候選 opencode build，(b) 重跑 POC `-all -replay-count=100`，(c) 若 P1–P10 在 classification rules 下全綠，把 `MinimumOpencodeVersion` 升到候選版本並重發 `opencode-server-mode-poc-report.md`。短取樣（`-replay-count` < 100）或單一 criterion 的測試**不算** floor backing — 那些屬於 `Dockerfile.release` image pin 級別的證據（spawn-mode 跑得起來就行），跟 server-mode floor 是兩條獨立 evidence pipeline。

## Commands

POC（在 feature branch 跑）：

```bash
git checkout poc/opencode-server-mode
go run ./cmd/dev/poc-opencode-server \
    -fixture ./testdata/harbor-4images \
    -opencode $(which opencode) \
    -concurrency 8 \
    -repeats 100 \
    -isolated-xdg-data /tmp/poc-opencode-xdg-data
```

Worker（Phase 3.2，server-mode opt-in）：

```bash
go run ./cmd/agentdock worker -c worker.yaml
# worker.yaml: opencode.mode: server
```

回歸驗證：

```bash
go test ./worker/... ./shared/... ./test/...
npx --yes -p @commitlint/cli -p @commitlint/config-conventional \
    commitlint --last --extends @commitlint/config-conventional
```

## Project Structure

**main branch（這次工作 merge 進去的東西）**：

```
worker/agent/                       # Phase 3.2
    opencode_server.go              # 新增: server-mode runtime (lazy lifecycle, HTTP/SSE)
    opencode_spawn.go               # 從現有 runner.go 析出: legacy spawn path
    runner.go                       # 改為 dispatcher，依 opencode.mode 選 path

worker/config/config.go             # 新增 OpencodeConfig top-level field
worker/config/builtin_agents.go     # 不動 (agents.opencode.* 走既有 generic AgentConfig)

shared/queue/stream.go              # 既有 ReadStreamJSONOpencode 重用；
                                    # SSE 加薄殼 strip "data: " prefix 後餵同一個 parser

docs/specs/opencode-server-mode.md              # 本 spec
docs/specs/opencode-server-mode-poc-report.md   # Phase 3.1 POC 結果 (從 poc branch merge)
docs/adr/0005-opencode-server-mode.md           # 架構決策
```

**`poc/opencode-server-mode` branch（Phase 3.1，code 不 merge 回 main）**：

```
cmd/dev/poc-opencode-server/        # 拋棄式
    main.go                         # POC entry, P1-P10 各跑一次出 REPORT.md
    server.go                       # opencode serve subprocess supervisor (lazy + idle)
    client.go                       # HTTP/SSE client
    fixture.go                      # 載入 sanitized fixture，runtime 組 prompt + attachments
    testdata/harbor-4images/        # sanitized multimodal prompt + 4 PNG
    REPORT.md                       # POC 執行結果，merge 時改名進 docs/specs/
```

**worker.yaml schema (Phase 3.2)**：

```yaml
agents:
  opencode:               # 既有：CLI invocation 描述（不動）
    command: opencode
    args: [...]
    timeout: 35m
    skill_dir: .opencode/skills

opencode:                 # 新增 (V3): runtime 設定，跟 queue: / redis: / secrets: 同階
  mode: spawn             # spawn (default Phase 3.2 首版) | server (≥2 週監測後翻)
  idle_timeout: 5m        # server idle 多久後 stop (lazy lifecycle 用)
  storage_dir: ""         # empty → 自動 ~/.local/share/agentdock-worker/opencode；
                          # 用來 isolate XDG_DATA_HOME，避開跟 user 自己 opencode 的 SQLite WAL 衝突
```

附件（圖檔）遞送：**path reference 不變**。worker 仍把 attachments 寫到 per-job temp dir 的 `.attachments/`，prompt 純 text 帶 `<attachment path="..." type="image">` 標籤。HTTP body `parts` 只塞 `{type:"text", text: prompt}`，不走 multimodal `file` part。

## Code Style

依照現有 agentdock Go 慣例 (no comments unless WHY non-obvious)，logging 走 `shared/logging/GUIDE.md` 中文 message + 英文 keys + component/phase taxonomy：

```go
type ServerHandle struct {
    cmd        *exec.Cmd
    httpClient *http.Client
    baseURL    string
    password   string
    cancel     context.CancelFunc
}

func StartServer(ctx context.Context, opencodePath string, logger *slog.Logger) (*ServerHandle, error) {
    port, err := pickFreePort()
    if err != nil {
        return nil, fmt.Errorf("pick free port: %w", err)
    }
    pw := randomPassword()
    runCtx, cancel := context.WithCancel(ctx)
    cmd := exec.CommandContext(runCtx, opencodePath, "serve",
        "--port", strconv.Itoa(port),
        "--hostname", "127.0.0.1",
    )
    cmd.Env = append(os.Environ(), "OPENCODE_SERVER_PASSWORD="+pw)
    cmd.Stderr = newLogWriter(logger, "[opencode:stderr] ")
    if err := cmd.Start(); err != nil {
        cancel()
        return nil, fmt.Errorf("start opencode serve: %w", err)
    }
    h := &ServerHandle{
        cmd:        cmd,
        httpClient: &http.Client{},
        baseURL:    fmt.Sprintf("http://127.0.0.1:%d", port),
        password:   pw,
        cancel:     cancel,
    }
    if err := h.waitReady(runCtx, 10*time.Second); err != nil {
        cancel()
        return nil, fmt.Errorf("opencode serve not ready: %w", err)
    }
    logger.Info("opencode serve 啟動", "phase", "完成", "port", port, "pid", cmd.Process.Pid)
    return h, nil
}
```

## Testing Strategy

**POC (Phase 3.1)** — 不寫 `go test`，POC 是個 main，輸出 markdown report (`REPORT.md`)。每條 criterion 要有 verdict；`P2/P3/P9` 額外標記 `server_mode_regression` 或 `provider_retry_transient` 分類。

**Worker (Phase 3.2)**：

- Unit: `ServerHandle` lifecycle (start, healthcheck, restart-on-crash, graceful SIGTERM teardown)
- Integration: 完整 ask path through `agent.opencode.mode: server`，replay 同份 fixture
- 既有 `test/import_direction_test.go` 必須繼續綠 (server-mode code 不能洩漏 module 邊界)
- 既有 worker test 不得 regress

## Boundaries

**Always**:

- Worker → app contract 不動 (`RawOutput` over Redis 仍為唯一輸出 channel)
- Bug A 偵測：`finish=other` + `tokens.output=0` 一定 mark failed (絕不能再寫「Agent 執行成功 output_len=0」)
- Loopback-only binding (`127.0.0.1`)；`OPENCODE_SERVER_PASSWORD` 隨機產生且使用
- Logging 走 `shared/logging/GUIDE.md`
- **Server-pool mapping = 1:N**：每個 worker process 起一個 `opencode serve`，pool 內 N 個 goroutine 共用（per-request `x-opencode-directory` header 自動 isolate session）
- **Server lifecycle = lazy**：worker boot 不自動 spawn server；第一個 job 觸發 spawn，pool idle ≥ `opencode.idle_timeout` 後 graceful stop server。worker SIGTERM 必須 graceful teardown server（不留 orphan child process）
- **Storage isolation**：`opencode serve` 子 process 的 `XDG_DATA_HOME` 必須指向 worker-owned 目錄（預設 `~/.local/share/agentdock-worker/opencode`），絕不可共用 user 自己的 `~/.local/share/opencode/` — laptop 部署下會跟 user 的 opencode 撞 SQLite WAL lock
- **Auth/config 沿用 user 環境**：`XDG_CONFIG_HOME`、`XDG_CACHE_HOME` 不動，跟 spawn mode 今天一致（user `opencode auth login` 過的 token 直接共享）；pod 部署一樣在 image 裡 pre-auth 或 mount auth file
- **Skill mounting 不變**：worker 仍 reuse `worker/pool/executor.go mountSkills()`，把 per-job skills 寫到 temp dir 的 `.opencode/skills/{name}/`；server mode 透過 `directory` header 觸發 opencode 對該 dir 的 per-Instance skill loading（已驗 codebase）
- **Version floor enforced at startup**：`mode: server` worker boot 時若 opencode binary < `MinimumOpencodeVersion` 必須拒絕啟動（log + non-zero exit）；`mode: spawn` 不受影響。Floor 只能在 POC 重跑全綠後才升 — 見 [Version Policy](#version-policy)

**Ask first**:

- 加新 `go.mod` 依賴 (現規劃 stdlib only)
- 砍 legacy spawn path (現規劃 prod 監測 ≥ 2 週後才能議)
- 修 `worker.yaml` schema (除了加 top-level `opencode:` block + 對應的 `worker/config/config.go` `OpencodeConfig` struct，其他修改要先確認)
- 動 `app/` 或 `shared/` (除重用 `ReadStreamJSONOpencode` 外)

**Never**:

- 放開 host sandbox (`--dangerously-skip-permissions` 等) — worker 可能跑使用者本機
- 把 opencode serve 綁到 `0.0.0.0`
- 跳 pre-commit hook (`--no-verify`)
- 直接讀 opencode SQLite session DB (V1 已被否決，schema 不穩；不能當 shortcut 反向)
- 把 worker 的 server `XDG_DATA_HOME` 跟 user 的 opencode 共用（會撞 WAL lock，laptop 部署直接死）
- 對 pool 內每個 goroutine 各自起 server（V3 mapping 設計是 1:N，per-goroutine 是被 reject 的選項）
- Server-mode 失敗時 auto-fallback 到 spawn-mode（會 mask bug、雙倍 latency 沒 recovery）

## Success Criteria

### Phase 3.1 — POC

POC 通過 = 下列**全部同時**綠：

| #  | Criterion                                                                                                                                         | Verification                                                                                  |
| -- | ------------------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------- |
| P1 | `opencode serve` cold start → `/health` 200 `{healthy:true}` < 10s                                                                                | POC 量測；assert < 10000ms                                                                    |
| P2 | 單 session replay harbor-4images fixture (opencode 1.14.29) → 成功 case 的 text part 含可解析 `===ASK_RESULT===` JSON、`answer` 欄位非空；失敗 case 必須分類為 `server_mode_regression` 或 `provider_retry_transient` | 跑 100 attempts；assert `server_mode_regression_count=0`；`provider_retry_transient_count` 獨立統計進 REPORT |
| P3 | 同 P2 但 opencode HEAD                                                                                                                            | 跑 100 attempts；assert `server_mode_regression_count=0`；`provider_retry_transient_count` 獨立統計進 REPORT |
| P4 | 8 並發 sessions × 5 batches = 40 calls，每 session 拿到自己 prompt 的答案 (hash 比對 unique)                                                       | assert 40/40 success + 0 cross-session bleed                                                  |
| P5 | Per-session `directory` 隔離：8 sessions 各跑 `read marker.txt`，每個 cwd 放唯一 marker，assert 各 session 只讀到自己的                            | assert 8/8 isolated                                                                           |
| P6 | `--pure` 等價機制 (or 驗證 server mode 預設不載 `.opencode/` plugin)：cwd 放毒 plugin 確認沒被載入                                                 | assert poison plugin 沒執行                                                                   |
| P7 | Bug A 偵測：mock provider 回空 SSE → worker 邏輯 identify 為失敗 (`finish=other` + `tokens.output=0`)，**不可** log「執行成功」                    | assert log 為失敗、status 為 failed                                                           |
| P8 | `/session`, `/session/{id}/message`, `/event` schema 在 1.14.29 vs HEAD 無 breaking 變更 (OpenAPI spec snapshot non-breaking)                     | fetch `/doc`，diff；assert 沒移除 required field、沒重命名                                    |
| P9 | **Lazy lifecycle**：POC 起 supervisor，第一 job 來才 spawn server；模擬 8 個 job 完成後 idle ≥ `idle_timeout` (POC 用 30s 而非 prod 的 5min)，server 自動 stop；下個 job 再來 server re-spawn 成功；re-spawn 後至少再成功 replay 一次同 fixture | assert spawn → busy → idle-stop → re-spawn 一條完整 cycle；post-respawn happy-path 至少成功一次；若只落在 `provider_retry_transient`，記錄但不直接判 lifecycle red |
| P10 | **Storage isolation**：POC 跑 server 時設 `XDG_DATA_HOME=/tmp/poc-opencode-xdg-data`；replay fixture；assert (a) `/tmp/poc-opencode-xdg-data/opencode/opencode.db` 有 session 紀錄，(b) user 的 `~/.local/share/opencode/opencode.db` mtime / size 不變 | assert 兩條都成立 |

`P2/P3/P9` failure classification — 判準是「worker ↔ opencode serve 之間 transport 有沒有壞」，不是「upstream 回的 error 是不是 retryable」。upstream 雜訊 spawn-mode 也會中，不該卡 server-mode ship gate。

「**有意義的 assistant text**」定義：`len(strings.TrimSpace(lastAssistantText)) > 0`。純空白（如 `"\n\n\n"`）不算 meaningful — 避免把 server-mode bug「stray-whitespace 後出 error」誤判為 transient。

- `provider_retry_transient`：upstream / provider 雜訊，spawn 與 server mode 都會中。判定 signal 至少其一：
  - `session.status=retry` 且尚未產生有意義的 assistant text
  - `session.error` event 已透過 SSE 送達 worker（delivery 本身證明 transport 通），含 `IsRetryable=true`
  - `session.error` event 已送達 worker，且有意義的 assistant text 已部分送達後才出現（partial-meaningful-text 後 upstream loud error，transport 正常、屬 upstream noise）
  - SSE scanner / `context.DeadlineExceeded` 超時（`scan event stream: ...` error），且尚未產生有意義的 assistant text（provider 太慢）
- `server_mode_regression`：worker ↔ opencode serve transport / contract 層壞掉。判定 signal 至少其一：
  - SSE channel 沒收到終止 signal 就被 server 關（`event stream closed unexpectedly`、`shared event stream closed unexpectedly`）
  - 已收到有意義的 assistant text 後 stream 中斷但沒任何 error event（mid-flight 流斷）
  - HTTP request 在 transport 層失敗
  - unexpected idle / failed without parseable answer（含 Bug A 偵測）
  - 任何無法分類的 failure

POC 程式不需 production-grade。產出 `REPORT.md` 紀錄每條的 raw 數據、通過/失敗，以及必要時的 failure classification / transient count。

### Phase 3.2 — Production swap

**Functional**：

- **F1** `opencode.mode: server` 下，ask 走 e2e (Slack ask → worker → opencode HTTP → 回 Slack)，行為等同 legacy 模式 (modulo timing)
- **F2** `opencode.mode: spawn` 下，行為跟今天完全一致 (legacy 路保留，opt-in)
- **F3** server crash 中 job 觸發：(a) `cmd.Wait()` goroutine 偵測到 process exit → auto-restart subprocess；(b) 失敗 job 從新 session 重試 1 次（不嘗試 resume，server 死後 session state 不可信）；(c) 重試仍敗 → mark failed + 明確 error 訊息給 Slack user (絕不 silent)
- **F4** Worker SIGTERM → 對所有 active sessions 發 `POST /session/{id}/abort` → 等 grace period（30s）→ SIGTERM server → SIGKILL fallback；無 orphan child process
- **F5** Bug A 偵測：`finish=other` + `tokens.output=0` 一定 mark failed
- **F6** 同 host 多 workers：各跑各自 `opencode serve` 在 unique random port，cold start 無 port collision crash
- **F7** Lazy lifecycle：worker boot 不 spawn server；第一 job 觸發；pool idle ≥ `opencode.idle_timeout` 後 server graceful stop；boot lock 防止 N 個 goroutine 並發拉 job 時 spawn 多個 server
- **F8** Version capability check：`opencode.mode: server` 啟動時 worker 跑 `opencode --version` 並跟 `MinimumOpencodeVersion` 比較；低於 floor → 拒絕啟動 + 明確中文 error message (含 detected 版本、required 版本，指回 `docs/specs/opencode-server-mode-poc-report.md`)；`mode: spawn` 啟動路徑不受影響。詳見 [Version Policy](#version-policy)

**Non-functional**：

- **N1** Per-job latency (server) ≤ legacy median + 200ms (server mode 攤平 startup，後續 job 不能比 legacy 慢)
- **N2** First-job latency (cold worker、serve 還在啟動) ≤ legacy + 5s
- **N3** Idle RSS ≤ legacy + 200MB
- **N4** `go.mod` 不引入 stdlib 之外依賴
- **N5** `test/import_direction_test.go` 持續綠
- **N6** Logging 走 `shared/logging/GUIDE.md` taxonomy

**Compatibility / migration**：

- **C1** 首版 default `opencode.mode: spawn` (server 為 opt-in)
- **C2** server mode 在 prod 跑 ≥ 2 週、零 answer-drop 後，default 翻 `server`
- **C3** Default 翻完之後 spawn path 還留 ≥ 2 週才議刪除
- **C4** 不做 server-mode → spawn-mode 的 auto-fallback：失敗就明確 fail，避免 mask bug、避免雙倍 latency 沒 recovery

## Open Questions

- **O1** opencode HEAD 的 `/event` SSE 是否會 reliably 發 `session.status idle`？(失敗 v1.14.29 case 沒發。若 HEAD 也沒修，server-mode SSE consumer 要找替代 completion 訊號 — 例如 correlate `message.part.updated` 的 `time.end` 跟 POST response。POC P2/P3 驗證時順帶確認。)
- **O2** `opencode serve` 對 `OPENCODE_PERMISSION` env 行為？agentdock 沒設、multica 設 `{"*":"allow"}`。server mode 可能要改用 per-session permission rule POST 進去，不再靠 env。POC P5 驗證時順帶確認。
- **O3** Loopback binding 下 `OPENCODE_SERVER_PASSWORD` 仍必須提供？POC 驗（不提供時 `cli/cmd/serve.ts` 會 print warning，要確認功能上是否可呼叫）。
- **O4** `opencode serve` SIGTERM 對 in-flight session 的處理？若直接 abandon，F4 已寫顯式 drain step；POC 不驗（生產 corner case）。

## Resolved (codebase-verified)

- **~~O5~~** Skill 掛載：**已答**。`packages/opencode/src/cli/cmd/serve.ts` 註解：「Server loads instances per-request via x-opencode-directory header」；`packages/opencode/src/skill/index.ts` 的 `discovered = InstanceState.make((ctx) => discoverSkills(..., ctx.directory, ctx.worktree))` 證明 skills 跟著 Instance 跑、Instance 跟著 request `directory` 跑 → server mode 自動 per-session isolation，agentdock 的 `mountSkills()` 邏輯不用改。
