package github

import (
	"strings"
	"testing"
)

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
