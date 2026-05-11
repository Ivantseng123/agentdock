<!--
本 template 主要給 AI agent（透過 `gh pr create` 自動帶入）或人類撰寫。

AI 填寫規則：
1. 順著章節順序寫，不要更動章節標題。
2. 「Verification」只列實際跑過的指令與真實輸出；未跑寫「未執行 — 原因：…」，不要把預填指令當作已執行貼回。
3. 「Module touched」打勾 `[x]` 表示動到、`[ ]` 表示沒動，不要刪除任何項，方便 grep / 自動審查。
4. 「Release note」遵守區塊內的判斷規則；不確定就填 NONE，不要為了顯眼亂貼 BREAKING CHANGE（見 #214 教訓）。
5. 不適用的章節整段刪除（含標題），不要留空標題。
-->

## What this PR does / why

<!-- 把問題與解法講清楚，比 commit 訊息更展開。對齊 repo 已有 PR 的習慣（例 #235 / #243 / #208）。 -->

## Module touched

<!-- 動到打 [x]，未動到打 [ ]；勿刪除任何項。 -->

- [ ] `app/`
- [ ] `worker/`
- [ ] `shared/`
- [ ] `cmd/`
- [ ] `docs/` / `.github/`

## Related issues

<!-- 至少填一項；無關聯填「N/A」。「Part of」對齊拆分模式（如 #228 → #230/#232/#233/#234）。 -->

- Fixes #
- Refs #
- Part of #

## Verification

<!--
列出實際跑過的指令與輸出摘要。未實際執行寫「未執行 — 原因：…」並說明，例如 "未執行 — 此 PR 僅動 docs，無 Go code"。
不要把下面預填的指令當作「跑過」直接貼回；要嘛刪掉換上你實際跑的，要嘛在後面接 "→ output: ..." 補實際結果。
-->

```bash
# build 四個 module
go build ./... && (cd app && go build ./...) && (cd worker && go build ./...) && (cd shared && go build ./...)

# 測試（含 race）
go test ./... -race

# import 邊界
go test ./test/... -run TestImportDirection

# commit message（commitlint gate 會擋 merge）
git log -1 --pretty=%B | npx --yes commitlint --extends @commitlint/config-conventional
```

## Release note

<!--
release-please 會吃這個區塊產生 CHANGELOG 與版號。填寫前先確認：

1. 真的是 user-facing change 嗎？metric label 改名、內部重構、test cleanup 通常不算 → 填 NONE。
2. 真的是 BREAKING CHANGE 嗎？參考 #214 — 過早或不必要的 BREAKING CHANGE 會讓 major 提前跳號。
   只有當 operator 需要動 config / 重建環境 / 改用法時才算。
3. AI 規則：除非使用者明確要求或符合上述條件，預設填 NONE。
-->

```release-note
NONE
```

## Notes for reviewer

<!-- 選填。風險點、需要特別看的檔案、follow-up 計畫、未自動驗證的 manual smoke 等。無則整段刪除。 -->
