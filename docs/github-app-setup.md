# GitHub App 設定

[English](github-app-setup.en.md)

agentdock 兩種認證模式可以擇一或並存：

- **PAT**（個人 access token）— 最簡單，所有指令文件預設模式
- **GitHub App**（單一 installation）— Bot 身份開 issue、細粒度 repo 權限、org 集中管理

兩者並存時 App 優先；App 沒安裝在某 owner 時會走 PAT fallback（沒設 PAT 則 fail loudly）。本文件涵蓋 App 模式從零的設定步驟。**從現有 PAT 部署切換到 App** 請見 [MIGRATION-github-app.md](MIGRATION-github-app.md)。

## 1. 建立 GitHub App

到 **Settings → Developer settings → GitHub Apps → New GitHub App**（個人）或 **Organization settings → GitHub Apps**（org）。

| 欄位 | 設定 |
|------|------|
| **App name** | 想在 issue/PR 顯示的名字（會自動加 `[bot]` 後綴） |
| **Homepage URL** | 任意 placeholder，沒被用到 |
| **Webhook → Active** | **取消勾選**（agentdock 不接 webhook） |

拉到底點 **Create GitHub App**。

## 2. 設定 Repository permissions

App 設定頁 **Permissions & events → Repository permissions**：

| 權限 | 等級 | 用途 |
|------|------|------|
| **Issues** | `Read & write` | 開 / 留言 issue |
| **Contents** | `Read-only` | clone / fetch repo |
| **Metadata** | `Read-only` | 列 repo / branch |
| **Pull requests** | `Read & write` | 寫 PR review comment |

其他全部留 `No access`。Preflight 會檢查這四項，缺任何一項啟動會 fail。

## 3. 安裝到 org / 個人帳號

App 設定頁左側 **Install App** → 選 org/帳號 → **Only select repositories**（推薦）並勾選 agentdock 要存取的 repo → **Install**。

## 4. 抄下 `app_id` 與 `installation_id`

- `app_id`：App 設定頁右上 **App ID**
- `installation_id`：安裝完成的 URL `https://github.com/settings/installations/<installation_id>` 末段

## 5. 產生 Private Key

App 設定頁 **Private keys → Generate a private key** → 瀏覽器下載 `.pem`。

- 放到 agentdock app 進程能讀的位置，例如 `/etc/agentdock/app-key.pem`
- `chmod 0600`，owner 為跑 app 的 user
- **私鑰永遠不過 app/worker boundary** — 不要放到 worker yaml，也不要透過 env 傳給 worker

## 6. 寫進 config

`app.yaml`：

```yaml
github:
  token: ghp_xxx               # 可選；雙模式並存時 App 優先，cross-installation repo 走 PAT fallback
  app:                         # 三個欄位必須齊全；任一缺漏會被 preflight 擋下
    app_id: 123456
    installation_id: 7890123
    private_key_path: /etc/agentdock/app-key.pem

secret_key: <64 hex chars>     # App 模式硬性要求；token 透過此金鑰加密跨 app/worker boundary
```

或用環境變數覆寫：

```bash
export GITHUB_APP_APP_ID=123456
export GITHUB_APP_INSTALLATION_ID=7890123
export GITHUB_APP_PRIVATE_KEY_PATH=/etc/agentdock/app-key.pem
```

`worker.yaml` **不需要改動**。Worker 不認 GitHub App 設定，私鑰永遠不離開 app 進程。**請確認 `worker.yaml` 沒有設 `secrets.GH_TOKEN`**——worker-side overlay 會覆蓋 app 鑄造的 token，造成 401。

## 7. 驗證

```
agentdock app
```

預期：

```
✓ GitHub App preflight passed (installation_id=7890123)
```

常見錯誤訊息與對應修法：

| 訊息 | 原因 |
|------|------|
| `github app config partial: missing ...` | 三個欄位缺一不可 |
| `github app private key invalid: <path>: ...` | 路徑錯了，或檔案不是 RSA PEM |
| `github app credentials rejected` | `app_id` 與 `private_key_path` 對不上 |
| `github app installation not found: id=<X>` | `installation_id` 抄錯，或 App 從 org 解安裝了 |
| `github app installation missing required permissions: missing=[...]` | §2 的四項權限缺一個以上 |
| `github app mode requires secret_key (...)` | App 模式必須有 `secret_key` |
| `github api unavailable during preflight (after 3 retries): ...` | GitHub 5xx；不是 config 問題，重啟即可 |

## 8. Agent timeout 邊界

GitHub installation token TTL 是 60 分鐘，agentdock 在剩 50 分鐘時就會 re-mint。但**單次 agent run 超過 50 分鐘**還是可能撞到邊界 token 在 fetch 中途過期變 401。

**建議：`queue.job_timeout ≤ 50min`。**

`queue.job_timeout > 50min` 時 preflight 會 log warn 但不 block 啟動。長 job 跑到一半失敗時這是首先要看的點。

## 進階：rotate / 撤銷

- **更新 private key**：App 設定頁 generate 新的 PEM → 覆蓋 `private_key_path` 指到的檔案 → 重啟 app
- **撤銷整個 App**：org/個人 settings → Installed GitHub Apps → Configure → Uninstall。agentdock 下次 mint 會 401，preflight 提示 installation not found
