---
name: Bug Report
about: 回報實際出錯的行為（卡住、結果錯亂、CI red、worker crash 等）
title: 'bug(<scope>): '
labels: bug
---

<!--
AI 填寫規則：
1. **填入的內容一律用英文**；章節標題保留中文。
2. 順著章節順序寫，標題勿動。
3. 「Supporting evidence」必須真實 log/commit/pod，沒有寫 "none"，不要編造。
4. 「根因分析」未掌握時寫 "unknown, pending investigation" 並保留章節。
5. `<scope>` 限定：`worker/agent`、`workflow/issue`、`workflow/ask`、`workflow/pr-review`、`bot`、`queue`、`config`、`github`。
6. 不適用的章節整段刪除（含標題）。
-->

## 問題現象

實際行為：

期望行為：

## 重現步驟

<!-- 無法穩定重現寫「偶發，條件未知」。 -->

1.
2.
3.

## 根因分析

<!-- 程式碼路徑 + 觸發條件。 -->

## 影響

<!-- blast radius / 是否阻塞 release / 影響範圍。 -->

-

## Supporting evidence

```
```

## Acceptance criteria

<!-- 3-5 個可被 grep / 自動驗證的條件。 -->

- [ ]
- [ ]
- [ ]

## Related

<!-- Fixes #N / Refs #N / Blocks #N。 -->
