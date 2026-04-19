package github

import (
	"strings"
	"testing"
)

func TestNormalizeLabels_NilBecomesEmptySlice(t *testing.T) {
	out := normalizeLabels(nil)
	if out == nil {
		t.Fatal("normalizeLabels(nil) must not return nil — GitHub rejects null labels")
	}
	if len(out) != 0 {
		t.Errorf("normalizeLabels(nil) = %v, want []", out)
	}
}

func TestNormalizeLabels_PreservesExisting(t *testing.T) {
	in := []string{"bug", "mobile"}
	out := normalizeLabels(in)
	if len(out) != 2 || out[0] != "bug" || out[1] != "mobile" {
		t.Errorf("normalizeLabels(%v) = %v", in, out)
	}
}

func TestIssueBody_WithHeader(t *testing.T) {
	header := "**Channel**: #general\n**Reporter**: alice\n**Branch**: main\n\n---\n\n"
	agentBody := "## Summary\n\nLogin page broken."

	body := header + agentBody

	if !strings.Contains(body, "#general") {
		t.Error("missing channel")
	}
	if !strings.Contains(body, "alice") {
		t.Error("missing reporter")
	}
	if !strings.Contains(body, "Login page broken") {
		t.Error("missing agent body")
	}
}
