# NPX Dynamic Skill Loading Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Enable runtime dynamic skill loading from npm registry via npx, with TTL cache, singleflight, two-layer fallback, file validation, and hot reload via k8s ConfigMap.

**Architecture:** New `internal/skill/` package owns all skill loading. Config split: main `config.yaml` points to a separate `skills.yaml` (mounted via ConfigMap). App fetches npx skills at job submit time with in-memory cache. Worker mount updated to restore full directory trees (not just SKILL.md). fsnotify watches `skills.yaml` for hot reload.

**Tech Stack:** Go stdlib (`os/exec`, `sync`, `log/slog`), `golang.org/x/sync/singleflight`, `github.com/fsnotify/fsnotify`, `gopkg.in/yaml.v3`

**Spec:** `docs/superpowers/specs/2026-04-14-npx-dynamic-skill-loading-design.md`

---

### Task 1: Add dependencies

**Files:**
- Modify: `go.mod`

- [ ] **Step 1: Add fsnotify and singleflight**

```bash
go get github.com/fsnotify/fsnotify@latest
go get golang.org/x/sync@latest
```

- [ ] **Step 2: Verify go.mod updated**

```bash
grep fsnotify go.mod
grep golang.org/x/sync go.mod
```

Expected: both lines present.

- [ ] **Step 3: Commit**

```bash
git add go.mod go.sum
git commit -m "chore: add fsnotify and x/sync dependencies"
```

---

### Task 2: SkillPayload type + Job struct change

**Files:**
- Modify: `internal/queue/job.go:18-37`
- Modify: `internal/bot/retry_handler_test.go:31`
- Modify: `internal/worker/executor.go:106-132`

- [ ] **Step 1: Write test for SkillPayload JSON serialization**

Create `internal/queue/job_test.go`:

```go
package queue

import (
	"encoding/json"
	"testing"
)

func TestSkillPayload_JSONRoundTrip(t *testing.T) {
	original := map[string]*SkillPayload{
		"code-review": {
			Files: map[string][]byte{
				"SKILL.md":          []byte("# Code Review Skill"),
				"examples/ex1.md":   []byte("example content"),
			},
		},
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded map[string]*SkillPayload
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	sp, ok := decoded["code-review"]
	if !ok {
		t.Fatal("missing code-review key")
	}
	if string(sp.Files["SKILL.md"]) != "# Code Review Skill" {
		t.Errorf("SKILL.md = %q", string(sp.Files["SKILL.md"]))
	}
	if string(sp.Files["examples/ex1.md"]) != "example content" {
		t.Errorf("examples/ex1.md = %q", string(sp.Files["examples/ex1.md"]))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd /Users/ivantseng/local_file/slack-issue-bot && go test ./internal/queue/ -run TestSkillPayload -v
```

Expected: FAIL — `SkillPayload` not defined.

- [ ] **Step 3: Add SkillPayload and update Job.Skills type**

In `internal/queue/job.go`, add the `SkillPayload` type and change `Job.Skills`:

```go
type SkillPayload struct {
	Files map[string][]byte `json:"files"`
}
```

Change line 29 from:
```go
Skills      map[string]string `json:"skills"`
```
to:
```go
Skills      map[string]*SkillPayload `json:"skills"`
```

- [ ] **Step 4: Run the new test to verify it passes**

```bash
go test ./internal/queue/ -run TestSkillPayload -v
```

Expected: PASS

- [ ] **Step 5: Fix retry_handler_test.go**

In `internal/bot/retry_handler_test.go`, line 31, change:
```go
Skills:    map[string]string{"s1": "content"},
```
to:
```go
Skills: map[string]*queue.SkillPayload{
    "s1": {Files: map[string][]byte{"SKILL.md": []byte("content")}},
},
```

- [ ] **Step 6: Update mountSkills and cleanupSkills in executor.go**

In `internal/worker/executor.go`, change `mountSkills`:

```go
func mountSkills(repoPath string, skills map[string]*queue.SkillPayload, skillDir string) error {
	if skillDir == "" {
		return nil
	}
	for name, payload := range skills {
		for relPath, content := range payload.Files {
			// Reject path traversal.
			if strings.Contains(relPath, "..") || filepath.IsAbs(relPath) {
				return fmt.Errorf("invalid skill file path: %s", relPath)
			}
			fullPath := filepath.Join(repoPath, skillDir, name, relPath)
			if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
				return err
			}
			if err := os.WriteFile(fullPath, content, 0644); err != nil {
				return err
			}
		}
	}
	return nil
}

func cleanupSkills(repoPath string, skills map[string]*queue.SkillPayload, skillDir string) {
	if skillDir == "" {
		return
	}
	dir := filepath.Join(repoPath, skillDir)
	for name := range skills {
		os.RemoveAll(filepath.Join(dir, name))
	}
	os.Remove(dir) // only succeeds if empty (safe)
}
```

Add `"strings"` to the imports in `executor.go` if not already present.

- [ ] **Step 7: Run all tests**

```bash
go test ./internal/... -count=1
```

Expected: all pass. No other code references `Job.Skills` in a type-incompatible way.

- [ ] **Step 8: Commit**

```bash
git add internal/queue/job.go internal/queue/job_test.go internal/bot/retry_handler_test.go internal/worker/executor.go
git commit -m "feat: change Job.Skills to SkillPayload with multi-file support"
```

---

### Task 3: Skill validation functions

**Files:**
- Create: `internal/skill/validate.go`
- Create: `internal/skill/validate_test.go`

- [ ] **Step 1: Write failing tests for validation**

Create `internal/skill/validate_test.go`:

```go
package skill

import (
	"strings"
	"testing"

	"agentdock/internal/queue"
)

func TestValidateSkillFiles_Valid(t *testing.T) {
	files := map[string][]byte{
		"SKILL.md":              []byte("# My Skill"),
		"examples/example1.md":  []byte("example"),
		"references/spec.yaml":  []byte("key: value"),
		"config.json":           []byte("{}"),
		"template.tmpl":         []byte("{{.Name}}"),
		"notes.txt":             []byte("notes"),
		"data.yml":              []byte("a: 1"),
		"sample.example":        []byte("sample"),
	}
	if err := ValidateSkillFiles(files); err != nil {
		t.Errorf("expected valid, got: %v", err)
	}
}

func TestValidateSkillFiles_BadExtension(t *testing.T) {
	files := map[string][]byte{
		"SKILL.md":  []byte("# My Skill"),
		"hack.sh":   []byte("#!/bin/bash\nrm -rf /"),
	}
	err := ValidateSkillFiles(files)
	if err == nil {
		t.Fatal("expected error for .sh file")
	}
	if !strings.Contains(err.Error(), "hack.sh") {
		t.Errorf("error should mention filename: %v", err)
	}
}

func TestValidateSkillFiles_PathTraversal(t *testing.T) {
	files := map[string][]byte{
		"SKILL.md":        []byte("ok"),
		"../etc/passwd":   []byte("bad"),
	}
	err := ValidateSkillFiles(files)
	if err == nil {
		t.Fatal("expected error for path traversal")
	}
}

func TestValidateSkillFiles_AbsolutePath(t *testing.T) {
	files := map[string][]byte{
		"/etc/shadow": []byte("bad"),
	}
	err := ValidateSkillFiles(files)
	if err == nil {
		t.Fatal("expected error for absolute path")
	}
}

func TestValidateSkillFiles_TooLarge(t *testing.T) {
	files := map[string][]byte{
		"SKILL.md": make([]byte, 1*1024*1024+1), // 1MB + 1 byte
	}
	err := ValidateSkillFiles(files)
	if err == nil {
		t.Fatal("expected error for oversized skill")
	}
}

func TestValidateJobSize_Under5MB(t *testing.T) {
	skills := map[string]*queue.SkillPayload{
		"a": {Files: map[string][]byte{"SKILL.md": make([]byte, 1*1024*1024)}},
		"b": {Files: map[string][]byte{"SKILL.md": make([]byte, 1*1024*1024)}},
	}
	if err := ValidateJobSize(skills); err != nil {
		t.Errorf("expected valid, got: %v", err)
	}
}

func TestValidateJobSize_Over5MB(t *testing.T) {
	skills := map[string]*queue.SkillPayload{
		"a": {Files: map[string][]byte{"SKILL.md": make([]byte, 3*1024*1024)}},
		"b": {Files: map[string][]byte{"SKILL.md": make([]byte, 3*1024*1024)}},
	}
	err := ValidateJobSize(skills)
	if err == nil {
		t.Fatal("expected error for oversized job")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./internal/skill/ -v
```

Expected: FAIL — package/functions not defined.

- [ ] **Step 3: Implement validation functions**

Create `internal/skill/validate.go`:

```go
package skill

import (
	"fmt"
	"path/filepath"
	"strings"

	"agentdock/internal/queue"
)

const (
	maxSkillDirSize = 1 * 1024 * 1024  // 1MB per skill directory
	maxJobSize      = 5 * 1024 * 1024  // 5MB total per job
)

var allowedExtensions = map[string]bool{
	".md":      true,
	".txt":     true,
	".yaml":    true,
	".yml":     true,
	".json":    true,
	".example": true,
	".tmpl":    true,
}

// ValidateSkillFiles checks a single skill's files for size, allowed extensions, and path safety.
func ValidateSkillFiles(files map[string][]byte) error {
	var totalSize int
	for relPath, content := range files {
		// Path safety: reject traversal and absolute paths.
		if strings.Contains(relPath, "..") || filepath.IsAbs(relPath) {
			return fmt.Errorf("invalid skill file path: %s", relPath)
		}

		// Extension whitelist.
		ext := filepath.Ext(relPath)
		if ext == "" || !allowedExtensions[ext] {
			return fmt.Errorf("disallowed file type %q: %s", ext, relPath)
		}

		totalSize += len(content)
	}

	if totalSize > maxSkillDirSize {
		return fmt.Errorf("skill directory too large: %d bytes (max %d)", totalSize, maxSkillDirSize)
	}
	return nil
}

// ValidateJobSize checks that total skill payload for a job does not exceed the limit.
func ValidateJobSize(skills map[string]*queue.SkillPayload) error {
	var total int
	for _, sp := range skills {
		for _, content := range sp.Files {
			total += len(content)
		}
	}
	if total > maxJobSize {
		return fmt.Errorf("job skills too large: %d bytes (max %d)", total, maxJobSize)
	}
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./internal/skill/ -v
```

Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/skill/validate.go internal/skill/validate_test.go
git commit -m "feat: add skill file validation (size, extension whitelist, path safety)"
```

---

### Task 4: Skills config types and loading

**Files:**
- Create: `internal/skill/config.go`
- Create: `internal/skill/config_test.go`
- Modify: `internal/config/config.go:12-36`

- [ ] **Step 1: Write failing tests for skills config loading**

Create `internal/skill/config_test.go`:

```go
package skill

import (
	"os"
	"testing"
	"time"
)

func TestLoadSkillsConfig_Full(t *testing.T) {
	yaml := `
skills:
  triage-issue:
    type: local
    path: agents/skills/triage-issue
  code-review:
    type: npx
    package: "@someone/skill-code-review"
    version: "latest"
  security-audit:
    type: npx
    package: "@team/security-skills"
    version: "^2.0.0"
    timeout: 60s
cache:
  ttl: 5m
`
	f, _ := os.CreateTemp("", "skills-*.yaml")
	f.WriteString(yaml)
	f.Close()
	defer os.Remove(f.Name())

	cfg, err := LoadSkillsConfig(f.Name())
	if err != nil {
		t.Fatalf("LoadSkillsConfig: %v", err)
	}

	if len(cfg.Skills) != 3 {
		t.Fatalf("skills count = %d, want 3", len(cfg.Skills))
	}

	local := cfg.Skills["triage-issue"]
	if local.Type != "local" {
		t.Errorf("triage-issue type = %q", local.Type)
	}
	if local.Path != "agents/skills/triage-issue" {
		t.Errorf("triage-issue path = %q", local.Path)
	}

	npx := cfg.Skills["code-review"]
	if npx.Type != "npx" {
		t.Errorf("code-review type = %q", npx.Type)
	}
	if npx.Package != "@someone/skill-code-review" {
		t.Errorf("code-review package = %q", npx.Package)
	}
	if npx.Version != "latest" {
		t.Errorf("code-review version = %q", npx.Version)
	}

	audit := cfg.Skills["security-audit"]
	if audit.Timeout != 60*time.Second {
		t.Errorf("security-audit timeout = %v", audit.Timeout)
	}

	if cfg.Cache.TTL != 5*time.Minute {
		t.Errorf("cache ttl = %v", cfg.Cache.TTL)
	}
}

func TestLoadSkillsConfig_Defaults(t *testing.T) {
	yaml := `
skills:
  review:
    type: npx
    package: "@team/review"
`
	f, _ := os.CreateTemp("", "skills-*.yaml")
	f.WriteString(yaml)
	f.Close()
	defer os.Remove(f.Name())

	cfg, err := LoadSkillsConfig(f.Name())
	if err != nil {
		t.Fatalf("LoadSkillsConfig: %v", err)
	}

	s := cfg.Skills["review"]
	if s.Version != "latest" {
		t.Errorf("default version = %q, want latest", s.Version)
	}
	if s.Timeout != 30*time.Second {
		t.Errorf("default timeout = %v, want 30s", s.Timeout)
	}
	if cfg.Cache.TTL != 5*time.Minute {
		t.Errorf("default cache TTL = %v, want 5m", cfg.Cache.TTL)
	}
}

func TestLoadSkillsConfig_FileNotFound(t *testing.T) {
	_, err := LoadSkillsConfig("/nonexistent/skills.yaml")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./internal/skill/ -run TestLoadSkillsConfig -v
```

Expected: FAIL — `LoadSkillsConfig` not defined.

- [ ] **Step 3: Implement skills config types and loading**

Create `internal/skill/config.go`:

```go
package skill

import (
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type SkillsFileConfig struct {
	Skills map[string]*SkillConfig `yaml:"skills"`
	Cache  CacheConfig             `yaml:"cache"`
}

type SkillConfig struct {
	Type    string        `yaml:"type"`    // "local" | "npx"
	Path    string        `yaml:"path"`    // local: disk path
	Package string        `yaml:"package"` // npx: npm package name
	Version string        `yaml:"version"` // npx: version spec (default "latest")
	Timeout time.Duration `yaml:"timeout"` // npx: execution timeout (default 30s)
}

type CacheConfig struct {
	TTL time.Duration `yaml:"ttl"`
}

func LoadSkillsConfig(path string) (*SkillsFileConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var cfg SkillsFileConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}

	applySkillsDefaults(&cfg)
	return &cfg, nil
}

func applySkillsDefaults(cfg *SkillsFileConfig) {
	for _, s := range cfg.Skills {
		if s.Type == "npx" {
			if s.Version == "" {
				s.Version = "latest"
			}
			if s.Timeout <= 0 {
				s.Timeout = 30 * time.Second
			}
		}
	}
	if cfg.Cache.TTL <= 0 {
		cfg.Cache.TTL = 5 * time.Minute
	}
}
```

- [ ] **Step 4: Add SkillsConfig field to main config**

In `internal/config/config.go`, add to the `Config` struct (after `Redis` field, line 36):

```go
SkillsConfig string `yaml:"skills_config"`
```

- [ ] **Step 5: Run tests to verify they pass**

```bash
go test ./internal/skill/ -run TestLoadSkillsConfig -v
go test ./internal/config/ -v
```

Expected: all PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/skill/config.go internal/skill/config_test.go internal/config/config.go
git commit -m "feat: add skills.yaml config types and loading"
```

---

### Task 5: NPX package scanning (read from node_modules)

**Files:**
- Create: `internal/skill/npx.go`
- Create: `internal/skill/npx_test.go`

This task implements the disk-scanning part (reading skills from `node_modules/`). The actual `npx` execution is a thin wrapper around this.

- [ ] **Step 1: Write failing tests for package scanning**

Create `internal/skill/npx_test.go`:

```go
package skill

import (
	"os"
	"path/filepath"
	"testing"
)

// writeFixture creates a file at dir/relPath with content.
func writeFixture(t *testing.T, dir, relPath, content string) {
	t.Helper()
	full := filepath.Join(dir, relPath)
	if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestScanPackageSkills_SingleSkill(t *testing.T) {
	tmpDir := t.TempDir()

	// Simulate: node_modules/@team/review/skills/code-review/SKILL.md
	writeFixture(t, tmpDir, "skills/code-review/SKILL.md", "# Code Review")
	writeFixture(t, tmpDir, "skills/code-review/examples/ex1.md", "example 1")
	writeFixture(t, tmpDir, "package.json", `{"name":"@team/review"}`)

	skills, err := scanPackageSkills(tmpDir)
	if err != nil {
		t.Fatalf("scanPackageSkills: %v", err)
	}
	if len(skills) != 1 {
		t.Fatalf("got %d skills, want 1", len(skills))
	}
	if skills[0].Name != "code-review" {
		t.Errorf("name = %q", skills[0].Name)
	}
	if string(skills[0].Files["SKILL.md"]) != "# Code Review" {
		t.Errorf("SKILL.md content = %q", string(skills[0].Files["SKILL.md"]))
	}
	if string(skills[0].Files["examples/ex1.md"]) != "example 1" {
		t.Errorf("examples/ex1.md = %q", string(skills[0].Files["examples/ex1.md"]))
	}
}

func TestScanPackageSkills_MultipleSkills(t *testing.T) {
	tmpDir := t.TempDir()

	writeFixture(t, tmpDir, "skills/skill-a/SKILL.md", "# Skill A")
	writeFixture(t, tmpDir, "skills/skill-b/SKILL.md", "# Skill B")
	writeFixture(t, tmpDir, "skills/skill-b/refs/api.yaml", "openapi: 3.0")
	writeFixture(t, tmpDir, "package.json", `{"name":"@team/multi"}`)

	skills, err := scanPackageSkills(tmpDir)
	if err != nil {
		t.Fatalf("scanPackageSkills: %v", err)
	}
	if len(skills) != 2 {
		t.Fatalf("got %d skills, want 2", len(skills))
	}

	// Verify both skills found (order not guaranteed).
	names := map[string]bool{}
	for _, s := range skills {
		names[s.Name] = true
	}
	if !names["skill-a"] || !names["skill-b"] {
		t.Errorf("skill names = %v", names)
	}
}

func TestScanPackageSkills_SkipsWithoutSkillMD(t *testing.T) {
	tmpDir := t.TempDir()

	writeFixture(t, tmpDir, "skills/has-skill/SKILL.md", "# Valid")
	writeFixture(t, tmpDir, "skills/no-skill/README.md", "# Not a skill")
	writeFixture(t, tmpDir, "package.json", `{}`)

	skills, err := scanPackageSkills(tmpDir)
	if err != nil {
		t.Fatalf("scanPackageSkills: %v", err)
	}
	if len(skills) != 1 {
		t.Fatalf("got %d skills, want 1 (should skip directory without SKILL.md)", len(skills))
	}
	if skills[0].Name != "has-skill" {
		t.Errorf("name = %q", skills[0].Name)
	}
}

func TestScanPackageSkills_NoSkillsDir(t *testing.T) {
	tmpDir := t.TempDir()
	writeFixture(t, tmpDir, "package.json", `{}`)

	_, err := scanPackageSkills(tmpDir)
	if err == nil {
		t.Fatal("expected error when skills/ directory is missing")
	}
}

func TestScanPackageSkills_SkipsBadExtension(t *testing.T) {
	tmpDir := t.TempDir()

	writeFixture(t, tmpDir, "skills/my-skill/SKILL.md", "# Valid")
	writeFixture(t, tmpDir, "skills/my-skill/hack.sh", "#!/bin/bash")
	writeFixture(t, tmpDir, "package.json", `{}`)

	// Scan succeeds but validation on the files should reject bad extensions.
	skills, err := scanPackageSkills(tmpDir)
	if err != nil {
		t.Fatalf("scanPackageSkills: %v", err)
	}
	// scanPackageSkills reads all files; validation is a separate step.
	if len(skills) != 1 {
		t.Fatalf("got %d skills", len(skills))
	}
	// The .sh file should be present in raw scan — ValidateSkillFiles catches it later.
	if _, ok := skills[0].Files["hack.sh"]; !ok {
		t.Error("expected hack.sh to be read (validation is separate)")
	}
}

func TestResolvePackagePath_Scoped(t *testing.T) {
	tmpDir := t.TempDir()
	pkgDir := filepath.Join(tmpDir, "node_modules", "@team", "review")
	os.MkdirAll(pkgDir, 0755)
	writeFixture(t, pkgDir, "skills/s1/SKILL.md", "ok")
	writeFixture(t, pkgDir, "package.json", `{}`)

	path, err := resolvePackagePath(tmpDir, "@team/review")
	if err != nil {
		t.Fatalf("resolvePackagePath: %v", err)
	}
	if path != pkgDir {
		t.Errorf("path = %q, want %q", path, pkgDir)
	}
}

func TestResolvePackagePath_Unscoped(t *testing.T) {
	tmpDir := t.TempDir()
	pkgDir := filepath.Join(tmpDir, "node_modules", "my-skill")
	os.MkdirAll(pkgDir, 0755)

	path, err := resolvePackagePath(tmpDir, "my-skill")
	if err != nil {
		t.Fatalf("resolvePackagePath: %v", err)
	}
	if path != pkgDir {
		t.Errorf("path = %q, want %q", path, pkgDir)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./internal/skill/ -run "TestScanPackage|TestResolvePackage" -v
```

Expected: FAIL — functions not defined.

- [ ] **Step 3: Implement npx.go**

Create `internal/skill/npx.go`:

```go
package skill

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// SkillFiles represents one skill's file tree as read from a package.
type SkillFiles struct {
	Name  string
	Files map[string][]byte // relative path -> content
}

// FetchPackage installs a package via npm and scans for skills.
// Uses `npm install --prefix` (not npx) to reliably install to a local node_modules.
func FetchPackage(ctx context.Context, pkg, version string) ([]*SkillFiles, error) {
	tmpDir, err := os.MkdirTemp("", "agentdock-skill-*")
	if err != nil {
		return nil, fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	arg := pkg + "@" + version
	cmd := exec.CommandContext(ctx, "npm", "install", "--prefix", tmpDir, "--no-save", arg)
	cmd.Dir = tmpDir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("npm install %s failed: %w\n%s", arg, err, string(output))
	}

	pkgPath, err := resolvePackagePath(tmpDir, pkg)
	if err != nil {
		return nil, fmt.Errorf("resolve package path: %w", err)
	}

	return scanPackageSkills(pkgPath)
}

// resolvePackagePath finds the installed package directory inside node_modules.
func resolvePackagePath(baseDir, pkg string) (string, error) {
	// Handle scoped packages: @scope/name -> node_modules/@scope/name
	parts := strings.Split(pkg, "/")
	elems := append([]string{baseDir, "node_modules"}, parts...)
	pkgDir := filepath.Join(elems...)

	if _, err := os.Stat(pkgDir); err != nil {
		return "", fmt.Errorf("package not found at %s: %w", pkgDir, err)
	}
	return pkgDir, nil
}

// scanPackageSkills reads all skills from a package's skills/ directory.
// Each subdirectory containing a SKILL.md is treated as a skill.
func scanPackageSkills(pkgDir string) ([]*SkillFiles, error) {
	skillsDir := filepath.Join(pkgDir, "skills")
	entries, err := os.ReadDir(skillsDir)
	if err != nil {
		return nil, fmt.Errorf("read skills directory: %w", err)
	}

	var result []*SkillFiles
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		skillDir := filepath.Join(skillsDir, entry.Name())

		// Must contain SKILL.md.
		if _, err := os.Stat(filepath.Join(skillDir, "SKILL.md")); err != nil {
			continue
		}

		files, err := readDirRecursive(skillDir, "")
		if err != nil {
			return nil, fmt.Errorf("read skill %s: %w", entry.Name(), err)
		}

		result = append(result, &SkillFiles{
			Name:  entry.Name(),
			Files: files,
		})
	}

	if len(result) == 0 {
		return nil, fmt.Errorf("no skills found in %s", skillsDir)
	}
	return result, nil
}

// readDirRecursive reads all files in a directory tree, returning relative paths.
func readDirRecursive(baseDir, prefix string) (map[string][]byte, error) {
	files := make(map[string][]byte)
	dir := baseDir
	if prefix != "" {
		dir = filepath.Join(baseDir, prefix)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	for _, entry := range entries {
		relPath := entry.Name()
		if prefix != "" {
			relPath = prefix + "/" + entry.Name()
		}

		if entry.IsDir() {
			sub, err := readDirRecursive(baseDir, relPath)
			if err != nil {
				return nil, err
			}
			for k, v := range sub {
				files[k] = v
			}
			continue
		}

		// Skip symlinks.
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 {
			continue
		}

		content, err := os.ReadFile(filepath.Join(baseDir, relPath))
		if err != nil {
			return nil, err
		}
		files[relPath] = content
	}
	return files, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./internal/skill/ -run "TestScanPackage|TestResolvePackage" -v
```

Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/skill/npx.go internal/skill/npx_test.go
git commit -m "feat: add npx package scanning and skill file reading"
```

---

### Task 6: Loader core (cache, singleflight, fallback, warmup)

**Files:**
- Create: `internal/skill/loader.go`
- Create: `internal/skill/loader_test.go`

- [ ] **Step 1: Write failing tests for loader**

Create `internal/skill/loader_test.go`:

```go
package skill

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"agentdock/internal/queue"
)

// mockFetcher replaces FetchPackage for testing.
type mockFetcher struct {
	mu      sync.Mutex
	calls   int
	results map[string][]*SkillFiles // keyed by "pkg@version"
	errors  map[string]error
}

func (m *mockFetcher) fetch(ctx context.Context, pkg, version string) ([]*SkillFiles, error) {
	m.mu.Lock()
	m.calls++
	m.mu.Unlock()

	key := pkg + "@" + version
	if err, ok := m.errors[key]; ok {
		return nil, err
	}
	if skills, ok := m.results[key]; ok {
		return skills, nil
	}
	return nil, fmt.Errorf("package not found: %s", key)
}

func newTestLoader(cfg *SkillsFileConfig, fetcher fetchFunc, bakedIn map[string]*SkillFiles) *Loader {
	if cfg == nil {
		cfg = &SkillsFileConfig{
			Skills: map[string]*SkillConfig{},
			Cache:  CacheConfig{TTL: 5 * time.Minute},
		}
	}
	l := &Loader{
		config:  cfg,
		cache:   make(map[string]*cacheEntry),
		bakedIn: bakedIn,
		fetcher: fetcher,
	}
	return l
}

func TestLoader_LoadAll_LocalSkill(t *testing.T) {
	bakedIn := map[string]*SkillFiles{
		"triage": {
			Name:  "triage",
			Files: map[string][]byte{"SKILL.md": []byte("# Triage")},
		},
	}
	cfg := &SkillsFileConfig{
		Skills: map[string]*SkillConfig{
			"triage": {Type: "local", Path: "agents/skills/triage"},
		},
		Cache: CacheConfig{TTL: 5 * time.Minute},
	}
	fetcher := &mockFetcher{}
	loader := newTestLoader(cfg, fetcher.fetch, bakedIn)

	result, err := loader.LoadAll(context.Background())
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if _, ok := result["triage"]; !ok {
		t.Fatal("missing triage skill")
	}
	if string(result["triage"].Files["SKILL.md"]) != "# Triage" {
		t.Errorf("content = %q", string(result["triage"].Files["SKILL.md"]))
	}
	if fetcher.calls != 0 {
		t.Errorf("fetcher should not be called for local skills, got %d calls", fetcher.calls)
	}
}

func TestLoader_LoadAll_NpxSkill_CacheMiss(t *testing.T) {
	cfg := &SkillsFileConfig{
		Skills: map[string]*SkillConfig{
			"review": {Type: "npx", Package: "@team/review", Version: "latest", Timeout: 30 * time.Second},
		},
		Cache: CacheConfig{TTL: 5 * time.Minute},
	}
	fetcher := &mockFetcher{
		results: map[string][]*SkillFiles{
			"@team/review@latest": {
				{Name: "code-review", Files: map[string][]byte{"SKILL.md": []byte("# Review")}},
			},
		},
	}
	loader := newTestLoader(cfg, fetcher.fetch, nil)

	result, err := loader.LoadAll(context.Background())
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if _, ok := result["code-review"]; !ok {
		t.Fatal("missing code-review skill")
	}
	if fetcher.calls != 1 {
		t.Errorf("fetcher calls = %d, want 1", fetcher.calls)
	}
}

func TestLoader_LoadAll_NpxSkill_CacheHit(t *testing.T) {
	cfg := &SkillsFileConfig{
		Skills: map[string]*SkillConfig{
			"review": {Type: "npx", Package: "@team/review", Version: "latest", Timeout: 30 * time.Second},
		},
		Cache: CacheConfig{TTL: 5 * time.Minute},
	}
	fetcher := &mockFetcher{
		results: map[string][]*SkillFiles{
			"@team/review@latest": {
				{Name: "code-review", Files: map[string][]byte{"SKILL.md": []byte("# Review")}},
			},
		},
	}
	loader := newTestLoader(cfg, fetcher.fetch, nil)

	// First call populates cache.
	loader.LoadAll(context.Background())
	// Second call should use cache.
	result, err := loader.LoadAll(context.Background())
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if _, ok := result["code-review"]; !ok {
		t.Fatal("missing code-review on cache hit")
	}
	if fetcher.calls != 1 {
		t.Errorf("fetcher calls = %d, want 1 (cache hit)", fetcher.calls)
	}
}

func TestLoader_LoadAll_NpxSkill_CacheExpired(t *testing.T) {
	cfg := &SkillsFileConfig{
		Skills: map[string]*SkillConfig{
			"review": {Type: "npx", Package: "@team/review", Version: "latest", Timeout: 30 * time.Second},
		},
		Cache: CacheConfig{TTL: 1 * time.Millisecond}, // expire immediately
	}
	fetcher := &mockFetcher{
		results: map[string][]*SkillFiles{
			"@team/review@latest": {
				{Name: "code-review", Files: map[string][]byte{"SKILL.md": []byte("# Review")}},
			},
		},
	}
	loader := newTestLoader(cfg, fetcher.fetch, nil)

	loader.LoadAll(context.Background())
	time.Sleep(5 * time.Millisecond)
	loader.LoadAll(context.Background())

	if fetcher.calls != 2 {
		t.Errorf("fetcher calls = %d, want 2 (cache expired)", fetcher.calls)
	}
}

func TestLoader_LoadAll_NpxFail_FallbackBakedIn(t *testing.T) {
	bakedIn := map[string]*SkillFiles{
		"code-review": {
			Name:  "code-review",
			Files: map[string][]byte{"SKILL.md": []byte("# Baked-in Review")},
		},
	}
	cfg := &SkillsFileConfig{
		Skills: map[string]*SkillConfig{
			"review": {Type: "npx", Package: "@team/review", Version: "latest", Timeout: 30 * time.Second},
		},
		Cache: CacheConfig{TTL: 5 * time.Minute},
	}
	fetcher := &mockFetcher{
		errors: map[string]error{
			"@team/review@latest": fmt.Errorf("network error"),
		},
	}
	loader := newTestLoader(cfg, fetcher.fetch, bakedIn)

	result, err := loader.LoadAll(context.Background())
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	// Should fallback to baked-in.
	if _, ok := result["code-review"]; !ok {
		t.Fatal("should fallback to baked-in skill")
	}
	if string(result["code-review"].Files["SKILL.md"]) != "# Baked-in Review" {
		t.Errorf("content = %q, want baked-in", string(result["code-review"].Files["SKILL.md"]))
	}
}

func TestLoader_LoadAll_NpxFail_NoBakedIn_Skip(t *testing.T) {
	cfg := &SkillsFileConfig{
		Skills: map[string]*SkillConfig{
			"review": {Type: "npx", Package: "@team/review", Version: "latest", Timeout: 30 * time.Second},
		},
		Cache: CacheConfig{TTL: 5 * time.Minute},
	}
	fetcher := &mockFetcher{
		errors: map[string]error{
			"@team/review@latest": fmt.Errorf("network error"),
		},
	}
	loader := newTestLoader(cfg, fetcher.fetch, nil)

	result, err := loader.LoadAll(context.Background())
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("expected empty result when npx fails and no baked-in, got %d", len(result))
	}
}

func TestLoader_LoadAll_NpxFail_NegativeCache(t *testing.T) {
	cfg := &SkillsFileConfig{
		Skills: map[string]*SkillConfig{
			"review": {Type: "npx", Package: "@team/review", Version: "latest", Timeout: 30 * time.Second},
		},
		Cache: CacheConfig{TTL: 5 * time.Minute},
	}
	fetcher := &mockFetcher{
		errors: map[string]error{
			"@team/review@latest": fmt.Errorf("network error"),
		},
	}
	loader := newTestLoader(cfg, fetcher.fetch, nil)

	// First call: fetch fails, writes negative cache.
	loader.LoadAll(context.Background())
	// Second call: should not retry (negative cache).
	loader.LoadAll(context.Background())

	if fetcher.calls != 1 {
		t.Errorf("fetcher calls = %d, want 1 (negative cache should prevent retry)", fetcher.calls)
	}
}

func TestLoader_LoadAll_SamePackageDedup(t *testing.T) {
	cfg := &SkillsFileConfig{
		Skills: map[string]*SkillConfig{
			"entry-a": {Type: "npx", Package: "@team/multi", Version: "latest", Timeout: 30 * time.Second},
			"entry-b": {Type: "npx", Package: "@team/multi", Version: "latest", Timeout: 30 * time.Second},
		},
		Cache: CacheConfig{TTL: 5 * time.Minute},
	}
	fetcher := &mockFetcher{
		results: map[string][]*SkillFiles{
			"@team/multi@latest": {
				{Name: "skill-a", Files: map[string][]byte{"SKILL.md": []byte("# A")}},
				{Name: "skill-b", Files: map[string][]byte{"SKILL.md": []byte("# B")}},
			},
		},
	}
	loader := newTestLoader(cfg, fetcher.fetch, nil)

	result, err := loader.LoadAll(context.Background())
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if len(result) != 2 {
		t.Errorf("got %d skills, want 2", len(result))
	}
	if fetcher.calls != 1 {
		t.Errorf("fetcher calls = %d, want 1 (same package should be fetched once)", fetcher.calls)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./internal/skill/ -run TestLoader -v
```

Expected: FAIL — `Loader` types and methods not defined.

- [ ] **Step 3: Implement loader.go**

Create `internal/skill/loader.go`:

```go
package skill

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"agentdock/internal/queue"

	"golang.org/x/sync/singleflight"
)

type fetchFunc func(ctx context.Context, pkg, version string) ([]*SkillFiles, error)

type cacheStatus int

const (
	cacheOK      cacheStatus = iota
	cacheFailed              // npx execution failed
	cacheInvalid             // validation failed
)

type cacheEntry struct {
	status    cacheStatus
	skills    []*SkillFiles
	reason    string
	fetchedAt time.Time
}

type Loader struct {
	mu       sync.RWMutex
	config   *SkillsFileConfig
	cache    map[string]*cacheEntry // keyed by "pkg@version"
	bakedIn  map[string]*SkillFiles // keyed by skill name
	fetcher  fetchFunc
	group    singleflight.Group
}

// NewLoader creates a Loader from a skills.yaml config path and baked-in skills directory.
// If configPath is empty or file doesn't exist, only baked-in skills are used.
func NewLoader(configPath, bakedInDir string) (*Loader, error) {
	var cfg *SkillsFileConfig

	if configPath != "" {
		var err error
		cfg, err = LoadSkillsConfig(configPath)
		if err != nil {
			slog.Warn("failed to load skills config, using baked-in only", "path", configPath, "error", err)
			cfg = &SkillsFileConfig{
				Skills: map[string]*SkillConfig{},
				Cache:  CacheConfig{TTL: 5 * time.Minute},
			}
		}
	} else {
		cfg = &SkillsFileConfig{
			Skills: map[string]*SkillConfig{},
			Cache:  CacheConfig{TTL: 5 * time.Minute},
		}
	}

	bakedIn := loadBakedInSkills(bakedInDir)

	l := &Loader{
		config:  cfg,
		cache:   make(map[string]*cacheEntry),
		bakedIn: bakedIn,
		fetcher: FetchPackage,
	}

	return l, nil
}

// Warmup prefetches all npx packages. Call after NewLoader at startup.
func (l *Loader) Warmup(ctx context.Context) {
	l.mu.RLock()
	cfg := l.config
	l.mu.RUnlock()

	seen := make(map[string]bool)
	for _, sc := range cfg.Skills {
		if sc.Type != "npx" {
			continue
		}
		key := sc.Package + "@" + sc.Version
		if seen[key] {
			continue
		}
		seen[key] = true

		fetchCtx, cancel := context.WithTimeout(ctx, sc.Timeout)
		skills, err := l.fetcher(fetchCtx, sc.Package, sc.Version)
		cancel()

		if err != nil {
			slog.Warn("skill.warmup_failed", "package", sc.Package, "error", err)
			l.setCacheEntry(key, &cacheEntry{
				status:    cacheFailed,
				reason:    err.Error(),
				fetchedAt: time.Now(),
			})
			continue
		}

		if err := validateSkillsBatch(skills); err != nil {
			slog.Warn("skill.warmup_invalid", "package", sc.Package, "error", err)
			l.setCacheEntry(key, &cacheEntry{
				status:    cacheInvalid,
				reason:    err.Error(),
				fetchedAt: time.Now(),
			})
			continue
		}

		l.setCacheEntry(key, &cacheEntry{
			status:    cacheOK,
			skills:    skills,
			fetchedAt: time.Now(),
		})
		for _, s := range skills {
			slog.Info("skill.warmup_loaded", "skill", s.Name, "package", sc.Package)
		}
	}
}

// LoadAll returns all skills as SkillPayloads for a job.
// Uses cache for npx skills, falls back to baked-in, skips if unavailable.
func (l *Loader) LoadAll(ctx context.Context) (map[string]*queue.SkillPayload, error) {
	// Snapshot config under RLock.
	l.mu.RLock()
	cfg := l.config
	l.mu.RUnlock()

	result := make(map[string]*queue.SkillPayload)
	seen := make(map[string]bool) // track processed packages

	for configKey, sc := range cfg.Skills {
		switch sc.Type {
		case "local":
			l.loadLocal(configKey, result)

		case "npx":
			key := sc.Package + "@" + sc.Version
			if seen[key] {
				continue
			}
			seen[key] = true
			l.loadNpx(ctx, sc, key, result)
		}
	}

	if err := ValidateJobSize(result); err != nil {
		return nil, err
	}

	return result, nil
}

func (l *Loader) loadLocal(configKey string, result map[string]*queue.SkillPayload) {
	if sf, ok := l.bakedIn[configKey]; ok {
		result[configKey] = &queue.SkillPayload{Files: sf.Files}
		slog.Info("skill.loaded", "skill", configKey, "source", "baked-in")
	}
}

func (l *Loader) loadNpx(ctx context.Context, sc *SkillConfig, cacheKey string, result map[string]*queue.SkillPayload) {
	// Check cache.
	l.mu.RLock()
	entry, exists := l.cache[cacheKey]
	l.mu.RUnlock()

	ttl := l.config.Cache.TTL

	if exists && time.Since(entry.fetchedAt) < ttl {
		switch entry.status {
		case cacheOK:
			for _, s := range entry.skills {
				result[s.Name] = &queue.SkillPayload{Files: s.Files}
				slog.Info("skill.loaded", "skill", s.Name, "source", "cache", "package", sc.Package)
			}
			return
		case cacheFailed, cacheInvalid:
			// Negative cache — skip silently.
			return
		}
	}

	// Cache miss or expired — fetch via singleflight.
	start := time.Now()
	val, err, _ := l.group.Do(cacheKey, func() (any, error) {
		fetchCtx, cancel := context.WithTimeout(ctx, sc.Timeout)
		defer cancel()
		return l.fetcher(fetchCtx, sc.Package, sc.Version)
	})

	if err != nil {
		slog.Warn("skill.fetch_failed", "package", sc.Package, "error", err, "duration_ms", time.Since(start).Milliseconds())

		// Write negative cache.
		l.setCacheEntry(cacheKey, &cacheEntry{
			status:    cacheFailed,
			reason:    err.Error(),
			fetchedAt: time.Now(),
		})

		// Fallback 1: previous OK cache (if any).
		if exists && entry.status == cacheOK {
			for _, s := range entry.skills {
				result[s.Name] = &queue.SkillPayload{Files: s.Files}
				slog.Info("skill.loaded", "skill", s.Name, "source", "cache", "package", sc.Package)
			}
			return
		}

		// Fallback 2: baked-in.
		l.fallbackBakedIn(sc.Package, result)
		return
	}

	skills := val.([]*SkillFiles)

	// Validate.
	if err := validateSkillsBatch(skills); err != nil {
		slog.Warn("skill.validation_failed", "package", sc.Package, "error", err)
		l.setCacheEntry(cacheKey, &cacheEntry{
			status:    cacheInvalid,
			reason:    err.Error(),
			fetchedAt: time.Now(),
		})
		l.fallbackBakedIn(sc.Package, result)
		return
	}

	// Success — update cache.
	l.setCacheEntry(cacheKey, &cacheEntry{
		status:    cacheOK,
		skills:    skills,
		fetchedAt: time.Now(),
	})

	for _, s := range skills {
		result[s.Name] = &queue.SkillPayload{Files: s.Files}
		slog.Info("skill.loaded", "skill", s.Name, "source", "npx", "package", sc.Package, "duration_ms", time.Since(start).Milliseconds())
	}
}

func (l *Loader) fallbackBakedIn(pkg string, result map[string]*queue.SkillPayload) {
	for name, sf := range l.bakedIn {
		if _, already := result[name]; already {
			continue
		}
		result[name] = &queue.SkillPayload{Files: sf.Files}
		slog.Info("skill.loaded", "skill", name, "source", "baked-in", "package", pkg, "fallback", true)
	}
}

// ReloadConfig re-reads the skills.yaml and updates internal config.
// Clears cache entries for packages whose config changed or were removed.
func (l *Loader) ReloadConfig(path string) error {
	newCfg, err := LoadSkillsConfig(path)
	if err != nil {
		return err
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	oldSkills := l.config.Skills

	// Find packages to invalidate.
	for key, oldSc := range oldSkills {
		newSc, exists := newCfg.Skills[key]
		if !exists || (oldSc.Type == "npx" && (oldSc.Package != newSc.Package || oldSc.Version != newSc.Version)) {
			if oldSc.Type == "npx" {
				delete(l.cache, oldSc.Package+"@"+oldSc.Version)
			}
		}
	}

	// Reload baked-in for local skills.
	for _, sc := range newCfg.Skills {
		if sc.Type == "local" && sc.Path != "" {
			if sf, err := loadSingleBakedIn(sc.Path); err == nil {
				l.bakedIn[filepath.Base(sc.Path)] = sf
			}
		}
	}

	l.config = newCfg
	return nil
}

func (l *Loader) setCacheEntry(key string, entry *cacheEntry) {
	l.mu.Lock()
	l.cache[key] = entry
	l.mu.Unlock()
}

func validateSkillsBatch(skills []*SkillFiles) error {
	for _, s := range skills {
		if err := ValidateSkillFiles(s.Files); err != nil {
			return fmt.Errorf("skill %s: %w", s.Name, err)
		}
	}
	return nil
}

func loadBakedInSkills(dir string) map[string]*SkillFiles {
	result := make(map[string]*SkillFiles)
	if dir == "" {
		return result
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		slog.Warn("failed to read baked-in skills dir", "dir", dir, "error", err)
		return result
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		sf, err := loadSingleBakedIn(filepath.Join(dir, entry.Name()))
		if err != nil {
			slog.Warn("failed to load baked-in skill", "name", entry.Name(), "error", err)
			continue
		}
		result[entry.Name()] = sf
	}
	return result
}

func loadSingleBakedIn(skillDir string) (*SkillFiles, error) {
	files, err := readDirRecursive(skillDir, "")
	if err != nil {
		return nil, err
	}
	if _, ok := files["SKILL.md"]; !ok {
		return nil, fmt.Errorf("missing SKILL.md in %s", skillDir)
	}
	return &SkillFiles{
		Name:  filepath.Base(skillDir),
		Files: files,
	}, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./internal/skill/ -run TestLoader -v
```

Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/skill/loader.go internal/skill/loader_test.go
git commit -m "feat: add SkillLoader with cache, singleflight, and fallback"
```

---

### Task 7: fsnotify watcher

**Files:**
- Create: `internal/skill/watcher.go`
- Create: `internal/skill/watcher_test.go`

- [ ] **Step 1: Write failing test for watcher**

Create `internal/skill/watcher_test.go`:

```go
package skill

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWatcher_ReloadsOnFileChange(t *testing.T) {
	// Requires "context" import — add to imports block.
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "skills.yaml")

	initialYAML := `
skills:
  triage:
    type: local
    path: agents/skills/triage
cache:
  ttl: 5m
`
	os.WriteFile(cfgPath, []byte(initialYAML), 0644)

	loader := &Loader{
		config:  &SkillsFileConfig{Skills: map[string]*SkillConfig{}, Cache: CacheConfig{TTL: 5 * time.Minute}},
		cache:   make(map[string]*cacheEntry),
		bakedIn: make(map[string]*SkillFiles),
		fetcher: func(ctx context.Context, pkg, version string) ([]*SkillFiles, error) {
			return nil, nil
		},
	}

	stop, err := loader.StartWatcher(cfgPath)
	if err != nil {
		t.Fatalf("StartWatcher: %v", err)
	}
	defer stop()

	// Give watcher time to start.
	time.Sleep(100 * time.Millisecond)

	// Write updated config.
	updatedYAML := `
skills:
  triage:
    type: local
    path: agents/skills/triage
  review:
    type: npx
    package: "@team/review"
    version: "latest"
cache:
  ttl: 10m
`
	os.WriteFile(cfgPath, []byte(updatedYAML), 0644)

	// Wait for debounce + reload.
	time.Sleep(1 * time.Second)

	loader.mu.RLock()
	defer loader.mu.RUnlock()

	if len(loader.config.Skills) != 2 {
		t.Errorf("skills count = %d, want 2 after reload", len(loader.config.Skills))
	}
	if loader.config.Cache.TTL != 10*time.Minute {
		t.Errorf("cache TTL = %v, want 10m after reload", loader.config.Cache.TTL)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./internal/skill/ -run TestWatcher -v
```

Expected: FAIL — `StartWatcher` not defined.

- [ ] **Step 3: Implement watcher.go**

Create `internal/skill/watcher.go`:

```go
package skill

import (
	"context"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
)

const debounceDuration = 500 * time.Millisecond

// StartWatcher watches the skills.yaml file for changes and reloads config on update.
// Returns a stop function to clean up the watcher.
func (l *Loader) StartWatcher(configPath string) (stop func(), err error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	// Watch the directory, not the file — k8s ConfigMap updates are symlink swaps.
	dir := filepath.Dir(configPath)
	if err := watcher.Add(dir); err != nil {
		watcher.Close()
		return nil, err
	}

	absPath, err := filepath.Abs(configPath)
	if err != nil {
		watcher.Close()
		return nil, err
	}

	ctx, cancel := context.WithCancel(context.Background())

	go l.watchLoop(ctx, watcher, absPath)

	return func() {
		cancel()
		watcher.Close()
	}, nil
}

func (l *Loader) watchLoop(ctx context.Context, watcher *fsnotify.Watcher, configPath string) {
	var debounceTimer *time.Timer

	for {
		select {
		case <-ctx.Done():
			if debounceTimer != nil {
				debounceTimer.Stop()
			}
			return

		case event, ok := <-watcher.Events:
			if !ok {
				return
			}

			absEvent, _ := filepath.Abs(event.Name)
			if absEvent != configPath {
				continue
			}

			if event.Op&(fsnotify.Write|fsnotify.Create) == 0 {
				continue
			}

			if debounceTimer != nil {
				debounceTimer.Stop()
			}
			debounceTimer = time.AfterFunc(debounceDuration, func() {
				if err := l.ReloadConfig(configPath); err != nil {
					slog.Error("skill.config_reload_failed", "path", configPath, "error", err)
				} else {
					slog.Info("skill.config_reloaded", "path", configPath)
				}
			})

		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			slog.Error("skill.watcher_error", "error", err)
		}
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

```bash
go test ./internal/skill/ -run TestWatcher -v -timeout 10s
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/skill/watcher.go internal/skill/watcher_test.go
git commit -m "feat: add fsnotify watcher for skills.yaml hot reload"
```

---

### Task 8: Wire Loader into Workflow

**Files:**
- Modify: `internal/bot/workflow.go:40-84`

- [ ] **Step 1: Write failing test for Workflow with Loader**

The Workflow currently takes `skills map[string]string`. We need to change it to accept a `SkillProvider` interface so we can mock it in tests.

Create `internal/bot/skill_provider.go`:

```go
package bot

import (
	"context"

	"agentdock/internal/queue"
)

// SkillProvider loads skills for a job.
type SkillProvider interface {
	LoadAll(ctx context.Context) (map[string]*queue.SkillPayload, error)
}
```

- [ ] **Step 2: Update Workflow to use SkillProvider**

In `internal/bot/workflow.go`:

Change field (line 51):
```go
skills        map[string]string
```
to:
```go
skillProvider SkillProvider
```

Change constructor parameter (line 68):
```go
skills map[string]string,
```
to:
```go
skillProvider SkillProvider,
```

Change assignment (line 80):
```go
skills:        skills,
```
to:
```go
skillProvider: skillProvider,
```

Change `runTriage` method (line 426, where job is created):
```go
Skills:      w.skills,
```
to:
```go
Skills:      w.loadSkills(ctx),
```

Add helper method after `runTriage`:
```go
func (w *Workflow) loadSkills(ctx context.Context) map[string]*queue.SkillPayload {
	if w.skillProvider == nil {
		return nil
	}
	skills, err := w.skillProvider.LoadAll(ctx)
	if err != nil {
		slog.Warn("failed to load skills for job", "error", err)
		return nil
	}
	return skills
}
```

- [ ] **Step 3: Run all tests to verify nothing is broken**

```bash
go test ./internal/... -count=1
```

Expected: PASS (or compile errors in `main.go` which we fix in Task 9).

- [ ] **Step 4: Commit**

```bash
git add internal/bot/skill_provider.go internal/bot/workflow.go
git commit -m "feat: replace skills map with SkillProvider interface in Workflow"
```

---

### Task 9: Wire Loader into main.go and worker.go

**Files:**
- Modify: `cmd/bot/main.go:78-86, 164`
- Modify: `cmd/bot/worker.go` (no skill loading needed on worker)

- [ ] **Step 1: Update main.go to use SkillLoader**

In `cmd/bot/main.go`, replace the skill loading section (lines 78-86):

```go
// Load skills for agent.
skills := make(map[string]string)
skillPath := "agents/skills/triage-issue/SKILL.md"
if data, err := os.ReadFile(skillPath); err == nil {
    skills["triage-issue"] = string(data)
    slog.Info("skill loaded", "path", skillPath)
} else {
    slog.Warn("skill not found, agents will run without skill", "path", skillPath, "error", err)
}
```

with:

```go
// Load skills via SkillLoader.
skillLoader, err := skill.NewLoader(cfg.SkillsConfig, "agents/skills")
if err != nil {
    slog.Error("failed to create skill loader", "error", err)
    os.Exit(1)
}
skillLoader.Warmup(context.Background())
if cfg.SkillsConfig != "" {
    stopWatcher, err := skillLoader.StartWatcher(cfg.SkillsConfig)
    if err != nil {
        slog.Warn("failed to start skill config watcher", "error", err)
    } else {
        defer stopWatcher()
    }
}
```

Add import `"agentdock/internal/skill"`.

Update the `NewWorkflow` call (line 164), replace `skills` parameter with `skillLoader`:

```go
wf := bot.NewWorkflow(cfg, slackClient, repoCache, repoDiscovery, agentRunner, mantisClient, coordinator, jobStore, bundle.Attachments, skillLoader)
```

- [ ] **Step 2: Build to verify compilation**

```bash
go build ./cmd/bot/
```

Expected: success.

- [ ] **Step 3: Run all tests**

```bash
go test ./... -count=1
```

Expected: all pass.

- [ ] **Step 4: Commit**

```bash
git add cmd/bot/main.go internal/bot/workflow.go
git commit -m "feat: wire SkillLoader into app startup and workflow"
```

---

### Task 10: Add SkillsConfig to config.yaml and update Dockerfile

**Files:**
- Modify: `config.example.yaml` (or `config.yaml`)
- Modify: `Dockerfile:33-37`

- [ ] **Step 1: Add skills_config field to config.example.yaml**

Add to the config:
```yaml
# Skills config (optional, for npx dynamic loading via ConfigMap)
# skills_config: "/etc/agentdock/skills.yaml"
```

- [ ] **Step 2: Update Dockerfile to copy agents/skills with correct path**

The Dockerfile currently copies skills and creates symlinks. After this change, the SkillLoader reads from `agents/skills/` directly (baked-in path). The existing symlink setup (lines 33-37) can remain for backward compat when running agents locally, but we also need to ensure `/opt/agents/skills/` is accessible as the baked-in dir.

Replace lines 33-37:
```dockerfile
# Agent skills (baked-in, used as fallback for npx dynamic loading)
COPY agents/skills/ /opt/agents/skills/
RUN mkdir -p /home/node/.claude/skills && \
    for d in /opt/agents/skills/*/; do \
      ln -s "$d" /home/node/.claude/skills/$(basename "$d"); \
    done
```

No change needed — the existing Dockerfile already copies to `/opt/agents/skills/`. The SkillLoader's `bakedInDir` should match. In `main.go`, we pass `"agents/skills"` for local dev and the Docker entrypoint should use `/opt/agents/skills` via config or default.

Actually, update `main.go` to use the correct baked-in path:

```go
bakedInDir := "agents/skills"
if _, err := os.Stat("/opt/agents/skills"); err == nil {
    bakedInDir = "/opt/agents/skills"
}
skillLoader, err := skill.NewLoader(cfg.SkillsConfig, bakedInDir)
```

- [ ] **Step 3: Build Docker image to verify**

```bash
docker build -t agentdock:test .
```

Expected: success.

- [ ] **Step 4: Commit**

```bash
git add Dockerfile cmd/bot/main.go config.example.yaml
git commit -m "feat: update config and Dockerfile for dynamic skill loading"
```

---

### Task 11: Run full test suite and verify

**Files:** None (verification only)

- [ ] **Step 1: Run all tests**

```bash
go test ./... -count=1 -v
```

Expected: all tests pass.

- [ ] **Step 2: Run build**

```bash
go build ./cmd/bot/
```

Expected: success.

- [ ] **Step 3: Verify with go vet**

```bash
go vet ./...
```

Expected: no issues.

- [ ] **Step 4: Final commit if any fixes needed**

```bash
git add -A
git commit -m "fix: address issues found in final verification"
```
