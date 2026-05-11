# Spec: Observability v3 — Metrics Gap Closure

> Implements [#47](https://github.com/Ivantseng123/agentdock/issues/47). Issue #47 was filed before substantial Prometheus infrastructure landed; this spec closes the remaining gap rather than rebuilding what already exists.

## Objective

#47 的 MVP 清單列了 7 個 metric 命名;經盤點(現有 22 個 metric 在 `shared/metrics/metrics.go`),其中 5 個已有實作或被現有 metric 涵蓋,**2 個是名義上的命名缺口**:

- `agentdock_agent_exit_code_total{provider, exit_code}` — 補強既有 `agent_executions_total{status}` 的「殺進程訊號」維度,讓 OOM kill (137) / timeout (124) / agent CLI 自爆 (1) 在 metric 層可分。
- `agentdock_slack_events_total{type}` — 補上 mention / slash / interaction 等 Slack 事件型別計數;現有 `request_total{status}` 只分 accepted/rate_limited/dedup,看不到流量組成。

第三個曾被認為是缺口的 `agentdock_job_duration_seconds{phase}` 經盤點**已由現有三個獨立 histogram**(`queue_wait_seconds` + `agent_prepare_seconds` + `agent_execution_seconds{provider}`)涵蓋;新增統一 metric 等同重複觀測同一段時間,違反「Never 改既有 metric」精神。**本 PR 不加第三個 metric**,改在 `docs/operations.md` 列 PromQL 拼法。

同時:
- 加 `metrics.enabled` config 開關(預設 `true`,維持既有行為)。
- 加 cardinality audit 自動測試,以 allowlist 對既有 `ref_write_violations_total{repo}` 做顯式例外。
- 寫 ADR `docs/adr/0005-metrics-prometheus.md` 紀錄選 Prometheus 而非 OTel metrics 的決定 + #47 命名對映表。

#47 列的四個 oncall 問題(成功率 / P95 執行時間 / queue 等待 / 失敗率最高 agent)以**現有** metric 即已可答;本 PR 同時把具體 PromQL 寫入 `docs/operations.md`,把這個事實顯化。

### Acceptance criteria(可驗證)

1. **既有 oncall 能力顯化**:`docs/operations.md` 列出回答 #47 四個 oncall 問題的 PromQL 範例(全部以既有 metric 寫成)。
2. **`metrics.enabled` toggle**:設定可切換 `/metrics` handler 是否掛載 **以及** `Register()` 是否執行。預設 `true`(維持現有行為);`false` 時 `/metrics` 回 404 且 Prometheus 預設 registry 不被弄髒。
3. **兩個新 metric 註冊與觀測**:
   - `agent_exit_code_total{provider, exit_code}`:在 `result_listener.recordMetrics` 觀測,當 `result.ExitCode >= 0` 時記錄(全 termination 含 `exit_code="0"`)。
   - `slack_events_total{type}`:在 `app.go:handleSocketEvent` switch 入口觀測,label 來自純函式 `slack.EventTypeLabel`。
4. **Cardinality audit**:`shared/metrics/metrics_test.go` 的 `TestLabelCardinality` 對 `staticCollectors` slice 走 `Desc` parsing,assert 沒有 banned label key 出現;allowlist 內的單一例外 `(ref_write_violations_total, "repo")` 顯式記錄並含理由。
5. **ADR 提交**:`docs/adr/0005-metrics-prometheus.md` 內含 Prometheus 選型理由、雙 stack(OTel traces / Prom metrics)決定、以及 #47 命名對映表附錄。

### 非目標(明確不做)
- Business metrics(`cost_per_issue` 等)— issue 已聲明。
- 高基數 label(`channel_id` / `user_id` / `repo` 入 metric label)— issue 已聲明。
- 新建 Grafana dashboard / alert rule — issue 已聲明。**但** repo 已有 `deploy/grafana/agentdock-dashboard.json`;把兩個新 metric(`agent_exit_code_total`、`slack_events_total`)的 panel 加進那份既有 dashboard 屬於「維護既有 artifact」,不算新建,本 PR 一併做。
- Custom Go runtime metrics — issue 已聲明。
- **Worker 自有 HTTP / metrics endpoint 與行為改動** — 經確認,worker 維持現狀;本 PR 對 worker 的改動限縮為**單一 data plumbing**(`JobResult.ExitCode` 欄位的 setter),不加 endpoint、不改觀測行為、不改 control flow。
- **OTel metrics push pipeline** — Prometheus pull 已是事實上的決定,不引入第二條 metrics pipeline。
- 重新命名既有 metrics(`agent_executions_total` → `jobs_total` 等)— 會破壞既有 dashboard / alert,且語意已涵蓋。
- **統一 phase histogram**(`job_phase_duration_seconds{phase}`)— 與既有三個獨立 histogram 重複觀測同一段;以 `docs/operations.md` 的 PromQL 拼法替代。
- **`metrics.listen_addr`(獨立 metrics port,與 `server.port` 分流)**— 本期不加;觀察 ops 實際需求後另案 minor PR。ADR 0005 Decision #4 記錄。

## Tech Stack

| 項目 | 版本 | 備註 |
|---|---|---|
| Go | 依 root `go.mod` | 不變 |
| `prometheus/client_golang` | v1.23.2(現有) | 不升版,不新加依賴 |
| OTel SDK | v1.43.0(現有,僅 tracing 用) | metrics SDK **不**引入 |

無新增第三方依賴。

## Commands

```bash
# 建置
go build -o agentdock ./cmd/agentdock/

# 測試(每個 module 都要跑,import direction test 在 root)
go test ./... -race
(cd shared && go test ./... -race)
(cd app && go test ./... -race)
(cd worker && go test ./... -race)   # 證 worker zero behavior change

# 單獨跑 metrics 套件
(cd shared && go test ./metrics/... -race -v)

# Cardinality audit(spec 內定義的腳本)
(cd shared && go test ./metrics/... -run TestLabelCardinality -v)

# Commit lint(每次 commit 後立即跑)
npx --yes -p @commitlint/cli -p @commitlint/config-conventional \
  commitlint --last --extends @commitlint/config-conventional
```

## Project Structure

僅以下檔案會被 touch:

```
shared/metrics/metrics.go            → 新增 2 個 metric(AgentExitCodeTotal, SlackEventsTotal);抽 staticCollectors slice,Register 從 slice 取
shared/metrics/metrics_test.go       → 新增 metric 單元測試 + TestLabelCardinality(走 staticCollectors + Desc parsing + banlist + allowlist)
shared/queue/job.go                  → JobResult 加 ExitCode int 欄位(sentinel -1 表「無進程 / 未 wait」)
worker/agent/runner.go               → runOne deferred closure 多寫一行 result.ExitCode = exitCode(zero behavior change,單純 data plumbing)
app/config/config.go                 → 新增 MetricsConfig struct(Enabled *bool 與 IsEnabled() helper)+ Config.Metrics 欄位
app/config/defaults.go               → 不需顯式預設(IsEnabled() 對 nil 回 true)
app/config/validate.go               → metrics.IsEnabled() && server.port <= 0 時 log warning(soft warn,不 fail)
app/app.go                           → handleSocketEvent switch 入口呼叫 metrics.SlackEventsTotal.Inc(label = slack.EventTypeLabel(evt));Run 抽 mountHTTPHandlers helper;條件化 Register + /metrics 掛載
app/app_http_test.go                 → 新檔,測 mountHTTPHandlers:enabled=true → /metrics 200;enabled=false → /metrics 404
app/bot/result_listener.go           → recordMetrics 加 AgentExitCodeTotal 觀測(條件 result.ExitCode >= 0)
app/bot/result_listener_test.go     → 對應測試(含 -1 sentinel 不觀測 / 0 與正值觀測 / cancelled 含 exit code 也觀測)
app/slack/event_label.go             → 新檔,純函式 EventTypeLabel(socketmode.Event) string
app/slack/event_label_test.go        → 新檔,table-driven 涵蓋 7 個 enum + unknown fallback
app/config/config_test.go(既存) → 加 MetricsConfig 載入測試 + IsEnabled 預設行為
docs/adr/0005-metrics-prometheus.md  → 新 ADR
docs/configuration-app.md            → 補 metrics 段落
docs/operations.md                   → 列 #47 四個 oncall 問題的 PromQL 範例 + phase breakdown PromQL 拼法 + 「>16 distinct exit_code 值視為 worker 異常」運維註記
SPEC.md                              → 本檔
```

不動 `shared/tracing/`(分屬 v2)。worker 改動範圍**嚴格限縮為 `runner.go` 一行 data plumbing**,以 `(cd worker && go test ./... -race)` 驗 zero regression。

## Code Style

匹配 `shared/metrics/metrics.go` 既有風格 — namespace `agentdock`,snake_case name,labels 為 `[]string{"key1","key2"}` slice,`Help` 短英文單行。

新增兩個 counter 範例:

```go
var AgentExitCodeTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
    Namespace: namespace,
    Name:      "agent_exit_code_total",
    Help:      "Agent process exit code distribution. Observed for any termination with a captured code; -1 (no process / not waited) is skipped at the call site.",
}, []string{"provider", "exit_code"})

var SlackEventsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
    Namespace: namespace,
    Name:      "slack_events_total",
    Help:      "Slack socketmode events received, labelled by business event type.",
}, []string{"type"})
```

新增指標時:
- Counter 用 `_total` suffix。
- Histogram 用 `_seconds` / `_bytes` 等單位 suffix(本期無新 histogram)。
- Buckets 依用途設計,不抄 `DefBuckets`。
- Label key 用 snake_case,值為固定列舉(不放動態 ID)。
- 觀測點呼叫:`metrics.X.WithLabelValues(...).Observe(...)` 或 `.Inc()`,放在 hot path 結束時(用 defer + closure 抓 elapsed)。

註冊改採 **`staticCollectors` slice**:新加的 metric 變數加進 slice,`Register()` 用 `reg.MustRegister(staticCollectors...)`。GaugeFunc 因需 q / store 仍在 `Register()` 內條件式註冊。`Register()` 是唯一註冊點,**不要**在 `init()` 註冊。

## Testing Strategy

**框架**:Go 標準 `testing` + `prometheus/client_golang/prometheus/testutil`(已是專案測試慣例,見 `app/bot/result_listener_test.go`、`app/workflow/ask_test.go`)。

**測試層級**:

1. **單元(必要)** — 每個新 metric 一個 test:用 `testutil.CollectAndCompare` 確認觸發點正確 increment,labels 對。Slack event label 純函式以 table-driven 涵蓋 7 個 enum + unknown fallback。
2. **Cardinality audit(必要)** — `TestLabelCardinality` 走 `staticCollectors` slice,對每個 collector 呼叫 `Describe()` 後 parse `Desc.String()` 撈出 variableLabels,assert 沒有 banned key(`channel_id`/`user_id`/`thread_ts`/`repo`/`pr_number`/`issue_number`)出現,**allowlist** 顯式記錄唯一例外 `(agentdock_ref_write_violations_total, "repo")` 與理由。
3. **Config(必要)** — `metrics.enabled` 的 nil(預設 true)/ true / false 三條路徑都要有 `app/config` 測試。
4. **Integration(必要)** — `app/app_http_test.go` 透過 `mountHTTPHandlers(mux, cfg, ...)` helper + `httptest`,驗 `enabled=true` /metrics 回 200、`enabled=false` 回 404。Helper 是 `app.go:Run` 的純結構性 refactor(零行為變動),把 mux 組裝集中到單一可測的函式。

**Coverage**:不設絕對門檻,但每個新增 public 函式 / 觀測點都需有對應 test。

**Worker zero-behavior-change 驗證**:`(cd worker && go test ./... -race)` 必須在新增 `result.ExitCode = exitCode` 那行前後皆全綠;新加的 worker test 限定為 `JobResult.ExitCode` 在 success / non-zero exit / 未啟動三種 path 的 setter 行為。

## Boundaries

### Always
- 新增 metric 時,檢查 label cardinality(預估每個 label 值的 unique 數;乘積上限 < 1000 為安全帶)。
- 用 **`staticCollectors` slice** 集中註冊;新增的 metric **必須**加進 slice。Audit test 也走這個 slice;落單會被同時遺漏 scrape + audit。
- Metric name 用 `agentdock_` namespace prefix(由 `namespace = "agentdock"` 自動加,只填 `Name`)。
- 文件改動同步:`docs/configuration-app.md` 加 metrics 段;`docs/operations.md` 補 PromQL examples + 異常閾值。
- 每次 commit 後跑 commitlint(見 CLAUDE.md commit hygiene)。
- Conventional Commits format,subject 用英文。

### Ask first
- 修改 `MetricsConfig` schema(欄位增刪、型別改變)— 影響 `app.yaml` 兼容性,提 PR 前確認設計。
- **新增 `metrics.listen_addr`(獨立 metrics port)** — 本期已 defer(見 ADR 0005 Decision #4);若 ops 實作中發現需要,獨立 minor PR 提。
- 對既有 metric 的 label / name / type 做變更 — 會破壞 dashboard。
- 新增 third-party 依賴(本 spec 應為零新依賴)。
- **Cardinality allowlist 新增條目** — 任何新加的「`(metric, label)` 例外」要在 PR 內顯式註記理由,reviewer 必看。

### Never
- 把 `channel_id`、`user_id`、`thread_ts`、`repo`(全名 `owner/repo`)、`pr_number`、`issue_number` 放進 metric label。
  **Why**:Prometheus 是 cardinality-bound 系統;這些 ID 是 unbounded growth set。Issue #47 已明確禁止。
  **How to apply**:寫新 metric 前先盤一次 label values 的 unique 數;若無法確界,改放進 log / span attr。Audit test 是這條規則的自動執行手段。
  **既有例外**:`agentdock_ref_write_violations_total{repo}` 已在 audit allowlist。理由:ref repos 為 channel config 顯式指定的固定集合,不是 user-supplied URL,cardinality 有界。新類似例外需獨立 review。
- 在 `init()` 中 `MustRegister` metric。`Register()`(透過 `staticCollectors`)是唯一入口,單元測試需要乾淨 registry。
- 用 `--no-verify` 或類似旗標跳過 commit hooks(CLAUDE.md commit hygiene)。
- 把 metrics 端點 bind 到 `0.0.0.0` 不加說明 — `/metrics` 通常含內部資訊,文件需提示 ops 用 ingress / 防火牆限縮(`metrics.listen_addr` 本期不實作,文件先寫出來)。
- **改變 worker 的觀察行為或 control flow**。`JobResult` schema 增欄位 + 對應 setter 屬於 data plumbing,允許;但 worker 內部既有 metric 觀測點、agent 執行邏輯、retry / cancel 路徑都不動。`(cd worker && go test ./... -race)` 必須驗 zero regression。

## Success Criteria

| # | 條件 | 驗證方式 |
|---|------|---------|
| 1 | 兩個新 metric(`agent_exit_code_total` / `slack_events_total`)註冊與觀測 | `go test ./shared/metrics/... ./app/...` 通過 |
| 2 | `result_listener` 與 slack event 觀測點有對應 unit test(含 `ExitCode=-1` 不觀測的 sentinel path) | 同上,含覆蓋率 |
| 3 | `metrics.enabled=false` 時 `/metrics` 回 404 **且** `Register()` 跳過 | `app/app_http_test.go` 驗 |
| 4 | `metrics.enabled` 預設 true,既有部署不變 | 預設值單元測試 + `app_http_test.go` enabled=true path 驗 200 |
| 5 | Cardinality audit 通過(allowlist 顯式記錄唯一例外) | `go test ./shared/metrics/... -run TestLabelCardinality` |
| 6 | ADR `docs/adr/0005-metrics-prometheus.md` 提交,內含選型理由 / 雙 stack / `metrics.enabled` 預設 / `listen_addr` defer / cardinality allowlist / #47 mapping appendix | 文件存在且過 review |
| 7 | `docs/operations.md` 列 #47 四個 oncall 問題的 PromQL + phase breakdown PromQL 拼法 + exit_code 異常閾值 | 文件存在 |
| 8 | `go test ./... -race` 全綠(含 worker 模組,證 zero behavior change;含 import direction test) | CI Test job pass |
| 9 | Commit lint 通過 | `commitlint --last` 0 violation |
| 10 | 既有 `/metrics` 輸出未失去任何 metric(無 regression) | `curl /metrics` diff 前後,只多不少 |

## Decisions

(本節原為 Open Questions;經 grilling 全部釐清,改記決定。)

1. **統一 phase metric**:**不實作**。三個獨立 histogram 已涵蓋 queue_wait / agent_prepare / agent_execute;`result_post` phase 由 `external_duration_seconds{service,operation}` 的 slack/github 觀測涵蓋 P95,total 可由 `request_duration_seconds_sum - 三段之和` 推導。`docs/operations.md` 列具體 PromQL 拼法。

2. **Slack event type 列舉值**:`app_mention` / `member_joined` / `member_left` / `slash_command` / `block_suggestion` / `block_action` / `unknown`(共 7 個固定值,audit cap=10)。**不**包含 socketmode 連線事件(`Connecting`/`Connected`/`Disconnect`/`Hello`/`IncomingError`)— 那是「socket 健康」概念,若需另加 `slack_connection_state` gauge。

3. **Agent exit code 桶化**:raw int(string label)。Audit cap **不寫進 unit test**(runtime 值的數量不能靜態檢);改在 `docs/operations.md` 記「>16 distinct values 視為 worker 異常,調查 `worker/agent/runner.go`」。

4. **`metrics.listen_addr`**:本期不加。ADR 0005 Decision #4 記錄。

5. **ADR 附錄收 #47 mapping 表**:納入(7 row,含「等同」/「superset」/「拆成 N 個」三種對映關係)。

6. **`JobResult.ExitCode` sentinel**:採 `-1`(`int` zero 0 與 success exit 衝突,需 sentinel 區分「未啟動」)。worker 端永遠寫(-1 或實際值);app 端 `if result.ExitCode >= 0 { observe }`。

7. **`metrics.enabled` toggle 範圍**:同時條件化 `Register()` 與 `/metrics` handler 掛載。觀測點本身的 `.Inc()` / `.Observe()` 不加條件(metric 變數仍存在,無 scraper 收即可)。

8. **既有 `RefWriteViolationsTotal{repo}` cardinality 違規**:不移除 label;以 audit test 的 **allowlist** 顯式記錄為已知且正當化的例外。

---

**狀態**:本 spec 待 user review。確認後依 spec-driven-development 流程進到 Phase 2(Plan)→ Phase 3(Tasks)→ Phase 4(Implement)。
