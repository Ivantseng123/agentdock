# Skill Metadata Storage

此目錄是目前 skill 的 metadata storage，對應 OpenSpec 與 skill 文件中的 authoritative / observed 雙層記憶模型。

## 結構

- `metadata/index.json`：skill 內 authoritative project 索引
- `metadata/projects/<projectId>.json`：authoritative project metadata snapshot
- `metadata/observed/<projectId>.json`：由成功 API 互動補齊的 observed cache

## Contract

- workflow 先查 authoritative snapshot，再查 non-stale observed cache，最後才補問使用者
- observed cache 只能存 structured project metadata，不可存 summary、description、note text 等 free-form content
- refresh 會讓同 project 的 observed cache 進入 stale / revalidation-required 狀態
- 若 API 後續明確拒絕某 observed metadata，該 entry 需標 invalid，不可繼續重用
