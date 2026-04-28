---
name: mantis
description: "Mantis 問題追蹤系統操作 — 查詢/建立/更新問題、下載附件圖片、統計分析。當使用者提到 mantis、issue、bug、問題追蹤、議題、附件下載、問題統計，或需要操作 Mantis Bug Tracker 時觸發此 skill。也適用於使用者想查看 mantis 上的截圖或附件內容時。額外支援 `metadata store`、`project metadata`、`projectId` 存起來、記住/保存專案資訊，以及以 `projectId` 為 key 保存專案 metadata；並在既有 Mantis 專案上下文中後續提到「把這個 projectId 存起來」時仍算命中，涵蓋 `refresh`、`bootstrap`、`stale` 等 metadata 維護情境。"
---

# Mantis 問題追蹤系統

這是 `mantis` skill 的入口文件。Workflow 內容分流如下：

- `references/issue-workflow.md`：issue 建立、更新、附件規則、USER 反饋規格
- `references/metadata-maintenance.md`：metadata store 與 metadata refresh / stale 判斷流程

## 觸發說明

- 當使用者提及 mantis、issue、bug、問題追蹤、議題、附件下載、問題統計時觸發
- 需要操作 Mantis Bug Tracker 時觸發
- 使用者查詢 mantis 上截圖或附件內容時觸發
- 在 Mantis 專案操作上下文內，提到「把這個 projectId 存起來」或要求**記住/保存專案資訊**時觸發
- 當提到 project metadata、projectId、以 projectId 保存專案 metadata、metadata store、bootstrap、refresh、stale metadata 時，視為相關命中

## 首次使用 `status` 檢查與 reason taxonomy

觸發此 skill 時，**必須先執行 `status` 檢查連線狀態**，再決定下一步。

### Step 1: 單一跨平台 status 呼叫

```bash
node <path-to-skill>/scripts/mantis.js status
```

### Step 2: 根據 status 結果判斷

回傳範例：

```json
{
  "api": {
    "status": "true",
    "url": "https://mantis.example.com/api/rest",
    "reason": "",
    "http_code": 200,
    "message": ""
  }
}
```

| `api.status` / `api.reason`                                            | 處理方式                                                               |
| ---------------------------------------------------------------------- | ---------------------------------------------------------------------- |
| `"true"`                                                               | 全功能正常 → **跳到 Step 4 執行使用者的請求**                          |
| `status = "false"` 且 `reason = "auth_failed"`                         | API 認證失敗 → **進入 Step 3 引導檢查/更新 Token**                     |
| `status = "false"` 且 `reason = "sandbox_network_blocked"`             | 沙箱/網路限制，**不要要求重填 `.env`**；應改為使用授權模式重跑必要命令 |
| `status = "false"` 且 `reason = "dns_error" / "tls_error" / "timeout"` | 網路或連線異常，先回報具體錯誤並協助排查，不要直接判定為憑證未設定     |
| `status = "false"` 且 `reason = ""`                                    | API 可能未設定；`message` 會直接提示缺少的設定 → **進入 Step 3 引導設定** |

### Step 3: 引導設定（請使用者自行在環境變數或 `.env` 寫入）

當 status 顯示未設定或異常時，**主動引導使用者完成設定**，不要只丟出連結。

1. **告知缺設定項目**（依 reason）
   - `api.status = "false"` 且 `api.reason = ""` → 需要 `MANTIS_API_URL` 與 `MANTIS_API_TOKEN`
   - `api.reason = "auth_failed"` → API Token 無效、過期或權限不足，更新 `MANTIS_API_TOKEN`
   - `api.reason = "sandbox_network_blocked"` → 這是執行環境限制，不要要求使用 `.env` 修改
   - `api.reason = "dns_error" / "tls_error" / "timeout"` → 回報連線例外與 `message`

2. 提供設定範本（可直貼環境變數，也可寫入 `.env`）

   ```
   MANTIS_API_URL=https://<your-mantis-host>/api/rest
   MANTIS_API_TOKEN=<your-api-token>
   ```

3. 引導使用者回覆「已設定」或「已寫入」後再度執行 `status`

補充規則：

- 若 `api.reason = ""` 且 `api.message` 非空，**直接把 `message` 當成提示內容回給使用者**，不要只回傳空白或只說「status=false」。
- `message` 會依缺少的欄位指出 `MANTIS_API_URL`、`MANTIS_API_TOKEN`，或兩者皆缺。

### Step 4: 執行使用者的請求

完成驗證後再執行對應命令。

## 操作路由（將 workflow 下放）

- **issue 建立 / 更新 / 附件 / USER feedback 規則**：請參考 `references/issue-workflow.md`
- **metadata store refresh / stale metadata 判斷**：請參考 `references/metadata-maintenance.md`

## 專案資訊與 projectId 持久化路由

- 當使用者在 **Mantis 專案操作上下文**中提到：
  - 「把這個 projectId 存起來」
  - 「幫我記住 / 保存專案資訊」
  - 明確給出 `projectId` 並要求保存該 project metadata
  
  則先做 `workspace` 的 **metadata store / project metadata** 查詢：
  1. 先看 `metadata/projects/<projectId>.json` 是否存在；該 snapshot 內會保存 `project.id` 與 `project.name`。
  2. 若不存在：立即使用 `node <path-to-skill>/scripts/maintenance/bootstrap-project-metadata.mjs --project <projectId>`，從 live API 建立該 project metadata。
  3. 若存在且僅是「存起來」：直接重用現有 snapshot，不主動啟動 refresh，避免主動刷新造成不必要變更。

- 若使用者明確要求**重新抓取 live project metadata**、**重建 snapshot**或明講 **bootstrap**，則使用：
  `node <path-to-skill>/scripts/maintenance/bootstrap-project-metadata.mjs --project <projectId>`。

- 若使用者明確要求 **refresh 現有 snapshot**、**更新本地 `last_refreshed_at`**，或**將 observed cache 標記為 stale**，才進入 refresh 流程：
  `node <path-to-skill>/scripts/maintenance/refresh-project-metadata.mjs --project <projectId>`。
  此流程不會重新打 live API 重抓 project metadata。

- metadata store 中若有 `stale` 或 `revalidation-required`，代表可能需要重新確認資料；若要從 live API 重建 project metadata，仍應使用 bootstrap，不是 refresh。一般 issue 工作流程仍採 metadata-first，不因欄位缺漏就刷新。

## CLI 工具總覽（issue / users-projects / stats / attachments）

本 skill 內提供的主要 CLI 操作透過 `node <path-to-skill>/scripts/mantis.js` 執行。**主要命令在成功時輸出為 JSON**；`help、usage 與缺參數錯誤會輸出文字訊息`。`status` 在缺少設定時也會維持 JSON 輸出，並在 `api.message` 內提供可直接回覆使用者的設定提示。metadata maintenance 不屬於 `mantis.js` 命令面，請依 `references/metadata-maintenance.md` 中的 workspace-specific 機制處理；canonical script 路徑為 `node <path-to-skill>/scripts/maintenance/bootstrap-project-metadata.mjs` 與 `node <path-to-skill>/scripts/maintenance/refresh-project-metadata.mjs`。

### Issue

```bash
# 取得問題
node <path-to-skill>/scripts/mantis.js get-issue <issue_id>
node <path-to-skill>/scripts/mantis.js get-issue <issue_id> --full

node <path-to-skill>/scripts/mantis.js list-issues --project <project_id> --page 1 --page-size 20
node <path-to-skill>/scripts/mantis.js list-issues --handler <handler_id> --filter <filter_id>
node <path-to-skill>/scripts/mantis.js list-issues --search "保費"
node <path-to-skill>/scripts/mantis.js list-issues --project <project_id> --full

node <path-to-skill>/scripts/mantis.js create-issue \
  --project <project_id> \
  --summary "問題摘要" \
  --description "詳細描述" \
  --handler <handler_id> \
  --priority "high" \
  --severity "major"

node <path-to-skill>/scripts/mantis.js update-issue <issue_id> \
  --status "assigned" \
  --handler <handler_id>

node <path-to-skill>/scripts/mantis.js add-note <issue_id> --text "已處理完成"
node <path-to-skill>/scripts/mantis.js add-note <issue_id> --text "內部備註" --private
```

### Users-Projects

```bash
node <path-to-skill>/scripts/mantis.js get-user <username>
node <path-to-skill>/scripts/mantis.js get-users
node <path-to-skill>/scripts/mantis.js get-project-users <project_id>
node <path-to-skill>/scripts/mantis.js get-projects
```

### Stats

```bash
node <path-to-skill>/scripts/mantis.js issue-stats --group-by status --project <project_id>
node <path-to-skill>/scripts/mantis.js issue-stats --group-by handler --period week
node <path-to-skill>/scripts/mantis.js assignment-stats --project <project_id>
```

### Attachments

```bash
node <path-to-skill>/scripts/mantis.js list-attachments <issue_id>
node <path-to-skill>/scripts/mantis.js download-attachment <issue_id> <file_id>
node <path-to-skill>/scripts/mantis.js download-attachment <issue_id> <file_id> --output ./screenshot.png
```

#### 查看圖片附件流程

1. 先用 `list-attachments` 取得附件清單與 `file_id`
2. 再用 `download-attachment` 下載附件
   - 不帶 `--output` 時，預設寫到當前 cwd：`./mantis_<file_id>_<filename>`
   - 若自行指定 `--output`，**請保留在 cwd 之內**（例如 `./foo.png`）
   - 禁止輸出到 `/tmp/`、`$HOME`、`~` 等 cwd 之外路徑：AgentDock worker 跑在 opencode 沙箱下，cwd 之外屬 `external_directory`，headless 模式會自動拒絕寫入導致任務靜默失敗
3. 下載完成後，用 Read 工具讀取圖片或檔案內容

## 常見狀態 ID 對照

| ID  | 名稱         | 說明     |
| --- | ------------ | -------- |
| 10  | new          | 新問題   |
| 20  | feedback     | 要求回覆 |
| 30  | acknowledged | 已確認   |
| 40  | confirmed    | 已核實   |
| 50  | assigned     | 已分配   |
| 80  | resolved     | 已解決   |
| 90  | closed       | 已關閉   |

## 重要安全與設定原則

- API 認證使用 `Authorization` header（不是 Bearer，直接放 token）
- Token 與 API URL 等敏感資訊不應貼入對話；引導使用者改寫環境變數或 `.env`
- 附件下載使用 API Token 認證，透過 `/issues/{id}/files/{file_id}` 取得檔案
- 預設輸出走精簡模式，必要時再加 `--full`
- 統計功能在本地端處理，單次最多取 150 筆問題
- 跨平台以 `node <path-to-skill>/scripts/mantis.js` 執行，僅依賴 Node.js runtime（需 Node 18+）
