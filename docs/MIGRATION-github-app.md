# 從 PAT 遷移到 GitHub App 認證

> **Status:** v3.6+ 起支援，PAT 仍可繼續使用。本文件描述如何切換到 App 認證以及何時應該切換。
> **English:** see [MIGRATION-github-app.en.md](MIGRATION-github-app.en.md)

---

## 1. 為何要切到 GitHub App

PAT 在三個面向上會撞到天花板，App 解掉這三點：

| 痛點 | PAT 行為 | App 行為 |
|------|---------|---------|
| Bot 身份/審計 | Issue 是 `Bob` 開的 | Issue 是 `<app-name>[bot]` 開的；不會跟個人離職一起消失 |
| Repo 範圍 | 通常拿到帳號 *所有* repo 的權限 | 安裝時可指定 repo 與限定權限 |
| Org 集中管理 | 換人就要重新發 PAT | admin 從 GitHub Settings 一處撤銷/重裝 |

短期權杖輪替（installation token 1h TTL）是吞下的副作用，不是切換的動機。

---

## 2. 在 GitHub.com 建立 GitHub App

到 **Settings → Developer settings → GitHub Apps → New GitHub App**（個人帳號）或 **Organization settings → GitHub Apps**（org）：

1. **App name** 填想要在 issue/PR 顯示的名字（會自動加上 `[bot]` 後綴）。
2. **Homepage URL** 隨意填一個 placeholder，沒被用到。
3. **Webhook** 取消勾選 "Active"（本專案不接 webhook）。
4. 拉到底點 **Create GitHub App**。

> 截圖位置：請補上實際 GitHub UI 的截圖（PR 階段補上）。

---

## 3. 設定 Repository permissions

App 設定頁的 **Permissions & events → Repository permissions**：

| 權限 | 等級 | 用途 |
|------|------|------|
| **Issues** | `Read & write` | 開 / 留言 issue |
| **Contents** | `Read-only` | clone / fetch repo |
| **Metadata** | `Read-only` | 列 repo / branch |
| **Pull requests** | `Read & write` | 寫 PR review comment |

其他權限保持 `No access`。Preflight 會檢查這四項；缺任何一項啟動會 fail。

---

## 4. 安裝 App 到 org 或個人帳號

App 設定頁左側 **Install App** → 選擇要安裝的 org/帳號 → 選 **Only select repositories**（推薦）並勾選 agentdock 要存取的 repo → **Install**。

安裝完成的 URL 形如 `https://github.com/settings/installations/<installation_id>`，記下這個 `installation_id`。

---

## 5. 產生 Private Key

App 設定頁的 **Private keys → Generate a private key**，瀏覽器會下載 `.pem` 檔。

- 把檔案放到 agentdock app 進程能讀的位置，例如 `/etc/agentdock/app-key.pem`。
- 權限收緊到 `0600`，owner 為跑 app 的 user。
- **私鑰永遠不過 app/worker boundary**——不要把它放到 worker yaml 裡，也不要透過 env var 傳給 worker。

---

## 6. 抄下 installation_id

從 §4 的安裝 URL 抄下來。也可以從 App 設定頁右上 **App ID** 看到 `app_id`。

---

## 7. 寫進 config

`app.yaml` 範例：

```yaml
github:
  token: ghp_xxx               # 可選；雙模式並存時 App 優先，cross-installation repo 走 PAT fallback
  app:
    app_id: 123456
    installation_id: 7890123
    private_key_path: /etc/agentdock/app-key.pem
```

或用環境變數（覆寫 yaml）：

```bash
export GITHUB_APP_APP_ID=123456
export GITHUB_APP_INSTALLATION_ID=7890123
export GITHUB_APP_PRIVATE_KEY_PATH=/etc/agentdock/app-key.pem
```

`worker.yaml` **不需要改動**——worker 不認 GitHub App 設定，private key 永遠不離開 app 進程。

---

## 8. 驗證 preflight 通過

```
agentdock app
```

預期看到：

```
✓ GitHub App preflight passed (installation_id=7890123)
```

可能的失敗訊息（每一條都有對應的修法）：

| 訊息 | 原因 |
|------|------|
| `github app config partial: missing github.app.installation_id, ...` | 三個欄位缺一不可 |
| `github app private key invalid: <path>: ...` | 路徑錯了或檔案不是 RSA PEM |
| `github app credentials rejected` | `app_id` 與 `private_key_path` 對不上 |
| `github app installation not found: id=<X>` | `installation_id` 抄錯，或 App 從那個 org 解安裝了 |
| `github app installation missing required permissions: missing=[...]` | §3 的四項權限缺一個以上 |
| `github api unavailable during preflight (after 3 retries): ...` | GitHub 5xx；不是 config 問題，重啟即可 |
| `github app mode requires secret_key (token cannot cross app/worker boundary unencrypted)` | App 模式必須有 `secret_key`，見 §11 |

---

## 9. 純 App vs App + PAT 並存

agentdock 支援以下三種部署：

| 部署 | 行為 | 何時用 |
|------|------|------|
| 純 PAT | 行為與 v3.5 之前一致 | 還沒切 App / 不打算切 |
| 純 App | 只認 App；cross-installation repo 失敗時 fail loudly | App 已涵蓋所有要存取的 repo |
| App + PAT 並存 | App 優先，cross-installation 走 PAT fallback | App 沒涵蓋部分 repo（例如使用者跨 org 提需求） |

「Cross-installation」指 App 沒安裝在 primary repo 的 owner——dispatch 階段會偵測到並用 PAT 去開 issue / clone。Slack 訊息會 log warn 說明 fallback 發生。

---

## 10. ⚠ Agent timeout 邊界警語

GitHub installation token 的 TTL 是 60 分鐘。agentdock 的 cache 在剩 50 分鐘時就會 re-mint，但**單次 agent run 超過 50 分鐘**還是可能撞到邊界 token 在 fetch 中途過期變 401。

**建議：`queue.job_timeout ≤ 50min`。**

如果 `queue.job_timeout > 50min`，preflight 會 log warn 但不 block 啟動。長 job 跑到一半失敗時這是首先要看的點。

---

## 11. 從 PAT 切換到 App 的步驟

逐步 checklist：

### 11.1 先確認 `secret_key` 已設

App 模式下，installation token 透過 `EncryptedSecrets` 跨 app/worker boundary。沒 `secret_key` 就過不去——preflight 會直接 fail。

```yaml
secret_key: <64 hex chars>   # `agentdock init` 會自動產生
```

### 11.2 ⚠ **不要**在 `worker.yaml` 設 `secrets.GH_TOKEN`

worker 端的 `secrets` 對 app 端的 secrets overlay 是 **worker wins** (`worker/pool/executor.go`)。如果 worker yaml 有 `secrets.GH_TOKEN`，會把 app 鑄造的新 token 覆蓋掉，agent 用到的就是 worker yaml 裡的舊值——多半是空字串，導致 401。

```yaml
# worker.yaml — 不要這樣寫
secrets:
  GH_TOKEN: ghp_xxx   # ← 拿掉，讓 app 鑄造的 token 過得去
```

### 11.3 設 App、preflight 過

照 §1–8 走完，看到 `✓ GitHub App preflight passed`。

### 11.4 Staging smoke

到 staging 環境手動觸發一次 `@bot triage`：

1. 等 issue 開出來。
2. 到 GitHub UI 看 issue 的 author——應該是 `<app-name>[bot]` 而非個人帳號。
3. Reporter 欄位仍是觸發者的 Slack 顯示名（不變）。

### 11.5 確認後再拿掉 PAT

App 模式 + PAT 並存的情況下，cross-installation repo 還是會用 PAT。確認所有要服務的 repo 都被 App 涵蓋之後，再從 `app.yaml` 拿掉 `github.token`：

```yaml
github:
  # token: ghp_xxx          # ← 確認過後拿掉
  app:
    app_id: 123456
    ...
```

---

## 進階：rotate / 撤銷 App

- **更新 private key**：在 App 設定頁 generate 新的 PEM，覆蓋 `private_key_path` 指到的檔案，重啟 app。
- **撤銷整個 App**：到 org/個人 settings 的 Installed GitHub Apps → Configure → Uninstall。agentdock 下次 mint 會 401，preflight 提示 installation not found。

