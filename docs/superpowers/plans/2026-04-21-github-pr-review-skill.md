# `github-pr-review` Skill Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship the `github-pr-review` skill — a baked-in skill plus an `agentdock pr-review-helper` subcommand that fingerprints a repo, validates comment line numbers against the PR's diff, and posts a single `COMMENT`-event review to GitHub.

**Architecture:** Two deliverables travel together. A new `shared/prreview/` Go package owns review logic and fingerprinting (pure Go, no external tools). `cmd/agentdock/pr_review_helper.go` is a thin cobra wrapper that exposes two subcommands (`fingerprint`, `validate-and-post`) reading stdin / writing JSON stdout. The skill's `SKILL.md` teaches the agent when and how to invoke these commands and emit the three-state `===REVIEW_RESULT===` contract.

**Tech Stack:**
- Go 1.25 (`shared/go.mod`)
- `net/http` + `net/http/httptest` (stdlib, no third-party HTTP/mock libs)
- `encoding/json` (stdlib)
- `github.com/spf13/cobra` (already in root module for subcommand wiring)
- No GitHub client library (direct REST + minimal typed structs) — keeps retry/Retry-After handling transparent

**Spec:** [`docs/superpowers/specs/2026-04-21-github-pr-review-skill-design.md`](../specs/2026-04-21-github-pr-review-skill-design.md)

**Parent spec** (for context, not implemented here): [`docs/superpowers/specs/2026-04-20-workflow-types-design.md`](../specs/2026-04-20-workflow-types-design.md) — PR Review workflow wiring is out of scope for this plan; handed off to a later session.

---

## File Structure

```
shared/prreview/
├── doc.go                   # package doc
├── types.go                 # ReviewJSON, CommentJSON, enums, size limits
├── types_test.go            # JSON round-trip tests
├── errors.go                # error-message constants
├── validator.go             # Validate(*ReviewJSON) error
├── validator_test.go
├── fingerprint.go           # Fingerprint() + local probes + PR-aware fields
├── fingerprint_test.go
├── github.go                # httpCallWithRetry + listDiffFiles + createReview
├── github_test.go
├── review.go                # ValidateAndPost() top-level orchestrator + truncation
└── review_test.go

cmd/agentdock/
└── pr_review_helper.go      # cobra subcommand wiring

agents/skills/github-pr-review/
├── SKILL.md                 # agent instructions (<300 lines)
└── evals/
    └── evals.json           # skill-creator eval baseline

skills.yaml                  # one-line addition (modify)
```

No `agents/skills/github-pr-review/testdata/` — test fixtures used by Go unit tests live at `shared/prreview/testdata/` so they're colocated with the tests that use them.

---

### Task 1: `shared/prreview` package scaffold — types, errors, doc

**Files:**
- Create: `shared/prreview/doc.go`
- Create: `shared/prreview/types.go`
- Create: `shared/prreview/errors.go`
- Create: `shared/prreview/types_test.go`

- [ ] **Step 1: Create package doc**

`shared/prreview/doc.go`:

```go
// Package prreview implements GitHub pull-request review analysis and posting
// for the github-pr-review skill. It is invoked by the agentdock pr-review-helper
// subcommand on the worker.
//
// Callers provide a review JSON via stdin (see ReviewJSON). The package
// validates line numbers against the PR's actual diff, truncates oversized
// content, and posts a single COMMENT-event review to GitHub. Rate-limit
// handling honors GitHub's Retry-After header with an exponential fallback.
package prreview
```

- [ ] **Step 2: Create types**

`shared/prreview/types.go`:

```go
package prreview

// Size limits for review content. GitHub accepts up to ~64KB per field, but
// real reviews should be far smaller. Beyond these thresholds we truncate
// rather than reject so a partial review still lands.
const (
	MaxCommentBody = 4096
	MaxSummaryBody = 2048

	commentTruncSuffix = "\n\n_…(comment truncated)_"
	summaryTruncSuffix = "\n\n_(summary truncated; see inline comments)_"
)

// Severity describes how serious the agent thinks one comment is.
// v1 enum; helper validates the value.
type Severity string

const (
	SeverityBlocker    Severity = "blocker"
	SeveritySuggestion Severity = "suggestion"
	SeverityNit        Severity = "nit"
)

// SeveritySummary is the overall assessment of the review. Informational only —
// the GitHub review event is always COMMENT regardless of summary.
type SeveritySummary string

const (
	SummaryClean SeveritySummary = "clean"
	SummaryMinor SeveritySummary = "minor"
	SummaryMajor SeveritySummary = "major"
)

// Side matches GitHub's REST API values for comment side selection.
type Side string

const (
	SideLeft  Side = "LEFT"
	SideRight Side = "RIGHT"
)

// CommentJSON is one inline comment in the review JSON fed on stdin.
// StartLine/StartSide are optional; both-present-or-both-absent denotes
// a multi-line comment.
type CommentJSON struct {
	Path      string   `json:"path"`
	Line      int      `json:"line"`
	Side      Side     `json:"side"`
	Body      string   `json:"body"`
	Severity  Severity `json:"severity"`
	StartLine *int     `json:"start_line,omitempty"`
	StartSide *Side    `json:"start_side,omitempty"`
}

// ReviewJSON is the full review payload the agent pipes into `validate-and-post`.
type ReviewJSON struct {
	Summary         string          `json:"summary"`
	SeveritySummary SeveritySummary `json:"severity_summary"`
	Comments        []CommentJSON   `json:"comments"`
}

// SkipReason is emitted in PostResult.SkipReasons for comments that couldn't
// be posted because their line was outside the PR's diff.
type SkipReason struct {
	Path   string `json:"path"`
	Line   int    `json:"line"`
	Reason string `json:"reason"`
}

// PostResult is the stdout JSON emitted by `validate-and-post`.
// DryRun / WouldPost / Payload are populated only in dry-run mode.
type PostResult struct {
	Posted            int              `json:"posted,omitempty"`
	WouldPost         int              `json:"would_post,omitempty"`
	Skipped           int              `json:"skipped"`
	TruncatedComments int              `json:"truncated_comments"`
	SummaryTruncated  bool             `json:"summary_truncated"`
	SkipReasons       []SkipReason     `json:"skip_reasons"`
	ReviewID          int64            `json:"review_id,omitempty"`
	CommitID          string           `json:"commit_id"`
	DryRun            bool             `json:"dry_run,omitempty"`
	Payload           *CreateReviewReq `json:"payload,omitempty"`
}

// FatalResult is the stdout JSON emitted by `validate-and-post` when helper
// exit code is 2.
type FatalResult struct {
	Error  string `json:"error"`
	Posted int    `json:"posted"`
}

// CreateReviewReq is the POST body we send to
// /repos/{owner}/{repo}/pulls/{n}/reviews. Kept minimal and exported so dry-run
// can embed it verbatim in PostResult.Payload.
type CreateReviewReq struct {
	CommitID string                  `json:"commit_id"`
	Body     string                  `json:"body"`
	Event    string                  `json:"event"` // always "COMMENT" in v1
	Comments []CreateReviewReqInline `json:"comments"`
}

type CreateReviewReqInline struct {
	Path      string `json:"path"`
	Line      int    `json:"line"`
	Side      string `json:"side"`
	Body      string `json:"body"`
	StartLine *int   `json:"start_line,omitempty"`
	StartSide *Side  `json:"start_side,omitempty"`
}

// FingerprintResult is the stdout JSON emitted by `fingerprint`.
type FingerprintResult struct {
	Language           string   `json:"language"`
	Confidence         string   `json:"confidence"`
	StyleSources       []string `json:"style_sources"`
	TestRunner         string   `json:"test_runner,omitempty"`
	Framework          string   `json:"framework,omitempty"`
	PRTouchedLanguages []string `json:"pr_touched_languages"`
	PRSubprojects      []string `json:"pr_subprojects"`
}
```

- [ ] **Step 3: Create error constants**

`shared/prreview/errors.go`:

```go
package prreview

// Error messages are kept as constants so tests can assert on exact strings
// without mirroring log formats.
const (
	ErrGitHubUnauth        = "GitHub token invalid or expired"
	ErrGitHubForbidden     = "Insufficient GitHub token scope (need PR write)"
	ErrGitHubNotFound      = "PR not found (404)"
	ErrGitHubStaleCommit   = "PR head moved during review (422); please re-trigger with current SHA"
	ErrGitHubRateLimit     = "GitHub rate-limited after 3 attempts (max 30s); please re-trigger later"
	ErrGitHubWallTime      = "GitHub rate-limited; wall time exceeded"
	ErrReviewSchemaInvalid = "review schema invalid"
	ErrMissingPRURL        = "PR_URL required"
	ErrMissingToken        = "GITHUB_TOKEN required"
	ErrGitRevParseFailed   = "git rev-parse HEAD failed"
)
```

- [ ] **Step 4: Write types round-trip tests**

`shared/prreview/types_test.go`:

```go
package prreview

import (
	"encoding/json"
	"testing"
)

func TestReviewJSON_SingleLineRoundTrip(t *testing.T) {
	original := ReviewJSON{
		Summary:         "LGTM.",
		SeveritySummary: SummaryClean,
		Comments: []CommentJSON{
			{
				Path:     "foo.go",
				Line:     10,
				Side:     SideRight,
				Body:     "nice",
				Severity: SeverityNit,
			},
		},
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got ReviewJSON
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.Comments[0].StartLine != nil || got.Comments[0].StartSide != nil {
		t.Errorf("expected start_line/start_side absent, got %v/%v",
			got.Comments[0].StartLine, got.Comments[0].StartSide)
	}
}

func TestReviewJSON_MultiLineRoundTrip(t *testing.T) {
	startLine := 10
	startSide := SideRight
	original := ReviewJSON{
		Summary:         "Multi-line concern.",
		SeveritySummary: SummaryMajor,
		Comments: []CommentJSON{
			{
				Path:      "bar.py",
				StartLine: &startLine,
				StartSide: &startSide,
				Line:      15,
				Side:      SideRight,
				Body:      "rewrite this block",
				Severity:  SeverityBlocker,
			},
		},
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got ReviewJSON
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.Comments[0].StartLine == nil || *got.Comments[0].StartLine != 10 {
		t.Errorf("start_line round-trip: got %v, want 10", got.Comments[0].StartLine)
	}
	if got.Comments[0].StartSide == nil || *got.Comments[0].StartSide != SideRight {
		t.Errorf("start_side round-trip: got %v, want RIGHT", got.Comments[0].StartSide)
	}
}

func TestPostResult_OmitEmpty(t *testing.T) {
	// Non-dry-run result should not include dry_run, would_post, payload
	result := PostResult{Posted: 3, CommitID: "abc"}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(data)
	for _, field := range []string{`"dry_run"`, `"would_post"`, `"payload"`} {
		if contains(s, field) {
			t.Errorf("non-dry-run result should omit %s, got %s", field, s)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
```

- [ ] **Step 5: Run tests to verify package builds and passes**

Run: `cd shared && go test ./prreview/... -run TestReviewJSON -v`

Expected: `PASS` for `TestReviewJSON_SingleLineRoundTrip`, `TestReviewJSON_MultiLineRoundTrip`, `TestPostResult_OmitEmpty`.

- [ ] **Step 6: Commit**

```bash
git add shared/prreview/
git commit -m "feat(prreview): add package scaffold with types and errors"
```

---

### Task 2: ReviewJSON validator

**Files:**
- Create: `shared/prreview/validator.go`
- Create: `shared/prreview/validator_test.go`

- [ ] **Step 1: Write failing validator test**

`shared/prreview/validator_test.go`:

```go
package prreview

import (
	"strings"
	"testing"
)

func TestValidate_OKSingleLine(t *testing.T) {
	r := ReviewJSON{
		Summary:         "good",
		SeveritySummary: SummaryMinor,
		Comments: []CommentJSON{
			{Path: "a.go", Line: 5, Side: SideRight, Body: "x", Severity: SeverityNit},
		},
	}
	if err := Validate(&r); err != nil {
		t.Fatalf("want nil, got %v", err)
	}
}

func TestValidate_OKMultiLine(t *testing.T) {
	sl := 3
	ss := SideRight
	r := ReviewJSON{
		Summary:         "good",
		SeveritySummary: SummaryMinor,
		Comments: []CommentJSON{
			{
				Path: "a.go", Line: 5, Side: SideRight, StartLine: &sl, StartSide: &ss,
				Body: "x", Severity: SeverityNit,
			},
		},
	}
	if err := Validate(&r); err != nil {
		t.Fatalf("want nil, got %v", err)
	}
}

func TestValidate_OKEmptyComments(t *testing.T) {
	r := ReviewJSON{Summary: "nothing to report", SeveritySummary: SummaryClean, Comments: []CommentJSON{}}
	if err := Validate(&r); err != nil {
		t.Fatalf("want nil, got %v", err)
	}
}

func TestValidate_MissingSummary(t *testing.T) {
	r := ReviewJSON{SeveritySummary: SummaryClean, Comments: []CommentJSON{}}
	err := Validate(&r)
	if err == nil || !strings.Contains(err.Error(), "summary") {
		t.Fatalf("want summary error, got %v", err)
	}
}

func TestValidate_BadSeveritySummary(t *testing.T) {
	r := ReviewJSON{Summary: "s", SeveritySummary: "bogus", Comments: []CommentJSON{}}
	err := Validate(&r)
	if err == nil || !strings.Contains(err.Error(), "severity_summary") {
		t.Fatalf("want severity_summary error, got %v", err)
	}
}

func TestValidate_BadCommentSeverity(t *testing.T) {
	r := ReviewJSON{
		Summary: "s", SeveritySummary: SummaryMinor,
		Comments: []CommentJSON{
			{Path: "a.go", Line: 1, Side: SideRight, Body: "x", Severity: "bogus"},
		},
	}
	err := Validate(&r)
	if err == nil || !strings.Contains(err.Error(), "severity") {
		t.Fatalf("want severity error, got %v", err)
	}
}

func TestValidate_BadSide(t *testing.T) {
	r := ReviewJSON{
		Summary: "s", SeveritySummary: SummaryMinor,
		Comments: []CommentJSON{
			{Path: "a.go", Line: 1, Side: "MIDDLE", Body: "x", Severity: SeverityNit},
		},
	}
	err := Validate(&r)
	if err == nil || !strings.Contains(err.Error(), "side") {
		t.Fatalf("want side error, got %v", err)
	}
}

func TestValidate_MultiLineHalfDefined(t *testing.T) {
	sl := 3
	r := ReviewJSON{
		Summary: "s", SeveritySummary: SummaryMinor,
		Comments: []CommentJSON{
			{Path: "a.go", Line: 5, Side: SideRight, StartLine: &sl, Body: "x", Severity: SeverityNit},
		},
	}
	err := Validate(&r)
	if err == nil || !strings.Contains(err.Error(), "start_line and start_side") {
		t.Fatalf("want half-defined error, got %v", err)
	}
}

func TestValidate_MultiLineMismatchedSides(t *testing.T) {
	sl := 3
	ss := SideLeft
	r := ReviewJSON{
		Summary: "s", SeveritySummary: SummaryMinor,
		Comments: []CommentJSON{
			{
				Path: "a.go", Line: 5, Side: SideRight, StartLine: &sl, StartSide: &ss,
				Body: "x", Severity: SeverityNit,
			},
		},
	}
	err := Validate(&r)
	if err == nil || !strings.Contains(err.Error(), "must match") {
		t.Fatalf("want mismatched-side error, got %v", err)
	}
}

func TestValidate_MultiLineStartAfterLine(t *testing.T) {
	sl := 10
	ss := SideRight
	r := ReviewJSON{
		Summary: "s", SeveritySummary: SummaryMinor,
		Comments: []CommentJSON{
			{
				Path: "a.go", Line: 5, Side: SideRight, StartLine: &sl, StartSide: &ss,
				Body: "x", Severity: SeverityNit,
			},
		},
	}
	err := Validate(&r)
	if err == nil || !strings.Contains(err.Error(), "start_line") {
		t.Fatalf("want start>line error, got %v", err)
	}
}
```

- [ ] **Step 2: Run tests to confirm they fail**

Run: `cd shared && go test ./prreview/... -run TestValidate -v`

Expected: compile error (`Validate` undefined) or multiple `FAIL`.

- [ ] **Step 3: Implement validator**

`shared/prreview/validator.go`:

```go
package prreview

import (
	"fmt"
)

// Validate enforces the ReviewJSON schema documented in
// docs/superpowers/specs/2026-04-21-github-pr-review-skill-design.md
// §Review JSON schema. Returns a human-readable error on the first failure;
// callers surface this through the helper's stderr on exit 2.
func Validate(r *ReviewJSON) error {
	if r == nil {
		return fmt.Errorf("review is nil")
	}
	if r.Summary == "" {
		return fmt.Errorf("summary must be non-empty")
	}
	switch r.SeveritySummary {
	case SummaryClean, SummaryMinor, SummaryMajor:
	default:
		return fmt.Errorf("severity_summary must be clean|minor|major, got %q", r.SeveritySummary)
	}
	if r.Comments == nil {
		return fmt.Errorf("comments must be an array (may be empty)")
	}
	for i, c := range r.Comments {
		if err := validateComment(&c); err != nil {
			return fmt.Errorf("comments[%d]: %w", i, err)
		}
	}
	return nil
}

func validateComment(c *CommentJSON) error {
	if c.Path == "" {
		return fmt.Errorf("path required")
	}
	if c.Line <= 0 {
		return fmt.Errorf("line must be > 0")
	}
	if c.Side != SideLeft && c.Side != SideRight {
		return fmt.Errorf("side must be LEFT|RIGHT, got %q", c.Side)
	}
	if c.Body == "" {
		return fmt.Errorf("body required")
	}
	switch c.Severity {
	case SeverityBlocker, SeveritySuggestion, SeverityNit:
	default:
		return fmt.Errorf("severity must be blocker|suggestion|nit, got %q", c.Severity)
	}
	// Multi-line rules: both present-or-both-absent.
	if (c.StartLine == nil) != (c.StartSide == nil) {
		return fmt.Errorf("start_line and start_side must both be present or both absent")
	}
	if c.StartLine != nil {
		if *c.StartSide != c.Side {
			return fmt.Errorf("start_side must match side for multi-line comments")
		}
		if *c.StartLine > c.Line {
			return fmt.Errorf("start_line must be <= line for multi-line comments")
		}
		if *c.StartLine <= 0 {
			return fmt.Errorf("start_line must be > 0")
		}
	}
	return nil
}
```

- [ ] **Step 4: Run tests to verify pass**

Run: `cd shared && go test ./prreview/... -run TestValidate -v`

Expected: all `TestValidate_*` tests PASS.

- [ ] **Step 5: Commit**

```bash
git add shared/prreview/validator.go shared/prreview/validator_test.go
git commit -m "feat(prreview): add review-JSON schema validator"
```

---

### Task 3: Fingerprint — local probes (language, style, test runner, framework)

**Files:**
- Create: `shared/prreview/fingerprint.go`
- Create: `shared/prreview/fingerprint_test.go`

- [ ] **Step 1: Write failing test for empty dir**

`shared/prreview/fingerprint_test.go` (start — more tests added in later steps):

```go
package prreview

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestFingerprintLocal_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	fp, err := fingerprintLocal(dir)
	if err != nil {
		t.Fatalf("fingerprintLocal: %v", err)
	}
	if fp.Language != "" {
		t.Errorf("empty dir: want language=\"\", got %q", fp.Language)
	}
	if fp.Confidence != "low" {
		t.Errorf("empty dir: want confidence=low, got %q", fp.Confidence)
	}
	if len(fp.StyleSources) != 0 {
		t.Errorf("empty dir: want no style_sources, got %v", fp.StyleSources)
	}
}

func TestFingerprintLocal_GoModHigh(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "go.mod"), "module x\n\ngo 1.25\n")
	mustWrite(t, filepath.Join(dir, "main.go"), "package main\n")
	mustWrite(t, filepath.Join(dir, "lib.go"), "package x\n")

	fp, err := fingerprintLocal(dir)
	if err != nil {
		t.Fatalf("fingerprintLocal: %v", err)
	}
	if fp.Language != "go" {
		t.Errorf("language: want go, got %q", fp.Language)
	}
	if fp.Confidence != "high" {
		t.Errorf("confidence: want high, got %q", fp.Confidence)
	}
	if fp.TestRunner != "go test" {
		t.Errorf("test_runner: want 'go test', got %q", fp.TestRunner)
	}
}

func TestFingerprintLocal_TSOverrideJS(t *testing.T) {
	dir := t.TempDir()
	pkg := `{"name":"x","dependencies":{"typescript":"^5.0.0"}}`
	mustWrite(t, filepath.Join(dir, "package.json"), pkg)
	mustWrite(t, filepath.Join(dir, "index.ts"), "export {}")

	fp, err := fingerprintLocal(dir)
	if err != nil {
		t.Fatalf("fingerprintLocal: %v", err)
	}
	if fp.Language != "ts" {
		t.Errorf("language: want ts, got %q", fp.Language)
	}
}

func TestFingerprintLocal_StyleSources(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "go.mod"), "module x\n\ngo 1.25\n")
	mustWrite(t, filepath.Join(dir, "CLAUDE.md"), "# rules")
	mustWrite(t, filepath.Join(dir, ".golangci.yml"), "linters: []\n")

	fp, err := fingerprintLocal(dir)
	if err != nil {
		t.Fatalf("fingerprintLocal: %v", err)
	}
	if !containsString(fp.StyleSources, "CLAUDE.md") {
		t.Errorf("want CLAUDE.md in style_sources, got %v", fp.StyleSources)
	}
	if !containsString(fp.StyleSources, ".golangci.yml") {
		t.Errorf("want .golangci.yml in style_sources, got %v", fp.StyleSources)
	}
}

func TestFingerprintLocal_FrameworkNext(t *testing.T) {
	dir := t.TempDir()
	pkg := `{"dependencies":{"next":"^14.0.0","react":"^18.0.0"}}`
	mustWrite(t, filepath.Join(dir, "package.json"), pkg)
	fp, err := fingerprintLocal(dir)
	if err != nil {
		t.Fatalf("fingerprintLocal: %v", err)
	}
	if fp.Framework != "next" {
		t.Errorf("framework: want next, got %q", fp.Framework)
	}
}

// Helpers.

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func containsString(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// Avoid "declared and not used" when later subtests add ctx-aware calls.
var _ = context.Background
```

- [ ] **Step 2: Run tests, expect fail**

Run: `cd shared && go test ./prreview/... -run TestFingerprintLocal -v`

Expected: compile error (`fingerprintLocal` undefined).

- [ ] **Step 3: Implement fingerprintLocal**

`shared/prreview/fingerprint.go`:

```go
package prreview

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// fingerprintLocal inspects a cloned repo on disk and returns the language,
// style sources, test runner, and framework best guesses. Missing files are
// not errors — they surface as zero values in FingerprintResult.
func fingerprintLocal(repoDir string) (*FingerprintResult, error) {
	fp := &FingerprintResult{
		StyleSources:       []string{},
		PRTouchedLanguages: []string{},
		PRSubprojects:      []string{},
	}

	manifest, lang := detectManifest(repoDir)
	extCounts := countExtensions(repoDir)

	switch {
	case lang != "" && extCounts[lang] > 0:
		fp.Language = lang
		fp.Confidence = "high"
	case lang != "":
		fp.Language = lang
		fp.Confidence = "medium"
	case len(extCounts) > 0:
		fp.Language = dominantExt(extCounts)
		fp.Confidence = "low"
	default:
		fp.Confidence = "low"
	}

	// TypeScript override: package.json with typescript in deps + .ts files.
	if manifest == "package.json" && hasDep(repoDir, "typescript") {
		fp.Language = "ts"
	}

	fp.StyleSources = detectStyleSources(repoDir)
	fp.TestRunner = detectTestRunner(repoDir, fp.Language)
	fp.Framework = detectFramework(repoDir)

	return fp, nil
}

// detectManifest returns the primary manifest filename found at repoDir root
// and the language it implies. Empty strings if none match.
func detectManifest(repoDir string) (manifest, lang string) {
	cands := []struct {
		file string
		lang string
	}{
		{"go.mod", "go"},
		{"package.json", "js"},
		{"pyproject.toml", "python"},
		{"setup.py", "python"},
		{"Cargo.toml", "rust"},
		{"Gemfile", "ruby"},
		{"pom.xml", "java"},
		{"build.gradle", "java"},
		{"build.gradle.kts", "java"},
	}
	for _, c := range cands {
		if fileExists(filepath.Join(repoDir, c.file)) {
			return c.file, c.lang
		}
	}
	return "", ""
}

func countExtensions(repoDir string) map[string]int {
	m := map[string]int{}
	extLang := map[string]string{
		".go":   "go",
		".py":   "python",
		".ts":   "ts",
		".tsx":  "ts",
		".js":   "js",
		".jsx":  "js",
		".rs":   "rust",
		".rb":   "ruby",
		".java": "java",
	}
	_ = filepath.WalkDir(repoDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // tolerate unreadable subdirs
		}
		if d.IsDir() {
			// Skip noisy dirs that would skew voting.
			name := d.Name()
			if name == ".git" || name == "node_modules" || name == "vendor" ||
				name == "target" || name == "dist" || name == "build" {
				return fs.SkipDir
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if l, ok := extLang[ext]; ok {
			m[l]++
		}
		return nil
	})
	return m
}

func dominantExt(counts map[string]int) string {
	var best string
	var bestN int
	for k, n := range counts {
		if n > bestN {
			best = k
			bestN = n
		}
	}
	return best
}

func hasDep(repoDir, depName string) bool {
	data, err := os.ReadFile(filepath.Join(repoDir, "package.json"))
	if err != nil {
		return false
	}
	var pkg struct {
		Dependencies    map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		return false
	}
	if _, ok := pkg.Dependencies[depName]; ok {
		return true
	}
	if _, ok := pkg.DevDependencies[depName]; ok {
		return true
	}
	return false
}

// detectStyleSources returns a list of style-relevant files present at repo
// root, in priority order. Nil-safe: returns empty slice, not nil.
func detectStyleSources(repoDir string) []string {
	cands := []string{
		"CLAUDE.md", "AGENTS.md", "CONTRIBUTING.md",
		".editorconfig",
		".golangci.yml", ".golangci.yaml",
		".eslintrc", ".eslintrc.js", ".eslintrc.json", ".eslintrc.yaml", ".eslintrc.yml",
		"ruff.toml", ".ruff.toml",
		"rustfmt.toml",
		".rubocop.yml",
		".prettierrc", ".prettierrc.json", ".prettierrc.yaml", ".prettierrc.yml",
	}
	out := []string{}
	for _, f := range cands {
		if fileExists(filepath.Join(repoDir, f)) {
			out = append(out, f)
		}
	}
	return out
}

func detectTestRunner(repoDir, lang string) string {
	switch lang {
	case "go":
		return "go test"
	case "python":
		return "pytest"
	case "rust":
		return "cargo test"
	case "ruby":
		return "rspec"
	case "java":
		return "mvn test"
	case "js", "ts":
		// Prefer package.json scripts.test if present.
		data, err := os.ReadFile(filepath.Join(repoDir, "package.json"))
		if err != nil {
			return "npm test"
		}
		var pkg struct {
			Scripts map[string]string `json:"scripts"`
		}
		if err := json.Unmarshal(data, &pkg); err == nil {
			if s, ok := pkg.Scripts["test"]; ok && s != "" {
				return s
			}
		}
		return "npm test"
	}
	return ""
}

func detectFramework(repoDir string) string {
	data, err := os.ReadFile(filepath.Join(repoDir, "package.json"))
	if err == nil {
		var pkg struct {
			Dependencies    map[string]string `json:"dependencies"`
			DevDependencies map[string]string `json:"devDependencies"`
		}
		if err := json.Unmarshal(data, &pkg); err == nil {
			for _, fw := range []string{"next", "react", "vue", "svelte", "nuxt", "express", "fastify"} {
				if _, ok := pkg.Dependencies[fw]; ok {
					return fw
				}
				if _, ok := pkg.DevDependencies[fw]; ok {
					return fw
				}
			}
		}
	}
	// Python: look for framework markers in pyproject.toml (shallow).
	if data, err := os.ReadFile(filepath.Join(repoDir, "pyproject.toml")); err == nil {
		s := string(data)
		for _, fw := range []string{"fastapi", "django", "flask"} {
			if strings.Contains(s, fw) {
				return fw
			}
		}
	}
	// Go: look for framework imports in go.mod.
	if data, err := os.ReadFile(filepath.Join(repoDir, "go.mod")); err == nil {
		s := string(data)
		for _, fw := range []string{"gin-gonic/gin", "labstack/echo", "gofiber/fiber"} {
			if strings.Contains(s, fw) {
				// Return short name.
				switch {
				case strings.Contains(fw, "gin"):
					return "gin"
				case strings.Contains(fw, "echo"):
					return "echo"
				case strings.Contains(fw, "fiber"):
					return "fiber"
				}
			}
		}
	}
	return ""
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
```

- [ ] **Step 4: Run tests to verify pass**

Run: `cd shared && go test ./prreview/... -run TestFingerprintLocal -v`

Expected: all `TestFingerprintLocal_*` tests PASS.

- [ ] **Step 5: Commit**

```bash
git add shared/prreview/fingerprint.go shared/prreview/fingerprint_test.go
git commit -m "feat(prreview): add local repo fingerprint (language, style, framework)"
```

---

### Task 4: HTTP retry wrapper

**Files:**
- Create: `shared/prreview/github.go`
- Create: `shared/prreview/github_test.go`

- [ ] **Step 1: Write failing tests**

`shared/prreview/github_test.go` (first batch):

```go
package prreview

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestHTTPCallRetry_SuccessFirstTry(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(200)
		_, _ = io.WriteString(w, "ok")
	}))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL, nil)
	resp, err := httpCallWithRetry(context.Background(), req, 5*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp.Body.Close()
	if hits != 1 {
		t.Errorf("want 1 hit, got %d", hits)
	}
}

func TestHTTPCallRetry_429ThenSuccess(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&hits, 1)
		if n == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(429)
			return
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL, nil)
	resp, err := httpCallWithRetry(context.Background(), req, 10*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp.Body.Close()
	if hits != 2 {
		t.Errorf("want 2 hits, got %d", hits)
	}
}

func TestHTTPCallRetry_429ExhaustsAttempts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(429)
	}))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL, nil)
	_, err := httpCallWithRetry(context.Background(), req, 30*time.Second)
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !strings.Contains(err.Error(), ErrGitHubRateLimit) {
		t.Errorf("error should mention rate-limit exhaustion, got %v", err)
	}
}

func TestHTTPCallRetry_WallTimeExceeded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "5")
		w.WriteHeader(429)
	}))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL, nil)
	_, err := httpCallWithRetry(context.Background(), req, 100*time.Millisecond)
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !strings.Contains(err.Error(), ErrGitHubWallTime) {
		t.Errorf("want wall time error, got %v", err)
	}
}

func TestHTTPCallRetry_403NonRateLimitDoesNotRetry(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(403)
		_, _ = io.WriteString(w, `{"message":"unrelated"}`)
	}))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL, nil)
	resp, err := httpCallWithRetry(context.Background(), req, 10*time.Second)
	if err != nil {
		t.Fatalf("want non-error response, got %v", err)
	}
	resp.Body.Close()
	if hits != 1 {
		t.Errorf("403 non-rate-limit should not retry, got %d hits", hits)
	}
}

func TestHTTPCallRetry_403RateLimitRetries(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&hits, 1)
		if n == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(403)
			_, _ = io.WriteString(w, `{"message":"You have exceeded a secondary rate limit."}`)
			return
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL, nil)
	resp, err := httpCallWithRetry(context.Background(), req, 10*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp.Body.Close()
	if hits != 2 {
		t.Errorf("want 2 hits, got %d", hits)
	}
}
```

- [ ] **Step 2: Run tests, expect fail**

Run: `cd shared && go test ./prreview/... -run TestHTTPCallRetry -v`

Expected: compile error (`httpCallWithRetry` undefined).

- [ ] **Step 3: Implement HTTP retry**

`shared/prreview/github.go` (initial — more added in Task 5/6/8):

```go
package prreview

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// DefaultMaxWallTime is the per-invocation ceiling for all retries combined.
// Exposed for tests; production callers should pass 30*time.Second.
const DefaultMaxWallTime = 30 * time.Second

// RetryAfterCap prevents an adversarial server from stalling a helper
// invocation for minutes. If Retry-After asks for longer, we give up.
const RetryAfterCap = 10 * time.Second

var fallbackDelays = []time.Duration{0, 2 * time.Second, 4 * time.Second}

// httpCallWithRetry executes req with GitHub-aware retry:
//   - 429 → retry (honor Retry-After header; cap at RetryAfterCap; fallback 2s/4s)
//   - 403 with secondary-rate-limit body → retry same way
//   - 5xx transient → retry
//   - Network error → retry
//   - Anything else → return response (caller handles)
//
// Max 3 attempts. Overall wall time bounded by maxWallTime.
// Request body must be re-readable between attempts — callers should use
// bytes.Buffer / bytes.Reader or set req.GetBody.
func httpCallWithRetry(ctx context.Context, req *http.Request, maxWallTime time.Duration) (*http.Response, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	deadline := time.Now().Add(maxWallTime)

	var lastResp *http.Response
	var lastErr error

	for attempt := 0; attempt < len(fallbackDelays); attempt++ {
		if attempt > 0 {
			wait := fallbackDelays[attempt]
			if lastResp != nil {
				if ra := parseRetryAfter(lastResp.Header); ra > 0 {
					wait = minDuration(ra, RetryAfterCap)
				}
			}
			if time.Now().Add(wait).After(deadline) {
				return nil, fmt.Errorf("%s: %w", ErrGitHubWallTime, lastErr)
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(wait):
			}
		}

		// Reset body on retries so subsequent Do calls can re-read it.
		if attempt > 0 && req.GetBody != nil {
			body, err := req.GetBody()
			if err != nil {
				return nil, fmt.Errorf("reset body: %w", err)
			}
			req.Body = body
		}

		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			lastResp = nil
			// Network-level error — retry unless context cancelled.
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			continue
		}

		if !shouldRetry(resp) {
			return resp, nil
		}

		// Consume + close body so we can retry with a fresh connection.
		bodyBytes, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		resp.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		lastResp = resp
		lastErr = fmt.Errorf("status %d", resp.StatusCode)
	}

	return nil, fmt.Errorf("%s: %w", ErrGitHubRateLimit, lastErr)
}

func shouldRetry(resp *http.Response) bool {
	switch resp.StatusCode {
	case 429:
		return true
	case 502, 503, 504:
		return true
	case 403:
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		resp.Body = io.NopCloser(bytes.NewReader(body))
		low := strings.ToLower(string(body))
		return strings.Contains(low, "secondary rate limit") ||
			strings.Contains(low, "abuse detection")
	}
	return false
}

func parseRetryAfter(h http.Header) time.Duration {
	v := h.Get("Retry-After")
	if v == "" {
		return 0
	}
	if n, err := strconv.Atoi(v); err == nil {
		return time.Duration(n) * time.Second
	}
	// Retry-After may be an HTTP-date; ignore and fall back to exponential.
	return 0
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
```

- [ ] **Step 4: Run tests, verify pass**

Run: `cd shared && go test ./prreview/... -run TestHTTPCallRetry -v`

Expected: all PASS. (Tests use `Retry-After: 0` / `1` to keep wall time short.)

- [ ] **Step 5: Commit**

```bash
git add shared/prreview/github.go shared/prreview/github_test.go
git commit -m "feat(prreview): add GitHub-aware HTTP retry with Retry-After + wall-time cap"
```

---

### Task 5: Fingerprint — PR-aware fields (GitHub API)

**Files:**
- Modify: `shared/prreview/fingerprint.go`
- Modify: `shared/prreview/fingerprint_test.go`
- Modify: `shared/prreview/github.go` (add `listDiffFiles` used here + by Task 6)

- [ ] **Step 1: Write failing tests using httptest**

Append to `shared/prreview/fingerprint_test.go`:

```go
import (
	"io"
	"net/http"
	"net/http/httptest"
)

func TestFingerprint_PRAware(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "go.mod"), "module x\n\ngo 1.25\n")
	mustWrite(t, filepath.Join(dir, "main.go"), "package main\n")
	// Simulate a subproject with its own manifest.
	mustWrite(t, filepath.Join(dir, "services/billing/package.json"), `{"name":"billing"}`)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/pulls/42/files") {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `[
			{"filename":"services/billing/index.py","status":"modified","patch":"@@ -1 +1 @@\n-old\n+new"},
			{"filename":"docs/intro.md","status":"added","patch":"@@ -0,0 +1 @@\n+hello"}
		]`)
	}))
	defer srv.Close()

	ctx := context.Background()
	fp, err := Fingerprint(ctx, dir, srv.URL+"/repos/x/y/pulls/42", "tok", fingerprintOptions{apiBase: srv.URL})
	if err != nil {
		t.Fatalf("Fingerprint: %v", err)
	}
	if !containsString(fp.PRTouchedLanguages, "python") {
		t.Errorf("want python in pr_touched_languages, got %v", fp.PRTouchedLanguages)
	}
	if !containsString(fp.PRTouchedLanguages, "markdown") {
		t.Errorf("want markdown in pr_touched_languages, got %v", fp.PRTouchedLanguages)
	}
	if !containsString(fp.PRSubprojects, "services/billing") {
		t.Errorf("want services/billing in pr_subprojects, got %v", fp.PRSubprojects)
	}
}
```

- [ ] **Step 2: Run, expect fail (compile error or missing behavior)**

Run: `cd shared && go test ./prreview/... -run TestFingerprint_PRAware -v`

Expected: compile error (`Fingerprint` / `fingerprintOptions` undefined).

- [ ] **Step 3: Add `listDiffFiles` in github.go**

Append to `shared/prreview/github.go`:

```go
import (
	"encoding/json"
	"net/url"
	"regexp"
)

// PRFile is the minimal shape we need from GET /pulls/:n/files.
type PRFile struct {
	Filename string `json:"filename"`
	Status   string `json:"status"`
	Patch    string `json:"patch"`
}

var prURLPattern = regexp.MustCompile(`github\.com/([^/]+)/([^/]+)/pull/(\d+)`)

// parsePRURL extracts (owner, repo, number) from a full PR URL.
func parsePRURL(s string) (owner, repo string, number int, err error) {
	m := prURLPattern.FindStringSubmatch(s)
	if m == nil {
		return "", "", 0, fmt.Errorf("not a github.com PR URL: %q", s)
	}
	n, err := strconv.Atoi(m[3])
	if err != nil {
		return "", "", 0, fmt.Errorf("invalid PR number in %q: %w", s, err)
	}
	return m[1], m[2], n, nil
}

// listDiffFiles fetches `GET /repos/{owner}/{repo}/pulls/{n}/files` with
// pagination. Caller passes the base API URL (https://api.github.com for
// production, httptest.Server.URL for tests).
func listDiffFiles(ctx context.Context, apiBase, prURL, token string, maxWallTime time.Duration) ([]PRFile, error) {
	owner, repo, num, err := parsePRURL(prURL)
	if err != nil {
		return nil, err
	}

	var all []PRFile
	page := 1
	for {
		u := fmt.Sprintf("%s/repos/%s/%s/pulls/%d/files?per_page=100&page=%d",
			strings.TrimRight(apiBase, "/"), url.PathEscape(owner), url.PathEscape(repo), num, page)
		req, _ := http.NewRequestWithContext(ctx, "GET", u, nil)
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}

		resp, err := httpCallWithRetry(ctx, req, maxWallTime)
		if err != nil {
			return nil, err
		}

		if resp.StatusCode == 401 {
			_ = resp.Body.Close()
			return nil, fmt.Errorf("%s", ErrGitHubUnauth)
		}
		if resp.StatusCode == 403 {
			_ = resp.Body.Close()
			return nil, fmt.Errorf("%s", ErrGitHubForbidden)
		}
		if resp.StatusCode == 404 {
			_ = resp.Body.Close()
			return nil, fmt.Errorf("%s", ErrGitHubNotFound)
		}
		if resp.StatusCode >= 400 {
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			return nil, fmt.Errorf("list files: %d %s", resp.StatusCode, string(body))
		}

		var page1 []PRFile
		if err := json.NewDecoder(resp.Body).Decode(&page1); err != nil {
			_ = resp.Body.Close()
			return nil, fmt.Errorf("decode list files: %w", err)
		}
		_ = resp.Body.Close()

		all = append(all, page1...)
		if len(page1) < 100 {
			break
		}
		page++
		if page > 20 {
			// Safety: 20 * 100 = 2000 files is already far beyond any
			// reviewable PR; stop rather than loop forever.
			break
		}
	}
	return all, nil
}
```

- [ ] **Step 4: Add `Fingerprint` wrapper + PR-aware fields**

Append to `shared/prreview/fingerprint.go`:

```go
import (
	"context"
	"time"
)

// fingerprintOptions is for tests to inject a mock GitHub API base URL.
// Production callers pass the zero value.
type fingerprintOptions struct {
	apiBase string
}

// Fingerprint returns a full FingerprintResult combining local probes with
// PR-aware fields fetched from the GitHub API.
func Fingerprint(ctx context.Context, repoDir, prURL, token string, opts fingerprintOptions) (*FingerprintResult, error) {
	fp, err := fingerprintLocal(repoDir)
	if err != nil {
		return nil, err
	}

	apiBase := opts.apiBase
	if apiBase == "" {
		apiBase = "https://api.github.com"
	}
	files, err := listDiffFiles(ctx, apiBase, prURL, token, DefaultMaxWallTime)
	if err != nil {
		// Don't fail the whole fingerprint on API error — surface empty
		// PR-aware fields + let the caller (agent) decide what to do.
		return fp, nil
	}

	fp.PRTouchedLanguages = prTouchedLanguages(files)
	fp.PRSubprojects = prSubprojects(repoDir, files)
	return fp, nil
}

func prTouchedLanguages(files []PRFile) []string {
	extLang := map[string]string{
		".go": "go", ".py": "python", ".ts": "ts", ".tsx": "ts",
		".js": "js", ".jsx": "js", ".rs": "rust", ".rb": "ruby",
		".java": "java", ".md": "markdown", ".yml": "yaml", ".yaml": "yaml",
		".toml": "toml", ".json": "json",
	}
	seen := map[string]bool{}
	out := []string{}
	for _, f := range files {
		ext := strings.ToLower(filepath.Ext(f.Filename))
		if l, ok := extLang[ext]; ok && !seen[l] {
			seen[l] = true
			out = append(out, l)
		}
	}
	return out
}

func prSubprojects(repoDir string, files []PRFile) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, f := range files {
		// Walk upward from the file's dir looking for a manifest file; record
		// the closest subdir (not the repo root itself).
		dir := filepath.Dir(f.Filename)
		for dir != "." && dir != "/" && dir != "" {
			if hasManifest(filepath.Join(repoDir, dir)) {
				if !seen[dir] {
					seen[dir] = true
					out = append(out, dir)
				}
				break
			}
			dir = filepath.Dir(dir)
		}
	}
	return out
}

func hasManifest(dir string) bool {
	for _, m := range []string{"go.mod", "package.json", "pyproject.toml", "Cargo.toml", "Gemfile", "pom.xml", "build.gradle"} {
		if fileExists(filepath.Join(dir, m)) {
			return true
		}
	}
	return false
}
```

- [ ] **Step 5: Run tests, verify pass**

Run: `cd shared && go test ./prreview/... -run TestFingerprint -v`

Expected: `TestFingerprintLocal_*` + `TestFingerprint_PRAware` PASS.

- [ ] **Step 6: Commit**

```bash
git add shared/prreview/fingerprint.go shared/prreview/fingerprint_test.go shared/prreview/github.go
git commit -m "feat(prreview): add PR-aware fingerprint fields via GitHub API"
```

---

### Task 6: Diff hunk parsing — build `path → valid (line, side)` map

**Files:**
- Modify: `shared/prreview/github.go` (add `parseDiffMap`)
- Modify: `shared/prreview/github_test.go`

- [ ] **Step 1: Write failing tests**

Append to `shared/prreview/github_test.go`:

```go
func TestParseDiffMap_AddedAndContextLines(t *testing.T) {
	files := []PRFile{
		{
			Filename: "foo.go",
			Patch: "@@ -5,3 +5,4 @@\n" +
				" context\n" +
				"-old\n" +
				"+added1\n" +
				"+added2\n",
		},
	}
	m := parseDiffMap(files)
	valid := m["foo.go"]

	// Expected on RIGHT side (new file): line 5 (context), 6, 7 (added)
	for _, ln := range []int{5, 6, 7} {
		if !valid.has(ln, string(SideRight)) {
			t.Errorf("want (line=%d, RIGHT) to be valid", ln)
		}
	}
	// Removed line "old" is at base line 6 (LEFT).
	if !valid.has(6, string(SideLeft)) {
		t.Errorf("want (line=6, LEFT) valid for removed line")
	}
}

func TestParseDiffMap_MultipleHunks(t *testing.T) {
	files := []PRFile{
		{
			Filename: "bar.py",
			Patch: "@@ -1,2 +1,3 @@\n" +
				" a\n" +
				"+b\n" +
				" c\n" +
				"@@ -10,1 +11,2 @@\n" +
				"+z\n" +
				" y\n",
		},
	}
	m := parseDiffMap(files)
	valid := m["bar.py"]

	// Hunk 1: RIGHT lines 1,2,3.
	for _, ln := range []int{1, 2, 3} {
		if !valid.has(ln, string(SideRight)) {
			t.Errorf("hunk1: want (line=%d, RIGHT) valid", ln)
		}
	}
	// Hunk 2: RIGHT lines 11, 12.
	for _, ln := range []int{11, 12} {
		if !valid.has(ln, string(SideRight)) {
			t.Errorf("hunk2: want (line=%d, RIGHT) valid", ln)
		}
	}
}

func TestParseDiffMap_EmptyPatch(t *testing.T) {
	files := []PRFile{{Filename: "binary.png", Patch: ""}}
	m := parseDiffMap(files)
	if v, ok := m["binary.png"]; !ok || v == nil {
		t.Fatalf("want empty valid-set entry for %q, got %+v", "binary.png", m)
	}
	if len(m["binary.png"].set) != 0 {
		t.Errorf("want 0 valid lines for empty patch, got %d", len(m["binary.png"].set))
	}
}
```

- [ ] **Step 2: Run, expect fail**

Run: `cd shared && go test ./prreview/... -run TestParseDiffMap -v`

Expected: compile error (`parseDiffMap` / `validLines.has` undefined).

- [ ] **Step 3: Implement parseDiffMap**

Append to `shared/prreview/github.go`:

```go
// validLines holds the (line, side) tuples that may host a comment on one file.
type validLines struct {
	set map[string]bool
}

func newValidLines() *validLines { return &validLines{set: map[string]bool{}} }

func (v *validLines) add(line int, side string) {
	v.set[fmt.Sprintf("%d:%s", line, side)] = true
}

func (v *validLines) has(line int, side string) bool {
	return v.set[fmt.Sprintf("%d:%s", line, side)]
}

// parseDiffMap converts PRFile slice into `{path → validLines}`. Patch format is
// GitHub's unified-diff-ish representation: one or more `@@ -lL,lC +rL,rC @@`
// hunk headers followed by ' ' / '+' / '-' prefixed lines.
func parseDiffMap(files []PRFile) map[string]*validLines {
	out := map[string]*validLines{}
	for _, f := range files {
		out[f.Filename] = parsePatch(f.Patch)
	}
	return out
}

var hunkHeaderPattern = regexp.MustCompile(`^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@`)

func parsePatch(patch string) *validLines {
	v := newValidLines()
	lines := strings.Split(patch, "\n")
	var leftLine, rightLine int
	for _, ln := range lines {
		if m := hunkHeaderPattern.FindStringSubmatch(ln); m != nil {
			leftLine, _ = strconv.Atoi(m[1])
			rightLine, _ = strconv.Atoi(m[2])
			continue
		}
		if ln == "" {
			continue
		}
		switch ln[0] {
		case '+':
			v.add(rightLine, string(SideRight))
			rightLine++
		case '-':
			v.add(leftLine, string(SideLeft))
			leftLine++
		case ' ':
			// Context line: valid to comment on RIGHT (conventional default).
			v.add(rightLine, string(SideRight))
			leftLine++
			rightLine++
		}
	}
	return v
}
```

- [ ] **Step 4: Run, verify pass**

Run: `cd shared && go test ./prreview/... -run TestParseDiffMap -v`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add shared/prreview/github.go shared/prreview/github_test.go
git commit -m "feat(prreview): parse PR patch into valid (line, side) map for inline comments"
```

---

### Task 7: Comment validation + truncation

**Files:**
- Create: `shared/prreview/review.go`
- Create: `shared/prreview/review_test.go`

- [ ] **Step 1: Write failing tests**

`shared/prreview/review_test.go` (first batch):

```go
package prreview

import (
	"strings"
	"testing"
)

func TestFilterAndTruncate_AllValid(t *testing.T) {
	files := []PRFile{
		{Filename: "a.go", Patch: "@@ -1,1 +1,2 @@\n a\n+b\n"},
	}
	diff := parseDiffMap(files)
	r := ReviewJSON{
		Summary:         "ok",
		SeveritySummary: SummaryMinor,
		Comments: []CommentJSON{
			{Path: "a.go", Line: 2, Side: SideRight, Body: "nit", Severity: SeverityNit},
		},
	}
	posted, skips, trunc, _ := filterAndTruncate(&r, diff)
	if len(posted) != 1 || len(skips) != 0 {
		t.Errorf("want 1 posted, 0 skipped, got %d / %d", len(posted), len(skips))
	}
	if trunc != 0 {
		t.Errorf("want 0 truncated, got %d", trunc)
	}
}

func TestFilterAndTruncate_LineOutsideDiff(t *testing.T) {
	diff := parseDiffMap([]PRFile{{Filename: "a.go", Patch: "@@ -1,1 +1,2 @@\n a\n+b\n"}})
	r := ReviewJSON{
		Summary: "ok", SeveritySummary: SummaryMinor,
		Comments: []CommentJSON{
			{Path: "a.go", Line: 99, Side: SideRight, Body: "x", Severity: SeverityNit},
		},
	}
	posted, skips, _, _ := filterAndTruncate(&r, diff)
	if len(posted) != 0 || len(skips) != 1 {
		t.Fatalf("want 0 posted 1 skipped, got %d / %d", len(posted), len(skips))
	}
	if !strings.Contains(skips[0].Reason, "not in diff") {
		t.Errorf("reason text: got %q", skips[0].Reason)
	}
}

func TestFilterAndTruncate_FileNotInDiff(t *testing.T) {
	diff := parseDiffMap([]PRFile{{Filename: "a.go", Patch: "@@ -1 +1 @@\n a\n"}})
	r := ReviewJSON{
		Summary: "ok", SeveritySummary: SummaryMinor,
		Comments: []CommentJSON{
			{Path: "other.go", Line: 1, Side: SideRight, Body: "x", Severity: SeverityNit},
		},
	}
	posted, skips, _, _ := filterAndTruncate(&r, diff)
	if len(posted) != 0 || len(skips) != 1 {
		t.Fatalf("want 0 posted 1 skipped, got %d / %d", len(posted), len(skips))
	}
	if !strings.Contains(skips[0].Reason, "file not in diff") {
		t.Errorf("reason text: got %q", skips[0].Reason)
	}
}

func TestFilterAndTruncate_MultiLineSideMismatchSkipped(t *testing.T) {
	// Multi-line comment where start_side != side would fail Validate(), but
	// filterAndTruncate defensively handles the case where the line validation
	// fails because RIGHT lines 1-2 are valid but LEFT 1-2 are not.
	diff := parseDiffMap([]PRFile{{Filename: "a.go", Patch: "@@ -1 +1,2 @@\n a\n+b\n"}})
	sl := 1
	ss := SideLeft
	r := ReviewJSON{
		Summary: "ok", SeveritySummary: SummaryMinor,
		Comments: []CommentJSON{
			{
				Path: "a.go", Line: 2, Side: SideLeft,
				StartLine: &sl, StartSide: &ss,
				Body: "x", Severity: SeverityNit,
			},
		},
	}
	posted, skips, _, _ := filterAndTruncate(&r, diff)
	if len(posted) != 0 || len(skips) != 1 {
		t.Fatalf("want skip, got %d / %d", len(posted), len(skips))
	}
}

func TestFilterAndTruncate_CommentBodyTruncated(t *testing.T) {
	diff := parseDiffMap([]PRFile{{Filename: "a.go", Patch: "@@ -1 +1,2 @@\n a\n+b\n"}})
	long := strings.Repeat("x", MaxCommentBody+200)
	r := ReviewJSON{
		Summary: "ok", SeveritySummary: SummaryMinor,
		Comments: []CommentJSON{
			{Path: "a.go", Line: 2, Side: SideRight, Body: long, Severity: SeverityNit},
		},
	}
	posted, _, trunc, _ := filterAndTruncate(&r, diff)
	if trunc != 1 {
		t.Errorf("want truncated_comments=1, got %d", trunc)
	}
	if len(posted) != 1 {
		t.Fatalf("want 1 posted, got %d", len(posted))
	}
	if !strings.HasSuffix(posted[0].Body, commentTruncSuffix) {
		t.Errorf("truncated body should end with suffix, got %q", posted[0].Body[len(posted[0].Body)-50:])
	}
}

func TestFilterAndTruncate_SummaryTruncated(t *testing.T) {
	long := strings.Repeat("y", MaxSummaryBody+200)
	r := ReviewJSON{
		Summary: long, SeveritySummary: SummaryClean,
		Comments: []CommentJSON{},
	}
	_, _, _, summaryTrunc := filterAndTruncate(&r, map[string]*validLines{})
	if !summaryTrunc {
		t.Errorf("summary should be truncated")
	}
	if !strings.HasSuffix(r.Summary, summaryTruncSuffix) {
		t.Errorf("truncated summary should end with suffix, got %q", r.Summary[len(r.Summary)-60:])
	}
}
```

- [ ] **Step 2: Run, expect fail**

Run: `cd shared && go test ./prreview/... -run TestFilterAndTruncate -v`

Expected: compile error.

- [ ] **Step 3: Implement**

`shared/prreview/review.go`:

```go
package prreview

// filterAndTruncate takes the validated review + diff map and produces the
// list of comments that will actually go into the POST payload, plus the
// SkipReasons for anything dropped. It also truncates over-long comment
// bodies and the summary, mutating `r.Summary` in place (helper is one-shot,
// so this is fine and simpler than copying the struct).
//
// Returns:
//   - posted: []CreateReviewReqInline ready to send
//   - skipped: []SkipReason
//   - truncatedComments: how many comment bodies were truncated
//   - summaryTruncated: whether the summary itself was truncated
func filterAndTruncate(r *ReviewJSON, diff map[string]*validLines) (
	posted []CreateReviewReqInline, skipped []SkipReason, truncatedComments int, summaryTruncated bool,
) {
	// Truncate summary first.
	if len(r.Summary) > MaxSummaryBody {
		r.Summary = r.Summary[:MaxSummaryBody] + summaryTruncSuffix
		summaryTruncated = true
	}

	for _, c := range r.Comments {
		valid, ok := diff[c.Path]
		if !ok {
			skipped = append(skipped, SkipReason{Path: c.Path, Line: c.Line, Reason: "file not in diff"})
			continue
		}
		// Multi-line: verify both ends; range check was done by Validate.
		if c.StartLine != nil {
			if !valid.has(*c.StartLine, string(*c.StartSide)) {
				skipped = append(skipped, SkipReason{Path: c.Path, Line: *c.StartLine, Reason: "start_line/side not in diff"})
				continue
			}
		}
		if !valid.has(c.Line, string(c.Side)) {
			skipped = append(skipped, SkipReason{Path: c.Path, Line: c.Line, Reason: "line/side not in diff"})
			continue
		}

		body := c.Body
		if len(body) > MaxCommentBody {
			body = body[:MaxCommentBody] + commentTruncSuffix
			truncatedComments++
		}
		posted = append(posted, CreateReviewReqInline{
			Path:      c.Path,
			Line:      c.Line,
			Side:      string(c.Side),
			Body:      body,
			StartLine: c.StartLine,
			StartSide: c.StartSide,
		})
	}
	return posted, skipped, truncatedComments, summaryTruncated
}
```

- [ ] **Step 4: Run, verify pass**

Run: `cd shared && go test ./prreview/... -run TestFilterAndTruncate -v`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add shared/prreview/review.go shared/prreview/review_test.go
git commit -m "feat(prreview): validate comments against diff and truncate oversized content"
```

---

### Task 8: PostReview orchestrator — end-to-end

**Files:**
- Modify: `shared/prreview/review.go`
- Modify: `shared/prreview/github.go` (add `createReview`)
- Modify: `shared/prreview/review_test.go`

- [ ] **Step 1: Write failing tests**

Append to `shared/prreview/review_test.go`:

```go
import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
)

func TestValidateAndPost_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/pulls/42/files") && r.Method == "GET":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `[{"filename":"a.go","status":"modified","patch":"@@ -1 +1,2 @@\n a\n+b\n"}]`)
		case strings.Contains(r.URL.Path, "/pulls/42/reviews") && r.Method == "POST":
			body, _ := io.ReadAll(r.Body)
			var got CreateReviewReq
			_ = json.Unmarshal(body, &got)
			if got.Event != "COMMENT" {
				t.Errorf("event: want COMMENT, got %q", got.Event)
			}
			if got.CommitID != "deadbeef" {
				t.Errorf("commit_id: want deadbeef, got %q", got.CommitID)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id": 12345}`)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	r := ReviewJSON{
		Summary: "ok", SeveritySummary: SummaryMinor,
		Comments: []CommentJSON{
			{Path: "a.go", Line: 2, Side: SideRight, Body: "nit", Severity: SeverityNit},
		},
	}
	res, err := ValidateAndPost(context.Background(), ValidateAndPostInput{
		Review:   &r,
		PRURL:    srv.URL + "/repos/x/y/pull/42",
		CommitID: "deadbeef",
		Token:    "tok",
		APIBase:  srv.URL,
	})
	if err != nil {
		t.Fatalf("ValidateAndPost: %v", err)
	}
	if res.Posted != 1 {
		t.Errorf("posted: want 1, got %d", res.Posted)
	}
	if res.ReviewID != 12345 {
		t.Errorf("review_id: want 12345, got %d", res.ReviewID)
	}
	if res.CommitID != "deadbeef" {
		t.Errorf("commit_id: want deadbeef, got %q", res.CommitID)
	}
	if res.DryRun {
		t.Error("expected DryRun=false")
	}
}

func TestValidateAndPost_DryRun(t *testing.T) {
	var postHits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/pulls/42/files") && r.Method == "GET" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `[{"filename":"a.go","status":"modified","patch":"@@ -1 +1,2 @@\n a\n+b\n"}]`)
			return
		}
		if r.Method == "POST" {
			postHits++
		}
		t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()

	r := ReviewJSON{
		Summary: "ok", SeveritySummary: SummaryMinor,
		Comments: []CommentJSON{
			{Path: "a.go", Line: 2, Side: SideRight, Body: "nit", Severity: SeverityNit},
		},
	}
	res, err := ValidateAndPost(context.Background(), ValidateAndPostInput{
		Review:   &r,
		PRURL:    srv.URL + "/repos/x/y/pull/42",
		CommitID: "deadbeef",
		Token:    "tok",
		APIBase:  srv.URL,
		DryRun:   true,
	})
	if err != nil {
		t.Fatalf("ValidateAndPost(DryRun): %v", err)
	}
	if !res.DryRun {
		t.Error("want DryRun=true")
	}
	if res.WouldPost != 1 {
		t.Errorf("would_post: want 1, got %d", res.WouldPost)
	}
	if res.Payload == nil {
		t.Error("Payload should be populated in dry-run")
	}
	if postHits != 0 {
		t.Errorf("POST should never be called in dry-run, got %d", postHits)
	}
}

func TestValidateAndPost_StaleCommit422(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/files") {
			_, _ = io.WriteString(w, `[{"filename":"a.go","status":"modified","patch":"@@ -1 +1,2 @@\n a\n+b\n"}]`)
			return
		}
		w.WriteHeader(422)
		_, _ = io.WriteString(w, `{"message":"commit_id not in PR"}`)
	}))
	defer srv.Close()

	r := ReviewJSON{
		Summary: "ok", SeveritySummary: SummaryMinor,
		Comments: []CommentJSON{
			{Path: "a.go", Line: 2, Side: SideRight, Body: "nit", Severity: SeverityNit},
		},
	}
	_, err := ValidateAndPost(context.Background(), ValidateAndPostInput{
		Review:   &r,
		PRURL:    srv.URL + "/repos/x/y/pull/42",
		CommitID: "stale",
		Token:    "tok",
		APIBase:  srv.URL,
	})
	if err == nil {
		t.Fatal("want error on 422")
	}
	if !strings.Contains(err.Error(), ErrGitHubStaleCommit) {
		t.Errorf("want stale-commit error, got %v", err)
	}
}

func TestValidateAndPost_Unauth401(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
	}))
	defer srv.Close()

	r := ReviewJSON{Summary: "ok", SeveritySummary: SummaryClean, Comments: []CommentJSON{}}
	_, err := ValidateAndPost(context.Background(), ValidateAndPostInput{
		Review:   &r,
		PRURL:    srv.URL + "/repos/x/y/pull/42",
		CommitID: "x",
		Token:    "bad",
		APIBase:  srv.URL,
	})
	if err == nil || !strings.Contains(err.Error(), ErrGitHubUnauth) {
		t.Errorf("want unauth error, got %v", err)
	}
}

// Unused import guard.
var _ = bytes.NewReader
```

- [ ] **Step 2: Run, expect fail**

Run: `cd shared && go test ./prreview/... -run TestValidateAndPost -v`

Expected: compile error (`ValidateAndPost` / `ValidateAndPostInput` / `createReview` undefined).

- [ ] **Step 3: Add createReview in github.go**

Append to `shared/prreview/github.go`:

```go
// createReview POSTs /pulls/:n/reviews. Returns review ID on 2xx; maps known
// failure statuses to constants from errors.go.
func createReview(ctx context.Context, apiBase, prURL, token string, payload *CreateReviewReq, maxWallTime time.Duration) (int64, error) {
	owner, repo, num, err := parsePRURL(prURL)
	if err != nil {
		return 0, err
	}
	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return 0, fmt.Errorf("marshal review: %w", err)
	}
	u := fmt.Sprintf("%s/repos/%s/%s/pulls/%d/reviews",
		strings.TrimRight(apiBase, "/"), url.PathEscape(owner), url.PathEscape(repo), num)

	req, err := http.NewRequestWithContext(ctx, "POST", u, bytes.NewReader(bodyBytes))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	// Allow retries to re-send the body.
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(bodyBytes)), nil
	}

	resp, err := httpCallWithRetry(ctx, req, maxWallTime)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case 401:
		return 0, fmt.Errorf("%s", ErrGitHubUnauth)
	case 403:
		return 0, fmt.Errorf("%s", ErrGitHubForbidden)
	case 404:
		return 0, fmt.Errorf("%s", ErrGitHubNotFound)
	case 422:
		return 0, fmt.Errorf("%s", ErrGitHubStaleCommit)
	}
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return 0, fmt.Errorf("create review: %d %s", resp.StatusCode, string(body))
	}
	var ok struct {
		ID int64 `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&ok); err != nil {
		return 0, fmt.Errorf("decode review response: %w", err)
	}
	return ok.ID, nil
}
```

- [ ] **Step 4: Add ValidateAndPost in review.go**

Append to `shared/prreview/review.go`:

```go
import (
	"context"
	"fmt"
)

// ValidateAndPostInput aggregates every parameter ValidateAndPost needs. Keeps
// the cobra wiring trivial.
type ValidateAndPostInput struct {
	Review   *ReviewJSON
	PRURL    string
	CommitID string // agent's git rev-parse HEAD; helper does not fetch this
	Token    string
	APIBase  string // override for tests; production uses https://api.github.com
	DryRun   bool
}

// ValidateAndPost runs schema validation, fetches the PR diff, filters /
// truncates comments against the diff, assembles the POST payload, and either
// sends it (real mode) or returns it (dry run).
func ValidateAndPost(ctx context.Context, in ValidateAndPostInput) (*PostResult, error) {
	if in.Review == nil {
		return nil, fmt.Errorf("%s: review nil", ErrReviewSchemaInvalid)
	}
	if in.PRURL == "" {
		return nil, fmt.Errorf("%s", ErrMissingPRURL)
	}
	if in.Token == "" {
		return nil, fmt.Errorf("%s", ErrMissingToken)
	}
	if err := Validate(in.Review); err != nil {
		return nil, fmt.Errorf("%s: %w", ErrReviewSchemaInvalid, err)
	}

	apiBase := in.APIBase
	if apiBase == "" {
		apiBase = "https://api.github.com"
	}

	files, err := listDiffFiles(ctx, apiBase, in.PRURL, in.Token, DefaultMaxWallTime)
	if err != nil {
		return nil, err
	}
	diff := parseDiffMap(files)

	inline, skips, trunc, summaryTrunc := filterAndTruncate(in.Review, diff)

	payload := &CreateReviewReq{
		CommitID: in.CommitID,
		Body:     in.Review.Summary,
		Event:    "COMMENT",
		Comments: inline,
	}

	res := &PostResult{
		Skipped:           len(skips),
		TruncatedComments: trunc,
		SummaryTruncated:  summaryTrunc,
		SkipReasons:       skips,
		CommitID:          in.CommitID,
	}

	if in.DryRun {
		res.DryRun = true
		res.WouldPost = len(inline)
		res.Payload = payload
		return res, nil
	}

	id, err := createReview(ctx, apiBase, in.PRURL, in.Token, payload, DefaultMaxWallTime)
	if err != nil {
		return nil, err
	}
	res.Posted = len(inline)
	res.ReviewID = id
	return res, nil
}
```

- [ ] **Step 5: Run all prreview tests, verify pass**

Run: `cd shared && go test ./prreview/... -v`

Expected: all tests PASS.

- [ ] **Step 6: Commit**

```bash
git add shared/prreview/review.go shared/prreview/github.go shared/prreview/review_test.go
git commit -m "feat(prreview): add ValidateAndPost orchestrator with dry-run support"
```

---

### Task 9: cobra subcommand wiring — root `pr-review-helper`

**Files:**
- Create: `cmd/agentdock/pr_review_helper.go`

- [ ] **Step 1: Create subcommand scaffold**

`cmd/agentdock/pr_review_helper.go`:

```go
package main

import (
	"github.com/spf13/cobra"
)

var prReviewHelperCmd = &cobra.Command{
	Use:    "pr-review-helper",
	Short:  "Internal helper invoked by the github-pr-review skill",
	Hidden: true, // not exposed in `agentdock --help` top level
	Long: "pr-review-helper hosts the deterministic parts of the PR review " +
		"workflow: repo fingerprinting and the validate-before-post step for " +
		"inline comments. The github-pr-review skill invokes these subcommands; " +
		"end users should not run them directly.",
}

func init() {
	rootCmd.AddCommand(prReviewHelperCmd)
}
```

- [ ] **Step 2: Confirm it registers**

Run: `cd . && go run ./cmd/agentdock pr-review-helper --help`

Expected: usage text mentioning `pr-review-helper`. No errors.

- [ ] **Step 3: Commit**

```bash
git add cmd/agentdock/pr_review_helper.go
git commit -m "feat(cmd): add hidden pr-review-helper subcommand scaffold"
```

---

### Task 10: `pr-review-helper fingerprint` subcommand

**Files:**
- Modify: `cmd/agentdock/pr_review_helper.go`

- [ ] **Step 1: Add fingerprint subcommand**

Append to `cmd/agentdock/pr_review_helper.go`:

```go
import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/Ivantseng123/agentdock/shared/prreview"
)

var (
	fpPRURL string
)

var prReviewFingerprintCmd = &cobra.Command{
	Use:   "fingerprint",
	Short: "Inspect the cloned repo + PR to emit a fingerprint JSON on stdout",
	Long: "fingerprint runs in the cloned repo (cwd). It probes the local " +
		"filesystem for language/style/framework markers, then fetches the PR's " +
		"files list from GitHub to compute pr_touched_languages and pr_subprojects.",
	RunE: func(cmd *cobra.Command, args []string) error {
		if fpPRURL == "" {
			return fmt.Errorf("--pr-url is required")
		}
		token := os.Getenv("GITHUB_TOKEN")
		if token == "" {
			return fmt.Errorf("%s", prreview.ErrMissingToken)
		}
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		fp, err := prreview.Fingerprint(cmd.Context(), cwd, fpPRURL, token, prreview.FingerprintOptions{})
		if err != nil {
			return err
		}
		out, err := json.Marshal(fp)
		if err != nil {
			return err
		}
		fmt.Println(string(out))
		return nil
	},
}

func init() {
	prReviewFingerprintCmd.Flags().StringVar(&fpPRURL, "pr-url", "", "GitHub PR URL (required)")
	prReviewHelperCmd.AddCommand(prReviewFingerprintCmd)
}
```

- [ ] **Step 2: Expose FingerprintOptions for the command**

Modify `shared/prreview/fingerprint.go` — rename `fingerprintOptions` → `FingerprintOptions` (public) and adjust function signature. Also rename field `apiBase` → `APIBase`.

```go
// Rename:
//   type fingerprintOptions struct { apiBase string }
// To:
type FingerprintOptions struct {
	APIBase string // defaults to https://api.github.com
}
```

Update `Fingerprint`:

```go
func Fingerprint(ctx context.Context, repoDir, prURL, token string, opts FingerprintOptions) (*FingerprintResult, error) {
	// ...
	apiBase := opts.APIBase
	// ... rest unchanged
}
```

Update `fingerprint_test.go` call sites: `fingerprintOptions{apiBase: srv.URL}` → `FingerprintOptions{APIBase: srv.URL}`.

- [ ] **Step 3: Run tests**

Run: `cd shared && go test ./prreview/... -v` and `cd . && go build ./cmd/...`

Expected: tests PASS; build succeeds.

- [ ] **Step 4: Manual smoke test**

```bash
cd /tmp && mkdir -p fp-smoke && cd fp-smoke && git init -q
echo "module x" > go.mod
GITHUB_TOKEN=tok ../../path/to/agentdock pr-review-helper fingerprint \
  --pr-url "https://github.com/example/repo/pull/1"
```

Expected: JSON output with `language: go` (if GITHUB_TOKEN is real and PR exists; otherwise the tool fails on the API step — that's fine, we're only smoke-testing subcommand wiring).

- [ ] **Step 5: Commit**

```bash
git add cmd/agentdock/pr_review_helper.go shared/prreview/fingerprint.go shared/prreview/fingerprint_test.go
git commit -m "feat(cmd): add pr-review-helper fingerprint subcommand"
```

---

### Task 11: `pr-review-helper validate-and-post` subcommand

**Files:**
- Modify: `cmd/agentdock/pr_review_helper.go`

- [ ] **Step 1: Add validate-and-post subcommand**

Append to `cmd/agentdock/pr_review_helper.go`:

```go
import (
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
	"strings"
)

var (
	vapPRURL  string
	vapDryRun bool
)

var prReviewValidateAndPostCmd = &cobra.Command{
	Use:   "validate-and-post",
	Short: "Read review JSON from stdin, validate against PR diff, then POST to GitHub",
	Long: "Reads a ReviewJSON document on stdin, fetches the PR's files from " +
		"GitHub, drops comments on lines outside the diff, truncates over-long " +
		"content, and submits the review as a single POST. Helper always uses " +
		"`git rev-parse HEAD` in cwd as commit_id — base or app-provided SHAs " +
		"are ignored.",
	RunE: func(cmd *cobra.Command, args []string) error {
		if vapPRURL == "" {
			return fmt.Errorf("--pr-url is required")
		}
		token := os.Getenv("GITHUB_TOKEN")
		if token == "" {
			return fmt.Errorf("%s", prreview.ErrMissingToken)
		}

		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return fmt.Errorf("read stdin: %w", err)
		}
		var review prreview.ReviewJSON
		if err := json.Unmarshal(bytes.TrimSpace(data), &review); err != nil {
			return fmt.Errorf("%s: %w", prreview.ErrReviewSchemaInvalid, err)
		}

		commitID, err := gitRevParseHEAD(cmd.Context())
		if err != nil {
			return fmt.Errorf("%s: %w", prreview.ErrGitRevParseFailed, err)
		}

		dryRun := vapDryRun
		if !dryRun && os.Getenv("DRY_RUN") == "1" {
			dryRun = true
		}

		res, err := prreview.ValidateAndPost(cmd.Context(), prreview.ValidateAndPostInput{
			Review:   &review,
			PRURL:    vapPRURL,
			CommitID: commitID,
			Token:    token,
			DryRun:   dryRun,
		})
		if err != nil {
			// Exit 2 with a FatalResult JSON on stdout; cobra's default
			// Printerror goes to stderr.
			out, _ := json.Marshal(prreview.FatalResult{Error: err.Error(), Posted: 0})
			fmt.Println(string(out))
			// Skip cobra's own error print.
			cmd.SilenceErrors = true
			cmd.SilenceUsage = true
			os.Exit(2)
		}
		out, _ := json.Marshal(res)
		fmt.Println(string(out))

		// Exit 1 if any comment was skipped but the review still posted.
		if !dryRun && res.Skipped > 0 {
			os.Exit(1)
		}
		if dryRun && res.Skipped > 0 {
			os.Exit(1)
		}
		return nil
	},
}

// gitRevParseHEAD runs `git rev-parse HEAD` in cwd. If cwd is not a git repo
// this returns an error; worker flows always run in a clone, so that's a
// programmer error, not a user-facing one.
func gitRevParseHEAD(ctx context.Context) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return "", fmt.Errorf("git rev-parse HEAD: %s", strings.TrimSpace(string(exitErr.Stderr)))
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func init() {
	prReviewValidateAndPostCmd.Flags().StringVar(&vapPRURL, "pr-url", "", "GitHub PR URL (required)")
	prReviewValidateAndPostCmd.Flags().BoolVar(&vapDryRun, "dry-run", false, "Validate + show payload; do not POST. DRY_RUN=1 env var also enables.")
	prReviewHelperCmd.AddCommand(prReviewValidateAndPostCmd)
}
```

- [ ] **Step 2: Compile & spot-test with --help**

Run: `cd . && go build ./cmd/... && ./agentdock pr-review-helper validate-and-post --help`

Expected: usage text without errors.

- [ ] **Step 3: Run full test suite**

Run: `cd shared && go test ./prreview/... -v` + `cd . && go test ./...`

Expected: all tests PASS (including `test/import_direction_test.go`).

- [ ] **Step 4: Manual dry-run smoke test**

```bash
cd /path/to/some/cloned/repo
cat <<'EOF' | GITHUB_TOKEN=ghp_... ./agentdock pr-review-helper validate-and-post \
    --pr-url "https://github.com/OWNER/REPO/pull/N" --dry-run
{
  "summary": "Looks fine.",
  "severity_summary": "clean",
  "comments": []
}
EOF
```

Expected: JSON on stdout with `dry_run: true`, `would_post: 0`, `payload` populated with `event: "COMMENT"`. No POST to GitHub.

- [ ] **Step 5: Commit**

```bash
git add cmd/agentdock/pr_review_helper.go
git commit -m "feat(cmd): add pr-review-helper validate-and-post subcommand"
```

---

### Task 12: SKILL.md + `skills.yaml` registration

**Files:**
- Create: `agents/skills/github-pr-review/SKILL.md`
- Modify: `skills.yaml`

- [ ] **Step 1: Write SKILL.md**

`agents/skills/github-pr-review/SKILL.md`:

````markdown
---
name: github-pr-review
description: Review a GitHub pull request — fingerprint the repository's language and style conventions, analyze the diff, and post line-level comments plus a summary review back to the PR via `agentdock pr-review-helper`. Invoked by the PR Review workflow when a user asks `@bot review <PR URL>`.
---

# GitHub PR Review

Review a pull request by fingerprinting the repo, analyzing the diff, and
posting a `COMMENT`-event review back to GitHub. This is a mostly hands-off
workflow — minimize questions to the user.

## Input

You will receive a prompt containing:

- **Thread Context**: Slack messages leading to this review request
- **PR URL**: full `https://github.com/{owner}/{repo}/pull/{number}` URL
- **PR Number**: numeric PR number
- **Repository**: already cloned at the PR's HEAD on disk (cwd)
- **Environment**: `GITHUB_TOKEN` set; `PR_URL` set to the full URL

The helper `agentdock pr-review-helper` is on PATH (it is this worker's binary).

## Process

### 1. Fingerprint the repo

```bash
agentdock pr-review-helper fingerprint --pr-url "$PR_URL" > /tmp/fp.json
```

Read `/tmp/fp.json`. It contains `language`, `confidence`, `style_sources`,
`test_runner`, `framework`, `pr_touched_languages`, `pr_subprojects`.

Why: reviewing generic code without knowing the project's conventions produces
boilerplate feedback. The fingerprint tells you what rules to apply.

If `style_sources` is empty, note in the review summary that "no project style
file found; reviewing against general language conventions." If `pr_subprojects`
is non-empty, read those subdirectories' manifests / linter configs too — a
monorepo may have per-service rules.

### 2. Decide whether to short-circuit

Before looking at the diff, check whether detailed review makes sense:

- Pure lockfile (`yarn.lock`, `pnpm-lock.yaml`, `go.sum`, `Cargo.lock`, …)
- Generated files (anything in `vendor/`, `third_party/`, `node_modules/`, or
  files with `Code generated` header)
- Pure docs (`docs/`, `*.md`, `README*`)
- Pure CI config (`.github/workflows/*.yml`) — use judgement; workflows can
  have subtle bugs

If the diff is fully in one of these categories, skip to step 7 and emit
`===REVIEW_RESULT===` with `status: SKIPPED`, a short `reason`, and a summary
explaining why.

Why: line-level review on a 30K-line generated file is noise; honest skip is
better than fake feedback.

### 3. Analyze the diff

Fetch the diff:

```bash
# The helper also fetches this internally; you can inspect it directly for planning.
gh_api_url="https://api.github.com/repos/{owner}/{repo}/pulls/{number}/files"
```

Or just read files on disk (they're already checked out at PR head). Use
`git diff origin/<base>..HEAD` if you need to see the exact hunk shape.

For each concerning change, prepare a comment candidate:
- `path`: repo-relative file path
- `line`: the line number on the appropriate side
- `side`: `RIGHT` for added/context lines, `LEFT` for removed lines
- `start_line` + `start_side` (optional): for multi-line comments spanning 2+ lines
- `body`: markdown explanation; may include a ```suggestion block (see step 4)
- `severity`: `blocker`, `suggestion`, or `nit`

Severity guidance:
- **blocker**: correctness / security / data loss; reviewer would hard-block merge
- **suggestion**: a clearly better way to do it; reviewer would push back in a meeting
- **nit**: style / taste; reviewer would mention in passing

Why: the skill's downstream logic (summary severity + Slack display) depends
on honest severity calls. Don't call everything a blocker.

### 4. Use `suggestion` blocks when the fix is mechanical

GitHub renders ````suggestion ```` fenced blocks as "Commit suggestion"
buttons. When you know the exact replacement, include one:

````markdown
This should null-check `result`:

```suggestion
if result == nil {
    return ErrNotFound
}
return result.Value
```
````

The suggestion block must be a compilable/runnable replacement for the exact
lines the comment covers. Single-line comments replace one line; multi-line
comments (with `start_line`) replace the full range. Fuzzy hints stay in prose.

Why: suggestions turn review from passive to actionable. A reviewer who tells
you "add null-check here" is useful; one who also gives the code to paste is
*valuable*.

### 5. Compute the severity summary

- `clean` if zero findings
- `minor` if only `nit` comments
- `major` if any `blocker` or any `suggestion`

Why: the workflow's Slack result message and dashboards read this field. Keep
it honest; inflated severity trains reviewers to ignore bot output.

### 6. Assemble and post the review

Build the review JSON:

```json
{
  "summary": "One-sentence overall assessment.\n\n**Issues (N)**: X blockers, Y suggestions, Z nits.\n\n<optional detail sentence>",
  "severity_summary": "minor",
  "comments": [
    {
      "path": "services/api/handler.go",
      "line": 45,
      "side": "RIGHT",
      "body": "Null-check needed…",
      "severity": "blocker"
    }
  ]
}
```

Summary length ≤ 2000 chars. Individual comment bodies ≤ 4000 chars. The
helper truncates with `(truncated)` markers if you exceed either, but aim to
stay under.

Pipe to the helper:

```bash
cat review.json | agentdock pr-review-helper validate-and-post --pr-url "$PR_URL"
```

Read the helper's stdout JSON:
- `posted`: how many comments landed
- `skipped`: how many dropped (typically because their line wasn't in the diff)
- `skip_reasons`: list of `{path, line, reason}` for operators
- `review_id`: the GitHub review ID

Exit codes:
- `0`: all comments posted
- `1`: review posted, some comments skipped — still a success
- `2`: fatal (auth / 422 / rate limit exhaustion); nothing posted

Why pipe through the helper: it validates every comment's line against the
actual diff before posting. If you hallucinate a line number, the helper drops
that comment rather than posting garbage to the PR.

### 7. Emit the result marker

Output `===REVIEW_RESULT===` followed by a JSON object. Three shapes per the
status:

**POSTED** (review landed):

```json
{
  "status": "POSTED",
  "summary": "<same text posted to GitHub>",
  "comments_posted": 12,
  "comments_skipped": 3,
  "severity_summary": "minor"
}
```

**SKIPPED** (short-circuited in step 2):

```json
{
  "status": "SKIPPED",
  "summary": "Diff is vendored/generated; detailed review skipped.",
  "reason": "lockfile_only"
}
```

Valid `reason`: `lockfile_only`, `vendored`, `generated`, `pure_docs`,
`pure_config`.

**ERROR** (helper exit 2):

```json
{
  "status": "ERROR",
  "error": "PR head moved during review (422); please re-trigger",
  "summary": "<what you would have posted, for operators to see>"
}
```

Do not retry; the user re-mentions `@bot review <URL>` manually if they want.

## Special cases

- **Closed / merged PR**: review anyway. Note in summary that review is
  historical ("Reviewing a merged PR — suggestions are for learning only").
- **Force-push mid-review**: helper exits 2 with 422. Emit `status: ERROR`.
- **Empty diff after filtering** (every comment's line was outside the diff):
  helper returns `posted: 0`, `skipped: N`. Still emit `status: POSTED` — the
  summary alone posts fine, and `comments_skipped` reflects the quality problem.
````

- [ ] **Step 2: Add skills.yaml entry**

Modify `skills.yaml`:

```yaml
skills:
  triage-issue:
    type: local
    path: agents/skills/triage-issue
  github-pr-review:
    type: local
    path: agents/skills/github-pr-review

cache:
  ttl: 5m
```

- [ ] **Step 3: Verify skill loader picks it up**

Run: `cd app && go test ./skill/... -v`

Expected: all skill loader tests PASS. The local type entries need no loader change.

- [ ] **Step 4: Commit**

```bash
git add agents/skills/github-pr-review/SKILL.md skills.yaml
git commit -m "feat(skill): add github-pr-review SKILL.md + register in skills.yaml"
```

---

### Task 13: Skill-level eval baseline

**Files:**
- Create: `agents/skills/github-pr-review/evals/evals.json`

- [ ] **Step 1: Write evals.json**

`agents/skills/github-pr-review/evals/evals.json`:

```json
{
  "skill_name": "github-pr-review",
  "evals": [
    {
      "id": 1,
      "prompt": "Review the pull request at https://github.com/Ivantseng123/agentdock/pull/EXAMPLE_A — a Go PR that violates the import-direction rule documented in the repository's CLAUDE.md (app/ imports worker/). Post a review with line-level comments identifying the violation.",
      "expected_output": "===REVIEW_RESULT=== with status=POSTED, severity_summary=major, at least one inline comment on the violating import line, and a summary that explicitly mentions CLAUDE.md or the import-direction rule.",
      "files": [],
      "assertions": [
        {"kind": "marker_present", "marker": "===REVIEW_RESULT==="},
        {"kind": "json_has_field", "field": "status", "equals": "POSTED"},
        {"kind": "json_has_field", "field": "severity_summary", "equals": "major"},
        {"kind": "json_field_gte", "field": "comments_posted", "value": 1},
        {"kind": "summary_mentions", "needles": ["CLAUDE.md", "import direction"]}
      ]
    },
    {
      "id": 2,
      "prompt": "Review the pull request at https://github.com/Ivantseng123/agentdock/pull/EXAMPLE_B — a Python PR adding an untested helper function, in a repository with no linter configs and no CLAUDE.md.",
      "expected_output": "===REVIEW_RESULT=== with status=POSTED, severity_summary=minor or major, summary notes the lack of style guidance, at least one comment suggests adding a test.",
      "files": [],
      "assertions": [
        {"kind": "marker_present", "marker": "===REVIEW_RESULT==="},
        {"kind": "json_has_field", "field": "status", "equals": "POSTED"},
        {"kind": "json_field_in", "field": "severity_summary", "values": ["minor", "major"]},
        {"kind": "summary_mentions", "needles": ["no project style file", "general"]},
        {"kind": "any_comment_mentions", "needles": ["test", "coverage"]}
      ]
    },
    {
      "id": 3,
      "prompt": "Review the pull request at https://github.com/Ivantseng123/agentdock/pull/EXAMPLE_C — a PR whose diff is 100% `pnpm-lock.yaml` changes from a routine dependency bump.",
      "expected_output": "===REVIEW_RESULT=== with status=SKIPPED, reason=lockfile_only, no inline comments, summary explains why detailed review was skipped.",
      "files": [],
      "assertions": [
        {"kind": "marker_present", "marker": "===REVIEW_RESULT==="},
        {"kind": "json_has_field", "field": "status", "equals": "SKIPPED"},
        {"kind": "json_has_field", "field": "reason", "equals": "lockfile_only"}
      ]
    }
  ]
}
```

Note: `EXAMPLE_A/B/C` are placeholders. Before running evals for real, replace
each with a persistent fixture PR URL. During the implementation PR, leave them
as placeholders and document in the PR description what fixtures are needed.

- [ ] **Step 2: Commit**

```bash
git add agents/skills/github-pr-review/evals/evals.json
git commit -m "test(skill): add evals.json baseline for github-pr-review"
```

- [ ] **Step 3: Final verify — full test + lint**

Run in sequence:

```bash
cd shared && go test ./... -v
cd ..
go test ./test/... -v   # import_direction_test, module boundary
go build ./cmd/...
```

Expected: all green. `test/import_direction_test.go` is the critical check —
it confirms `shared/prreview/` doesn't accidentally import `app/` or `worker/`.

- [ ] **Step 4: No code commit needed; document completion**

If everything passes, this plan is complete. The skill is merged; the parent
spec's PR 6 (PRReviewWorkflow) can pick up from here in a separate session.

---

## Self-Review

Quick gaps check against the spec:

- **§Skill anatomy**: Task 12 creates `SKILL.md` + `evals/`; no `scripts/`, no `references/` (deferred). ✓
- **§SKILL.md structure**: Task 12 body covers the 8-step process, skipping short-circuit, summary guidance, suggestion blocks. ✓
- **§Helper `fingerprint`**: Tasks 3+5 implement; Task 10 wires CLI. ✓
- **§Helper `validate-and-post`**: Tasks 7+8 implement; Task 11 wires CLI with `--dry-run` + `DRY_RUN=1`. ✓
- **§Review JSON schema**: Task 1 types + Task 2 validator. ✓
- **§Result marker contract (POSTED/SKIPPED/ERROR)**: Task 12 SKILL.md instructs the agent. No Go code needed — the app-side parser lives in PR Review workflow (out of scope). ✓
- **§Rate limit / retry policy**: Task 4 implements; tests cover 429, 429-exhaustion, wall-time, 403-rate-limit, 403-non-rate-limit. ✓
- **§Content length limits**: Task 7 implements truncation in `filterAndTruncate`; constants live in `types.go` (Task 1). ✓
- **§Go package structure**: Tasks 1-8 lay out the files as specified. ✓
- **§Integration (skills.yaml)**: Task 12. ✓
- **§Integration (Dockerfile)**: no changes needed per spec; confirmed. ✓
- **§Testing — Layer 1 unit tests**: covered throughout Tasks 1-8. ✓
- **§Testing — Layer 2 skill evals**: Task 13 writes `evals.json`; running against real fixtures is post-plan. ✓
- **§Testing — Layer 3 E2E smoke**: intentionally deferred per spec. ✓

No `TODO` / `TBD` / "handle edge cases"-style placeholders in the plan.
Type names match across tasks (`ReviewJSON`, `CreateReviewReq`, etc. are used
consistently with how they were defined in Task 1).

---

## Execution handoff

The plan is saved to `docs/superpowers/plans/2026-04-21-github-pr-review-skill.md`. Two execution options:

1. **Subagent-Driven (recommended)** — dispatch a fresh subagent per task, review between tasks, fast iteration.
2. **Inline Execution** — execute tasks in a continuing session using executing-plans, batch execution with checkpoints.

Per this session's scope, neither execution is run here — this plan is handed off to a later session. Pick an option when the next session opens this file.
