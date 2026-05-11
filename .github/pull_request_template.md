## What this PR does / why

<!-- 把問題與解法講清楚，比 commit 訊息更展開。對齊 repo 已有 PR 的習慣（例 #235 / #243 / #208）。 -->

## Module touched

<!-- 列出此 PR 動到的 module；reviewer 會用這個評估 import-direction 影響（test/import_direction_test.go）。沒動到的請刪除。 -->

- `app/`
- `worker/`
- `shared/`
- `cmd/`
- `docs/` / `.github/`

## Related issues

<!-- 沒有就填 N/A。「Part of」對齊 PR 拆分模式（如 #228 → #230/#232/#233/#234）。 -->

- Fixes #
- Refs #
- Part of #

## Verification

<!-- 列出實際跑過的指令與結果。CI 會跑，但本地驗過可以加速 review 並抓到 import 邊界違規。 -->

```bash
# build 四個 module
go build ./... && (cd app && go build ./...) && (cd worker && go build ./...) && (cd shared && go build ./...)

# 測試（含 race；module 內也要各自跑）
go test ./... -race

# import 邊界
go test ./test/... -run TestImportDirection

# commit message（commitlint gate 會擋 merge）
npx --yes -p @commitlint/cli -p @commitlint/config-conventional \
  commitlint --last --extends @commitlint/config-conventional
```

## Release note

<!--
release-please 會吃這個區塊產生 CHANGELOG 與版號。填寫前先確認：

1. 真的是 user-facing change 嗎？metric label 改名、內部重構、test cleanup 通常不算。
2. 真的是 BREAKING CHANGE 嗎？參考 #214 — 過早或不必要的 BREAKING CHANGE 會讓 major 提前跳號。
   只有當 operator 需要動 config / 重建環境 / 改用法時才算。

無 user-facing change 就填 NONE。
-->

```release-note
NONE
```

## Notes for reviewer

<!-- 選填。風險點、需要特別看的檔案、follow-up 計畫、未自動驗證的 manual smoke 等。 -->
