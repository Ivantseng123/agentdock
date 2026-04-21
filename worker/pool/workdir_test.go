package pool

import (
	"os"
	"path/filepath"
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

type fakeRepoProvider struct {
	prepared struct {
		cloneURL string
		branch   string
		token    string
	}
	cleaned string
}

func (f *fakeRepoProvider) Prepare(cloneURL, branch, token string) (string, error) {
	f.prepared.cloneURL = cloneURL
	f.prepared.branch = branch
	f.prepared.token = token
	return "/tmp/fake-repo", nil
}
func (f *fakeRepoProvider) RemoveWorktree(p string) error { f.cleaned = p; return nil }
func (f *fakeRepoProvider) CleanAll() error               { return nil }
func (f *fakeRepoProvider) PurgeStale() error             { return nil }

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
