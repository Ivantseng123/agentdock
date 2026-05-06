# TODO: Observability v2 — OpenTelemetry Tracing

> Plan: `docs/superpowers/plans/2026-05-06-otel-tracing.md` · Spec: `docs/superpowers/specs/2026-05-06-otel-tracing-design.md`

## Phase 1 — 基建(無流量,build 不變)

- [x] **T1** 新增 `shared/tracing/` package(setup + propagation + constants),fail-soft,empty endpoint = silent skip
- [x] **T2** `Job.Traceparent` add-only + 雙向相容 fixture test(v1 ↔ v2 payload 都不爆)
- [x] **T3** 砍 v1 fallback:刪 `WithTraceID` / `TraceIDFrom`,handler 改讀 OTel SpanContext,清 2 個 callers
- [x] **T4** `cmd/agentdock/` 配線:YAML schema(`tracing.otlp_endpoint`)+ env override + `BuildTracerProvider` 啟動 + init 樣板
- [x] **T5** Commitlint baseline 跑一次

### ✅ Checkpoint Phase 1
- [ ] `go test ./... -race` 全綠(root + 三 module)
- [ ] `agentdock app/worker` 啟動成功
- [ ] 無 `WithTraceID` / `TraceIDFrom` 殘留
- [ ] (預期)log 暫時無 `trace_id`,Jaeger 無 span — 由 Phase 2 補回

## Phase 2 — App 端可見的第一個 trace(端到端最小 slice)

> ⚠️ **T3 + T6 + T7 強制綁同一 PR merge**,避免過渡期 log 缺 trace_id

- [x] **T6** `shared/queue.Submit()` auto-instrument `queue.enqueue` span(allowlist: queue/job_id/task_type/priority)
- [x] **T7** `app/app.go submitJob` 起 `bot.handle_event` root span,`Job.RequestID` 改填 OTel hex,`Job.Traceparent` Inject

### ✅ Checkpoint Phase 2
- [ ] log `trace_id` 回歸,為 OTel 16-byte hex
- [ ] Jaeger UI 看到 `bot.handle_event` + `queue.enqueue`(2 spans,`agentdock-app` service)
- [ ] `Job.RequestID` ≡ log `trace_id` ≡ Jaeger trace ID
- [ ] Empty endpoint 路徑仍正常

## Phase 3 — Worker 端跨 process 接上

- [x] **T8** Worker `worker.handle_job` umbrella + `agent.execute` span(無 `queue.dequeue`,Q10);agent attrs 嚴守 allowlist(無 prompt/stdout 內容)

### ✅ Checkpoint Phase 3
- [ ] Jaeger UI 4-span tree:`bot.handle_event` → `queue.enqueue` → `worker.handle_job` → `agent.execute`,跨兩 service 同 trace_id

## Phase 4 — 完整 5+ span tree

- [x] **T9** `shared/github/repo.go` `clone_repo` span(primary + ref sibling,`repo_role` attr 區分)
- [x] **T10** `result_listener` Extract `state.Job.Traceparent` + workflow `github.create_issue` span(parent = 原 root;不依賴 JobResult 加欄位)

### ✅ Checkpoint Phase 4
- [ ] 完整 5+ span tree
- [ ] PII 抽查:每個 span attr 對 ADR-0003 allowlist,無 thread/prompt/token/stdout 內容

## Phase 5 — Retry 拓樸

- [x] **T11** `retry_handler` 逐字複製 `Traceparent` + `RequestID`,**不開新 span**(ADR-0002);queue.enqueue auto-span 自動 child of 原 root

### ✅ Checkpoint Phase 5
- [ ] retry 後多個 `worker.handle_job` sibling 在同 trace_id
- [ ] 無任何 `bot.retry_handler` 之類 app-side umbrella span

## Phase 6 — Manual verify

- [ ] **T12** 手動跑三個 scenario(issue / ask+ref / retry)+ Jaeger 截圖貼 PR + ADR-0003 allowlist 抽查

### ✅ Final
- [ ] SPEC §Success Criteria 七條全 ✅
- [ ] CHANGELOG 更新
- [ ] PR ready for review
