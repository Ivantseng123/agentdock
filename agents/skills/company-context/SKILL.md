---
name: company-context
description: "Softleader 組織內部 context — GitHub org 名稱、產品中文名到 repo 的查找方式、gh CLI 使用、mantis 相關聯。當使用者提到公司產品（中文名稱）、詢問「哪個 repo」「公司的 X 在哪」「請幫我找 X 的 code」時觸發。"
---

# Softleader 公司 Context

本 skill 說明如何在 Softleader GitHub org 內查找產品對應的 repo，以及相關工具使用方式。

## Org 名稱

GitHub org 名稱為 **`softleader`**。所有公司 repo 均位於 `github.com/softleader/` 下。

## 環境

`GH_TOKEN` 已注入環境變數，可直接執行 `gh` CLI，不需額外設定或條件判斷。

## 查詢 Recipes

以下指令可直接在工作環境中執行：

```bash
# 列出 org 內所有 repo 及描述
gh repo list softleader --limit 300 --json name,description

# 在 org 內搜尋包含關鍵字的程式碼
gh search code --owner softleader <keyword>

# 查看單一 repo 的 description 與 topics
gh repo view softleader/<name> --json description,topics

# 讀取 repo 的 README 內容
gh api /repos/softleader/<name>/readme --jq .content | base64 -d
```

> **注意**：不要把 `gh repo list` 的完整 269 筆輸出貼給使用者；先從中篩選出相關候選，再回覆。

## 查找策略：面對「公司的 X 是哪個 repo」

遇到使用者詢問「公司的 X 的 repo 是哪個」、「X 在哪個倉庫」等問題時，按以下步驟處理：

**Step 1 — 用 keyword 搜 repo name + description**

```bash
gh repo list softleader --limit 300 --json name,description \
  | jq '.[] | select(.name | test("<keyword>"; "i")) | {name, description}'
```

或用 description 欄位搜尋中文關鍵字：

```bash
gh repo list softleader --limit 300 --json name,description \
  | jq '.[] | select(.description // "" | test("<中文關鍵字>"; "i")) | {name, description}'
```

**Step 2 — 若有多個候選或名稱不明確，用 `gh search code` 確認**

```bash
gh search code --owner softleader "<關鍵字>" --limit 10
```

搜尋結果的 `repository.name` 欄位即為所在 repo。

**Step 3 — 都找不到時，回問使用者**

若 Step 1 和 Step 2 都沒有可信的命中，**不要猜**，直接回問：

> 「找不到明確對應的 repo，請問 X 是否有另一個內部代號或英文名稱？」

不要自行推斷、不要輸出沒把握的結果。

## 中文產品名命名慣例提示

Softleader 產品常以**水果或花卉的英文代號**作為 repo prefix，再接保險或業務線識別碼。

舉例：「兆豐火險」的內部代號為 `jasmine-cki-fir`，所以 repo 可能命名為 `jasmine-cki-fir` 或含有該 prefix 的變體。

**查找中文產品名的建議路徑：**

1. 先嘗試把中文名拆解成可能的英文關鍵字（保險公司、險種縮寫等）
2. 若上一步沒有命中，考慮「產品線代號」（如 `jasmine-*`、`cherry-*` 等）可能對應某個保險產品線
3. 用 `gh search code` 搜尋中文名本身，README 或設定檔裡可能有

## Mantis 交互

若使用者的問題同時涉及 Mantis ticket 或 issue ID（如「mantis #1234 是哪個 repo 的問題」），請交由 `mantis` skill 處理 Mantis 查詢部分，本 skill 只負責 repo 定位。

## 觸發條件

以下情況應啟用本 skill：

- 「公司的 X」、「我們的 X 在哪」
- 「哪個 repo / 倉庫 / 庫」
- 「請幫我找 X 的 code / 程式碼」
- 「X 的 README / 文件 / 文檔在哪」
- 使用者提到中文產品名（保險商品、系統名稱）並詢問對應 repo

## 不要做的事

- **不要**把 `gh repo list` 的完整輸出直接貼給使用者（269 筆太多）
- **不要**在沒有確切證據時猜測中文名與 repo 的對應關係
- **不要**在找不到結果時編造 repo 名稱
- **不要**對使用者暴露 `GH_TOKEN` 的值
