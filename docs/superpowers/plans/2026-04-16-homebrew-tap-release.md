# Homebrew Tap Release Channel Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 讓團隊成員以 `brew install agentdock` 取得 binary,對應 issue [#29](https://github.com/Ivantseng123/agentdock/issues/29)。

**Architecture:** agentdock 的 goreleaser 加入 `brews:` 區塊(`repository.branch` = feature head `bump-agentdock-{{.Tag}}`,`pull_request.base.branch: main`),每次 release 自動推 PR 到 `Ivantseng123/homebrew-tap`;tap 側裝一個 `pull_request_target` workflow,檢查 PR 作者 == `Ivantseng123` + PR 動 `Formula/` 或 `Casks/` 路徑 + `brew audit --strict --online` 過,三者皆 true 才自動 squash-merge。

**Tech Stack:** goreleaser v2、GitHub Actions、Homebrew、fine-grained PAT。

**Spec:** [`docs/superpowers/specs/2026-04-16-homebrew-tap-release-design.md`](../specs/2026-04-16-homebrew-tap-release-design.md)

---

## File Structure

### agentdock repo(`/Users/matt/code/github.com/Ivantseng123/agentdock/`)

- **Create:** `LICENSE` — 標準 MIT 模板(為 brew audit 設的 SPDX match target)
- **Modify:** `.goreleaser.yaml` — 新增 `brews:` 區塊 + `archives.files` 加入 `LICENSE`
- **Modify:** `.github/workflows/release.yml:57-58` — goreleaser step env 新增 `HOMEBREW_TAP_TOKEN`
- **Modify:** `README.md:20` — Quick Start 節加入 Homebrew 安裝路徑
- **Modify:** `README.en.md:20` — 對應英文版

### homebrew-tap repo(`/Users/matt/code/github.com/Ivantseng123/homebrew-tap/`)

- **Create:** `.github/workflows/auto-merge.yml` — PR-triggered trust-gated auto-merge
- **Create:** `.github/CODEOWNERS` — 保護 `.github/` 下所有檔案

### GitHub UI 設定(非檔案)

- Ivantseng123 帳號產生 fine-grained PAT
- `Ivantseng123/agentdock` repo secret:`HOMEBREW_TAP_TOKEN`
- `Ivantseng123/homebrew-tap` main branch protection rules

---

## Execution Order Rationale

順序不可隨意調換,否則會卡住:

```
1. 產 PAT(手動)
2. Tap 裝 auto-merge workflow + CODEOWNERS(tap self-merge,此時還沒 protection)
3. Tap 啟動 branch protection(從此 tap 都要 PR)
4. agentdock 存 PAT secret(必須先於 agentdock 改動 merge 前)
5. agentdock 改 .goreleaser.yaml / release.yml / LICENSE / README(單一 branch 多 commits)
6. agentdock PR merge
7. 等下次 release-please 觸發 → 驗 E2E
```

step 2 在 3 前:branch protection 一啟動就禁 direct push,要先裝 workflow 再鎖。
step 3 在 4 前:protection 就位才釋放 PAT,避免真空期。
step 4 在 6 前:secret 必須於 agentdock main 合入 brews: 之前存在,否則下一次 release workflow 會炸。

---

## Prerequisites (Manual GitHub UI Steps)

### Task 1: Generate fine-grained Personal Access Token

**Files:** 無(純 UI 操作)

- [ ] **Step 1: 登入 GitHub 為 `Ivantseng123` 帳號**

  確認右上角頭像顯示 `Ivantseng123`。若是 `shihyuho` 或其他帳號需要切換。

- [ ] **Step 2: 開啟 PAT 產生頁**

  瀏覽:GitHub → 右上頭像 → Settings → Developer settings(左側最下)→ Personal access tokens → **Fine-grained tokens** → **Generate new token**

- [ ] **Step 3: 填寫 PAT 欄位**

  逐項填入:
  - **Token name:** `HOMEBREW_TAP_TOKEN (agentdock release)`
  - **Resource owner:** `Ivantseng123`
  - **Expiration:** 選 365 days(最大值)
  - **Repository access:** 選 **Only select repositories** → 搜尋並選 `Ivantseng123/homebrew-tap`
  - **Permissions** → **Repository permissions** 區塊:
    - `Contents`: Read and write
    - `Pull requests`: Read and write
    - `Metadata`: Read-only(選 Contents 會自動啟用)
  - 其他權限全部保持 No access

- [ ] **Step 4: 產出並妥善保存**

  按底部 **Generate token** 按鈕。頁面頂端會顯示 `github_pat_11...` 開頭的字串 — **這是唯一能看到的機會**,立即複製到 1Password / Bitwarden / 密碼管理器,標籤 `agentdock homebrew tap 2026-04`。

- [ ] **Step 5: 記錄 PAT 過期日期**

  建立日曆提醒:2027-04-09(約 step 3 起 11 個月後,留 1 個月 buffer),事件名稱「輪替 HOMEBREW_TAP_TOKEN」,描述貼此 plan URL。

  **No commit for this task.**

---

### Task 2: Bootstrap homebrew-tap with auto-merge workflow + CODEOWNERS

**Working directory:** `/Users/matt/code/github.com/Ivantseng123/homebrew-tap/`

**Files:**
- Create: `/Users/matt/code/github.com/Ivantseng123/homebrew-tap/.github/workflows/auto-merge.yml`
- Create: `/Users/matt/code/github.com/Ivantseng123/homebrew-tap/.github/CODEOWNERS`

- [ ] **Step 1: 切到 tap repo 並同步 main**

  ```bash
  cd /Users/matt/code/github.com/Ivantseng123/homebrew-tap
  git checkout main
  git pull --ff-only
  ```

- [ ] **Step 2: 建立 feature branch**

  ```bash
  git checkout -b feat/auto-merge-workflow
  ```

- [ ] **Step 3: 建立 `.github/workflows/auto-merge.yml`**

  內容(原封不動寫入):

  ```yaml
  name: Auto-merge trusted formula bumps

  on:
    pull_request_target:
      types: [opened, synchronize, reopened]

  permissions:
    contents: write
    pull-requests: write

  concurrency:
    group: auto-merge-${{ github.event.pull_request.number }}
    cancel-in-progress: true

  jobs:
    auto-merge:
      runs-on: ubuntu-latest
      if: github.event.pull_request.user.login == 'Ivantseng123'
      steps:
        - uses: actions/checkout@v4
          with:
            ref: ${{ github.event.pull_request.head.sha }}
        - uses: Homebrew/actions/setup-homebrew@master
        - name: Identify formula/cask changes and audit
          id: audit
          run: |
            changed=$(gh pr view ${{ github.event.pull_request.number }} \
              --json files --jq '.files[].path' \
              | grep -E '^(Formula|Casks)/' || true)
            if [ -z "$changed" ]; then
              echo "No formula or cask changes; skipping auto-merge"
              echo "ok=false" >> "$GITHUB_OUTPUT"
              exit 0
            fi
            for f in $changed; do
              echo "::group::Auditing $f"
              brew audit --strict --online "$f"
              echo "::endgroup::"
            done
            echo "ok=true" >> "$GITHUB_OUTPUT"
          env:
            GH_TOKEN: ${{ github.token }}
        - name: Merge
          if: steps.audit.outputs.ok == 'true'
          run: gh pr merge ${{ github.event.pull_request.number }} --squash
          env:
            GH_TOKEN: ${{ github.token }}
  ```

  **Trust gate 設計**:`if:` 只檢查 actor 身份。**路徑過濾**由 audit step 透過 `grep '^(Formula|Casks)/'` 完成,設 `ok` output;Merge step 以 `if: steps.audit.outputs.ok == 'true'` 作為條件,確保只有 Formula/Casks 變更 + `brew audit` 過 才 merge。避免純 README / workflow PR 被意外 auto-merge。

- [ ] **Step 4: 建立 `.github/CODEOWNERS`**

  內容(一行):

  ```
  /.github/ @Ivantseng123
  ```

  結尾加一個 newline。

- [ ] **Step 5: 驗 workflow YAML 語法(若有 actionlint)**

  ```bash
  which actionlint && actionlint .github/workflows/auto-merge.yml || echo "actionlint not installed, skipping"
  ```
  
  Expected:若 `actionlint` 已裝,輸出應為空(無錯誤);若未裝,會印出「actionlint not installed」。未裝可跳過,merge 後 GitHub 會 runtime 回報錯誤。

- [ ] **Step 6: Commit**

  ```bash
  git add .github/workflows/auto-merge.yml .github/CODEOWNERS
  git commit -m "$(cat <<'EOF'
  feat: add trust-gated auto-merge workflow

  Adds pull_request_target workflow that auto-merges PRs from
  Ivantseng123 after brew audit passes. Merge is additionally gated
  on the PR touching Formula/ or Casks/ paths (via audit step output)
  — PRs touching only workflows, README, or config don't auto-merge.
  CODEOWNERS protects .github/ so trust-critical files require
  Ivantseng123 approval once branch protection is on.

  Context: Ivantseng123/agentdock#29

  Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>
  EOF
  )"
  ```

- [ ] **Step 7: Push + 開 PR + self-merge**

  ```bash
  git push -u origin feat/auto-merge-workflow
  gh pr create \
    --title "feat: add trust-gated auto-merge workflow" \
    --body "Bootstraps auto-merge infra for goreleaser-driven formula bumps from Ivantseng123/agentdock. See context in Ivantseng123/agentdock#29."
  gh pr merge --squash --delete-branch
  ```

  Expected:PR 開成功,以 squash 合併進 main,本地 branch 被刪。

- [ ] **Step 8: 回到 main 並同步**

  ```bash
  git checkout main
  git pull --ff-only
  ```

  Expected:本地 main 有 auto-merge workflow + CODEOWNERS 兩個新檔。

---

### Task 3: Enable branch protection on homebrew-tap main

**Files:** 無(GitHub UI 操作)

- [ ] **Step 1: 開啟 tap 的 Branch protection 設定頁**

  瀏覽:https://github.com/Ivantseng123/homebrew-tap/settings/branches

- [ ] **Step 2: 為 `main` 新增保護規則**

  點擊 **Add branch ruleset** 或 **Add rule**(視 UI 版本)。若已有 rule,則 **Edit**。
  
  **Branch name pattern:** `main`

- [ ] **Step 3: 設定規則(對應 spec B3)**

  勾選:
  - ✅ **Require a pull request before merging**
    - ❌ Require approvals(保持未勾;勾了會卡住 auto-merge)
    - ✅ Dismiss stale pull request approvals when new commits are pushed
    - ✅ Require review from Code Owners
    - ❌ Require approval of the most recent reviewable push(保持未勾)
  - ❌ Require status checks to pass before merging(保持未勾;避免 self-referential 循環)
  - ❌ Require conversation resolution before merging(保持未勾)
  - ✅ Include administrators(紀律;你自己也要走 PR)
  - ❌ Allow force pushes
  - ❌ Allow deletions

- [ ] **Step 4: 儲存規則**

  按 **Create** 或 **Save changes**。

- [ ] **Step 5: 驗證規則已啟用**

  回到 branches 頁,`main` 旁應顯示「Branch protection rules」對應已啟用狀態。

  **No commit for this task.**

---

### Task 4: Store `HOMEBREW_TAP_TOKEN` secret in agentdock

**Files:** 無(GitHub UI 操作)

- [ ] **Step 1: 開啟 agentdock 的 Secrets 設定頁**

  瀏覽:https://github.com/Ivantseng123/agentdock/settings/secrets/actions

- [ ] **Step 2: 新增 repository secret**

  點擊 **New repository secret**。

- [ ] **Step 3: 填入 secret**

  - **Name:** `HOMEBREW_TAP_TOKEN`(全大寫,底線分隔,與 `.goreleaser.yaml` 和 `release.yml` 使用的名稱一致)
  - **Value:** Task 1 產出的 PAT 字串(從密碼管理器複製,格式為 `github_pat_11...`)

- [ ] **Step 4: 儲存**

  按 **Add secret**。

- [ ] **Step 5: 驗證 secret 已列於清單**

  頁面重新載入後,Secrets 列表應含 `HOMEBREW_TAP_TOKEN`(值被遮罩,只能看名稱)。

  **No commit for this task.**

---

## Agentdock Side Implementation

### Task 5: Create feature branch in agentdock

**Working directory:** `/Users/matt/code/github.com/Ivantseng123/agentdock/`

- [ ] **Step 1: 同步 main**

  ```bash
  cd /Users/matt/code/github.com/Ivantseng123/agentdock
  git checkout main
  git pull --ff-only
  ```

- [ ] **Step 2: 建立 feature branch**

  ```bash
  git checkout -b feat/homebrew-tap-release
  ```

  **No commit for this task.**

---

### Task 6: Add `LICENSE` file at repo root

**Files:**
- Create: `/Users/matt/code/github.com/Ivantseng123/agentdock/LICENSE`

- [ ] **Step 1: 建立 `LICENSE` 檔**

  內容(原封不動):

  ```
  MIT License

  Copyright (c) 2025-2026 Ivantseng123

  Permission is hereby granted, free of charge, to any person obtaining a copy
  of this software and associated documentation files (the "Software"), to deal
  in the Software without restriction, including without limitation the rights
  to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
  copies of the Software, and to permit persons to whom the Software is
  furnished to do so, subject to the following conditions:

  The above copyright notice and this permission notice shall be included in all
  copies or substantial portions of the Software.

  THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
  IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
  FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
  AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
  LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
  OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
  SOFTWARE.
  ```

  注意:檔案不要有 leading/trailing whitespace;copyright 那行恰為 `Copyright (c) 2025-2026 Ivantseng123`,開頭無縮排。

- [ ] **Step 2: 驗證檔案內容首末行**

  ```bash
  head -3 LICENSE
  tail -3 LICENSE
  ```

  Expected:head 顯示 `MIT License` / 空行 / `Copyright (c) 2025-2026 Ivantseng123`。tail 結尾是 `SOFTWARE.`。

- [ ] **Step 3: Commit**

  ```bash
  git add LICENSE
  git commit -m "$(cat <<'EOF'
  docs: add MIT LICENSE file

  README 原就宣告 MIT but 無對應 LICENSE 檔。新增標準 SPDX MIT 模板
  以滿足 brew audit --strict --online 的 license match check,
  版權行採用 Ivantseng123(repo owner account),年份 2025-2026。

  Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>
  EOF
  )"
  ```

---

### Task 7: Add `LICENSE` to `.goreleaser.yaml` archives

**Files:**
- Modify: `/Users/matt/code/github.com/Ivantseng123/agentdock/.goreleaser.yaml:32-35`

- [ ] **Step 1: 編輯 `.goreleaser.yaml` archive files 區塊**

  原內容(lines 32-35):

  ```yaml
      files:
        - README.md
        - README.en.md
        - docs/MIGRATION-v1.md
  ```

  改為:

  ```yaml
      files:
        - README.md
        - README.en.md
        - LICENSE
        - docs/MIGRATION-v1.md
  ```

  `LICENSE` 插在 `README.en.md` 與 `docs/MIGRATION-v1.md` 之間。

- [ ] **Step 2: 驗證檔案仍為合法 YAML**

  ```bash
  python3 -c "import yaml; yaml.safe_load(open('.goreleaser.yaml'))" && echo OK
  ```

  Expected:`OK`

- [ ] **Step 3: Commit**

  ```bash
  git add .goreleaser.yaml
  git commit -m "$(cat <<'EOF'
  build: include LICENSE in release archives

  brew audit --strict --online 會下載 archive 並檢查 LICENSE 是否
  匹配 SPDX 模板,archive 未包 LICENSE 會觸發 audit 失敗。

  Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>
  EOF
  )"
  ```

---

### Task 8: Add `brews:` block to `.goreleaser.yaml`

**Files:**
- Modify: `/Users/matt/code/github.com/Ivantseng123/agentdock/.goreleaser.yaml` — 於 `docker_manifests:` 區塊結尾後、`release:` 之前新增

- [ ] **Step 1: 確定插入位置**

  ```bash
  grep -n "^release:\|^docker_manifests:" .goreleaser.yaml
  ```

  Expected output(行號可能因 Task 7 編輯而略動,以當下為準):

  ```
  76:docker_manifests:
  94:release:
  ```

  新 block 要插在 `release:` 那一行之前、當前 line 93(空行之後)。

- [ ] **Step 2: 插入 `brews:` 區塊**

  於 `release:` 之前插入(含前方一個空行):

  ```yaml

  brews:
    - name: agentdock
      repository:
        owner: Ivantseng123
        name: homebrew-tap
        branch: "bump-agentdock-{{.Tag}}"
        token: '{{ .Env.HOMEBREW_TAP_TOKEN }}'
        pull_request:
          enabled: true
          base:
            branch: main
      directory: Formula
      commit_author:
        name: Ivantseng123
        email: 170440613+Ivantseng123@users.noreply.github.com
      commit_msg_template: 'chore: bump agentdock to {{ .Tag }}'
      description: AgentDock — Slack-driven LLM agent orchestrator
      homepage: https://github.com/Ivantseng123/agentdock
      license: MIT
      install: |
        bin.install "agentdock"
      test: |
        system "#{bin}/agentdock", "--version"
      skip_upload: auto
  ```

  **Schema 語意關鍵**(初版踩坑的根因):
  - `repository.branch` = **HEAD**(goreleaser push 檔案的 feature branch),不是 target
  - `pull_request.base.branch` = **BASE**(PR 目標)
  - 源碼 reference:`goreleaser/goreleaser:internal/pipe/brew/brew.go`

- [ ] **Step 3: 驗證檔案仍為合法 YAML**

  ```bash
  python3 -c "import yaml; yaml.safe_load(open('.goreleaser.yaml'))" && echo OK
  ```

  Expected:`OK`

- [ ] **Step 4: 本地 snapshot dry-run(可選,需本機裝 goreleaser v2)**

  ```bash
  which goreleaser && goreleaser check || echo "goreleaser not installed; skipping local check"
  ```

  Expected:`config is valid` 或跳過訊息。若本機未裝,`release-validate.yml` 會在 PR 上跑 snapshot 替代驗證。

- [ ] **Step 5: Commit**

  ```bash
  git add .goreleaser.yaml
  git commit -m "$(cat <<'EOF'
  feat(release): publish Homebrew formula to Ivantseng123/homebrew-tap

  goreleaser brews: 區塊 — 每次 release 產 Formula/agentdock.rb,
  於 tap 開 bump-agentdock-{{.Tag}} branch 並 push PR(base=main)。
  skip_upload: auto 跳過 prerelease。tap 側 auto-merge workflow
  (已於 tap PR #1 裝)驗 actor + Formula/Casks path gate + brew
  audit 三條件後自動 squash-merge。

  Spec: docs/superpowers/specs/2026-04-16-homebrew-tap-release-design.md
  Ref: #29

  Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>
  EOF
  )"
  ```

---

### Task 9: Update `.github/workflows/release.yml` with `HOMEBREW_TAP_TOKEN` env

**Files:**
- Modify: `/Users/matt/code/github.com/Ivantseng123/agentdock/.github/workflows/release.yml:57-58`

- [ ] **Step 1: 編輯 release.yml 的 goreleaser step env**

  原內容(lines 53-58):

  ```yaml
        - uses: goreleaser/goreleaser-action@v6
          with:
            version: "~> v2"   # pin to goreleaser v2.x for reproducibility
            args: release --clean
          env:
            GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}
  ```

  改為:

  ```yaml
        - uses: goreleaser/goreleaser-action@v6
          with:
            version: "~> v2"   # pin to goreleaser v2.x for reproducibility
            args: release --clean
          env:
            GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}
            HOMEBREW_TAP_TOKEN: ${{ secrets.HOMEBREW_TAP_TOKEN }}
  ```

  僅新增 `HOMEBREW_TAP_TOKEN` 一行。

- [ ] **Step 2: 驗證檔案仍為合法 YAML**

  ```bash
  python3 -c "import yaml; yaml.safe_load(open('.github/workflows/release.yml'))" && echo OK
  ```

  Expected:`OK`

- [ ] **Step 3: 驗證 env 區塊兩 token 都在**

  ```bash
  grep -n "GITHUB_TOKEN\|HOMEBREW_TAP_TOKEN" .github/workflows/release.yml
  ```

  Expected:至少兩行輸出,包含 `GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}` 與 `HOMEBREW_TAP_TOKEN: ${{ secrets.HOMEBREW_TAP_TOKEN }}`。

- [ ] **Step 4: Commit**

  ```bash
  git add .github/workflows/release.yml
  git commit -m "$(cat <<'EOF'
  ci(release): pass HOMEBREW_TAP_TOKEN to goreleaser

  goreleaser brews: 會用此 token push PR 到 Ivantseng123/homebrew-tap。
  GITHUB_TOKEN 是 agentdock repo scoped,無法跨 repo。

  Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>
  EOF
  )"
  ```

---

### Task 10: Update `README.md` Quick Start with Homebrew option

**Files:**
- Modify: `/Users/matt/code/github.com/Ivantseng123/agentdock/README.md:20-36`

- [ ] **Step 1: 讀取現有 Quick Start 完整內容以確認上下文**

  ```bash
  sed -n '20,36p' README.md
  ```

  現行內容(作為參考):

  ```markdown
  ## Quick Start

  首次使用？用 `init` 產生起始 config：

  ```bash
  go build -o agentdock ./cmd/agentdock/
  ./agentdock init -i   # 互動式填入 Slack / GitHub token
  ./run.sh
  ```

  或直接：

  ```bash
  ./agentdock app -c config.yaml
  # 等同：go build -o agentdock ./cmd/agentdock/ && ./agentdock app -c config.yaml
  ```
  ```

- [ ] **Step 2: 編輯 Quick Start 加入 Homebrew 路徑**

  將整段 `## Quick Start` 到下一個 `##` 標題之前替換為:

  ```markdown
  ## Quick Start

  **macOS / Linux(Homebrew,推薦團隊開發者):**

  ```bash
  brew tap Ivantseng123/tap
  brew install agentdock
  agentdock init -i   # 互動式填入 Slack / GitHub token
  agentdock app -c config.yaml
  ```

  升級:`brew upgrade agentdock`。

  > brew 只裝 binary;若要完整使用 `app`/`worker` 子指令,仍需 config 與外部 CLI(`claude`、`opencode`、`codex`、`gemini`)。正式部署請使用 Docker 映像 `ghcr.io/ivantseng123/agentdock`。

  **從源碼:**

  ```bash
  go build -o agentdock ./cmd/agentdock/
  ./agentdock init -i
  ./run.sh
  ```

  或直接:

  ```bash
  ./agentdock app -c config.yaml
  # 等同：go build -o agentdock ./cmd/agentdock/ && ./agentdock app -c config.yaml
  ```
  ```

- [ ] **Step 3: 驗證 Quick Start 區段完整**

  ```bash
  grep -c "## Quick Start\|brew install agentdock\|go build" README.md
  ```

  Expected:至少 `3`(一個 Quick Start 標題、至少一個 brew install 行、至少一個 go build 行)。

- [ ] **Step 4: Commit**

  ```bash
  git add README.md
  git commit -m "$(cat <<'EOF'
  docs(readme): add Homebrew install path to Quick Start

  Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>
  EOF
  )"
  ```

---

### Task 11: Update `README.en.md` Quick Start with Homebrew option

**Files:**
- Modify: `/Users/matt/code/github.com/Ivantseng123/agentdock/README.en.md:20-36`

- [ ] **Step 1: 讀取現有 Quick Start 完整內容**

  ```bash
  sed -n '20,36p' README.en.md
  ```

- [ ] **Step 2: 編輯 Quick Start 加入 Homebrew 路徑**

  將整段 `## Quick Start` 到下一個 `##` 標題之前替換為:

  ```markdown
  ## Quick Start

  **macOS / Linux (Homebrew, recommended for team developers):**

  ```bash
  brew tap Ivantseng123/tap
  brew install agentdock
  agentdock init -i   # interactive Slack / GitHub token setup
  agentdock app -c config.yaml
  ```

  Upgrade: `brew upgrade agentdock`.

  > Homebrew installs only the binary. To use `app`/`worker` subcommands fully, you still need a config and external CLIs (`claude`, `opencode`, `codex`, `gemini`). For production deployment, use the Docker image `ghcr.io/ivantseng123/agentdock`.

  **From source:**

  ```bash
  go build -o agentdock ./cmd/agentdock/
  ./agentdock init -i
  ./run.sh
  ```

  Or directly:

  ```bash
  ./agentdock app -c config.yaml
  # equivalent to: go build -o agentdock ./cmd/agentdock/ && ./agentdock app -c config.yaml
  ```
  ```

- [ ] **Step 3: 驗證 Quick Start 區段完整**

  ```bash
  grep -c "## Quick Start\|brew install agentdock\|go build" README.en.md
  ```

  Expected:至少 `3`。

- [ ] **Step 4: Commit**

  ```bash
  git add README.en.md
  git commit -m "$(cat <<'EOF'
  docs(readme-en): add Homebrew install path to Quick Start

  Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>
  EOF
  )"
  ```

---

### Task 12: Push branch and open PR

**Files:** 無(git + GitHub API 操作)

- [ ] **Step 1: Push feature branch**

  ```bash
  git push -u origin feat/homebrew-tap-release
  ```

- [ ] **Step 2: 開 PR**

  ```bash
  gh pr create --title "feat: publish agentdock to Homebrew tap" --body "$(cat <<'EOF'
  ## Summary

  新增 Homebrew tap release 管道(closes #29):
  - goreleaser `brews:` 區塊,每次 release 推 PR 到 `Ivantseng123/homebrew-tap`
  - `HOMEBREW_TAP_TOKEN` secret 傳進 goreleaser step
  - Archive 新加 `LICENSE`,以符合 `brew audit --strict --online`
  - 新增 `LICENSE` 檔(SPDX MIT)
  - README 中英文 Quick Start 加入 brew install 路徑

  ## Prerequisites(已完成)

  - [x] Ivantseng123 帳號產 fine-grained PAT(scope: `homebrew-tap`,`Contents: RW`、`Pull requests: RW`)
  - [x] agentdock secrets 存入 `HOMEBREW_TAP_TOKEN`
  - [x] `Ivantseng123/homebrew-tap` 裝 auto-merge workflow + CODEOWNERS + branch protection

  ## Verification

  - [x] `release-validate.yml` 於此 PR 跑 `goreleaser --snapshot --skip=publish` 通過
  - [ ] PR merge 後,下一次 release-please release 會觸發完整 E2E:
    - [ ] release workflow 無錯
    - [ ] tap 收到 `bump-agentdock-vX.Y.Z` PR
    - [ ] tap auto-merge workflow pass,PR 自動 squash-merge
    - [ ] `brew tap Ivantseng123/tap && brew install agentdock && agentdock --version` 印出 `vX.Y.Z`

  Spec: [docs/superpowers/specs/2026-04-16-homebrew-tap-release-design.md](docs/superpowers/specs/2026-04-16-homebrew-tap-release-design.md)
  EOF
  )"
  ```

  Expected:輸出 PR URL。

- [ ] **Step 3: 等 `release-validate.yml` check 完成**

  ```bash
  gh pr checks --watch
  ```

  Expected:`release-validate.yml` 的 `snapshot` job 綠燈。若紅燈,看 log 找原因,多半是 `.goreleaser.yaml` 欄位名稱與 goreleaser v2 schema 不匹配 — 修改對應 task(Task 8 brews: 區塊最可能)並 push fix。

---

### Task 13: Merge PR

**Files:** 無(GitHub 操作)

- [ ] **Step 1: 確認所有 check 通過**

  ```bash
  gh pr checks
  ```

  Expected:全綠。

- [ ] **Step 2: Merge(用 squash)**

  ```bash
  gh pr merge --squash --delete-branch
  ```

  Expected:PR merged 進 main,本地 + remote branch 被刪。

- [ ] **Step 3: 同步本地 main**

  ```bash
  git checkout main
  git pull --ff-only
  ```

  **No additional commit for this task.**

---

## Post-Implementation Verification

### Task 14: Wait for next release-please release cycle

**Files:** 無(被動等待)

- [ ] **Step 1: 識別下一次 release 來源**

  此 PR 本身是 `feat:` commit,會讓 release-please 將下個版本 bump 成 minor(例如 v1.0.1 → v1.1.0)。Merge 後 release-please workflow 會在 main 自動開一個「Release PR」(title 類似 `chore(main): release 1.1.0`)。

  ```bash
  gh pr list --search "release-please" --state open --repo Ivantseng123/agentdock
  ```

  Expected:列出一個由 `googleapis-release-please` 開的 PR。

- [ ] **Step 2: Merge release-please PR 觸發正式 release**

  Review release PR 內容(CHANGELOG、version bump),無誤後:

  ```bash
  gh pr merge <release-pr-number> --squash
  ```

  觸發流程:release-please 合併 → tag 建立 → `release.yml` workflow_call → goreleaser 跑完 → **brews: 區塊開 PR 到 tap** → tap auto-merge workflow 觸發。

- [ ] **Step 3: 監看 release workflow**

  ```bash
  gh run watch --exit-status $(gh run list --workflow=release.yml --limit 1 --json databaseId --jq '.[0].databaseId')
  ```

  Expected:整個 run 綠燈。其中 goreleaser step log 應含類似 `creating pull request to Ivantseng123/homebrew-tap`。

---

### Task 15: Verify tap-side E2E success

**Files:** 無(驗證)

- [ ] **Step 1: 確認 tap PR 被 auto-merge**

  ```bash
  gh pr list --repo Ivantseng123/homebrew-tap --state all --limit 3
  ```

  Expected:最上面一筆是類似 `chore: bump agentdock to v1.1.0`,status `merged`,作者 `Ivantseng123`,head branch `bump-agentdock-v1.1.0`。

- [ ] **Step 2: 確認 tap 有 Formula/agentdock.rb**

  ```bash
  gh api repos/Ivantseng123/homebrew-tap/contents/Formula/agentdock.rb --jq '.name' 2>&1
  ```

  Expected:輸出 `agentdock.rb`。

- [ ] **Step 3: 於 macOS 上測 brew 安裝(若開發機即 macOS)**

  ```bash
  brew tap Ivantseng123/tap
  brew install agentdock
  agentdock --version
  ```

  Expected 最後一行輸出(以 v1.1.0 為例):

  ```
  agentdock version 1.1.0 (commit <7-digit-sha>, built <ISO date>)
  ```

- [ ] **Step 4: 驗 `brew info` 顯示正確 metadata**

  ```bash
  brew info agentdock
  ```

  Expected:description 顯示 `AgentDock — Slack-driven LLM agent orchestrator`,homepage 指 `https://github.com/Ivantseng123/agentdock`,license `MIT`。

  若 Step 3 / 4 任一失敗,記錄錯誤訊息、對照 spec 的「Implementation-time Unknowns」表找對應假設、於新 PR 修正後重驗。

---

### Task 16: Team onboarding broadcast

**Files:** 無(團隊通訊)

- [ ] **Step 1: 選擇合適頻道公告**

  Slack #dev 或專案的對應頻道(視團隊慣例)。

- [ ] **Step 2: 發送 onboarding 訊息**

  訊息範本(以實際 release 版號取代 `vX.Y.Z`):

  ```
  📦 AgentDock 現已支援 Homebrew(macOS / Linux):

  brew tap Ivantseng123/tap
  brew install agentdock
  agentdock --version   # 應印 vX.Y.Z

  之後升級:brew upgrade agentdock

  ⚠️ brew 只裝 binary,`app`/`worker` 子指令仍需配置 config 與外部 CLI
  (claude、opencode、codex、gemini)。
  正式部署請繼續使用 Docker 映像 ghcr.io/ivantseng123/agentdock。

  任何 brew install 失敗請附 `brew doctor` 輸出開 issue。
  ```

  **No commit for this task.**

---

## Troubleshooting(若 Task 14–15 E2E 失敗)

對照 spec 第「Implementation-time Unknowns」表逐條排查。常見情境與對應調整:

| 症狀 | 最可能原因 | 調整位置 |
|---|---|---|
| release-validate.yml 於 Task 12 即紅(snapshot 失敗) | goreleaser v2 brews: schema 細節不符 | `.goreleaser.yaml` 的 `brews:` 區塊(Task 8) |
| release.yml 跑完但無 tap PR | PAT 未正確傳入或權限不足 | 檢查 Task 4 secret 值、Task 1 PAT 的 Permissions 勾選 |
| Tap PR 開了但 auto-merge workflow 未觸發 | PR actor 非 `Ivantseng123`(commit_author 與 PAT owner 不一致) | 檢查 `.goreleaser.yaml:brews.commit_author.name` 與 PAT owner 一致;PR user.login 應為 PAT owner |
| goreleaser 直推 main,沒開 PR(v1.1.0 原始症狀) | `repository.branch` 誤設為目標 base(main)而非 feature head | `.goreleaser.yaml:brews.repository.branch` 必為 template 化 feature branch(如 `bump-agentdock-{{.Tag}}`),`pull_request.base.branch: main` 才是目標。詳見 `fix/homebrew-tap-pr-flow` commit message |
| Auto-merge workflow 進了但 Merge step 被 skip | PR 未動 Formula/Casks 路徑(path gate 未觸發) | 正常行為;若確實是 formula bump 卻被 skip,看 audit step 輸出判斷 `grep -E '^(Formula\|Casks)/'` 為何沒 match |
| Auto-merge workflow 跑到 `brew audit` 失敗 | 產出的 Formula 某欄位不符合 audit 規範 | 從 audit 輸出訊息判斷;多半是 `description` 首字大小寫、SPDX license、或 archive 缺 LICENSE(應已補) |
| `brew install` 失敗於 sha256 驗證 | Formula 中的 sha256 與 release asset 不符 | 不應發生(goreleaser 自計算);若發生代表 goreleaser pipeline 有 race,重 tag 一次 |
| Branch protection 阻擋 auto-merge 自己 merge | `Include administrators` 或 CODEOWNER review 意外觸發 | 若路徑為 `Formula/*`,CODEOWNER 不該觸發;若觸發代表 CODEOWNERS 範圍設太廣,回 Task 2 Step 4 確認僅 `/.github/` 受保護 |
