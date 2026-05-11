---
name: Refactor / Tech Debt
about: 內部清理、結構調整、命名修正等非功能性改動
title: 'refactor(<scope>): '
labels: enhancement
---

<!--
AI 填寫規則：
1. 此 template 為短版；不需要 RCA / 多方案比較。
2. 若改動跨 app/worker/shared 三 module 之 import 邊界（`test/import_direction_test.go` 把關），在「改動建議」明確說明預期影響。
3. `<scope>`：`worker`、`queue`、`config`、`workflow`、`shared`、`app`。
4. 不適用章節整段刪除。
-->

## 現狀

<!-- 目前長什麼樣 + 為何想動 + 牽涉的檔案或 package。 -->

## 改動建議

<!-- 預期動到哪些檔案或 package；牽動 import 邊界請點明。 -->

## 驗證方式

```bash
go build ./... && (cd app && go build ./...) && (cd worker && go build ./...) && (cd shared && go build ./...)
go test ./... -race
go test ./test/... -run TestImportDirection
```

## Related

<!-- "Refs #N" / "Part of #N"；無則寫 N/A。 -->
