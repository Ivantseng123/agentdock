# GitHub App setup

[中文](github-app-setup.md)

agentdock supports two auth modes; pick one or run both:

- **PAT** (personal access token) — simplest; all command docs assume this by default.
- **GitHub App** (single installation) — bot identity on issues, fine-grained repo scope, org-managed.

When both are configured, App takes priority; when the App isn't installed at a repo's owner, agentdock falls back to PAT (or fails loudly if no PAT). This doc covers a fresh App setup. **Switching an existing PAT deployment to App auth** is in [MIGRATION-github-app.en.md](MIGRATION-github-app.en.md).

## 1. Create the GitHub App

**Settings → Developer settings → GitHub Apps → New GitHub App** (personal) or **Organization settings → GitHub Apps** (org).

| Field | Setting |
|-------|---------|
| **App name** | What shows in issues/PRs (`[bot]` is appended automatically) |
| **Homepage URL** | Any placeholder; not used |
| **Webhook → Active** | **uncheck** (agentdock doesn't consume webhooks) |

Scroll down and click **Create GitHub App**.

## 2. Configure repository permissions

App settings → **Permissions & events → Repository permissions**:

| Permission | Level | Used for |
|------------|-------|----------|
| **Issues** | `Read & write` | Open / comment on issues |
| **Contents** | `Read-only` | Clone / fetch repo |
| **Metadata** | `Read-only` | List repos / branches |
| **Pull requests** | `Read & write` | Post PR review comments |

Leave everything else `No access`. Preflight checks all four; any missing one fails startup.

## 3. Install on an org / personal account

App settings → **Install App** → pick the org/account → **Only select repositories** (recommended) → tick the repos agentdock needs → **Install**.

## 4. Capture `app_id` and `installation_id`

- `app_id`: top of the App settings page (**App ID**)
- `installation_id`: trailing segment of `https://github.com/settings/installations/<installation_id>`

## 5. Generate the private key

App settings → **Private keys → Generate a private key** → browser downloads a `.pem`.

- Place where the agentdock app process can read it, e.g. `/etc/agentdock/app-key.pem`
- `chmod 0600`, owned by the user that runs the app
- **The private key never crosses the app/worker boundary** — don't put it in worker yaml or pass it via env to the worker

## 6. Wire into config

`app.yaml`:

```yaml
github:
  token: ghp_xxx               # Optional; with both set, App wins, cross-installation repos fall back to PAT
  app:                         # All three fields required; missing any → preflight fails
    app_id: 123456
    installation_id: 7890123
    private_key_path: /etc/agentdock/app-key.pem

secret_key: <64 hex chars>     # Required in App mode; token crosses app/worker boundary inside an AES-encrypted bundle
```

Or via environment variables:

```bash
export GITHUB_APP_APP_ID=123456
export GITHUB_APP_INSTALLATION_ID=7890123
export GITHUB_APP_PRIVATE_KEY_PATH=/etc/agentdock/app-key.pem
```

`worker.yaml` **does not change**. Workers never see GitHub App config; the private key never leaves the app process. **Confirm `worker.yaml` does not set `secrets.GH_TOKEN`** — the worker-side overlay would overwrite the app-minted token and trigger 401s.

## 7. Verify

```
agentdock app
```

Expected:

```
✓ GitHub App preflight passed (installation_id=7890123)
```

Failure modes:

| Message | Cause |
|---------|-------|
| `github app config partial: missing ...` | All three fields are required |
| `github app private key invalid: <path>: ...` | Wrong path, or file isn't an RSA PEM |
| `github app credentials rejected` | `app_id` doesn't match `private_key_path` |
| `github app installation not found: id=<X>` | `installation_id` typo or App was uninstalled |
| `github app installation missing required permissions: missing=[...]` | One or more of the four §2 permissions is missing |
| `github app mode requires secret_key (...)` | App mode requires `secret_key` |
| `github api unavailable during preflight (after 3 retries): ...` | GitHub 5xx — infrastructure, not config; restart |

## 8. Agent timeout boundary

The installation token TTL is 60 minutes; agentdock re-mints when 50 minutes remain. **A single agent run lasting more than 50 minutes** can still hit the boundary mid-fetch (401).

**Recommendation: `queue.job_timeout ≤ 50min`.**

If `queue.job_timeout > 50min`, preflight logs a warning but does not block startup. When a long job fails mid-run, this is the first place to look.

## Advanced: rotate / revoke

- **Rotate private key**: generate a new PEM in App settings → overwrite the file at `private_key_path` → restart the app.
- **Revoke the App**: org/personal Settings → Installed GitHub Apps → Configure → Uninstall. agentdock's next mint will 401; preflight reports installation not found.
