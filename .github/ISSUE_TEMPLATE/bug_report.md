---
name: Bug Report
about: Report broken behaviour (hangs, wrong results, CI red, worker crash, etc.)
title: 'bug(<scope>): '
labels: bug
---

<!--
AI authoring rules:
1. Follow the section order; do not change headings.
2. "Supporting evidence" must be real log / commit / pod names. Write "none" if you don't have any — do not fabricate.
3. If "Root cause analysis" is unknown, write "unknown, pending investigation" and keep the section.
4. `<scope>` must be one of: `worker/agent`, `workflow/issue`, `workflow/ask`, `workflow/pr-review`, `bot`, `queue`, `config`, `github`.
5. Delete inapplicable sections entirely (including the heading); do not leave empty headings.
-->

## Symptom

Actual behaviour:

Expected behaviour:

## Reproduction

<!-- If not consistently reproducible, write "intermittent, conditions unknown". -->

1.
2.
3.

## Root cause analysis

<!-- Code path + trigger conditions. -->

## Impact

<!-- Blast radius / release-blocking / affected users. -->

-

## Supporting evidence

```
```

## Acceptance criteria

<!-- 3-5 grep-friendly, verifiable conditions. -->

- [ ]
- [ ]
- [ ]

## Related

<!-- Fixes #N / Refs #N / Blocks #N. -->
