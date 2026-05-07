# Implementation Plan: Observability v2 — OpenTelemetry Tracing

**Spec:** `docs/superpowers/specs/2026-05-06-otel-tracing-design.md`
**ADRs:** `docs/adr/0001-otel-tracing-posture.md` … `0004-no-v1-trace-id-fallback-path.md`
**Issue:** [#46](https://github.com/Ivantseng123/agentdock/issues/46)
**Date:** 2026-05-06
**Branch suggestion:** `feat/otel-tracing`

## Overview

引入 OpenTelemetry distributed tracing,讓 oncall 在 K8s `istio-system/jaeger-collector` 看到「一次 Slack 觸發」的完整 span tree(`bot.handle_event` → `queue.enqueue` → `worker.handle_job`(含 `github.clone_repo` / `agent.execute`)→ `github.create_issue`),並讓 log 行的 `trace_id` 與 trace 後端一致(OTel 16-byte hex)。

關鍵姿態(spec 與 ADR-0001/0002/0003/0004):
- **Fail-soft**:tracing 失敗永不致命,endpoint 空 = silent skip(real SDK 無 exporter,SpanContext 仍流經 ctx)
- **無 v1 fallback**:`logging.WithTraceID` / `TraceIDFrom` API 整段刪除,`Job.RequestID` 改填 OTel hex(同值)
- **No knob, no env override**:sampling 寫死 `AlwaysOn`,只有 `tracing.otlp_endpoint` 一個 YAML 欄位
- **Auto-instrument 在 shared/queue.Submit()**(不在 app caller 端)
- **Retry 不開新 span**:逐字複製 `Traceparent` + `RequestID`(ADR-0002)
- **PII allowlist(ADR-0003)是 cross-cutting**:每個 span 加 attr 都對表

## Architecture Decisions(摘要 SPEC §Decisions)

1. **gRPC over OTLP**(`otlptracegrpc`,Jaeger 1.35+ OTLP receiver,port 4317)
2. **`shared/tracing/` package** 落在 shared module,app + worker 共用,不違反 import direction
3. **`Job.Traceparent` add-only**:wire format 變更走 rolling deploy(worker 先升、app 後升)
4. **`BuildTracerProvider` 唯一呼叫點在 `cmd/agentdock/`**;library 用 package-level `var tracer = otel.Tracer(...)`,測試預設 noop
5. **`worker.handle_job` umbrella 取代 `queue.dequeue`**(worker 端唯一 ctx-bridging span)
6. **`github.create_issue` parent 取自 `state.Job.Traceparent`**(不依賴 `JobResult` 加欄位)

## Dependency Graph

```
T1 shared/tracing skeleton ────┬─→ T2 Job.Traceparent + fixture ────┬─→ T6 queue.Submit auto-span ─┬─→ T7 app submitJob root span ─┬─→ T9 worker.handle_job umbrella + agent.execute ──→ T10 github.clone_repo span
                               │                                    │                              │                               └─→ T11 retry handler 逐字複製
                               │                                    │                              └─→ T12 result_listener + workflow github.create_issue span
                               └─→ T3 logging trace.go 砍 v1 fallback ─┘
T1 ── T4 cmd/config 配線(YAML + cmd setup + init.go 樣板) ─────────────────────┘
                              T5 commitlint sanity 跑一次  ← 動工前 baseline,獨立
                                                                                                                                                                                T13 manual verify (Jaeger UI)
```

依賴解讀:
- **T1, T2, T3, T4** 是基建,互不依賴(可並行,但同一 PR 內 merge 較安全)
- **T6** 依賴 T1(用 tracer)+ T2(Submit 要在 enqueue 時知道 ctx,但 Job 有 traceparent 主要是 T7 之後才會有意義)
- **T3 砍 v1 fallback 過渡期 log 會無 trace_id**,因此實務上 T3 + T6 + T7 應在同一 PR merge,operator 不會看到過渡
- **T9 之後的 task** 都依賴 cross-process payload 已通(T7 + T9 上前)

## Task List

### Phase 1 — 基建(無流量,build green,行為不變)

#### Task 1 — `shared/tracing/` package skeleton

**Description:** 新增 `shared/tracing/` package,提供 `BuildTracerProvider`、`InjectFromContext` / `ExtractToContext`、span name 常數。`endpoint == ""` → 真 SDK 無 exporter(SpanContext 仍流經 ctx,不送任何 OTLP 流量);`endpoint != ""` → SDK + OTLP gRPC exporter(`BatchSpanProcessor` + `AlwaysOn`);任何錯誤 log warn 後 fallback 到無 exporter,**永不 panic**。`Shutdown(ctx)` 用 5s timeout flush。

**Acceptance criteria:**
- [ ] `BuildTracerProvider(ctx, cfg)` 簽名清楚(回 `*sdktrace.TracerProvider` + `shutdown func(context.Context) error`)
- [ ] `endpoint == ""`:回 real SDK 無 exporter;tracer.Start 後 ctx 仍可取 SpanContext
- [ ] `endpoint != ""` 但 collector 連不到:不 panic,log warn,回可用 provider(fail-soft)
- [ ] `InjectFromContext(ctx)` 回 W3C `traceparent` 字串(無 span → `""`)
- [ ] `ExtractToContext(ctx, str)` 對空字串 / malformed input graceful(回原 ctx,不 panic)
- [ ] `constants.go` 列出所有 span name(`SpanBotHandleEvent`、`SpanWorkerHandleJob`、`SpanQueueEnqueue`、`SpanGithubCloneRepo`、`SpanAgentExecute`、`SpanGithubCreateIssue`)

**Verification:**
- [ ] `(cd shared && go test ./tracing/... -race)` 全綠
- [ ] `test/import_direction_test.go` 仍通過

**Dependencies:** None

**Files likely touched:**
- `shared/tracing/setup.go` + `setup_test.go` (新)
- `shared/tracing/propagation.go` + `propagation_test.go` (新)
- `shared/tracing/constants.go` (新)
- `shared/go.mod` / `shared/go.sum`(加 OTel 相依)

**Estimated scope:** M(5 files,純新增)

---

#### Task 2 — `Job.Traceparent` 欄位 + 雙向相容 fixture

**Description:** `shared/queue/job.go` 新增 `Traceparent string `json:"traceparent,omitempty"``。新增 fixture test 證明:(a) v1 worker 反序列化 v2 payload(含 traceparent)不報錯、不 crash;(b) v2 worker 反序列化 v1 payload(無 traceparent)不報錯、`Job.Traceparent == ""`。

**Acceptance criteria:**
- [ ] `Traceparent` 欄位加在 Job struct,json tag `traceparent,omitempty`(空字串不出現在 wire)
- [ ] Fixture test 兩組 JSON payload(v1 / v2)雙向 unmarshal 不爆
- [ ] 既有 `shared/queue` test 全綠

**Verification:**
- [ ] `(cd shared && go test ./queue/... -race)` 全綠
- [ ] 用 `jq '.traceparent'` 對 fixture v1 payload 應為 `null`(欄位不存在)

**Dependencies:** None(獨立 schema 變更)

**Files likely touched:**
- `shared/queue/job.go`
- `shared/queue/job_test.go`(新增 fixture)

**Estimated scope:** XS(2 files)

---

#### Task 3 — 砍 v1 trace_id fallback path

**Description:** 依 ADR-0004,刪除 `shared/logging/trace.go` 的 `WithTraceID` / `TraceIDFrom` API,把 `TraceIDHandler` 改成純讀 OTel `SpanContext`(`trace.SpanContextFromContext(ctx)` → `TraceID().String()`);無 span → 不噴 trace_id attr。同步移除兩個 caller(`app/app.go:273`、`worker/pool/pool.go:104`)。

**過渡期警告:** 此 task 完成後,log 暫時無 `trace_id` attr(OTel ctx 還沒 span,要等 Task 7)。實務上 **Task 3 + Task 6 + Task 7 應綁同一 PR merge**,operator 不會看到過渡。

**Acceptance criteria:**
- [ ] `shared/logging/trace.go` 移除 `traceIDKey`、`WithTraceID`、`TraceIDFrom`;保留 `TraceIDHandler`(改讀 OTel)、`NewTraceIDHandler`
- [ ] `shared/logging/trace_test.go` 重寫:case (a) ctx 有 OTel span → record 帶 hex trace_id;case (b) 無 span → 無 trace_id attr;case (c) 無 span 不 panic
- [ ] `app/app.go:273` 移除 `ctx = logging.WithTraceID(ctx, p.RequestID)` 行(連同其上方 v1 過渡 comment)
- [ ] `worker/pool/pool.go:104` 移除 `jobCtx := logging.WithTraceID(ctx, job.RequestID)` 行,改為 `jobCtx := ctx`(維持 ctx 串)
- [ ] `grep -r "WithTraceID\|TraceIDFrom"` 結果只剩 0 行(全清乾淨)

**Verification:**
- [ ] `go test ./... -race` 在 root + 三個 module 全綠
- [ ] `gofmt -l . | grep -v vendor` 無輸出
- [ ] `(cd shared && go test ./logging/... -race)` 兩個新 case 都過

**Dependencies:** Task 1(handler 要 import OTel `trace` 套件取 SpanContext)

**Files likely touched:**
- `shared/logging/trace.go`
- `shared/logging/trace_test.go`
- `app/app.go`
- `worker/pool/pool.go`

**Estimated scope:** S(4 files)

---

#### Task 4 — `cmd/agentdock/` 啟動點配線 + YAML schema

**Description:** 在 `app/config/config.go` 與 `worker/config/config.go` 各新增 `TracingConfig{ OTLPEndpoint string }`,綁 `tracing.otlp_endpoint`;env override `OTEL_EXPORTER_OTLP_ENDPOINT` 優先 YAML。`cmd/agentdock/app.go` 與 `worker.go` 啟動時呼叫 `tracing.BuildTracerProvider`,`otel.SetTracerProvider`,defer `Shutdown(5s)`。`cmd/agentdock/init.go` YAML 樣板加 `tracing:` 區塊(預設 `otlp_endpoint: ""` + 註解說明「填了才送、不填 silent skip」)。

**Acceptance criteria:**
- [ ] `app.yaml` / `worker.yaml` schema 增 `tracing.otlp_endpoint`(string,預設 `""`)
- [ ] `OTEL_EXPORTER_OTLP_ENDPOINT` env 設值時覆蓋 YAML
- [ ] `agentdock app` / `agentdock worker` 啟動成功(空 endpoint silent skip)
- [ ] 啟動 log 印出 endpoint 來源(`from yaml` / `from env` / `silent skip`),屬於 info-level
- [ ] `agentdock init app -i` / `agentdock init worker -i` 產生的 YAML 含 `tracing:` 區塊與註解
- [ ] Shutdown 5s timeout 行為:正常退出 ≤ 5s;timeout 觸發時 log warn,不 block process

**Verification:**
- [ ] `(cd app && go test ./config/... -race)`、`(cd worker && go test ./config/... -race)` 綠
- [ ] `(cd cmd/agentdock && go test ./...)` 綠(尤其 init_test.go)
- [ ] 手動:`OTEL_EXPORTER_OTLP_ENDPOINT=localhost:9999 ./agentdock app` 啟動不死(連不到 9999 也活)
- [ ] 手動:`./agentdock app`(空 endpoint)啟動不死、無 OTLP 流量(`netstat`)

**Dependencies:** Task 1

**Files likely touched:**
- `app/config/config.go`
- `worker/config/config.go`
- `cmd/agentdock/app.go`
- `cmd/agentdock/worker.go`
- `cmd/agentdock/init.go`

**Estimated scope:** M(5 files)

---

#### Task 5 — Commitlint baseline

**Description:** 動工前先跑一次 commitlint 對 HEAD 確認 baseline 通過(避免 spec PR 之外的後續 commit 被 CI 卡)。CLAUDE.md 已要求每次 commit 後立刻跑;此處只是確認環境就緒。

**Acceptance criteria:**
- [ ] `npx --yes -p @commitlint/cli -p @commitlint/config-conventional commitlint --last --extends @commitlint/config-conventional` 對 HEAD 通過

**Verification:**
- [ ] 上面指令 exit 0

**Dependencies:** None(任何時候都可以跑,放這裡只是 sanity check)

**Files likely touched:** none(只跑指令)

**Estimated scope:** XS

---

### ✅ Checkpoint: Phase 1 完成

- [ ] `go test ./... -race` 在 root + 三個 module 全綠
- [ ] `agentdock app` / `agentdock worker` 啟動成功(任意 endpoint 配置)
- [ ] 沒有任何 `WithTraceID` / `TraceIDFrom` 殘留
- [ ] **此時 log 的 `trace_id` attr 暫時消失** — 預期行為,Phase 2 補回
- [ ] **此時 Jaeger 還沒任何 span** — 預期行為,Phase 2 開始送

---

### Phase 2 — App 端可見的第一個 trace(端到端最小 vertical slice)

#### Task 6 — `shared/queue.Submit()` 內部 auto-instrument `queue.enqueue` span

**Description:** 在 `shared/queue/redis_jobqueue.go` 的 `Submit(ctx, job)` 起一個 `queue.enqueue` span(child of caller's ctx),attrs: `queue=<taskType>`、`job_id`、`task_type`、`priority`(全部對 ADR-0003 allowlist;**禁止** payload / prompt 內容)。span 起在 `Submit` 第一行,`defer span.End()`;Redis err → `RecordError` + `codes.Error`。

**Acceptance criteria:**
- [ ] `Submit` 進入第一行起 `queue.enqueue` span;attrs 僅含 4 個 allowlist 欄位
- [ ] 失敗路徑:`RecordError` + `span.SetStatus(codes.Error, err.Error())`
- [ ] 成功路徑:無 status set(預設 Unset)
- [ ] Unit test:in-memory `tracetest.SpanRecorder` 注入後 Submit 一個 Job,recorder 撈出 1 個 `queue.enqueue` span,attrs 對得上

**Verification:**
- [ ] `(cd shared && go test ./queue/... -race)` 綠,新增的 auto-span test 過

**Dependencies:** Task 1, Task 2

**Files likely touched:**
- `shared/queue/redis_jobqueue.go`
- `shared/queue/redis_jobqueue_test.go`

**Estimated scope:** S(2 files)

---

#### Task 7 — `app/app.go submitJob` 起 root span + `RequestID` 改填 OTel hex + Inject Traceparent

**Description:** 把 `submitJob` 改造成:
1. `tracer.Start(ctx, tracing.SpanBotHandleEvent, ...)` 起 root span,attrs: `task_type`、`channel_id`(對 ADR-0003 allowlist;`reporter`、`thread_ts` 等 PII-adjacent 不放)
2. 取 `span.SpanContext().TraceID().String()` 蓋掉 `Pending.RequestID`(workflow 階段產的 timestamp+rand 被覆蓋成 OTel hex)
3. `tracing.InjectFromContext(ctx)` 寫進 `Job.Traceparent`
4. Submit 失敗 → `RecordError` + `codes.Error`
5. 成功 → defer `span.End()`,讓 Submit 回傳後 root span 也結束(注意:`bot.handle_event` 是 entry-point span,不延伸到 worker)

**注意:** SPEC 範例顯示 root span 在 submitJob 結束時就 End(span tree 上 worker.handle_job 是它的 child sibling,不是「root 還沒結束」的 nested)。

**Acceptance criteria:**
- [ ] `submitJob` 進入時起 `bot.handle_event` span;`defer span.End()` 在第一行 cleanup
- [ ] Span attrs 只含 `task_type` + `channel_id`(對表)
- [ ] `Job.RequestID` 為 16-byte hex(`len == 32`,純 hex)
- [ ] `Job.Traceparent` 非空,符合 W3C 格式 `00-<32hex>-<16hex>-<2hex>`
- [ ] Submit 失敗 → span 標 `codes.Error` + RecordError
- [ ] 既有 `app/app_test.go` 等測試仍綠;若有 fixture 預設 RequestID 是 timestamp 格式的測試需要更新

**Verification:**
- [ ] `(cd app && go test ./... -race)` 綠
- [ ] 手動:啟動 `agentdock app` + `agentdock worker`(`tracing.otlp_endpoint=localhost:4317`,本機 docker jaeger),觸發 `@bot issue`,Jaeger UI 出現 `bot.handle_event` + `queue.enqueue` 共 2 個 span,trace_id 一致

**Dependencies:** Task 1, Task 2, Task 3(必須先砍 fallback,否則 RequestID 邏輯衝突), Task 4(cmd 啟動 provider), Task 6(queue.enqueue auto-span)

**Files likely touched:**
- `app/app.go`(主要)
- `app/app_test.go` 或 `app/workflow/*_test.go`(若有 fixture 預設 RequestID 格式)

**Estimated scope:** S(2 files)

---

### ✅ Checkpoint: Phase 2 完成

- [ ] log 的 `trace_id` attr 回歸,且為 OTel 16-byte hex
- [ ] Jaeger UI 在 `agentdock-app` service 下看到 `bot.handle_event` + `queue.enqueue` span(2 個)
- [ ] `Job.RequestID` 與 log `trace_id` 與 Jaeger UI 上的 trace ID **三方同值**
- [ ] Empty endpoint 路徑:啟動正常、log trace_id 仍出現(取自 ctx SpanContext)、無 OTLP 流量
- [ ] **Phase 2 三 task(3 + 6 + 7)強烈建議綁同一 PR merge**,避免 prod 過渡期 log 缺 trace_id

---

### Phase 3 — Worker 端跨 process 接上

#### Task 8 — Worker `worker.handle_job` umbrella + `agent.execute` span

**Description:**
1. `worker/pool/pool.go` `runWorker` 收到 job 後:`ctx = tracing.ExtractToContext(ctx, job.Traceparent)`(空 traceparent → 起新 root span,fail-soft 相容 v1 app payload),`tracer.Start(ctx, tracing.SpanWorkerHandleJob, ...)` 起 umbrella span。Attrs: `task_type`、`repo`、`retry_count`(對表)。
2. `worker/agent/runner.go` `Run` / `runOne` 內部包 `agent.execute` span,attrs: `agent_type`(provider 名,例 `claude`)、`exit_code`、`stdout_len`(int,字數,**不放 stdout 內容**)、`stderr_len`、`duration_ms`。timeout / 非 0 exit_code 不算 OTel error,只放 attr;agent 啟動失敗(如 binary 不存在)算 `codes.Error`。

**Acceptance criteria:**
- [ ] `worker.handle_job` span 在 `runWorker` 收 job 第一處起,涵蓋 dequeue 後到 publish result 之間整段
- [ ] 空 `Traceparent`(v1 app 來源)→ `ExtractToContext` 不 panic、新 root span(fail-soft)
- [ ] `agent.execute` 為 `worker.handle_job` 子 span,attrs 嚴格遵 ADR-0003 allowlist(無 prompt / args / stdout 內容)
- [ ] timeout / non-zero exit:status 維持 Unset(business failure 由 attr 表達);binary missing → `codes.Error`
- [ ] 既有 worker test 全綠

**Verification:**
- [ ] `(cd worker && go test ./... -race)` 綠
- [ ] `go test ./test/... -race`(root module integration test)綠
- [ ] 手動:跑 `@bot issue`,Jaeger UI 看到 `bot.handle_event` → `worker.handle_job` → `agent.execute` 跨 service 連起,**全部同 trace_id**

**Dependencies:** Task 1, Task 2, Task 7

**Files likely touched:**
- `worker/pool/pool.go`
- `worker/agent/runner.go`
- `worker/pool/pool_test.go` 或 `worker/agent/runner_test.go`(span attr 驗證)

**Estimated scope:** M(3 files)

---

### ✅ Checkpoint: Phase 3 完成

- [ ] Jaeger UI 顯示 4-span tree:`bot.handle_event` (app) → `queue.enqueue` (app) → `worker.handle_job` (worker) → `agent.execute` (worker)
- [ ] 兩個 service(`agentdock-app`、`agentdock-worker`)在 Jaeger 都列出
- [ ] 跨 service 的 parent/child 連線正確

---

### Phase 4 — 完整 5+ span tree

#### Task 9 — `shared/github/repo.go` `clone_repo` span(primary + ref)

**Description:** 在 `EnsureRepo` / 主要 clone 入口包 `github.clone_repo` span,attrs: `repo_role`(`primary` / `ref`)、`repo`(owner/name)、`branch`、`duration_ms`、`cache_hit`(bool)。多個 ref repo 的 clone 是 sibling span(都是 `worker.handle_job` 子 span)。失敗 → `codes.Error` + RecordError。

**Acceptance criteria:**
- [ ] `EnsureRepo`(或對應入口)起 `github.clone_repo` span,attrs 對表
- [ ] Multi-repo Ask 場景下,primary 與每個 ref 各自一個 span,sibling 關係(同 parent)
- [ ] Clone 失敗(認證 / 網路 / 不存在)→ span error;ref clone 失敗不影響其他 ref(維持既有 graceful 行為)
- [ ] Test:用 in-memory recorder 驗 span 數與 attrs

**Verification:**
- [ ] `(cd shared && go test ./github/... -race)` 綠
- [ ] 手動:`@bot ask <Q>` 帶 ref repo,Jaeger UI 看到多個 `github.clone_repo` sibling span

**Dependencies:** Task 8(span 必須是 `worker.handle_job` 子 span,要 worker 端 ctx 建好)

**Files likely touched:**
- `shared/github/repo.go`
- `shared/github/repo_test.go`

**Estimated scope:** S(2 files)

---

#### Task 10 — `app/bot/result_listener.go` Extract + workflow `github.create_issue` span

**Description:**
1. `ResultListener.handleResult` 第一行 `ctx = tracing.ExtractToContext(ctx, state.Job.Traceparent)`,讓後續 workflow.HandleResult 內的 span 是原 `bot.handle_event` 的子 span。
2. `app/workflow/issue.go` `createAndPostIssue`(對應 `w.github.CreateIssue` 那一段)包 `github.create_issue` span,attrs: `repo`(owner/name)、`labels_count`、`title_len`(int,**不放 title / body 字串**)、`duration_ms`。
3. **Job result 不加 Traceparent 欄位**(SPEC line 134 / Q12)— parent 來自 store.Get 拿到的 state.Job.Traceparent。

**Acceptance criteria:**
- [ ] `handleResult` Extract 順序正確(在 `state, _ := r.store.Get(...)` 之後立刻 Extract)
- [ ] `github.create_issue` span 出現在 workflow.HandleResult / `createAndPostIssue` 路徑
- [ ] Span 是 `bot.handle_event` 的子 span(同 trace_id,parent_id 對得上)
- [ ] PII 對表:title / body 不出現;只放長度 / 計數
- [ ] CreateIssue 失敗 → `codes.Error`

**Verification:**
- [ ] `(cd app && go test ./bot/... -race)`、`(cd app && go test ./workflow/... -race)` 綠
- [ ] 手動:跑 `@bot issue`,Jaeger UI 在 root 之下看到 `github.create_issue` 子 span(與 `worker.handle_job` 為 sibling)

**Dependencies:** Task 7(state.Job.Traceparent 要先存在)

**Files likely touched:**
- `app/bot/result_listener.go`
- `app/workflow/issue.go`
- `app/workflow/issue_test.go` 或 `app/bot/result_listener_test.go`

**Estimated scope:** M(3 files)

---

### ✅ Checkpoint: Phase 4 完成

- [ ] Jaeger UI 顯示完整 tree:`bot.handle_event` → (`queue.enqueue`, `worker.handle_job` (含 `github.clone_repo` × n + `agent.execute`), `github.create_issue`)
- [ ] **5+ span 在同一 trace_id**
- [ ] PII 抽查:每個 span 的 attr 在 Jaeger UI 點開,確認無 thread message、prompt、token、stderr 內容

---

### Phase 5 — Retry 拓樸

#### Task 11 — `app/bot/retry_handler.go` 逐字複製 `Traceparent` + `RequestID`,不開新 span

**Description:** ADR-0002 明定 retry 沿用原 trace_id,**不開 app-side `bot.retry_handler` umbrella**。`RetryHandler.Handle`:
1. `original, _ := h.store.Get(ctx, jobID)` 拿原 Job
2. `ctx := tracing.ExtractToContext(context.Background(), original.Job.Traceparent)`(retry 可能由 Slack interaction 觸發,沒 inbound ctx,自開根 + Extract)
3. 構建 newJob 時 `RequestID = original.Job.RequestID`、`Traceparent = original.Job.Traceparent`(逐字)
4. `h.queue.Submit(ctx, newJob)` — `queue.enqueue` auto-span 自動 child of 原 root

**Acceptance criteria:**
- [ ] retry 後 newJob 的 RequestID 與 Traceparent **與原 Job 完全一致**
- [ ] 沒有任何 `tracer.Start("bot.retry_handler", ...)` 出現(對表禁止)
- [ ] Test:retry 兩次後,3 個 worker.handle_job span(原 + retry × 2)在同一 trace_id 下,sibling 關係
- [ ] Test:`queue.enqueue` retry 路徑也 auto-span,parent 為原 root

**Verification:**
- [ ] `(cd app && go test ./bot/... -race)` 綠
- [ ] 手動:觸發一個會失敗 + retry 的 job(可用 worker 端故意失敗 fixture),Jaeger UI 看到原 root 之下 2-3 個 `worker.handle_job` sibling

**Dependencies:** Task 7(原 Job 必須帶 Traceparent), Task 8(才有 worker.handle_job)

**Files likely touched:**
- `app/bot/retry_handler.go`
- `app/bot/retry_handler_test.go`

**Estimated scope:** S(2 files)

---

### ✅ Checkpoint: Phase 5 完成

- [ ] retry 鏈在 Jaeger 同一 trace_id 下顯示為多個 `worker.handle_job` sibling
- [ ] retry 不產生新的 app-side umbrella span

---

### Phase 6 — Manual verify + PR description

#### Task 12 — Manual end-to-end verify with Jaeger,寫進 PR

**Description:** 用 K8s `istio-system/jaeger-collector`(`kubectl port-forward svc/jaeger-collector 4317:4317`)作為 endpoint,跑三個 scenario 各一次:`@bot issue`(產出 5+ span tree)、`@bot ask <Q> + ref`(產出 multi-clone sibling)、retry 場景(產出多個 worker.handle_job sibling)。截圖貼 PR,並驗證 SPEC §Success Criteria 七條。

**Acceptance criteria:**
- [ ] Scenario 1 截圖:完整 5+ span tree
- [ ] Scenario 2 截圖:multi-clone sibling
- [ ] Scenario 3 截圖:retry 共 trace_id
- [ ] PR 描述列出 SPEC §Success Criteria 七條的逐條 ✅
- [ ] 抽查至少 1 個 trace 的所有 span attrs,**逐項對 ADR-0003 allowlist**,無 forbidden 欄位
- [ ] Empty endpoint 場景:停 port-forward,確認 worker log 仍印 trace_id(來自 ctx),app 業務正常

**Verification:**
- [ ] PR 描述 + 截圖 + ADR-0003 對表 checklist

**Dependencies:** Task 1-11 全部完成

**Files likely touched:** none(只是手動驗收 + PR doc)

**Estimated scope:** S

---

### ✅ Checkpoint: 完整實作完成,Ready for review

- [ ] 12 個 task 全 ✅
- [ ] SPEC §Success Criteria 七條全部驗證
- [ ] CHANGELOG 更新(release-please flow)
- [ ] PR description 完整,含 manual verify 截圖

---

## Risks and Mitigations

| Risk | Impact | Mitigation |
|------|--------|------------|
| Phase 1 Task 3 砍 fallback 後,Task 7 還沒 merge → prod log 失去 trace_id | High(oncall 一段時間無法 trace) | Task 3 + 6 + 7 強制綁同一 PR merge,plan 已標明 |
| `Job.RequestID` 從 timestamp+rand 變 hex,既有 dashboard / log query 失效 | Med | release notes 標 breaking,提供 query update 指南;雙寫策略 spec 早期討論已被否決(ADR-0001 + Q3) |
| Rolling deploy 時 v1 worker 反序列化 v2 payload | Med | Task 2 fixture test 已驗;deploy SOP 走「worker 先升、app 後升」 |
| collector 連不到時 buffer 滿 → 業務 latency 飆 | Med | `BatchSpanProcessor` 預設 drop-on-full;在 Task 1 setup 顯式設 `WithMaxQueueSize` 限制 |
| PII 不小心放進 span attr | High | ADR-0003 allowlist 是 cross-cutting,每個 span task 自己對表;Task 12 manual verify 抽查;後續可考慮 lint(超出本 plan 範圍) |
| Span 太多吃 collector throughput | Low | 流量低(<10 events/s)、`AlwaysOn` 安全;Q4 已決議超過再降比例 |
| Library code 誤呼叫 `BuildTracerProvider` 兩次 | Low | Task 1 文件 + Task 4 cmd 註解明標「唯一呼叫點」;test 預設 noop 也是防線 |

## Open Questions

無 — SPEC 已全部拍板。動工中若發現意外設計取捨,以新增 Decisions / ADR 處理(非更動 plan task 結構)。

## Implementation Notes(動工人請讀)

1. **Build green 至上**:每個 task 結束時 `go test ./... -race` 必須綠;不要為了 task 切割犧牲 build green
2. **PII 對表是 muscle memory**:每加一個 `attribute.String(...)` 之前,問「這個值會出現在 Jaeger UI 嗎?是否在 ADR-0003 allowlist?」
3. **不要動現有 logger 介面**:既有 `logger.Info(...)` / `logger.InfoContext(...)` 不需改;只是 handler 內部從 `WithTraceID` 讀換成 OTel ctx 讀
4. **Test 預設 noop**:任何 unit test 不要呼叫 `BuildTracerProvider`;需要驗 span 的 test 用 `tracetest.NewSpanRecorder` + `otel.SetTracerProvider(testProvider)` + `t.Cleanup` 復位(SPEC §Testing Strategy 最末段)
5. **Commit message**:CLAUDE.md 已要求 commitlint;每次 commit 後立刻跑驗證
