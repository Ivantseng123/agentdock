# NPX Dynamic Skill Loading

## Problem

Skills are currently baked into the Docker image at build time. When skills are provided by external contributors, there's no way to ensure the latest version is used at runtime without rebuilding and redeploying. We need a mechanism to dynamically fetch skills from npm registry while maintaining fallback safety.

## Design Decisions

| Decision | Choice | Rationale |
|----------|--------|-----------|
| Fetch timing | App-side, at job submit | Worker stays stateless |
| Package convention | `{pkg}/skills/{name}/SKILL.md` | Supports multi-skill packages |
| Config source | Separate `skills.yaml` via k8s ConfigMap | Admin-managed, hot-reloadable |
| Caching | In-memory TTL cache + singleflight | Balance freshness vs. performance |
| Fallback | npx cache → baked-in → skip | Two-layer, graceful degradation |
| Multi-file skills | Carry entire directory tree | Support examples, references, etc. |
| Validation timing | At fetch time, not job submit | Avoid repeated log spam + wasted npx calls |
| Backward compat | Sync deploy, no transition period | No existing workers to support |
| npx execution | `exec.Command("npx", ...)`, no `sh -c` | Prevent arbitrary shell injection |
| Cache key | By package, not by skill name | One fetch per package, multiple skills share entry |
| Skill naming | Package directory names, not config keys | Skill author controls naming |
| Startup warmup | Prefetch all npx skills at init | First job has no fetch delay |

## Config Format

### Main config (`config.yaml`)

```yaml
skills_config: "/etc/agentdock/skills.yaml"
```

Or via CLI flag: `-skills-config /etc/agentdock/skills.yaml`

Falls back to existing `agents/skills/` directory scan if path not set or file missing.

### Skills config (`skills.yaml`, mounted via k8s ConfigMap)

```yaml
skills:
  # Local baked-in skill
  triage-issue:
    type: local
    path: agents/skills/triage-issue

  # NPX dynamic skills
  code-review:
    type: npx
    package: "@someone/skill-code-review"
    version: "latest"

  security-audit:
    type: npx
    package: "@team/security-skills"
    version: "^2.0.0"
    timeout: 60s  # default 30s

cache:
  ttl: 5m
```

Config keys (e.g. `code-review`) are identifiers for grouping and configuration only.
Actual skill names are determined by directory names inside the package's `skills/` folder.
Multiple config entries pointing to the same package only trigger one npx fetch.

### K8s ConfigMap

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: skills-config
data:
  skills.yaml: |
    skills:
      triage-issue:
        type: local
        path: agents/skills/triage-issue
      code-review:
        type: npx
        package: "@someone/skill-code-review"
        version: "latest"
    cache:
      ttl: 5m
```

```yaml
volumes:
  - name: skills-config
    configMap:
      name: skills-config
containers:
  - volumeMounts:
      - name: skills-config
        mountPath: /etc/agentdock/skills.yaml
        subPath: skills.yaml
```

### Private npm registries

Private registries (GitHub Packages, Artifactory, Verdaccio, etc.) require `.npmrc` configuration with registry URL and auth token. This is a deployment concern — mount `.npmrc` to `/home/node/.npmrc` via k8s Secret. SkillLoader does not handle registry authentication.

### Go structs

```go
type SkillsFileConfig struct {
    Skills map[string]SkillConfig `yaml:"skills"`
    Cache  SkillCacheConfig       `yaml:"cache"`
}

type SkillConfig struct {
    Type    string        `yaml:"type"`    // "local" | "npx"
    Path    string        `yaml:"path"`    // local: disk path
    Package string        `yaml:"package"` // npx: npm package name (e.g. "@someone/skill-code-review")
    Version string        `yaml:"version"` // npx: version spec (default "latest")
    Timeout time.Duration `yaml:"timeout"` // npx: execution timeout (default 30s)
}

type SkillCacheConfig struct {
    TTL time.Duration `yaml:"ttl"`
}
```

## Architecture

### New component: SkillLoader (`internal/skill/`)

Central component responsible for all skill loading, caching, validation, and hot reload.

#### Data structures

```go
type SkillFiles struct {
    Name  string
    Files map[string][]byte // relative path -> content
}

type Loader struct {
    mu       sync.RWMutex
    config   SkillsFileConfig
    cache    map[string]*cacheEntry   // keyed by package name, not skill name
    bakedIn  map[string]*SkillFiles   // keyed by skill name
    group    singleflight.Group
    watcher  *fsnotify.Watcher
}

type cacheEntry struct {
    status    cacheStatus     // ok | failed | invalid
    skills    []*SkillFiles   // populated when status=ok (one package -> N skills)
    reason    string          // populated when status=failed|invalid
    fetchedAt time.Time
}

type cacheStatus int
const (
    cacheOK      cacheStatus = iota
    cacheFailed              // npx execution failed
    cacheInvalid             // validation failed
)
```

### Startup warmup

On `NewLoader()`, after loading config and baked-in skills, prefetch all `type: npx` packages:

```
for each unique package in config:
  singleflight execute npx -> validate -> cache
  success -> log info
  failure -> log warning, will fallback to baked-in at job time
```

This ensures the first job has no fetch delay and surfaces broken npx skills at startup.

### LoadAll flow (called at job submit time)

```
RLock -> snapshot config -> RUnlock
(npx fetch happens outside lock to avoid blocking reload)

for each skill in config:
  if type == local:
    -> return bakedIn (loaded and validated at startup)

  if type == npx:
    -> cache entry for this package exists and not expired?
      status=ok      -> use cached skills
      status=failed  -> skip all skills from this package, no retry, no log
      status=invalid -> skip all skills from this package, no retry, no log
    -> cache miss or expired? -> singleflight execute npx
      -> npx success + validation pass  -> cache(ok)
      -> npx success + validation fail  -> cache(invalid), log warning once
      -> npx failure -> cache(failed), log warning once
        -> fallback: previous ok cache -> bakedIn -> skip
```

### NPX execution

```go
func (l *Loader) fetchNpx(ctx context.Context, pkg, version string) ([]*SkillFiles, error) {
    tmpDir, _ := os.MkdirTemp("", "agentdock-skill-*")
    defer os.RemoveAll(tmpDir)

    // No sh -c — only npx allowed
    arg := pkg + "@" + version
    cmd := exec.CommandContext(ctx, "npx", arg)
    cmd.Dir = tmpDir
    cmd.Env = append(os.Environ(), "NPM_CONFIG_PREFIX="+tmpDir)
    cmd.Run()

    // scan node_modules/{package}/skills/*/
    // each subdirectory with SKILL.md = one skill
}
```

- Executed in isolated temp dir to avoid polluting global node_modules
- Only `npx` is executed — no `sh -c`, preventing arbitrary shell injection
- Package and version are separate config fields, no parsing needed
- Timeout controlled by context (default 30s, configurable per skill)

### NPM package convention

```
node_modules/@someone/skill-code-review/
  package.json
  skills/
    code-review/
      SKILL.md           # required, skip directory if missing
      examples/
        example1.md
      references/
        api-spec.yaml
    another-skill/
      SKILL.md
```

One package can contain multiple skills. Each subdirectory under `skills/` with a `SKILL.md` is treated as a skill. The directory name is the skill name.

### Validation (at fetch time, not job submit)

Applied immediately after npx fetch succeeds, before writing to cache:

- **Size limit**: single skill directory total < 1MB
- **Job total limit**: all skills combined < 5MB per job (checked at LoadAll)
- **File type whitelist**: `.md`, `.txt`, `.yaml`, `.yml`, `.json`, `.example`, `.tmpl`
- **Path safety**: reject `..`, symlinks, absolute paths (prevent path traversal)

Validation failure -> `cacheInvalid` entry with TTL -> no retry until expired.

### Job struct change

```go
type Job struct {
    // ... existing fields unchanged ...
    Skills map[string]*SkillPayload `json:"skills"`
}

type SkillPayload struct {
    Files map[string][]byte `json:"files"` // relative path -> content
}
```

Serialized example (JSON, `[]byte` fields auto base64-encoded):
```json
{
  "code-review": {
    "files": {
      "SKILL.md": "<base64>",
      "examples/example1.md": "<base64>"
    }
  }
}
```

### Worker mount change (`executor.go`)

Updated to restore full directory tree instead of single SKILL.md:

```go
func mountSkills(repoPath string, skills map[string]*SkillPayload, skillDir string) error {
    for name, payload := range skills {
        for relPath, content := range payload.Files {
            // validate relPath has no path traversal
            fullPath := filepath.Join(repoPath, skillDir, name, relPath)
            os.MkdirAll(filepath.Dir(fullPath), 0755)
            os.WriteFile(fullPath, content, 0644)
        }
    }
    return nil
}
```

### fsnotify hot reload (`watcher.go`)

```
watch directory containing skills.yaml (not the file itself)
  -> k8s ConfigMap updates via symlink swap, file-level watch misses events

on CREATE/WRITE event for skills.yaml:
  -> debounce 500ms
  -> read new skills.yaml
  -> diff against current config:
    - new skill added       -> add to config, next LoadAll will fetch
    - skill removed         -> remove from config, clear cache for its package
    - skill package changed -> clear that package's cache, force re-fetch
  -> reload local skills from disk
  -> RWMutex swap config
  -> on parse failure -> keep old config, log error
```

## Observability

Structured logging per LoadAll call and per skill:

```go
// Per-skill log (emitted once per fetch, not per job)
slog.Warn("skill.fetch_failed",
    "package", pkg,
    "error", err,
    "fallback", "cache|baked-in|skipped",
)

// Per-job summary (emitted every LoadAll call)
slog.Info("skill.loaded",
    "skill", name,
    "source", "npx|cache|baked-in|skipped",
    "package", pkg,
    "duration_ms", elapsed,
)
```

- **source=npx**: fresh fetch this call (cache was expired)
- **source=cache**: served from in-memory cache (cache was valid)
- **source=baked-in**: npx failed, fell back to baked-in version
- **source=skipped**: no version available, skill omitted from job

Per-job summary gives visibility into which skills are active and where they came from without log spam.

## Data flow

```
APP STARTUP
  1. Read config.yaml -> get skills_config path
  2. Read skills.yaml -> create SkillLoader
  3. Load local skills -> bakedIn map (validated once)
  4. Warmup: prefetch all npx packages -> populate cache
  5. Start fsnotify watcher -> watch skills.yaml
                              |
SLACK TRIGGER -> Workflow submit job
  1. Call loader.LoadAll()
  2. RLock -> snapshot config -> RUnlock
  3. For each skill (outside lock):
     local -> return bakedIn
     npx   -> cache valid? use it
              cache miss  -> singleflight npx -> validate -> cache
              failed      -> fallback chain -> skip
  4. Check job total size < 5MB
  5. Assemble Job.Skills (map[string]*SkillPayload)
  6. Log per-skill summary
  7. Submit to queue
                              |
WORKER EXECUTION
  1. Clone repo
  2. Mount skills: iterate SkillPayload.Files, restore directory tree
  3. Spawn CLI agent -> agent discovers skills
  4. Cleanup skill directories
                              |
CONFIG CHANGE (k8s ConfigMap update)
  1. fsnotify detects symlink swap
  2. Debounce 500ms
  3. Read new skills.yaml
  4. Diff -> clear/update cache as needed
  5. RWMutex swap config
  6. Failure -> keep old config, log error
```

## Files to change

| File | Action | Description |
|------|--------|-------------|
| `internal/skill/loader.go` | New | SkillLoader core: LoadAll, cache, fallback, warmup |
| `internal/skill/npx.go` | New | npx execution + node_modules reading |
| `internal/skill/validate.go` | New | File validation (size, whitelist, path safety) |
| `internal/skill/watcher.go` | New | fsnotify hot reload with debounce |
| `internal/config/config.go` | Modify | Add SkillConfig, SkillCacheConfig, skills_config path |
| `internal/queue/job.go` | Modify | Skills field -> map[string]*SkillPayload |
| `internal/worker/executor.go` | Modify | mountSkills supports multi-file SkillPayload |
| `internal/bot/workflow.go` | Modify | Use loader.LoadAll() instead of direct map |
| `cmd/bot/main.go` | Modify | Initialize SkillLoader, pass to workflow |
