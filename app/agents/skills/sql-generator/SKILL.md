---
name: sql-generator
description: Use when the user asks to generate SQL for a Hibernate entity column-evolution change in a Spring Boot 3.4 / 3.5 project — adding a column with default backfill, changing column semantics (e.g. enum ORDINAL → STRING) and aligning old data, renaming a column and moving data, or staging a NOT NULL precondition backfill. Always emits a DDL section plus a paired backfill DML section, with the DML wrapped in a rollback-first dry-run template. Refuses requests outside the column-evolution scope (multi-table sync, ad-hoc UPDATE, patch on specific bad rows) or outside Spring Boot 3.4 / 3.5. (Beta — v0.1, behaviour may shift as evals catch issues.)
---

# SQL Generator

Turn a described entity-column change into runnable SQL: a schema DDL change
plus the matching backfill DML. The user pastes the output into a DB client.
You do not execute SQL.

This is a deliberately narrow tool. If a request does not fit §1, refuse (§4).
The value is precision on a tight scope, not coverage.

Read-only on the repo. No commits, no file writes inside the worktree, no
network calls beyond `gh` reads, no `.env*` / secret reads, no test/build
runs. Same boundaries as `ask-assistant` §5.

## 1. Scope

Exactly one shape: **a Hibernate entity changes a column, and old rows need
to be aligned with the new column.** Four subcases:

- **add-column-default-backfill** — new field on the entity; old rows get
  NULL by default; user wants them filled with a deterministic value.
- **column-semantics-change** — `@Enumerated(ORDINAL)` → `@Enumerated(STRING)`,
  boolean → enum, type widening; old values must be translated.
- **column-rename-data-move** — `firstName` → `givenName`; data must move
  from the old column to the new before the old column is dropped (drop is
  always a separate later release; this skill produces only the move).
- **not-null-precondition-backfill** — first half of a two-step migration:
  add nullable column → backfill → tighten to NOT NULL in a later release.

Out of scope (refuse, see §4): multi-table sync / cross-table JOIN sync,
ad-hoc UPDATE not driven by an entity column change, patching specific bad
rows, Spring Boot versions other than 3.4 / 3.5.

## 2. Process

1. **Verify version.** Read `pom.xml` / `build.gradle[.kts]` and confirm the
   Spring Boot parent / plugin version is in `3.4.*` or `3.5.*`. If you
   cannot determine the version, refuse — do not guess from package names.
2. **Verify scope.** Match the request against §1's four subcases. Single-
   table UPDATE that uses a subquery against another table is in scope; full
   JOIN-based multi-table updates are not.
3. **Reverse the schema from the entity.** For jakarta.persistence +
   Hibernate 6.x:
   - `@Column(name = ...)` wins. Without it, `SpringPhysicalNamingStrategy`
     turns camelCase into snake_case (`createdBy` → `created_by`).
   - `@Table(name = ...)` wins for the table name; otherwise the entity
     simple name is snake_cased.
   - `@Enumerated(STRING)` stores the enum constant as VARCHAR;
     `(ORDINAL)` stores the int.
   - `@Embedded` flattens the embeddable's columns onto the parent table —
     **no** child table.
   - `@Inheritance(SINGLE_TABLE)` keeps everything on the base table with a
     discriminator column. State the strategy in the summary so the user
     can sanity-check the table choice. `JOINED` / `TABLE_PER_CLASS` target
     different tables; pick the right one.
   - `@JoinTable(name = ...)` is its own table; column changes target that
     name, not the owning entities'.
   - Annotation you do not recognise → say which one and refuse.

## 3. Output template

Two sections, always together. DDL is **not** wrapped in BEGIN/ROLLBACK:
on MySQL and Oracle DDL is implicit-commit, so `BEGIN; ALTER ...; ROLLBACK;`
commits the ALTER on the first run. The "dry-run" already changed the
schema. Do not pretend otherwise.

DML uses a rollback-first dry-run template. This is **a nudge, not safety**:
swapping `ROLLBACK` for `COMMIT` is one keystroke. The protection is "force
the user to read affected rows once" — not a physical block. The skill
comment must say so honestly.

```sql
-- ⚠️ DDL 段：MySQL/Oracle 上 DDL 是 implicit commit，BEGIN/ROLLBACK 包不住。
-- 請先在 staging 環境跑驗證；或考慮改寫成 Liquibase changeset 進版本控制。
ALTER TABLE users ADD COLUMN created_by VARCHAR(50) DEFAULT 'system';

-- ⚠️ DML 段：dry-run 安全模板（並非物理保護，只是逼你看一眼影響範圍）。
-- 確認 rows affected 合理後，將 ROLLBACK 換成 COMMIT 才會落地。
BEGIN;
UPDATE users SET created_by = 'system' WHERE created_by IS NULL;
-- COMMIT;   <- 確認影響範圍後取消註解
ROLLBACK;
```

Special-case wording the summary must contain:

- `@Inheritance(SINGLE_TABLE)` cases — summary mentions `SINGLE_TABLE` so
  the reviewer can verify the table choice.
- `not-null-precondition-backfill` — summary mentions `兩段` and `第二步`
  to make it explicit the NOT NULL constraint is deferred.
- `column-rename-data-move` — summary mentions that `drop` of the old
  column is in a `第二步` / `follow-up` release.

## 4. Refusing

When the request does not fit §1 or version check fails, emit `REJECTED`
cleanly. Do not produce partial SQL with an apology — that defeats the
point of a refusal.

The refusal `summary` must contain anchor words so downstream evals can
grep:

- **Multi-table / scope mismatch** — include `scope`, `單表`, and
  `Liquibase` in the summary; suggest splitting the request or going
  through a DBA.
- **Wrong Spring Boot version** — include `版本`, `3.4`, and `3.5` in the
  summary; mention that other versions' Hibernate behaviour is not
  validated.
- **Unreadable entity / version** — say what you could not determine and
  ask for clarification or hand off to manual SQL.

## 5. Output format

Wrap the final result in a marker so the workflow parser can pick it up:

```
===SQL_RESULT===
{
  "status": "GENERATED" | "REJECTED",
  "entity": "User",
  "subcase": "add-column-default-backfill",
  "spring_boot_version": "3.5.x",
  "summary": "簡短一句說明 SQL 在做什麼，或拒絕原因；按 §3 / §4 含必要 anchor 字",
  "sql": "<full DDL+DML block, only when status=GENERATED>",
  "reason": "<refusal reason, only when status=REJECTED>"
}
```

`subcase` must be one of §1's four ids (or omitted when status=REJECTED).
For REJECTED, omit `sql` and put the canonical phrasing into `reason`.
Slack mrkdwn rules apply outside the marker (single-asterisk bold,
`<url|label>` links, no `#` headings).

## Self-check

1. Did I confirm Spring Boot 3.4 / 3.5 from the build file (not guessed)?
2. Does the request fit one of §1's four subcases? If not, did I emit
   REJECTED cleanly without producing partial SQL?
3. Is the DDL targeting the right table (especially for inheritance,
   `@Embedded`, and `@JoinTable`), and does the summary contain the
   special-case anchor words from §3?
4. Are both banner lines present (DDL implicit-commit warning + DML
   nudge-not-safety note)?
