# Migrating from PAT to GitHub App auth

> **Status:** Supported from v3.6+. PAT continues to work. This doc describes how to switch and when you should.
> **中文：** see [MIGRATION-github-app.md](MIGRATION-github-app.md)

---

## 1. Why switch to GitHub App

PAT hits a ceiling in three places that App fixes:

| Pain point | PAT behavior | App behavior |
|------------|--------------|--------------|
| Bot identity / audit | Issues opened by `Bob` | Issues opened by `<app-name>[bot]`; doesn't disappear when individuals leave |
| Repo scope | Usually grants access to *all* of an account's repos | Per-install repo selection plus scoped permissions |
| Org central management | Rotate the human, rotate the PAT | Admin revokes/reinstalls in one place from GitHub Settings |

The 1h installation-token TTL is a side effect, not a motivation.

---

## 2. Create the GitHub App

**Settings → Developer settings → GitHub Apps → New GitHub App** (personal) or **Organization settings → GitHub Apps** (org):

1. **App name** — what shows in issue/PR (`[bot]` suffix is appended automatically).
2. **Homepage URL** — any placeholder; not used by agentdock.
3. **Webhook** — uncheck "Active". This project does not consume webhooks.
4. Scroll down and click **Create GitHub App**.

> Screenshot placeholder: add real GitHub UI screenshots in a follow-up PR.

---

## 3. Configure repository permissions

App settings → **Permissions & events → Repository permissions**:

| Permission | Level | Used for |
|------------|-------|----------|
| **Issues** | `Read & write` | Open / comment on issues |
| **Contents** | `Read-only` | Clone / fetch repo |
| **Metadata** | `Read-only` | List repos / branches |
| **Pull requests** | `Read & write` | Post PR review comments |

Leave everything else `No access`. Preflight checks all four; missing any one fails startup.

---

## 4. Install the App

App settings → **Install App** → pick the org/account → choose **Only select repositories** (recommended) → tick the repos agentdock needs → **Install**.

The post-install URL looks like `https://github.com/settings/installations/<installation_id>`. Copy that `installation_id`.

---

## 5. Generate the private key

App settings → **Private keys → Generate a private key**. The browser downloads a `.pem` file.

- Place it where the app process can read it, e.g. `/etc/agentdock/app-key.pem`.
- Tighten permissions to `0600`, owned by the user that runs the app.
- **The private key never crosses the app/worker boundary** — don't put it in worker yaml or pass it via env to the worker.

---

## 6. Capture installation_id

From the URL in §4. The `app_id` is shown at the top of the App settings page.

---

## 7. Wire it into config

`app.yaml` example:

```yaml
github:
  token: ghp_xxx               # Optional; when both are set App wins, cross-installation repos fall back to PAT
  app:
    app_id: 123456
    installation_id: 7890123
    private_key_path: /etc/agentdock/app-key.pem
```

Or via environment variables (override yaml):

```bash
export GITHUB_APP_APP_ID=123456
export GITHUB_APP_INSTALLATION_ID=7890123
export GITHUB_APP_PRIVATE_KEY_PATH=/etc/agentdock/app-key.pem
```

`worker.yaml` **does not change** — workers never see GitHub App config; the private key never leaves the app process.

---

## 8. Verify preflight passes

```
agentdock app
```

Expected:

```
✓ GitHub App preflight passed (installation_id=7890123)
```

Failure modes (each maps to a fix):

| Message | Cause |
|---------|-------|
| `github app config partial: missing github.app.installation_id, ...` | All three fields are required |
| `github app private key invalid: <path>: ...` | Wrong path or file isn't an RSA PEM |
| `github app credentials rejected` | `app_id` doesn't match `private_key_path` |
| `github app installation not found: id=<X>` | `installation_id` typo or App was uninstalled |
| `github app installation missing required permissions: missing=[...]` | One or more of the four §3 permissions is missing |
| `github api unavailable during preflight (after 3 retries): ...` | GitHub 5xx — infrastructure, not config; restart |
| `github app mode requires secret_key (token cannot cross app/worker boundary unencrypted)` | App mode requires `secret_key`; see §11 |

---

## 9. Pure App vs App + PAT

agentdock supports three deployment shapes:

| Deployment | Behavior | When to use |
|------------|----------|-------------|
| PAT only | Identical to pre-v3.6 | Haven't switched / not switching |
| App only | App-only; cross-installation repos fail loudly | App covers every repo you need |
| App + PAT | App-priority, PAT fallback for cross-installation | App doesn't cover every relevant owner (e.g. cross-org requests) |

"Cross-installation" means the App is not installed at the primary repo's owner — dispatch detects this and uses the PAT for that job's `GH_TOKEN`. The Slack/log entry warns when fallback fires.

---

## 10. ⚠ Agent timeout boundary

The installation token TTL is 60 minutes. agentdock's cache re-mints when 50 minutes remain, but **a single agent run lasting more than 50 minutes** can still hit the boundary mid-fetch, resulting in 401.

**Recommendation: `queue.job_timeout ≤ 50min`.**

If `queue.job_timeout > 50min`, preflight logs a warning but does not block startup. When a long job fails mid-run, this is the first place to look.

---

## 11. Switching from PAT to App, step-by-step

### 11.1 Confirm `secret_key` is set

In App mode the installation token crosses the app/worker boundary inside `EncryptedSecrets`. Without `secret_key`, the token can't get through — preflight fails.

```yaml
secret_key: <64 hex chars>   # `agentdock init` generates one
```

### 11.2 ⚠ Do **not** set `secrets.GH_TOKEN` in `worker.yaml`

The worker's secrets overlay over app-side secrets is **worker wins** (`worker/pool/executor.go`). If `worker.yaml` has `secrets.GH_TOKEN`, it overwrites the freshly minted App token. The agent then sees the worker yaml value (usually empty) and 401s.

```yaml
# worker.yaml — don't do this
secrets:
  GH_TOKEN: ghp_xxx   # ← remove, let the app-minted token through
```

### 11.3 Apply App config; preflight passes

Walk through §1–8 until you see `✓ GitHub App preflight passed`.

### 11.4 Staging smoke test

Trigger `@bot triage` once in staging:

1. Wait for the issue to open.
2. Open the issue in GitHub UI — the author should be `<app-name>[bot]`, not a personal account.
3. The Reporter field still shows the triggering user's Slack display name (unchanged).

### 11.5 Drop the PAT once you've confirmed

In App + PAT mode, cross-installation repos still use PAT. Once you're sure every served repo is covered by the App, remove `github.token` from `app.yaml`:

```yaml
github:
  # token: ghp_xxx          # ← remove after confirmation
  app:
    app_id: 123456
    ...
```

---

## Advanced: rotate / revoke

- **Rotate private key**: generate a new PEM in App settings, overwrite the file at `private_key_path`, restart the app.
- **Revoke the App**: org/personal Settings → Installed GitHub Apps → Configure → Uninstall. agentdock's next mint will 401; preflight reports installation not found.
