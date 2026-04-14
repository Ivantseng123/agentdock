# NPX Dynamic Skill Loading

## Problem

Skills are currently baked into the Docker image at build time. When skills are provided by external contributors, there's no way to ensure the latest version is used at runtime without rebuilding and redeploying. We need a mechanism to dynamically fetch skills from npm registry while maintaining fallback safety.

## Design Decisions

| Decision | Choice | Rationale |
|----------|--------|-----------|
| Fetch timing | App-side, at job submit | Worker stays stateless |
| Package convention | `{pkg}/skills/{name}/SKILL.md` | Supports multi-skill packages |
| Config source | Separate `skills.yaml` via k8s ConfigMap | Admin-managed, hot-reloadable |
| Caching | TTL cache + singleflight | Balance freshness vs. performance |
| Fallback | npx cache → baked-in → skip | Two-layer, graceful degradation |
| Multi-file skills | Carry entire directory tree | Support examples, references, etc. |
| Validation timing | At fetch time, not job submit | Avoid repeated log spam + wasted npx calls |
| Backward compat | Sync deploy, no transition period | No existing workers to support |

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
    command: "npx @someone/skill-code-review@latest"

  security-audit:
    type: npx
    command: "npx @team/security-skills@^2.0.0"
    timeout: 60s  # default 30s

cache:
  dir: "/tmp/agentdock/skill-cache"
  ttl: 5m
```

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
        command: "npx @someone/skill-code-review@latest"
    cache:
      dir: "/tmp/agentdock/skill-cache"
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

### Go structs

```go
type SkillsFileConfig struct {
    Skills map[string]SkillConfig `yaml:"skills"`
    Cache  SkillCacheConfig       `yaml:"cache"`
}

type SkillConfig struct {
    Type    string        `yaml:"type"`    // "local" | "npx"
    Path    string        `yaml:"path"`    // local: disk path
    Command string        `yaml:"command"` // npx: full npx command
    Timeout time.Duration `yaml:"timeout"` // npx: execution timeout (default 30s)
}

type SkillCacheConfig struct {
    Dir string        `yaml:"dir"`
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
    cache    map[string]*cacheEntry
    bakedIn  map[string]*SkillFiles
    group    singleflight.Group
    watcher  *fsnotify.Watcher
}

type cacheEntry struct {
    status    cacheStatus  // ok | failed | invalid
    files     *SkillFiles  // populated when status=ok
    reason    string       // populated when status=failed|invalid
    fetchedAt time.Time
}

type cacheStatus int
const (
    cacheOK      cacheStatus = iota
    cacheFailed              // npx execution failed
    cacheInvalid             // validation failed
)
```

### LoadAll flow (called at job submit time)

```
for each skill in config:
  if type == local:
    -> return bakedIn (loaded and validated at startup)

  if type == npx:
    -> cache exists and not expired?
      status=ok      -> use cache
      status=failed  -> skip, no retry, no log
      status=invalid -> skip, no retry, no log
    -> cache miss or expired? -> singleflight execute npx
      -> npx success + validation pass  -> cache(ok)
      -> npx success + validation fail  -> cache(invalid), log warning once
      -> npx failure -> cache(failed), log warning once
        -> fallback: previous ok cache -> bakedIn -> skip
```

### NPX execution

```go
func (l *Loader) fetchNpx(ctx context.Context, name string, cfg SkillConfig) (*SkillFiles, error) {
    tmpDir, _ := os.MkdirTemp("", "agentdock-skill-*")
    defer os.RemoveAll(tmpDir)

    cmd := exec.CommandContext(ctx, "sh", "-c", cfg.Command)
    cmd.Dir = tmpDir
    cmd.Env = append(os.Environ(), "NPM_CONFIG_PREFIX="+tmpDir)
    cmd.Run()

    // scan node_modules/{package}/skills/*/
    // each subdirectory with SKILL.md = one skill
}
```

- Executed in isolated temp dir to avoid polluting global node_modules
- Package name parsed from command: strip `npx ` prefix, strip version suffix (`@latest`, `@^2.0.0`), handle scoped packages (`@scope/name`)
  - `npx @someone/skill-code-review@latest` -> `@someone/skill-code-review`
  - `npx skill-simple@^1.0.0` -> `skill-simple`
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
```

### Validation (at fetch time, not job submit)

Applied immediately after npx fetch succeeds, before writing to cache:

- **Size limit**: single skill directory total < 1MB
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

Serialized example:
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
    - new skill added     -> add to config, next LoadAll will fetch
    - skill removed       -> remove from config, clear cache
    - skill command changed -> clear that skill's cache, force re-fetch
  -> reload local skills from disk
  -> RWMutex swap config
  -> on parse failure -> keep old config, log error
```

## Data flow

```
APP STARTUP
  1. Read config.yaml -> get skills_config path
  2. Read skills.yaml -> create SkillLoader
  3. Load local skills -> bakedIn map (validated once)
  4. Start fsnotify watcher -> watch skills.yaml
                              |
SLACK TRIGGER -> Workflow submit job
  1. Call loader.LoadAll()
  2. For each skill:
     local -> return bakedIn
     npx   -> cache valid? use it
              cache miss  -> singleflight npx -> validate -> cache
              failed      -> fallback chain -> skip
  3. Assemble Job.Skills (map[string]*SkillPayload)
  4. Submit to queue
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
| `internal/skill/loader.go` | New | SkillLoader core: LoadAll, cache, fallback |
| `internal/skill/npx.go` | New | npx execution + node_modules reading |
| `internal/skill/validate.go` | New | File validation (size, whitelist, path safety) |
| `internal/skill/watcher.go` | New | fsnotify hot reload with debounce |
| `internal/config/config.go` | Modify | Add SkillConfig, SkillCacheConfig, skills_config path |
| `internal/queue/job.go` | Modify | Skills field -> map[string]*SkillPayload |
| `internal/worker/executor.go` | Modify | mountSkills supports multi-file SkillPayload |
| `internal/bot/workflow.go` | Modify | Use loader.LoadAll() instead of direct map |
| `cmd/bot/main.go` | Modify | Initialize SkillLoader, pass to workflow |
