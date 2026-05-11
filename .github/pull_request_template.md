<!--
AI 填寫規則：
1. **填入的內容一律用英文**；章節標題保留英文/中文混用現狀。
2. 順著章節順序寫，標題勿動。
3. 「Verification」只列實際跑過的指令與輸出；未跑寫 "not run — reason: …"，不要把預填指令當已執行貼回。
4. 「Module touched」動到打 `[x]`、沒動打 `[ ]`；勿刪除任何項。
5. 「Release note」預設 NONE；除非 operator 需動 config / 重建環境 / 改用法才填內容（見 #214）。
6. 不適用章節整段刪除（含標題）。
-->

## What this PR does / why

<!-- 把問題與解法講清楚，比 commit 訊息更展開。 -->

## Module touched

- [ ] `app/`
- [ ] `worker/`
- [ ] `shared/`
- [ ] `cmd/`
- [ ] `docs/` / `.github/`

## Related issues

<!-- 至少填一項，無關聯填 N/A。 -->

- Fixes #
- Refs #
- Part of #

## Verification

<!-- 列實際跑過的指令與輸出摘要；未跑寫「未執行 — 原因：…」。 -->

```bash
go build ./... && (cd app && go build ./...) && (cd worker && go build ./...) && (cd shared && go build ./...)
go test ./... -race
go test ./test/... -run TestImportDirection
git log -1 --pretty=%B | npx --yes commitlint --extends @commitlint/config-conventional
```

## Release note

<!--
release-please 會吃這個區塊產生 CHANGELOG 與版號。
metric label 改名、內部重構、test cleanup → NONE。
BREAKING CHANGE 只用在 operator 需動 config / 重建環境 / 改用法的情境。
-->

```release-note
NONE
```

## Notes for reviewer

<!-- 選填。風險點、需特別檢查的檔案、follow-up 計畫等。無則整段刪除。 -->
