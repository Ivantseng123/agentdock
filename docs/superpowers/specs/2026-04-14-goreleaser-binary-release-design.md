# Design: goreleaser Binary Release

**Date:** 2026-04-14
**Related:** [#9](https://github.com/Ivantseng123/agentdock/issues/9)

## Problem Statement

**HMW**：在 release-please 打 tag 後，自動產出並發佈預編譯 binary，讓使用者（尤其是 external worker 場景，見 `README.md` 部署模式表）不需要 `go build` 或拉 Docker 也能跑 AgentDock。

目前：release-please 產 GitHub Release，`release-publish.yml` 只 build Docker image 推 ghcr.io。沒有 binary artifact，external worker 要跑就得自己 clone + `go build`。

## Goals

- Release 發佈後自動產出 5 個預編譯 binary（linux/darwin × amd64/arm64 + windows/amd64）附加到 GH Release
- 同一份 build 產物注入 Docker image，消除「Docker 版 binary ≠ Release 版 binary」的分裂
- 一套工具（goreleaser）取代現有 `release-publish.yml`，單一真相

## Non-Goals

- Homebrew tap / apt / rpm 發佈
- SBOM / cosign signing（值得做，但不是這輪 MVP）
- Windows arm64（使用者基數過低，goreleaser `ignore` 排除）
- 動 release-please 設定（它繼續負責 version bump + CHANGELOG）
- 自動 smoke test（需要 Slack / GH token，CI 成本不成比例）

## Architecture

```
release-please 合併 Release PR
  → GitHub 建 tag + Release（trigger: release.published）
     ↓
  .github/workflows/release.yml
     ↓
  goreleaser-action
     ├─ builds: 5 個 binary
     │   linux/amd64, linux/arm64,
     │   darwin/amd64, darwin/arm64,
     │   windows/amd64
     ├─ archives: tar.gz（非 win）+ zip（win），含 README/LICENSE/config.example.yaml
     ├─ checksums.txt
     ├─ dockers: linux/amd64 + linux/arm64 image（吃 goreleaser 編好的 binary）
     ├─ docker_manifests: multi-arch manifest list
     │   tag: {{.Version}} 和 {{.Major}}.{{.Minor}}
     └─ 推 GH Release assets（append）+ ghcr.io
```

舊 `release-publish.yml` 刪除。舊 `Dockerfile` 刪除，新建 `Dockerfile.release`。

## Components

### 1. `.goreleaser.yaml`（新增）

```yaml
version: 2
project_name: agentdock

builds:
  - id: bot
    main: ./cmd/bot
    binary: bot
    env: [CGO_ENABLED=0]
    goos: [linux, darwin, windows]
    goarch: [amd64, arm64]
    ignore:
      - { goos: windows, goarch: arm64 }
    ldflags:
      - -s -w
      - -X main.version={{.Version}}
      - -X main.commit={{.Commit}}
      - -X main.date={{.Date}}

archives:
  - id: default
    formats: [tar.gz]
    format_overrides:
      - { goos: windows, formats: [zip] }
    files:
      - README.md
      - LICENSE
      - config.example.yaml

checksum:
  name_template: 'checksums.txt'

dockers:
  - image_templates:
      - 'ghcr.io/ivantseng123/agentdock:{{ .Version }}-amd64'
    dockerfile: Dockerfile.release
    use: buildx
    goarch: amd64
    build_flag_templates:
      - --platform=linux/amd64
      - --build-arg=OPENCODE_VERSION=1.4.3
      - --build-arg=GH_VERSION=2.65.0
    extra_files:
      - agents/skills
      - config.example.yaml

  - image_templates:
      - 'ghcr.io/ivantseng123/agentdock:{{ .Version }}-arm64'
    dockerfile: Dockerfile.release
    use: buildx
    goarch: arm64
    build_flag_templates:
      - --platform=linux/arm64
      - --build-arg=OPENCODE_VERSION=1.4.3
      - --build-arg=GH_VERSION=2.65.0
    extra_files:
      - agents/skills
      - config.example.yaml

docker_manifests:
  - name_template: 'ghcr.io/ivantseng123/agentdock:{{ .Version }}'
    image_templates:
      - 'ghcr.io/ivantseng123/agentdock:{{ .Version }}-amd64'
      - 'ghcr.io/ivantseng123/agentdock:{{ .Version }}-arm64'
  - name_template: 'ghcr.io/ivantseng123/agentdock:{{ .Major }}.{{ .Minor }}'
    image_templates:
      - 'ghcr.io/ivantseng123/agentdock:{{ .Version }}-amd64'
      - 'ghcr.io/ivantseng123/agentdock:{{ .Version }}-arm64'

release:
  github:
    owner: Ivantseng123
    name: agentdock
  mode: append
```

### 2. `Dockerfile.release`（新增；取代舊 Dockerfile）

```dockerfile
FROM node:22-alpine

RUN apk add --no-cache git ca-certificates curl

RUN npm install -g @anthropic-ai/claude-code @openai/codex

ARG OPENCODE_VERSION=1.4.3
RUN curl -sL https://github.com/anomalyco/opencode/releases/download/v${OPENCODE_VERSION}/opencode-linux-x64-musl.tar.gz | \
    tar xzf - -C /usr/local/bin opencode

ARG GH_VERSION=2.65.0
ARG TARGETARCH
RUN curl -sL https://github.com/cli/cli/releases/download/v${GH_VERSION}/gh_${GH_VERSION}_linux_${TARGETARCH}.tar.gz | \
    tar xz -C /usr/local/bin --strip-components=2 gh_${GH_VERSION}_linux_${TARGETARCH}/bin/gh

COPY bot /bot

COPY agents/skills/ /opt/agents/skills/
RUN mkdir -p /home/node/.claude/skills && \
    for d in /opt/agents/skills/*/; do \
      ln -s "$d" /home/node/.claude/skills/$(basename "$d"); \
    done

RUN mkdir -p /data/repos && chown node:node /data/repos

COPY config.example.yaml /config.yaml

USER node
ENTRYPOINT ["/bot"]
CMD ["-config", "/config.yaml"]
```

與舊 `Dockerfile` 的差異：
- 移除 builder stage（原 line 1-9），binary 由 goreleaser build 並由 `COPY bot /bot` 注入
- `gh` 下載 URL 用 `${TARGETARCH}` 取代寫死 `amd64`（順手修掉舊 Dockerfile 的隱性 bug：arm64 image 會裝錯 arch 的 `gh`）

### 3. `cmd/bot/main.go` 修改

現狀（line 26）：
```go
var version = "dev"
```

新增 commit 與 build date 注入點：
```go
var (
    version = "dev"
    commit  = "unknown"
    date    = "unknown"
)
```

啟動 log 擴充（line 229 附近）：
```go
slog.Info("starting bot", "version", version, "commit", commit, "date", date)
```

### 4. `.github/workflows/release.yml`（新增；取代 `release-publish.yml`）

```yaml
name: Release
on:
  release:
    types: [published]
  workflow_dispatch:
    inputs:
      tag:
        description: 'Release tag to build (e.g. v1.2.3)'
        required: true

permissions:
  contents: write
  packages: write

jobs:
  goreleaser:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0
          ref: ${{ inputs.tag || github.event.release.tag_name }}

      - uses: actions/setup-go@v5
        with:
          go-version: '1.25'

      - uses: docker/setup-qemu-action@v3
      - uses: docker/setup-buildx-action@v3

      - uses: docker/login-action@v3
        with:
          registry: ghcr.io
          username: ${{ github.actor }}
          password: ${{ secrets.GITHUB_TOKEN }}

      - uses: goreleaser/goreleaser-action@v6
        with:
          version: latest
          args: release --clean
        env:
          GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}
```

### 5. 刪除清單

- `.github/workflows/release-publish.yml`
- `Dockerfile`（被 `Dockerfile.release` 取代；`run.sh` 是 `go build`，本地開發不依賴 Dockerfile）

## Data Flow

Trigger → goreleaser 單一 job 內完成：

1. Checkout tag（`fetch-depth: 0` 供 goreleaser 讀 git 歷史）
2. goreleaser 讀 `.goreleaser.yaml`
3. 對 5 個 target 並行 `go build` → binary artifacts
4. Archive 打包（tar.gz / zip）+ checksums.txt
5. 對 linux/amd64 + linux/arm64 的 binary，各自 `docker buildx build` 用 `Dockerfile.release`（binary 作為 build context 一部分）→ push 到 ghcr.io
6. `docker_manifests` 組 multi-arch manifest list → push 兩個 tag（`{{.Version}}`、`{{.Major}}.{{.Minor}}`）
7. Archive + checksums append 到 GitHub Release assets（`release.mode: append`，不覆寫 release-please 已建好的 release body）

## Error Handling

- **goreleaser 執行失敗**：workflow 紅燈；`workflow_dispatch` 可用同 tag 重跑（goreleaser `--clean` 清乾淨舊 artifact）
- **ghcr.io push 失敗**：同上，重跑 workflow
- **GH Release assets append 失敗**：goreleaser 在 step 末期才執行，前面 binary / docker 已完成；人工刪 release assets + 重跑
- **release-please Release body 被覆蓋風險**：`release.mode: append` 明確保留原始 body，只加 assets

## Testing Strategy

1. **本地 dry-run（開發階段）**
   ```bash
   goreleaser release --snapshot --clean --skip=publish
   ```
   驗 `.goreleaser.yaml` 合法、5 個 binary 能編、`Dockerfile.release` 兩個 arch 能 build。不進 CI（太慢）。

2. **首次 release 驗證（手動）**
   合第一個用新 workflow 的 Release PR 後，檢查：
   - [ ] GH Release 頁面有 5 個 archive + `checksums.txt`
   - [ ] `ghcr.io/ivantseng123/agentdock:<version>` 的 manifest inspect 顯示 amd64 + arm64
   - [ ] 下載 linux/amd64 binary → `./bot -version`（或啟動 log）顯示正確 version/commit/date
   - [ ] `docker pull ghcr.io/ivantseng123/agentdock:<version>` + `docker run --rm ... -version` 與 binary 一致
   - [ ] arm64 image 裡的 `/usr/local/bin/gh` 是 arm64 版本（`file` 檢查）

3. **Rollback**：git 歷史留有舊 `release-publish.yml` + `Dockerfile`，`git revert` 可回滾。不做 feature flag。

## Key Assumptions

- [x] `cmd/bot/main.go:26` 有 `var version = "dev"`，ldflags 注入可行 —— 已驗證
- [ ] `Dockerfile.release` 的 runtime 層（node + opencode + gh CLI + skills）在移除 builder stage 後仍能運作，且 `COPY bot /bot` 接 goreleaser 產出的 binary 位置正確（goreleaser `dockers.extra_files` + `COPY` 路徑需實測）
- [ ] goreleaser `dockers` 區塊配合 `docker/setup-qemu-action` + `buildx`，能在 ubuntu-latest runner 上跨編 arm64 image
- [ ] release-please 產的 tag 是 `v0.1.1` 這類 semver，goreleaser 預設 `tag_sort` 與 version template 相容
- [ ] `goreleaser/goreleaser-action@v6` + `GITHUB_TOKEN` 可 append assets 到 release-please 已建好的 Release（`contents: write` 權限）

## Open Questions

- Windows binary 實際使用量未知，MVP 包入但首輪 release 後需回看下載數，無人使用則下輪縮減
- `Dockerfile.release` 裡的 `OPENCODE_VERSION` / `GH_VERSION` 目前以 `build-arg` 固定版本。goreleaser `build_flag_templates` 硬編值，日後升級要同時改 `.goreleaser.yaml` 與 `Dockerfile.release` 兩處 —— 可接受，重構成集中配置不在 MVP 範圍

## References

- Issue #9: <https://github.com/Ivantseng123/agentdock/issues/9>
- 現有 `release-publish.yml`（將刪除）
- 現有 `Dockerfile`（將刪除，由 `Dockerfile.release` 取代）
- `cmd/bot/main.go:26`、`cmd/bot/main.go:229`
- `README.md` 部署模式表（external worker 段落）
