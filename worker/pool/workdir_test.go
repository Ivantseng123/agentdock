package pool

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Ivantseng123/agentdock/shared/queue"
)

func TestEmptyDirProvider_PrepareAndCleanup(t *testing.T) {
	p := &EmptyDirProvider{}
	job := &queue.Job{ID: "j1"}

	dir, err := p.Prepare(job)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if dir == "" {
		t.Fatal("empty dir path")
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("expected dir to exist: %v", err)
	}

	// Writable?
	if err := os.WriteFile(filepath.Join(dir, "marker"), []byte("x"), 0644); err != nil {
		t.Errorf("dir not writable: %v", err)
	}

	p.Cleanup(dir)
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("Cleanup did not remove dir")
	}
}

func TestSelectProvider_ChoosesByCloneURL(t *testing.T) {
	tests := []struct {
		name     string
		cloneURL string
		wantKind string
	}{
		{"empty clone URL → EmptyDirProvider", "", "empty"},
		{"repo URL → RepoCloneProvider", "https://github.com/foo/bar.git", "clone"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo := &fakeRepoProvider{}
			got := selectProvider(&queue.Job{CloneURL: tc.cloneURL}, repo, "")
			switch tc.wantKind {
			case "empty":
				if _, ok := got.(*EmptyDirProvider); !ok {
					t.Errorf("want *EmptyDirProvider, got %T", got)
				}
			case "clone":
				if _, ok := got.(*RepoCloneProvider); !ok {
					t.Errorf("want *RepoCloneProvider, got %T", got)
				}
			}
		})
	}
}

type fakePrepareAtCall struct {
	cloneURL string
	branch   string
	token    string
	target   string
}

type fakeRepoProvider struct {
	prepared struct {
		cloneURL string
		branch   string
		token    string
	}
	cleaned string

	// PrepareAt-related state. behavior is the per-call hook for tests that
	// want to selectively fail clones; nil = default success (mkdir target).
	prepareAtCalls    []fakePrepareAtCall
	prepareAtBehavior func(target string) error
	removed           []string
}

func (f *fakeRepoProvider) Prepare(cloneURL, branch, token string) (string, error) {
	f.prepared.cloneURL = cloneURL
	f.prepared.branch = branch
	f.prepared.token = token
	return "/tmp/fake-repo", nil
}

func (f *fakeRepoProvider) PrepareAt(cloneURL, branch, token, targetPath string) error {
	f.prepareAtCalls = append(f.prepareAtCalls, fakePrepareAtCall{
		cloneURL: cloneURL, branch: branch, token: token, target: targetPath,
	})
	if f.prepareAtBehavior != nil {
		return f.prepareAtBehavior(targetPath)
	}
	// Default: succeed and create the target dir so callers see a real path
	// they can RemoveAll later.
	return os.MkdirAll(targetPath, 0755)
}

func (f *fakeRepoProvider) RemoveWorktree(p string) error {
	f.cleaned = p
	f.removed = append(f.removed, p)
	return nil
}
func (f *fakeRepoProvider) CleanAll() error   { return nil }
func (f *fakeRepoProvider) PurgeStale() error { return nil }

func TestRepoCloneProvider_ForwardsArgs(t *testing.T) {
	fake := &fakeRepoProvider{}
	provider := &RepoCloneProvider{Repo: fake, Token: "tkn"}
	job := &queue.Job{CloneURL: "https://github.com/foo/bar.git", Branch: "main"}

	got, err := provider.Prepare(job)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if got != "/tmp/fake-repo" {
		t.Errorf("path = %q, want /tmp/fake-repo", got)
	}
	if fake.prepared.cloneURL != "https://github.com/foo/bar.git" {
		t.Errorf("cloneURL = %q", fake.prepared.cloneURL)
	}
	if fake.prepared.branch != "main" {
		t.Errorf("branch = %q", fake.prepared.branch)
	}
	if fake.prepared.token != "tkn" {
		t.Errorf("token = %q", fake.prepared.token)
	}

	provider.Cleanup(got)
	if fake.cleaned != "/tmp/fake-repo" {
		t.Errorf("cleaned = %q, want /tmp/fake-repo", fake.cleaned)
	}
}

// TestSkillMountInEmptyDir_ForEachRunner validates that for each registered
// agent's skill_dir convention, mountSkills lays out files correctly in a
// non-git empty directory. This test gates PR 5 (Ask workflow): if any
// runner's path layout fails here, EmptyDirProvider needs a fallback
// (git init / HOME mount) before Ask ships.
//
// This is a pure filesystem-layout assertion — no real CLI binaries are
// launched. If mountSkills ever grows a dependency on `git` commands or
// other non-empty-dir semantics, at least one of the four sub-tests will
// trip before Phase 5 regresses.
func TestSkillMountInEmptyDir_ForEachRunner(t *testing.T) {
	providers := []struct {
		name     string
		skillDir string
	}{
		{"claude", ".claude/skills"},
		{"codex", ".agents/skills"},
		{"gemini", ".gemini/skills"},
		{"opencode", ".agents/skills"},
	}

	for _, tc := range providers {
		t.Run(tc.name, func(t *testing.T) {
			emptyProvider := &EmptyDirProvider{}
			job := &queue.Job{
				ID: "spike-" + tc.name,
				Skills: map[string]*queue.SkillPayload{
					"spike-skill": {
						Files: map[string][]byte{
							"SKILL.md": []byte("---\nname: spike-skill\n---\ndetector"),
						},
					},
				},
			}

			dir, err := emptyProvider.Prepare(job)
			if err != nil {
				t.Fatalf("Prepare: %v", err)
			}
			defer emptyProvider.Cleanup(dir)

			// Mount the skill using the same mountSkills function executor.go uses.
			if err := mountSkills(dir, job.Skills, tc.skillDir); err != nil {
				t.Fatalf("mountSkills: %v", err)
			}

			// Assert file exists at the expected path.
			want := filepath.Join(dir, tc.skillDir, "spike-skill", "SKILL.md")
			if _, err := os.Stat(want); err != nil {
				t.Errorf("skill file not found at %s: %v", want, err)
			}
		})
	}
}

// TestPrepareRefs_PartialSuccess ensures one failed ref clone is absorbed
// into UnavailableRefs while successful refs flow through into RefRepos.
// This is the core "best-effort" contract from spec §4.3 / Q3.
func TestPrepareRefs_PartialSuccess(t *testing.T) {
	tmp := t.TempDir()
	primary := filepath.Join(tmp, "triage-repo-abc")
	if err := os.MkdirAll(primary, 0755); err != nil {
		t.Fatalf("setup primary: %v", err)
	}

	fake := &fakeRepoProvider{
		prepareAtBehavior: func(target string) error {
			if strings.Contains(target, "broken__repo") {
				return fmt.Errorf("simulated clone fail")
			}
			return os.MkdirAll(target, 0755)
		},
	}
	refs := []queue.RefRepo{
		{Repo: "frontend/web", CloneURL: "u1", Branch: "main"},
		{Repo: "broken/repo", CloneURL: "u2"},
		{Repo: "backend/api", CloneURL: "u3", Branch: "release"},
	}
	successful, successfulPaths, unavailable, refsRoot, err :=
		prepareRefs(fake, primary, "tkn", refs, slog.Default())
	if err != nil {
		t.Fatalf("prepareRefs returned err: %v", err)
	}

	if len(successful) != 2 {
		t.Errorf("successful count = %d, want 2 (refs=%+v)", len(successful), successful)
	}
	if len(unavailable) != 1 || unavailable[0] != "broken/repo" {
		t.Errorf("unavailable = %v, want [broken/repo]", unavailable)
	}
	if len(successfulPaths) != 2 {
		t.Errorf("successfulPaths len = %d, want 2", len(successfulPaths))
	}
	if !strings.HasSuffix(refsRoot, "-refs") {
		t.Errorf("refs root naming wrong: %s", refsRoot)
	}
	if !strings.HasSuffix(successful[0].Path, "frontend__web") {
		t.Errorf("ref dir naming wrong for ref[0]: %s", successful[0].Path)
	}
	if successful[0].Branch != "main" {
		t.Errorf("ref[0] branch = %q, want main", successful[0].Branch)
	}
	if successful[1].Branch != "release" {
		t.Errorf("ref[1] branch = %q, want release", successful[1].Branch)
	}

	// Token is forwarded for every PrepareAt call.
	for i, c := range fake.prepareAtCalls {
		if c.token != "tkn" {
			t.Errorf("prepareAtCalls[%d].token = %q, want tkn", i, c.token)
		}
	}
}

// TestPrepareRefs_EmptyRefs_NoOp asserts the function is a clean no-op
// when no refs are requested — refsRoot stays empty (no mkdir) and no
// PrepareAt calls fire.
func TestPrepareRefs_EmptyRefs_NoOp(t *testing.T) {
	fake := &fakeRepoProvider{}
	successful, successfulPaths, unavailable, refsRoot, err :=
		prepareRefs(fake, "/tmp/p", "t", nil, slog.Default())
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if len(successful) != 0 || len(unavailable) != 0 || len(successfulPaths) != 0 {
		t.Errorf("non-empty results for nil refs: ok=%v fail=%v paths=%v",
			successful, unavailable, successfulPaths)
	}
	if refsRoot != "" {
		t.Errorf("refsRoot = %q, want empty for nil refs", refsRoot)
	}
	if len(fake.prepareAtCalls) != 0 {
		t.Errorf("expected zero PrepareAt calls, got %d", len(fake.prepareAtCalls))
	}
}

// TestPrepareRefs_MkdirFailure surfaces refs-root mkdir errors as the
// only failure mode that bubbles up — per-ref failures absorb to
// `unavailable` (covered by PartialSuccess test).
func TestPrepareRefs_MkdirFailure(t *testing.T) {
	// Force a mkdir failure by passing a primary path whose parent is a file.
	tmp := t.TempDir()
	parentFile := filepath.Join(tmp, "blocker")
	if err := os.WriteFile(parentFile, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	// "blocker/triage-repo-x" → mkdir attempts to use blocker as parent dir.
	primary := filepath.Join(parentFile, "triage-repo-x")

	refs := []queue.RefRepo{{Repo: "any/ref", CloneURL: "u"}}
	_, _, _, _, err :=
		prepareRefs(&fakeRepoProvider{}, primary, "t", refs, slog.Default())
	if err == nil {
		t.Fatalf("expected mkdir error for primary %q, got nil", primary)
	}
}

// TestCleanupRefs_ReverseOrder asserts cleanup walks ref worktrees in
// reverse-add order and rm's the refs-root last. Order-sensitive only for
// log/debug consistency — semantically the order doesn't matter, but
// reversing matches the typical "last in, first out" cleanup pattern.
func TestCleanupRefs_ReverseOrder(t *testing.T) {
	tmp := t.TempDir()
	root := filepath.Join(tmp, "refs-root")
	paths := []string{
		filepath.Join(root, "a"),
		filepath.Join(root, "b"),
		filepath.Join(root, "c"),
	}
	for _, p := range paths {
		if err := os.MkdirAll(p, 0755); err != nil {
			t.Fatal(err)
		}
	}

	fake := &fakeRepoProvider{}
	cleanupRefs(fake, paths, root)

	if got := fake.removed; len(got) != 3 || got[0] != paths[2] || got[2] != paths[0] {
		t.Errorf("RemoveWorktree order = %v, want reversed of %v", got, paths)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Errorf("refs root should be removed; stat err = %v", err)
	}
}

// TestCleanupRefs_EmptyArgs asserts no-op behavior when refs were not
// prepared (zero paths + empty root).
func TestCleanupRefs_EmptyArgs(t *testing.T) {
	fake := &fakeRepoProvider{}
	cleanupRefs(fake, nil, "")
	if len(fake.removed) != 0 {
		t.Errorf("expected zero RemoveWorktree calls, got %v", fake.removed)
	}
}
