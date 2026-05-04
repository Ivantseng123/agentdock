package workflow

import (
	"testing"

	"github.com/Ivantseng123/agentdock/app/githubapp"
)

type fakeTokenSource struct {
	token      string
	accessible map[string]bool
}

func (f *fakeTokenSource) Get() (string, error)       { return f.token, nil }
func (f *fakeTokenSource) MintFresh() (string, error) { return f.token, nil }
func (f *fakeTokenSource) IsAccessible(repo string) bool {
	if f.accessible == nil {
		return true
	}
	return f.accessible[repo]
}

func TestChooseRepoAuth_AppCovered(t *testing.T) {
	src := &fakeTokenSource{
		token:      "ghs_app",
		accessible: map[string]bool{"org/repo": true},
	}
	got, err := chooseRepoAuth("org/repo", "", src)
	if err != nil {
		t.Fatalf("chooseRepoAuth: %v", err)
	}
	if got.authSource != "app" {
		t.Fatalf("authSource = %q, want app", got.authSource)
	}
	if got.perCallToken != "" {
		t.Fatalf("perCallToken = %q, want empty", got.perCallToken)
	}
}

func TestChooseRepoAuth_PATFallback(t *testing.T) {
	src := &fakeTokenSource{
		token:      "ghs_app",
		accessible: map[string]bool{"org/repo": false},
	}
	got, err := chooseRepoAuth("org/repo", "ghp_fallback", src)
	if err != nil {
		t.Fatalf("chooseRepoAuth: %v", err)
	}
	if got.authSource != "pat_fallback" {
		t.Fatalf("authSource = %q, want pat_fallback", got.authSource)
	}
	if got.perCallToken != "ghp_fallback" {
		t.Fatalf("perCallToken = %q, want ghp_fallback", got.perCallToken)
	}
	if !githubapp.IsPATSource(got.source) {
		t.Fatal("expected PAT source on fallback")
	}
}

func TestChooseRepoAuth_NoPATFails(t *testing.T) {
	src := &fakeTokenSource{
		token:      "ghs_app",
		accessible: map[string]bool{"org/repo": false},
	}
	if _, err := chooseRepoAuth("org/repo", "", src); err == nil {
		t.Fatal("expected error without PAT fallback")
	}
}
