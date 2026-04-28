# Metadata Maintenance Guardrails

## When To Use

- 先用現有 metadata 做一般 issue 工作；只有遇到下面情況才切換到 metadata maintenance flow。
- 情況 1：使用者**明確要求** refresh metadata。
- 情況 2：現有 metadata 明顯過時，例如 `category`、`custom field`、`enum values` 已無法對應目前操作需要的值。
- 情況 3：新專案 onboarding，workspace 內還沒有可用的 metadata snapshot。
- 情況 4：需要比對 live metadata 以確認本地 snapshot 是否落後。
- **正常 issue 工作流程不要主動 refresh**：若只是欄位不足或使用者資訊不完整，先補問缺少欄位，不要先跑 refresh。

## 專案快取與 projectId 保存路由

- 當使用者要求「存起來 / 保存 / 記住」 projectId 或專案資訊時：

  1. 先查 `metadata store` 中 `project metadata`：`metadata/projects/<projectId>.json`。
  2. 若檔案**不存在**，代表要 bootstrap：
      - 使用 `node <skill root>/scripts/maintenance/bootstrap-project-metadata.mjs --project <projectId>`。
      - bootstrap 是用來建 `authoritative snapshot`；該 snapshot 會保存 `project.id` 與 `project.name` 等 project metadata。
  3. 若檔案**已存在**且只是在「存起來」情境：
      - 直接重用既有 metadata，不主動 refresh。
  4. 若使用者明確要求**重新抓取 live project metadata**、**重建 snapshot**或明講 **bootstrap**：
      - 使用 `node <skill root>/scripts/maintenance/bootstrap-project-metadata.mjs --project <projectId>`。
  5. 若使用者明確要求 **refresh 現有 snapshot**、**更新本地 `last_refreshed_at`**，或**將 observed cache 標記為 stale**：
      - 使用 `node <skill root>/scripts/maintenance/refresh-project-metadata.mjs --project <projectId>`。
      - 這個 refresh 流程不會重新打 live API 重抓 project metadata。

- refresh 路徑（明確要求）：
  - `node <skill root>/scripts/maintenance/refresh-project-metadata.mjs --project <projectId>`

## Metadata Source & Fallback

- **Primary source**：`GET /api/rest/projects/{project_id}`。
- 若該專案回應缺少 `categories`，改 fallback 到 `GET /api/rest/projects/`，取回專案清單並比對 `project_id`。
- fallback 只補齊缺漏欄位來源，不代表就放棄 primary source；若兩者都無法取得有效 `categories`，視為驗證失敗。

## Skill Metadata Store

- 將 workspace metadata store 視為雙層記憶：
  - **authoritative snapshot**：`metadata/index.json` 與 `metadata/projects/<projectId>.json`
  - **observed cache**：`metadata/observed/<projectId>.json`
- 在目前 repo，skill metadata store 落點對應 `metadata/`，refresh canonical script 對應 `scripts/maintenance/refresh-project-metadata.mjs`。
- 一般 issue 工作先讀 authoritative；authoritative 不足時，才讀同 project 的 non-stale observed cache。

## Refresh Commands

- 以下指令適用於 workspace 內已提供 metadata refresh script 的情況；不要假設 personal skill 目錄內一定存在同名腳本。

- 抽象形式：`node <skill root>/scripts/maintenance/refresh-project-metadata.mjs --project <projectId>`
- 抽象形式：`node <skill root>/scripts/maintenance/refresh-project-metadata.mjs --all`
- 目前 repo 對應：`node <path-to-skill>/scripts/maintenance/refresh-project-metadata.mjs --project <projectId>`
- 目前 repo 對應：`node <path-to-skill>/scripts/maintenance/refresh-project-metadata.mjs --all`

若目前工作區沒有這支 script，先停止 refresh，明確回報「此 workspace 尚未提供可直接執行的 metadata refresh script」，再請使用者指定可用的 refresh 機制或腳本位置，不要自行猜測替代指令。

## Verification After Refresh / Bootstrap

1. 驗證 `metadata/index.json` 存在且可被 parse。
2. 若是單一專案 refresh 或 bootstrap，驗證對應的 `metadata/projects/<projectId>.json` 存在且可被 parse；若是 `--all`，驗證受影響的 project metadata 檔都存在且可被 parse。
3. 操作結果必須回報以下三組欄位：
   - `changed`：本次刷新 / bootstrap 實際變更的專案、欄位、規則。
   - `verified`：已完成哪些存在性與格式驗證。
   - `risk`：可能的影響點（例如舊欄位映射失效、分類對應可能改變、需要重新確認的欄位）。
4. refresh 後，將同 project 的 observed cache 標為 `stale` 或 `revalidation-required`，不要把 refresh 前的 observed 值繼續當成已驗證資料。
5. 若後續 API rejection 明確打臉某個 observed metadata，將該 entry 標為 invalid，直到重新驗證通過。
6. 核對 required fields 與主要 project rules 仍可對上目前專案操作需求；若出現缺欄位、欄位型別改變、列舉值異常或規則不一致，需明確回報。

## Bootstrap / Refresh 失敗保護

- bootstrap 或 refresh 驗證不通過時，**不得落盤半套 snapshot**：
  - `metadata/index.json` 不得異動為不完整版本。
  - `metadata/projects/<projectId>.json` 不得落盤未通過驗證的內容。
  - 觀測值保持原狀，待下一次驗證通過後再更新。
- 若驗證不通過，先回報風險與缺失，建議先補齊再繼續後續 issue 任務。
