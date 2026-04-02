package github

import (
	"strings"
	"testing"

	"slack-issue-bot/internal/llm"
)

func TestFormatIssueBody_Bug(t *testing.T) {
	body := FormatIssueBody(IssueInput{
		Type:        "bug",
		Channel:     "#backend-bugs",
		Reporter:    "ivan",
		Message:     "Login page crashes after submit",
		RepoOwner:   "org",
		RepoName:    "backend",
		Branch:      "main",
		Diagnosis: llm.DiagnoseResponse{
			Summary: "JWT decode fails on missing field",
			Files: []llm.FileRef{
				{Path: "src/auth/jwt.go", LineNumber: 45, Description: "nil check"},
			},
			Suggestions:   []string{"Add nil check in DecodeToken"},
			OpenQuestions:  []string{"Which JWT library version is in use?"},
			Confidence:    "high",
		},
	})

	checks := []struct{ label, want string }{
		{"channel", "#backend-bugs"},
		{"reporter no @", "ivan"},
		{"summary", "JWT decode fails"},
		{"github link", "github.com/org/backend/blob/main/src/auth/jwt.go#L45"},
		{"filename display", "jwt.go"},
		{"triage heading", "AI Triage"},
		{"open questions", "Needs Clarification"},
	}
	for _, c := range checks {
		if !strings.Contains(body, c.want) {
			t.Errorf("body should contain %s (%q)", c.label, c.want)
		}
	}
	if strings.Contains(body, "@ivan") {
		t.Error("reporter should NOT have @ prefix")
	}
}

func TestFormatIssueBody_Feature(t *testing.T) {
	body := FormatIssueBody(IssueInput{
		Type:      "feature",
		Channel:   "#product",
		Reporter:  "ivan",
		Message:   "Need CSV batch export",
		RepoOwner: "org",
		RepoName:  "backend",
		Diagnosis: llm.DiagnoseResponse{
			Summary:    "Single export exists",
			Files:      []llm.FileRef{{Path: "src/export/single.go", Description: "export logic"}},
			Suggestions: []string{"Extend existing handler"},
			Confidence:  "medium",
		},
	})

	if !strings.Contains(body, "AI Triage") {
		t.Error("feature body should have AI Triage heading")
	}
	if !strings.Contains(body, "single.go") {
		t.Error("should show filename")
	}
	if !strings.Contains(body, "github.com/org/backend") {
		t.Error("should have github link")
	}
}

func TestFormatIssueBody_FileWithoutLineNumber(t *testing.T) {
	body := FormatIssueBody(IssueInput{
		Type:      "bug",
		Channel:   "#test",
		Reporter:  "ivan",
		Message:   "test",
		RepoOwner: "org",
		RepoName:  "repo",
		Diagnosis: llm.DiagnoseResponse{
			Summary:    "some issue",
			Files:      []llm.FileRef{{Path: "src/foo.go", LineNumber: 0, Description: "relevant"}},
			Confidence: "medium",
		},
	})

	if strings.Contains(body, "#L0") {
		t.Error("should not show #L0 for unknown line numbers")
	}
	if !strings.Contains(body, "foo.go") {
		t.Error("should show filename")
	}
}

func TestFormatIssueBody_LiteMode(t *testing.T) {
	body := FormatIssueBody(IssueInput{
		Type:     "bug",
		Channel:  "#backend",
		Reporter: "ivan",
		Message:  "Something broke",
	})

	if !strings.Contains(body, "No AI diagnosis") {
		t.Error("should indicate lite mode")
	}
}

func TestFormatIssueBody_LiteMode_WithFiles(t *testing.T) {
	body := FormatIssueBody(IssueInput{
		Type:     "feature",
		Channel:  "#product",
		Reporter: "ivan",
		Message:  "Need batch export",
		Diagnosis: llm.DiagnoseResponse{
			Files: []llm.FileRef{
				{Path: "src/export/single.go", Description: "matched keywords"},
			},
		},
	})

	if !strings.Contains(body, "Related Files") {
		t.Error("should list related files")
	}
	if !strings.Contains(body, "single.go") {
		t.Error("should show filename")
	}
}

func TestFormatIssueBody_ContextFilesSkipped(t *testing.T) {
	body := FormatIssueBody(IssueInput{
		Type:      "bug",
		Channel:   "#test",
		Reporter:  "ivan",
		Message:   "test",
		RepoOwner: "org",
		RepoName:  "repo",
		Diagnosis: llm.DiagnoseResponse{
			Summary: "issue",
			Files: []llm.FileRef{
				{Path: "[context] README.md", Description: "context doc"},
				{Path: "src/main.go", Description: "entry point"},
			},
			Confidence: "high",
		},
	})

	if strings.Contains(body, "[context]") {
		t.Error("context files should be filtered out of issue body")
	}
	if !strings.Contains(body, "main.go") {
		t.Error("real files should still appear")
	}
}
