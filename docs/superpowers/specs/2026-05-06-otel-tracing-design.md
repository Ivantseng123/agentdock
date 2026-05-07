# Spec: Observability v2 — OpenTelemetry Tracing 導入

> 對應 issue: [#46](https://github.com/Ivantseng123/agentdock/issues/46)
> 前置: [#45](https://github.com/Ivantseng123/agentdock/issues/45)（已 closed,v1 已 ship)
> 後續: observability v3 — metrics(尚未開 issue)
>
> **Backend 已就緒**:K8s `istio-system/jaeger-collector` 開放 OTLP gRPC `4317` 與 HTTP `4318`,本 spec 採 **gRPC**。Issue #46 的硬前提(collector 存在)已驗證。
>
> **Decision provenance**:本 spec 經 grill 修訂,設計姿態與關鍵取捨見 [`docs/adr/0001-otel-tracing-posture.md`](../../adr/0001-otel-tracing-posture.md)、[`0002-retry-trace-topology.md`](../../adr/0002-retry-trace-topology.md)、[`0003-span-attribute-pii-allowlist.md`](../../adr/0003-span-attribute-pii-allowlist.md)、[`0004-no-v1-trace-id-fallback-path.md`](../../adr/0004-no-v1-trace-id-fallback-path.md)。

## Objective

延續 observability v1(#45)。v1 提供了 `trace_id`(來源 `Job.RequestID`,timestamp+rand 字串),讓 oncall 在 log aggregator 用一個 ID 撈出整條鏈;但無法回答「**這次 45 秒的 triage,哪一段最慢?**」這類耗時定位問題。

v2 引入 OpenTelemetry distributed tracing,讓使用者(agentdock 的 oncall / SRE)能:

1. 在 trace 後端(Jaeger,K8s `istio-system/jaeger-collector`)看到「一次 Slack 觸發」從 `app` 進入點到 `worker` 完成、再回到 `app` 建 issue 的 span tree:

```
bot.handle_event (app, root, ~45s)
├── queue.enqueue (app, ~5ms, auto from shared/queue.Submit)
├── worker.handle_job (worker umbrella, ~40s)
│   ├── github.clone_repo (primary)
│   ├── github.clone_repo (ref1, ref2, ...)   ← multi-repo Ask 時才有,平行兄弟
│   ├── agent.execute [claude] (~10s)
│   └── (worker 結束 → publish result)
└── github.create_issue (app, ~5s, child of bot.handle_event)
```

2. 從 log 行的 `trace_id`(OTel 16-byte hex)跳到對應 span(兩端一致)。`Job.RequestID` 也是 OTel trace_id 的 16-byte hex,wire 上跟 log 上同值。

3. **Tracing 失敗永不致命**:endpoint 未設、collector 連不到、跑到一半網路抖,都不會讓 app/worker 死掉(見 ADR-0001)。

## Tech Stack

- Go(既有 module split:`app/`、`worker/`、`shared/`)
- 新增依賴(集中落在 `shared/`,app + worker 共用):
  - `go.opentelemetry.io/otel`
  - `go.opentelemetry.io/otel/sdk`
  - `go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc`(gRPC,對應 Jaeger collector 4317)
  - `go.opentelemetry.io/otel/semconv/v1.x.x`
- 配置:既有 YAML(`app.yaml` / `worker.yaml`)新增 `tracing:` 區塊,**只有一個欄位** `otlp_endpoint`。沒有 `enabled` flag、沒有 sampling knob、沒有 environment knob;`deployment.environment` 走 OTel 標準 env `OTEL_RESOURCE_ATTRIBUTES`。
- Sampling:**寫死 `AlwaysOn`**,不開 knob、不支援 `OTEL_TRACES_SAMPLER` env override。要降比例直接出 PR 改 code(預期 >10 events/s 才會發生)。
- Cross-process carrier:`Job` struct 新增 `Traceparent string`(W3C Trace Context 格式 `00-<trace-id>-<span-id>-<flags>`)。
- 不引入 baggage、不引入自訂 exporter、不引入 metrics(留給 v3)。

## Commands

```bash
# Build
go build -o agentdock ./cmd/agentdock/

# Test(root + 三個 module)
go test ./... -race
(cd shared && go test ./... -race)
(cd app && go test ./... -race)
(cd worker && go test ./... -race)

# Lint(repo 慣例)
gofmt -l . | grep -v vendor

# Commit message 驗證(CLAUDE.md 要求,每次 commit 後立刻跑)
npx --yes -p @commitlint/cli -p @commitlint/config-conventional \
  commitlint --last --extends @commitlint/config-conventional

# Local 驗收路徑 1:Jaeger all-in-one(本機 docker)
docker run -d --name jaeger -p 16686:16686 -p 4317:4317 jaegertracing/all-in-one:latest
# 設 tracing.otlp_endpoint=localhost:4317
./agentdock app
./agentdock worker
# 觸發 @bot issue,到 http://localhost:16686 看 span tree

# Local 驗收路徑 2:port-forward 到叢集真實 Jaeger(更貼近 prod)
kubectl -n istio-system port-forward svc/jaeger-collector 4317:4317
# 設 tracing.otlp_endpoint=localhost:4317

# Local 驗收路徑 3:Worker on laptop 但無 Jaeger
# tracing.otlp_endpoint 留空 → silent skip,worker spans 不送任何地方,
# 但 worker log 仍可印出 trace_id(來自 Job.Traceparent extract),跨 process log grep 可用。

# In-cluster 部署(prod):endpoint 走 service DNS
#   tracing.otlp_endpoint: jaeger-collector.istio-system.svc.cluster.local:4317
# 或用 OTel 標準 env:OTEL_EXPORTER_OTLP_ENDPOINT(env 覆蓋 YAML)
```

## Project Structure

```
shared/
  tracing/                       ← 新增 package
    setup.go                     ← BuildTracerProvider:
                                   • endpoint 空 → real SDK 無 exporter(SpanContext 仍流經 ctx,
                                     log 仍能取 trace_id;不送任何 OTLP 流量)
                                   • endpoint 非空 → SDK + OTLP gRPC exporter(BatchSpanProcessor)
                                   • 任何錯誤 → log warn 後 fallback 到無 exporter,**永不 panic**
                                   • Shutdown(ctx) 用 5s timeout flush
    setup_test.go
    propagation.go               ← Inject(ctx)→string / Extract(ctx, string)→ctx,封裝 W3C
    propagation_test.go
    constants.go                 ← span name 常數,例:
                                   SpanBotHandleEvent   = "bot.handle_event"
                                   SpanWorkerHandleJob  = "worker.handle_job"   ← worker umbrella
                                   SpanQueueEnqueue     = "queue.enqueue"        ← auto from queue layer
                                   SpanGithubCloneRepo  = "github.clone_repo"
                                   SpanAgentExecute     = "agent.execute"
                                   SpanGithubCreateIssue = "github.create_issue"
                                   (注:無 queue.dequeue — worker.handle_job umbrella 已涵蓋)
  logging/
    trace.go                     ← **WithTraceID / TraceIDFrom 刪除**(見 ADR-0004)。
                                   TraceIDHandler 改為純讀 OTel SpanContext;
                                   無 span → 不噴 trace_id attr(不 panic、不空字串)
    trace_test.go                ← 兩個 case:有 OTel span / 無 span
  queue/
    job.go                       ← +Traceparent string `json:"traceparent,omitempty"`(add-only)
    redis_jobqueue.go            ← Submit() 內部 auto-instrument `queue.enqueue` span
                                   (attrs: queue, job_id, task_type, priority)

app/
  app.go                         ← submitJob 起 root span `bot.handle_event`,
                                   生成 Job.RequestID = trace_id 16-byte hex,
                                   Inject ctx → Job.Traceparent;
                                   **不再呼叫 WithTraceID**(OTel ctx 已涵蓋)
  workflow/                      ← 各 workflow 視需求開 sub-span(BuildJob 預設不 span,
                                   除非耗時 >100ms 的 GitHub call 才要包)
  bot/
    retry_handler.go             ← retry 不開自己的 span(見 ADR-0002):
                                   • newJob.RequestID = original.RequestID
                                   • newJob.Traceparent = original.Traceparent(逐字複製)
                                   • Extract(original.Traceparent) → ctx → 直接 Submit
                                   queue.enqueue 由 shared/queue auto-span,parent 自動為原 root
    result_listener.go           ← github.create_issue span:
                                   Extract(state.Job.Traceparent) → ctx,
                                   tracer.Start(ctx, "github.create_issue")
                                   (parent = 原 bot.handle_event,不依賴 JobResult.Traceparent)

worker/
  worker.go                      ← startup 呼叫 BuildTracerProvider 並 otel.SetTracerProvider
  pool/pool.go                   ← handle() 進來:Extract(Job.Traceparent) → ctx,
                                   tracer.Start(ctx, "worker.handle_job") 起 umbrella;
                                   **無 queue.dequeue span**(umbrella 已涵蓋 dequeue 後的 ~5ms)
  agent/                         ← agent.execute span 點位 + attrs(見 ADR-0003 allowlist)

shared/github/repo.go            ← github.clone_repo span(已是 shared,app + worker 共用)
                                   primary 與 ref clone 都用同一個 span name,
                                   `repo_role` attr 區分(primary/ref);
                                   失敗 → span Status `codes.Error` + RecordError

cmd/agentdock/
  app.go / worker.go             ← config → tracing.BuildTracerProvider →
                                   otel.SetTracerProvider → defer Shutdown(5s)
                                   **此處是 BuildTracerProvider 唯一呼叫點**(見 §Testing Strategy)
  init.go                        ← `agentdock init` 的 YAML 樣板加入 tracing: 區塊,
                                   otlp_endpoint 預設 "" + 註解(填了才送、不填 silent skip)
```

## Code Style

延用既有 `component + phase` 結構化 log + ctx 流。span 命名一律 `<component>.<verb_object>`,lower-snake。

```go
// app/app.go: submitJob 起 root span
import (
    "go.opentelemetry.io/otel"
    "go.opentelemetry.io/otel/attribute"
    "go.opentelemetry.io/otel/codes"
    "go.opentelemetry.io/otel/trace"

    "github.com/Ivantseng123/agentdock/shared/logging"
    "github.com/Ivantseng123/agentdock/shared/tracing"
)

var tracer = otel.Tracer("agentdock/app")

submitJob := func(ctx context.Context, p *workflow.Pending) {
    ctx, span := tracer.Start(ctx, tracing.SpanBotHandleEvent,
        trace.WithAttributes(
            attribute.String("task_type", p.TaskType),
            attribute.String("channel_id", p.ChannelID),
        ),
    )
    defer span.End()

    job := buildJob(ctx, p)
    job.RequestID = span.SpanContext().TraceID().String()  // OTel trace_id hex
    job.Traceparent = tracing.InjectFromContext(ctx)        // W3C carrier

    if err := jobQueue.Submit(ctx, job); err != nil {       // queue 層 auto-span queue.enqueue
        span.RecordError(err)
        span.SetStatus(codes.Error, err.Error())
        return
    }
}

// worker/pool/pool.go: dequeue 後起 umbrella
func (p *Pool) handle(ctx context.Context, job *queue.Job) {
    ctx = tracing.ExtractToContext(ctx, job.Traceparent)
    ctx, span := tracer.Start(ctx, tracing.SpanWorkerHandleJob,
        trace.WithAttributes(
            attribute.String("task_type", job.TaskType),
            attribute.String("repo", job.Repo),
            attribute.Int("retry_count", job.RetryCount),
        ),
    )
    defer span.End()
    // ...clone / agent execute 各自起子 span(都 child of worker.handle_job)
}

// app/bot/retry_handler.go: retry 不開自己的 span
func (h *RetryHandler) Handle(channelID, jobID, statusMsgTS string) {
    original, _ := h.store.Get(ctx, jobID)
    ctx := tracing.ExtractToContext(context.Background(), original.Job.Traceparent)
    newJob := &queue.Job{
        // ...其他欄位 copy...
        RequestID:    original.Job.RequestID,        // 沿用 trace_id
        Traceparent:  original.Job.Traceparent,      // 逐字複製
        RetryCount:   original.Job.RetryCount + 1,
        RetryOfJobID: original.Job.ID,
    }
    h.queue.Submit(ctx, newJob)  // queue 層 auto-span queue.enqueue,parent 自動為原 root
}

// app/bot/result_listener.go: github.create_issue 取原 root 為 parent
func (r *ResultListener) handleResult(ctx context.Context, result *queue.JobResult) {
    state, _ := r.store.Get(ctx, result.JobID)
    ctx = tracing.ExtractToContext(ctx, state.Job.Traceparent)
    // workflow.HandleResult 內部會 tracer.Start("github.create_issue", ...)
    // 它的 parent 是原 bot.handle_event(透過上面 Extract)
}
```

關鍵慣例:
- Span attrs 用 lower-snake,**僅限 ADR-0003 allowlist**;PII / prompt / token / 子 process 內容絕對不放
- 任何耗時段加 span 前先確認可拿到 `context.Context`;拿不到 → 先把 ctx 串進去,別開全域 tracer 繞過
- Span Status:OTel 操作失敗 / 網路 err / clone 失敗 → `codes.Error` + RecordError;agent 退出碼非 0 → Unset(business 失敗,exit_code attr 已表達);使用者取消 → Unset(`cancelled=true` attr)
- Library code(`app/`, `worker/`, `shared/`)用 package-level `var tracer = otel.Tracer("agentdock/<process>")`,讀 global TracerProvider;**不直接呼叫 `BuildTracerProvider`**(只在 cmd 啟動流程呼叫一次)

## Testing Strategy

- **Unit(per package)**:
  - `shared/tracing/setup_test.go`:
    - endpoint 空 → BuildTracerProvider 回 real SDK 無 exporter,SpanContext 在 tracer.Start 後仍可從 ctx 取出
    - endpoint 非空 → 建構 OTLP exporter 不 panic;collector 連不到 → log warn 後仍回可用 provider(fail-soft)
    - `Shutdown(ctx)` 在 5s timeout 內 flush,timeout 到 graceful return
  - `shared/tracing/propagation_test.go`:Inject 出來的 traceparent 字串可 Extract 回相同 trace_id / span_id;空字串 / malformed input graceful(回原 ctx)
  - `shared/logging/trace_test.go`:兩個 case — (a) ctx 內有 OTel span:取 OTel hex;(b) ctx 內無 span:不噴 trace_id attr、不 panic
  - `shared/queue/redis_jobqueue_test.go`:Submit() auto-span 驗證 — 在已注入 in-memory recorder 的 ctx 下呼叫 Submit,recorder 能撈到 `queue.enqueue` span 與其 attrs
- **Integration(root module `test/`)**:
  - 跨 process 的 span parent/child:用 `sdktrace.NewTracerProvider` + in-memory `tracetest.NewSpanRecorder` 替代真實 OTLP collector;檢查 root → enqueue → handle_job → 子 spans 共用同一 trace_id
  - Retry 鏈:retry 後新 Job 的 worker spans 與原 Job 的 worker spans 共用 trace_id,但 span_id 各自獨立(ADR-0002)
  - 既有 `test/import_direction_test.go` 必須繼續通過(tracing 落 `shared/`,不違反 `shared ✗ app|worker`)
- **Manual verify**(動工人手動跑一次,寫進 PR 描述):
  - `docker run jaegertracing/all-in-one`,跑 `@bot issue` end-to-end,UI 出現 5+ span 的完整 tree(含 `bot.handle_event` 為 root、`worker.handle_job` 含 clone/execute 子 spans、`github.create_issue` 為 root 的 sibling)
  - Worker on laptop empty endpoint:確認 worker log 印出 `trace_id="<與 app 一致的 hex>"`,Jaeger UI 上可看到 app 端的 span tree(worker spans 缺,但 trace_id 仍對得上)
- **Coverage gate**:repo 沒設硬 % 目標,但每個新 span 點位至少對應一個 unit test
- **TracerProvider 隔離**:測試預設不呼叫 `BuildTracerProvider`,`otel.GetTracerProvider()` 回內建 noop,所有 `tracer.Start` 為 no-op、零外送、零 panic。需要驗證 span 的測試在 `TestMain` 或個別 test 裡 `otel.SetTracerProvider(testProvider)` + `t.Cleanup` 復位。

## Boundaries

**Always do(無腦做)**:
- 新增 span 點位前,先確認函式已能拿到 `context.Context`;沒有就先把 ctx 串進去
- 每加一個 span 同步寫 unit test 驗 attrs 與 status code
- Commit 後立刻跑 commitlint(CLAUDE.md 要求);CI 用 `wagoid/commitlint-github-action@v6` 把關
- 動 `Job` 等 wire-format struct 時,必須新增 fixture test 證明「舊 struct 反序列化新 payload」與「新 struct 反序列化舊 payload」都不爆
- 任何新 span attribute 都對照 [ADR-0003 allowlist](../../adr/0003-span-attribute-pii-allowlist.md);若不在表上 → 改用衍生的安全值(長度 / 計數 / enum)或開 ADR amendment
- Retry handler 路徑:**逐字複製** `original.Traceparent` 與 `original.RequestID` 到 newJob;不另開 span(見 ADR-0002)

**Ask first(動之前先問)**:
- 動 `Job` struct(就算 add-only field) — 因 wire format 影響 rolling deploy 順序(worker 先升、app 後升),要先 confirm deploy 計畫
- 新增任何不在「Tech Stack」清單上的 dependency
- Sampling 策略偏離 `AlwaysOn` 預設(例如想加 head-based 0.1)
- OTLP exporter 從 gRPC 換到 HTTP(套件不同、K8s service port 不同)
- 想把 worker → Jaeger 的直推改成「worker → app → Jaeger」中繼(ADR-0001 已明確否決,要動就要新 ADR 推翻)

**Never do(紅線,需明確授權才可動)**:
- 讓 tracing 失敗(endpoint 未設、collector 連不到、SDK 錯誤)弄掛 app/worker 主流程 — 任何 panic / os.Exit / log.Fatal 都不行(見 ADR-0001)
- 在 agent CLI subprocess 注入 OTel SDK — 外部 CLI 不可控,issue 已明定 subprocess 是一個 leaf span
- 改名或移除 `Job.RequestID` — v1 已 ship,wire format breaking
- 把 ADR-0003 forbidden list 上的欄位放上 span(尤其 thread message 文字、prompt 內容、加密 secrets、子 process 完整 args / stdout / stderr 內容)
- 為了過 OTel sandbox 加 `--dangerously-skip-permissions` 或放寬 worker host 沙盒(CLAUDE.md 紅線,與本 issue 無關但仍適用)
- 把 retry 改成「新 trace_id + OTel link」或「同 trace_id 但開 app-side `bot.retry_handler` umbrella」— 見 ADR-0002

## Success Criteria(可驗收)

1. **跨 process trace tree**:`@bot issue` end-to-end 後,trace 後端出現 root `bot.handle_event`,含 `queue.enqueue` / `worker.handle_job`(含 `github.clone_repo` / `agent.execute` 子 spans)/ `github.create_issue` 子 spans,**全部同一個 trace_id**
2. **Log↔trace 對齊**:同一筆事件的 app stderr / worker stderr / log file 至少 1 行 record,其 `trace_id` 等於 trace 後端 root span 的 trace ID(OTel 16-byte hex);`Job.RequestID` 同值
3. **Tracing 失敗不致命**:
   - endpoint 未設 → app/worker 正常啟動跑完整流程,無 OTLP 流量(`netstat` / `lsof` 驗證),trace_id 仍出現在 log(取自 ctx SpanContext)
   - endpoint 設了但 collector 不通 → app/worker 啟動成功,SDK log warn,業務流程不受影響
   - collector 跑到一半掛 → 業務流程不受影響,buffer 滿 spans graceful drop
4. **Rolling deploy 安全**:
   - v1 worker(無 traceparent 解析、把 RequestID 當 timestamp+rand)收到 v2 app 的 Job 不 crash(舊 worker 略過未知欄位、把 hex RequestID 當 opaque 字串)
   - v2 worker 收到 v1 app(無 `traceparent`、RequestID 為 timestamp+rand)的 Job 不 crash,fallback 起新 root span
5. **Retry 在同一 trace**:retry 後的 worker spans 與原 worker spans 共用 trace_id,Jaeger UI 在原 `bot.handle_event` 之下看到多個 `worker.handle_job` sibling
6. **PII 不外洩**:span attrs 在 unit test 與 manual verify 雙重檢查下,完全符合 ADR-0003 allowlist;forbidden list 上的欄位不出現在 Jaeger UI 任一 span
7. **既有測試全綠**:`go test ./... -race` 在 root + 三個 module 全綠;`test/import_direction_test.go` 不違反

## Decisions(已拍板)

| # | 問題 | 拍板 | ADR |
|---|------|------|-----|
| Q1 | OTLP transport | **gRPC**(`otlptracegrpc`,Jaeger collector port `4317`) | — |
| Q2 | Trace backend | **Jaeger**(K8s `istio-system/jaeger-collector`,1.35+ 內建 OTLP receiver) | — |
| Q3 | `trace_id` 格式 | **OTel 16-byte hex 唯一**;v1 timestamp+rand 格式整段移除;`Job.RequestID` 也填 OTel hex(同值) | [0001](../../adr/0001-otel-tracing-posture.md) |
| Q4 | Sampling | **`AlwaysOn` 寫死**,無 YAML knob、無 `OTEL_TRACES_SAMPLER` env override。改變需 PR | — |
| Q5 | Endpoint 來源優先序 | **env > YAML > 空字串**;空字串 = silent skip(real SDK 無 exporter) | [0001](../../adr/0001-otel-tracing-posture.md) |
| Q6 | OTLP collector 前提 | ✅ 已具備(`jaeger-collector.istio-system:4317`) | — |
| Q7 | Tracing posture | **永遠嘗試,永不致命**:endpoint 空 silent skip / 啟動連不到 fail-soft / runtime 抖動 fail-soft | [0001](../../adr/0001-otel-tracing-posture.md) |
| Q8 | TraceIDHandler | **砍 v1 fallback**:`WithTraceID` / `TraceIDFrom` API 刪除,handler 純讀 OTel SpanContext | [0004](../../adr/0004-no-v1-trace-id-fallback-path.md) |
| Q9 | Worker → Jaeger 路徑 | **直推,不經 app 中繼**;empty endpoint = 不送但保留 ctx SpanContext | [0001](../../adr/0001-otel-tracing-posture.md) |
| Q10 | Worker 端 span tree 形狀 | 加 `worker.handle_job` umbrella;**砍 `queue.dequeue` span**(umbrella 已含) | — |
| Q11 | Retry trace 拓樸 | **沿用 trace_id,不開 app-side umbrella**;copy `Traceparent` + `RequestID` | [0002](../../adr/0002-retry-trace-topology.md) |
| Q12 | `github.create_issue` parent | 原 `bot.handle_event`,經 `state.Job.Traceparent` extract;`JobResult` 不加 Traceparent 欄位 | — |
| Q13 | Span attribute schema | **PII allowlist**(ADR-0003);任何新 attr 對表 | [0003](../../adr/0003-span-attribute-pii-allowlist.md) |
| Q14 | `queue.enqueue` 開在哪 | **`shared/queue.Submit()` 內部 auto-instrument**,所有 caller 共享 | — |
| Q15 | `BuildTracerProvider` 呼叫位置 | **僅在 `cmd/agentdock/` 啟動流程**;library code 用 global `otel.Tracer(...)`,測試免設 = 預設 noop | — |

> Resource attrs 採 OTel 標準(Jaeger 兼容):`service.name=agentdock-app` / `agentdock-worker`(依 process 兩個獨立 service)、`service.version=<main.version>`、`deployment.environment` 從 `OTEL_RESOURCE_ATTRIBUTES` env 自然帶入(YAML 不設此欄位,維持 single source of truth)。

## Open Questions(可動工後再決議)

無 — 全部拍板。若實作中發現意外,以新增 Decisions 條目處理(屬於 hard-to-reverse + surprising + real-tradeoff 三條件 → 同步開新 ADR)。
