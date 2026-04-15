# Attachment Transfer via Redis & Worker Cleanup

## Summary

Two problems, one spec:

1. **Attachment transfer**: App downloads Slack attachments locally, but Worker runs on a different machine (colleague's laptop or k8s pod). Files need to travel App → Redis → Worker.
2. **Worker cleanup**: RepoCache is designed as a persistent cache. Workers on ephemeral machines (colleague laptops, pods) need aggressive cleanup — after each job and on shutdown/crash.

## Motivation

### Attachment transfer
Current state: App downloads files to `/tmp/triage-meta-*`, writes local paths into the prompt, stores only metadata (filename + empty URL) in Redis. Worker calls `Resolve()`, gets metadata back, but has no way to access the actual files. The "copy attachments" loop in `executor.go` is a no-op.

### Worker cleanup
`RepoCache` clones repos and never deletes them. On a colleague's laptop running as a worker, this means repos accumulate forever. There's no cleanup on job completion, no cleanup on shutdown, and no cleanup on next startup after a crash.

## Scope

### In scope
- Store compressed file bytes in Redis alongside metadata
- Worker downloads bytes from Redis, writes to local temp dir
- Worker appends attachment section to prompt (instead of App)
- Per-file size limit (10 MB) and per-job limit (30 MB)
- Repo cleanup after job completion
- Repo cleanup on graceful shutdown (SIGTERM/SIGINT)
- Stale repo purge on worker startup (crash recovery)

### Out of scope
- S3/MinIO or other object storage (only Redis available)
- Inline file content in prompt (files given as local paths, agent reads them)
- File type processing changes (xlsx parsing, vision — covered by existing `2026-04-09-attachment-support-design.md`)
- Config YAML for size limits (hardcoded initially)

## Architecture

### Attachment data flow

```
BEFORE (broken):
App: Slack → download → /tmp/ → prompt has /tmp paths → Redis {filename, ""}
Worker: Resolve() → {filename, ""} → no-op loop → agent sees dead paths

AFTER:
App: Slack → download → gzip each file → Redis {filename, mimetype, compressed bytes}
Worker: Resolve() → gunzip → write to local temp dir → append paths to prompt
```

### Worker lifecycle

```
Startup:  PurgeStale() → wipe leftover cache dir from previous crash
Job done: Remove(repoRef) → delete this job's repo clone + temp attachments
Shutdown: CleanAll() → wipe entire cache dir (SIGTERM/SIGINT handler)
Crash:    next startup's PurgeStale() catches it
```

## Data Model Changes

### `queue/job.go`

`AttachmentReady` gains two fields:

```go
type AttachmentReady struct {
    Filename string `json:"filename"`
    URL      string `json:"url"`       // kept for backward compat, unused
    Data     []byte `json:"data"`      // gzip compressed file bytes
    MimeType string `json:"mime_type"` // "image", "text", or "document"
}
```

New payload type for `Prepare`:

```go
type AttachmentPayload struct {
    Filename string
    MimeType string
    Data     []byte // raw file bytes (pre-compression)
    Size     int64  // original size for limit checking
}
```

### `queue/interface.go`

`AttachmentStore.Prepare` signature changes:

```go
type AttachmentStore interface {
    Prepare(ctx context.Context, jobID string, payloads []AttachmentPayload) error
    Resolve(ctx context.Context, jobID string) ([]AttachmentReady, error)
    Cleanup(ctx context.Context, jobID string) error
}
```

### `worker/executor.go`

`RepoProvider` gains cleanup methods:

```go
type RepoProvider interface {
    Prepare(cloneURL, branch string) (string, error)
    Remove(repoRef string) error // delete single repo clone
    CleanAll() error             // delete entire cache dir
    PurgeStale() error           // startup: wipe leftover state
}
```

## Component Design

### Redis attachment store (`queue/redis_attachments.go`)

**Prepare**: receives `[]AttachmentPayload`, gzip-compresses each `Data` field, marshals to JSON (bytes become base64 in JSON encoding), stores with 30-min TTL. Enforces limits before storing:
- Single file > 10 MB (pre-compression): skip, log warning
- Total job > 30 MB (pre-compression): skip remaining files, log warning

**Resolve**: unchanged polling pattern. Returns `[]AttachmentReady` now containing `Data` (compressed bytes) and `MimeType`.

**Cleanup**: unchanged (`DEL` key).

### In-memory attachment store (`queue/inmem_attachments.go`)

Mirror the same changes for local dev/testing. Channel carries `[]AttachmentReady` with `Data`.

### App side (`bot/workflow.go`)

Changes to `runTriage`:

1. `DownloadAttachments` — unchanged, downloads to temp dir
2. **New**: Read each downloaded file into `[]byte`, build `[]AttachmentPayload`
3. `BuildPrompt` — **remove attachment section** (worker handles this now)
4. `Prepare(ctx, jobID, payloads)` — now sends actual file bytes

`defer os.RemoveAll(tempDir)` stays — app cleans its own temp dir after pushing to Redis.

### Prompt (`bot/prompt.go`)

Remove the attachment path section (lines 58-74). Add a new exported function for worker use:

```go
func AppendAttachmentSection(prompt string, attachments []AttachmentInfo) string
```

This generates the same format as before:
```
## Attachments
- /tmp/triage-attach-xxx/screenshot.png (image — use your file reading tools to view)
- /tmp/triage-attach-xxx/error.log (text — read directly)
```

### Worker side (`worker/executor.go`)

Replace the no-op attachment loop with:

1. Create temp dir: `/tmp/triage-attach-<jobID>/`
2. For each `AttachmentReady`: gunzip `Data` → write to `<temp_dir>/<filename>`
3. Build `[]AttachmentInfo` from written files
4. Call `AppendAttachmentSection(job.Prompt, attachInfos)` to get final prompt
5. `defer os.RemoveAll(tempDir)` for the attachment temp dir

### Worker pool (`worker/pool.go`)

**Job completion** — in `executeWithTracking`, after publishing result:

```go
// Clean up repo clone for this job.
// Use job.CloneURL — same key passed to Prepare/EnsureRepo/dirName.
if err := p.cfg.RepoCache.Remove(job.CloneURL); err != nil {
    logger.Warn("repo cleanup failed", "error", err)
}
```

This replaces the current post-kill cleanup that only runs `git checkout .` / `git clean -fd` on failures. Now ALL jobs (success and failure) get full repo removal.

**Shutdown** — in `workerHeartbeat`'s `ctx.Done()` branch, after unregistering workers:

```go
if err := p.cfg.RepoCache.CleanAll(); err != nil {
    slog.Warn("shutdown repo cleanup failed", "error", err)
}
```

### RepoCache (`github/repo.go`)

Three new methods:

```go
// Remove deletes a single repo's clone directory and clears its cache entry.
func (rc *RepoCache) Remove(repoRef string) error {
    rc.mu.Lock()
    defer rc.mu.Unlock()
    delete(rc.lastPull, repoRef)
    return os.RemoveAll(filepath.Join(rc.dir, rc.dirName(repoRef)))
}

// CleanAll removes the entire cache directory.
func (rc *RepoCache) CleanAll() error {
    rc.mu.Lock()
    defer rc.mu.Unlock()
    rc.lastPull = make(map[string]time.Time)
    return os.RemoveAll(rc.dir)
}

// PurgeStale wipes and recreates the cache directory.
// Call on startup to recover from previous unclean shutdown.
func (rc *RepoCache) PurgeStale() error {
    rc.mu.Lock()
    defer rc.mu.Unlock()
    rc.lastPull = make(map[string]time.Time)
    os.RemoveAll(rc.dir)
    return os.MkdirAll(rc.dir, 0755)
}
```

### Worker startup (`cmd/bot/main.go` or worker init)

Call `RepoCache.PurgeStale()` before `Pool.Start()` in worker mode.

## Safety Limits

| Limit | Value | Rationale |
|-------|-------|-----------|
| Per-file size | 10 MB | Slack typical max; keeps Redis reasonable |
| Per-job total | 30 MB | ~3 files at max; compressed ~10-15 MB in Redis |
| Redis TTL | 30 min | Existing value; sufficient for job lifecycle |
| Gzip level | `gzip.DefaultCompression` | Good balance of speed vs size |

Files exceeding limits are silently skipped with a log warning. The job proceeds with whatever files fit — partial attachment is better than no job.

## Cleanup Coverage Matrix

| Scenario | Mechanism | What gets cleaned |
|----------|-----------|-------------------|
| Job completes (success) | `Remove(repoRef)` in `executeWithTracking` | Repo clone dir |
| Job completes (failure) | Same as success (replaces current `git checkout .` hack) | Repo clone dir |
| Attachment temp files | `defer os.RemoveAll(tempDir)` in `executeJob` | Worker-side attachment dir |
| Redis attachment data | `attachments.Cleanup()` in `ResultListener` (existing) | Redis key |
| Graceful shutdown | `CleanAll()` in `workerHeartbeat` ctx.Done | Entire cache dir |
| SIGKILL / OOM / crash | `PurgeStale()` on next startup | Entire cache dir (recreated empty) |
| App-side temp files | `defer os.RemoveAll(tempDir)` in `runTriage` (existing) | App's download dir |

## Files Changed

| File | Change |
|------|--------|
| `queue/job.go` | Add `Data`, `MimeType` to `AttachmentReady`; add `AttachmentPayload` struct |
| `queue/interface.go` | Change `Prepare` signature to accept `[]AttachmentPayload` |
| `queue/redis_attachments.go` | `Prepare`: gzip + store bytes; `Resolve`: return bytes; add size limit checks |
| `queue/inmem_attachments.go` | Mirror changes for dev/test |
| `bot/workflow.go` | Read file bytes, build payloads, pass to `Prepare`; remove attachment paths from prompt building |
| `bot/prompt.go` | Remove attachment section from `BuildPrompt`; add `AppendAttachmentSection` for worker use |
| `worker/executor.go` | Replace no-op loop: gunzip, write files, append to prompt; add `RepoProvider.Remove`/`CleanAll`/`PurgeStale` to interface |
| `worker/pool.go` | Add `Remove` after job completion; add `CleanAll` on shutdown |
| `github/repo.go` | Add `Remove`, `CleanAll`, `PurgeStale` methods |
| `cmd/agentdock/adapters.go` | Add `Remove`, `CleanAll`, `PurgeStale` to `repoCacheAdapter` (delegates to `RepoCache`) |
| `cmd/bot/main.go` | Call `PurgeStale()` on worker startup |

## Testing

- Unit: `redis_attachments_test.go` — Prepare with bytes, Resolve returns bytes, size limit enforcement
- Unit: `inmem_attachments_test.go` — same for in-memory store
- Unit: `prompt_test.go` — `AppendAttachmentSection` output format
- Unit: `repo_test.go` — `Remove`, `CleanAll`, `PurgeStale` filesystem behavior
- Integration: full flow — Prepare with payloads on app side, Resolve + write on worker side, verify file content matches
- Edge: file exceeds 10 MB limit — skipped with warning, job continues
- Edge: job exceeds 30 MB total — partial files stored, job continues
