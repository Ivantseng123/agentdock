# App 設定 (`app.yaml`)

[English](configuration-app.en.md)

`agentdock app` 讀取的 YAML。預設位置：`~/.config/agentdock/app.yaml`。跑 `agentdock init app -i` 可自動產生含註解的範本。

## Schema

```yaml
log_level: info                       # console / stderr 層級：debug | info | warn | error

server:
  port: 8080                          # /healthz, /jobs, /metrics HTTP endpoint

slack:
  bot_token: xoxb-...                 # REQUIRED
  app_token: xapp-...                 # REQUIRED

github:
  token: ghp-...                      # REQUIRED：列出 repos、建 issues

channels:
  C0123456789:
    repos: [owner/repo-a, owner/repo-b]
    default_labels: [triage]
    branches: [main, release]
    branch_select: true

channel_defaults:
  branch_select: true
  default_labels: [from-slack]

auto_bind: true                       # 加入 channel 時自動綁
max_thread_messages: 50               # 讀多少訊息進 prompt
semaphore_timeout: 30s

rate_limit:
  per_user: 5
  per_channel: 10
  window: 1m

mantis:
  base_url: https://mantis.example.com    # host 根即可，不必含 /api/rest
  api_token: xxxxx                        # 兩個欄位必須同時填或同時留空

channel_priority:
  C_INCIDENTS: 100
  default: 50

prompt:
  language: 繁體中文
  allow_worker_rules: true            # 是否讓 worker 的 extra_rules 生效

  # 每個 workflow 各自一組 goal + response_schema + output_rules。留空就用內建預設。
  issue:
    goal: "Use the /triage-issue skill to investigate and produce a triage result."
    response_schema: |
      Your final response MUST end with ONE of these three shapes after ===TRIAGE_RESULT===:
      CREATED  → {"status":"CREATED","title":"<required>","body":"...","labels":[...],"confidence":"high|medium","files_found":<int>,"open_questions":<int>}
      REJECTED → {"status":"REJECTED","message":"..."}
      ERROR    → {"status":"ERROR","message":"..."}
    output_rules: []                  # 格式規則寫在 triage-issue skill 的 SKILL.md body template，不在這
  ask:
    goal: "Answer the user's question using the thread, and (if a codebase is attached) the repo. Follow the ask-assistant skill for scope, boundaries, and punt rules."
    response_schema: |
      Your final response MUST end with this exact block (no leading whitespace, no markdown fence around it):

      ===ASK_RESULT===
      {"answer": "<your full markdown answer as a single JSON string>"}

      The JSON key MUST be literally "answer". Do NOT use "text", "content", "response" or any synonym.
    output_rules:
      - "Format the answer in Slack mrkdwn — NOT GitHub markdown ..."
      - "No title, no labels — output the answer content only. Keep it ≤30000 chars."
      - "When referring to yourself, use the exact Slack handle from the <bot> tag ..."
  pr_review:
    goal: "Review the PR. Use the github-pr-review skill to analyze the diff and post line-level comments plus a summary review via agentdock pr-review-helper."
    response_schema: |
      Your final response MUST end with ONE of these three shapes after ===REVIEW_RESULT===:
      POSTED  → {"status":"POSTED","summary":"...","comments_posted":<int>,"comments_skipped":<int>,"severity_summary":"clean|minor|major"}
      SKIPPED → {"status":"SKIPPED","summary":"...","reason":"lockfile_only|vendored|generated|pure_docs|pure_config"}
      ERROR   → {"status":"ERROR","error":"<diagnostic>","summary":"<what you would have posted>"}
    output_rules:
      - "Focus on correctness, security, style"
      - "Summary ≤ 2000 chars"

  # Legacy flat 欄位（v2.1 之前的寫法）。只有在 prompt.issue.* 留空時，才會被拷到
  # prompt.issue.*。新配置建議直接寫 prompt.issue.*，不要混用。
  # goal: "..."
  # output_rules: []

pr_review:
  enabled: true                       # PR Review workflow feature flag；預設開啟，`enabled: false` 才關

skills_config: /etc/agentdock/skills.yaml   # 動態 skill 載入設定（可選）

attachments:
  store: ""                           # 預留：未來可切換 backend
  temp_dir: /tmp/triage-attachments
  ttl: 30m

repo_cache:
  dir: /var/cache/agentdock/repos     # 必須是絕對路徑
  max_age: 10m

queue:
  capacity: 50
  transport: redis                    # 擴充點；目前僅支援 redis
  store: redis                        # JobStore backend：redis（預設）/ mem
  store_ttl: 1h                       # store=redis 時每筆紀錄的 TTL（store=mem 忽略）
  job_timeout: 20m                    # watchdog：job 生命週期上限
  agent_idle_timeout: 5m              # stream-json 無事件多久視為卡住
  prepare_timeout: 3m
  cancel_timeout: 60s
  status_interval: 5s

logging:
  dir: logs
  level: debug                        # 滾動檔案層級
  retention_days: 30
  agent_output_dir: logs/agent-outputs

redis:
  addr: redis:6379                    # queue.transport=redis 時必填
  password: ""
  db: 0
  tls: false

secret_key: 0123456789abcdef...       # 64 hex chars (32-byte AES-256 key)，redis 模式必填

secrets:
  GH_TOKEN: ghp_xxx                   # key = 環境變數名，value = 明文；會加密傳給 worker
  K8S_TOKEN: your-k8s-token
```

## JobStore backend（`queue.store`）

App 用 `JobStore` 追蹤每個 Job 的 lifecycle（Pending → Running → Completed/Failed/Cancelled）。`queue.store` 決定狀態放哪裡：

| 值 | 說明 | 建議場景 |
|---|---|---|
| `redis`（預設） | 持久化到 Redis（`jobstore:*` key）。app 重啟仍可 resume in-flight job。 | 生產環境、多數部署 |
| `mem` | 在 app process 記憶體。app 重啟就全掉。 | Unit test、單 pod local dev（無 Redis 持久化需求時） |

`queue.store_ttl`（預設 `1h`）是 `redis` 模式每筆紀錄的 TTL，每次 Put / UpdateStatus / SetWorker / SetAgentStatus 會 refresh。Terminal-state 的 job 不主動刪除，讓 TTL 自己 evict。TTL 要設得比**最長預期 job 執行時間**明顯還大，否則長時間 job 可能在執行中被 TTL 砍掉 state。`mem` 模式忽略這個欄位（MemJobStore 自己跑 1h cleanup）。

重啟時若走 `redis`，app 會 `ListAll()` 一次並 log `rehydrated in-flight jobs from previous instance`（數量 = 非 terminal 狀態筆數）。不會把 state 塞回任何 in-memory index——`ResultListener` 查 `store.Get` 直接打 Redis。

**Redis 負載預期**：切到 `store=redis` 後，每次 worker StatusReport（預設 `worker.status_interval: 5s`）會觸發 ~2 次 `store.Get` + 視狀態轉換觸發 1 次 `UpdateStatus`（WATCH/MULTI/EXEC 往返）。N 個 active worker × 3 ops / 5s ≈ `0.6N` QPS 的額外 Redis 流量。Sizing 時納入考量，但對一般 Redis 規模可忽略。

背景 / incident：[#123](https://github.com/Ivantseng123/agentdock/issues/123)（app 重啟後 Slack 端 in-flight job orphan）、[#146](https://github.com/Ivantseng123/agentdock/issues/146)（wire-up PR）。

## Workflow-specific prompts

`prompt.issue` / `prompt.ask` / `prompt.pr_review` 各自一組 `goal` + `response_schema` + `output_rules`：
- `goal` 是給 agent 的**任務描述**——做什麼事、該呼叫哪個 skill（`triage-issue` / `ask-assistant` / `github-pr-review`）。不要在 goal 裡寫輸出格式。
- `response_schema` 是**輸出契約**——機器可讀的 marker + JSON 結構（`===ASK_RESULT===` / `===REVIEW_RESULT===` 等）。此區塊在 prompt builder 裡**不會**被 XML escape，literal `"` 和 `<` 原樣送給 LLM，避免弱模型看到 `&quot;` 後把它複製成字面輸出導致 JSON parse 失敗。
- `output_rules` 是**格式規則**——Slack mrkdwn 語法、字數限制、自稱 handle 等。會被 xml-escape 後 render 到 prompt 尾端。
- 任何欄位留空都會由 `app/config/defaults.go` 的 `defaultIssueGoal` / `defaultAskGoal` / `defaultPRReviewGoal` / `defaultIssueResponseSchema` / `defaultAskResponseSchema` / `defaultPRReviewResponseSchema` / `defaultAskOutputRules` / `defaultPRReviewOutputRules` 填上。`issue.output_rules` 預設為空——格式規則走 `triage-issue` skill 的 SKILL.md body template，config 這層不重複。

**Legacy alias**：`prompt.goal` / `prompt.output_rules`（扁平）在載入時會被拷到 `prompt.issue.*`（前提是 `prompt.issue.*` 還沒設）。這只是為了讓 v2.1 之前的 yaml 還能跑；新配置直接寫 `prompt.issue.*`。

## PR Review

`pr_review.enabled` **預設 `true`**（`github-pr-review` skill 和 `agentdock pr-review-helper` subcommand 都已經包進 release image，預設 opt-out 即可）。若要關掉，顯式寫 `pr_review.enabled: false`。

`@bot review <PR URL>` 走 PRReviewWorkflow；沒帶 URL 時會掃 thread、掃不到就開 modal。執行前確認：
1. Worker 側已經載到 `github-pr-review` skill（`skills_config` 有指到 `app/agents/skills/github-pr-review`）。
2. Worker container 的 `agentdock pr-review-helper` 可執行（內建 subcommand，app/worker binary 要同版）。
3. `secrets.GH_TOKEN` 在目標 PR 有 review comments 權限。

## Log 層級

兩個獨立的 log 層級：

| 欄位 | 去哪 | 預設 |
|---|---|---|
| `log_level` | console / stderr | `info` |
| `logging.level` | 滾動檔案 `logs/YYYY-MM-DD.jsonl` | `debug` |

支援值：`debug` / `info` / `warn` / `error`。可改用 CLI flag（`--log-level debug`）或 env var（`LOG_LEVEL`）。

## Secrets

Redis 模式下，app 集中管理 secrets 並加密傳給 worker：

1. App config 設定 `secret_key`（AES-256 金鑰）和 `secrets`（key-value）。
2. App 啟動時把 beacon 寫入 Redis，worker 靠 beacon 驗證金鑰一致性。
3. 每個 job 提交時，`secrets` 用 AES-256-GCM 加密後放進 `Job.EncryptedSecrets`。
4. Worker 解密後把值注入 agent 子進程的環境變數。

`github.token` 會自動 merge 進 `secrets["GH_TOKEN"]`。`AGENTDOCK_SECRET_<NAME>` 環境變數也會被收進 `secrets`。

## Mantis（選用）

當 thread 中出現 Mantis issue URL（`view.php?id=` 或 `/issues/`），agent 會透過內建的
`mantis` skill 抓 issue title/description/附件。設定兩個欄位即可：

```yaml
mantis:
  base_url: https://mantis.example.com    # host 根即可，不必含 /api/rest
  api_token: <your-mantis-api-token>
```

兩個欄位必須同時填寫或同時留空；只填一個會在啟動驗證時失敗。

**運作機制**：app 啟動時把 `base_url + /api/rest` 存入 `secrets["MANTIS_API_URL"]`、
`api_token` 存入 `secrets["MANTIS_API_TOKEN"]`，worker 在啟動 agent 子程序時把這兩個值當 env
var 推入。bundled `mantis` skill 讀 env，agent 看到 thread 裡的 Mantis URL 就主動呼叫 skill
擷取內容。

**Basic auth 已移除**：bundled skill 只支援 API token。若 Mantis 版本太舊不支援 API token，
請升級 Mantis 或留空 Mantis 區段（不啟用）。

**未配置行為**：agent 仍會看到 thread 裡的 Mantis URL，只是不會擷取內容——URL 照原樣留在輸出。

**Worker host 先決條件**：worker 需要 Node.js 18+ 才能執行 bundled mantis skill 裡的 JS。若
使用官方 Docker image，已內建。
