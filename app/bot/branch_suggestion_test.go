package bot

import (
	"log/slog"
	"testing"

	"github.com/Ivantseng123/agentdock/app/workflow"
)

// fakeBranchState implements workflow.BranchStateReader so the
// selectedRepoFromState extractor can be exercised without pulling the
// concrete issueState/askState types from app/workflow.
type fakeBranchState struct{ repo string }

func (f *fakeBranchState) BranchSelectedRepo() string { return f.repo }

// TestFilterBranches_Substring — the type-ahead filter must do a
// case-insensitive substring match, cap at limit, and preserve order.
func TestFilterBranches_Substring(t *testing.T) {
	branches := []string{
		"main", "develop",
		"feature/login", "feature/signup", "feature/logout",
		"release/1.0", "release/2.0",
		"hotfix/LOGIN-123",
	}

	cases := []struct {
		name  string
		query string
		limit int
		want  []string
	}{
		{
			name:  "empty query returns first N in order",
			query: "",
			limit: 3,
			want:  []string{"main", "develop", "feature/login"},
		},
		{
			name:  "case-insensitive match",
			query: "LOGIN",
			limit: 100,
			want:  []string{"feature/login", "hotfix/LOGIN-123"},
		},
		{
			name:  "substring match preserves order",
			query: "feature",
			limit: 100,
			want:  []string{"feature/login", "feature/signup", "feature/logout"},
		},
		{
			name:  "limit caps output",
			query: "feature",
			limit: 2,
			want:  []string{"feature/login", "feature/signup"},
		},
		{
			name:  "no match returns empty",
			query: "nonexistent",
			limit: 100,
			want:  []string{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := filterBranches(branches, tc.query, tc.limit)
			if len(got) != len(tc.want) {
				t.Fatalf("filterBranches len = %d, want %d (got=%v)", len(got), len(tc.want), got)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("filterBranches[%d] = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestSelectedRepoFromState_Interface — selectedRepoFromState must read
// SelectedRepo off any workflow state implementing BranchStateReader,
// return "" on unknown state, and tolerate nil.
func TestSelectedRepoFromState_Interface(t *testing.T) {
	if got := selectedRepoFromState(nil); got != "" {
		t.Errorf("nil state: got %q, want empty", got)
	}
	if got := selectedRepoFromState(struct{}{}); got != "" {
		t.Errorf("unknown state: got %q, want empty", got)
	}
	if got := selectedRepoFromState(&fakeBranchState{repo: "owner/repo"}); got != "owner/repo" {
		t.Errorf("BranchStateReader: got %q, want owner/repo", got)
	}
}

// TestHandleBranchSuggestion_NilRepoCache — without a configured RepoCache,
// the suggestion handler must degrade gracefully (nil result, no panic).
func TestHandleBranchSuggestion_NilRepoCache(t *testing.T) {
	wf := &Workflow{
		logger:  slog.Default(),
		pending: make(map[string]*workflow.Pending),
	}
	// Seed a pending so the early-return isn't masked by the map lookup.
	wf.pending["sel-1"] = &workflow.Pending{State: &fakeBranchState{repo: "owner/repo"}}
	if got := wf.HandleBranchSuggestion("sel-1", "feat"); got != nil {
		t.Errorf("nil repoCache: got %v, want nil", got)
	}
}

// TestHandleBranchSuggestion_MissingPending — an unknown selectorTS (the
// pending was already consumed / timed out) must not panic.
func TestHandleBranchSuggestion_MissingPending(t *testing.T) {
	wf := &Workflow{
		logger:  slog.Default(),
		pending: make(map[string]*workflow.Pending),
	}
	if got := wf.HandleBranchSuggestion("missing", ""); got != nil {
		t.Errorf("missing pending: got %v, want nil", got)
	}
}

// TestHandleBranchSuggestion_NoSelectedRepo — if the pending exists but
// carries no SelectedRepo yet (state type doesn't implement the reader or
// is nil), return nil rather than attempting to fetch branches.
func TestHandleBranchSuggestion_NoSelectedRepo(t *testing.T) {
	wf := &Workflow{
		logger:  slog.Default(),
		pending: make(map[string]*workflow.Pending),
	}
	// State without BranchStateReader — selectedRepoFromState returns "".
	wf.pending["sel-2"] = &workflow.Pending{State: struct{}{}}
	if got := wf.HandleBranchSuggestion("sel-2", ""); got != nil {
		t.Errorf("no SelectedRepo: got %v, want nil", got)
	}
}
