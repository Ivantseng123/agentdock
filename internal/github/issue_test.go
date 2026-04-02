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
		Diagnosis: llm.DiagnoseResponse{
			Summary: "JWT decode fails on missing field",
			Files: []llm.FileRef{
				{Path: "src/auth/jwt.go", LineNumber: 45, Description: "Missing nil check"},
			},
			Suggestions: []string{"Add nil check in DecodeToken"},
		},
	})

	if !strings.Contains(body, "#backend-bugs") {
		t.Error("body should contain channel name")
	}
	if !strings.Contains(body, "ivan") {
		t.Error("body should contain reporter")
	}
	if !strings.Contains(body, "JWT decode fails") {
		t.Error("body should contain diagnosis summary")
	}
	if !strings.Contains(body, "src/auth/jwt.go:45") {
		t.Error("body should contain file ref with line number")
	}
	if !strings.Contains(body, "Possible Cause") {
		t.Error("bug body should use 'Possible Cause' heading")
	}
}

func TestFormatIssueBody_Feature(t *testing.T) {
	body := FormatIssueBody(IssueInput{
		Type:        "feature",
		Channel:     "#product",
		Reporter:    "ivan",
		Message:     "Need CSV batch export",
		Diagnosis: llm.DiagnoseResponse{
			Summary:    "Single export exists in export/single.go",
			Files:      []llm.FileRef{{Path: "src/export/single.go", LineNumber: 10, Description: "Existing export"}},
			Suggestions: []string{"Extend existing export handler"},
			Complexity: "medium",
		},
	})

	if !strings.Contains(body, "Existing Related Functionality") {
		t.Error("feature body should use 'Existing Related Functionality' heading")
	}
	if !strings.Contains(body, "Complexity Assessment") {
		t.Error("feature body should contain complexity assessment")
	}
	if !strings.Contains(body, "medium") {
		t.Error("feature body should contain complexity value")
	}
}

func TestFormatIssueBody_NoDiagnosis(t *testing.T) {
	body := FormatIssueBody(IssueInput{
		Type:     "bug",
		Channel:  "#backend",
		Reporter: "ivan",
		Message:  "Something broke",
	})

	if !strings.Contains(body, "Something broke") {
		t.Error("body should contain original message")
	}
	if !strings.Contains(body, "AI diagnosis was unavailable") {
		t.Error("body should note diagnosis was unavailable")
	}
}
