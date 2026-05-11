---
name: Bug Report
about: 回報實際出錯的行為（卡住、結果錯亂、CI red、worker crash 等）
title: 'bug(<scope>): '
labels: bug
---

<!--
本 template 主要給 AI agent 透過 `gh issue create --template bug_report.md` 或人類手填使用。

AI 填寫規則：
1. 順著章節順序寫，不要更動章節標題。
2. 「Supporting evidence」必須是真實 log/commit/pod 名稱；沒有就寫「無」，不要編造。
3. 「根因分析」未掌握時寫「未知，待調查」，不要省略章節，方便後續補。
4. `<scope>` 替換為 repo 常用 scope 之一：`worker/agent`、`workflow/issue`、`workflow/ask`、`workflow/pr-review`、`bot`、`queue`、`config`、`github`。
5. 不適用的章節整段刪除（含標題）；不要留空標題。
6. 中英文皆可，混寫亦可。
-->

## 問題現象

<!-- 兩段：實際行為與期望行為。 -->

實際行為：

期望行為：

## 重現步驟

<!-- 編號 1. 2. 3.；從觸發到觀察到問題的最短路徑。無法穩定重現就改寫「偶發，重現條件未知」。 -->

1.
2.
3.

## 根因分析

<!-- 程式碼路徑 + 觸發條件；未知寫「未知，待調查」並保留章節。 -->

## 影響

<!-- bullet list：blast radius / 是否阻塞 release / 影響的 user 範圍。 -->

-

## Supporting evidence

<!-- log 片段、pod name、commit hash、時間戳。code block 包起來。沒有寫「無」。 -->

```
```

## Acceptance criteria

<!-- 3-5 個可被 grep / 自動驗證的條件，每行一個 - [ ]。 -->

- [ ]
- [ ]
- [ ]

## Related

<!-- 例如 "Fixes #N" / "Refs #N" / "Blocks #N"。無則整段刪除。 -->
