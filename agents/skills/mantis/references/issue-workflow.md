# Issue Workflow Guardrails

## Read First / metadata-first

- 處理任何 issue 建立、更新或關聯操作前，先做「Read First」：先閱讀工作區中已存在的 Mantis metadata snapshot（例如專案索引、project metadata、既有操作說明）。
- 將工作區 metadata 視為 **workspace metadata storage**：先查 authoritative snapshot；authoritative 不完整時，再查同一 workspace、同一 project 的 non-stale observed metadata。
- 若 workspace metadata storage 已能提供 project / handler / status / required fields 的對應，就直接重用，對同一 workspace 的相同 metadata **不重複追問**。
- 不要硬寫固定設定路徑或錯誤訊息；以「先查既有 metadata / 文件，查不到才補問」來描述流程。

## Interaction Rules

- **只追問**：只詢問目前缺少的資訊（缺少的資訊要精準列出），不要補問已具備的欄位。
- **critical fields 一次問齊**：同一輪先收斂所有必要欄位（如 project、summary、description、handler 需求，必要時含 priority / severity / issue type），同批請清楚。
- 若資訊已足夠，直接執行，不要以「先確認再執行」作為預設流程。
- 若資訊不足，回覆前先確認是否有可用於自動對應的 metadata；確認 authoritative 與 observed 兩層都仍不足時，再以一則精準問題一次問齊。
- observed metadata 只用於補齊 structured project metadata，不可把 summary、description、note text 等 free-form 內容當成可重用記憶。

## Attachment Rules

- 建立 issue 時，附件預設放到 issue 本體描述，不預設新增 note。
- 只有在使用者明確要求（例如「請用 note」）時，才在附註中補充內容。
- 不要移動、複製、刪除原始附件檔案；保留來源檔案原樣。

## USER Feedback

- 對外回覆採公開文字、非技術、可閱讀：
  - 例：`已幫你建立 issue，待會會回報編號與連結。`
  - 例：`已依你提供的資訊建立問題單，完成後會直接回報結果。`
- 若回覆內容過於技術化，改為更淺白轉譯或改成 non-public note。
- 當涉及 token、raw API payload 或除錯堆疊時，請改以 `non-public note` 記錄，不直接塞入一般對話文字。

## Issue Relationships

- 不要預設一律設為 `related-to`。
- 只有在使用者明確提供關聯線索（如「關聯到 #1234」「關係為重複/阻塞/依賴」）時才推斷。
- 若關係不明確，先詢問一次性補齊：關聯型態、對象 issue id、是否要同步欄位。

## Success Output

每次回報成功結果至少包含：

- issue id
- URL
- project
- handler
- status
- attachments（清楚列出是否有附件，與檔名/來源）
- feedback 狀態（例如：已回報使用者、待補資料、已結案）
