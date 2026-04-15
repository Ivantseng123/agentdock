package bot

import (
	"testing"
)

func TestParseAgentOutput_JSONCreated(t *testing.T) {
	output := "Some analysis output...\n\n===TRIAGE_RESULT===\n" + `{
  "status": "CREATED",
  "title": "Login page broken after 3 failed attempts",
  "body": "## Problem\n\nLogin page crashes...",
  "labels": ["bug"],
  "confidence": "high",
  "files_found": 5,
  "open_questions": 0
}`
	result, err := ParseAgentOutput(output)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if result.Status != "CREATED" {
		t.Errorf("status = %q, want CREATED", result.Status)
	}
	if result.Title != "Login page broken after 3 failed attempts" {
		t.Errorf("title = %q", result.Title)
	}
	if result.Confidence != "high" {
		t.Errorf("confidence = %q", result.Confidence)
	}
	if result.FilesFound != 5 {
		t.Errorf("files_found = %d, want 5", result.FilesFound)
	}
	if len(result.Labels) != 1 || result.Labels[0] != "bug" {
		t.Errorf("labels = %v", result.Labels)
	}
}

func TestParseAgentOutput_JSONRejected(t *testing.T) {
	output := "Investigation complete.\n\n===TRIAGE_RESULT===\n" + `{
  "status": "REJECTED",
  "message": "Could not find relevant code"
}`
	result, err := ParseAgentOutput(output)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if result.Status != "REJECTED" {
		t.Errorf("status = %q", result.Status)
	}
	if result.Message == "" {
		t.Error("message should not be empty")
	}
}

func TestParseAgentOutput_LegacyCreated(t *testing.T) {
	output := "Analysis done.\n\n===TRIAGE_RESULT===\nCREATED: https://github.com/owner/repo/issues/42"
	result, err := ParseAgentOutput(output)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if result.Status != "CREATED" {
		t.Errorf("status = %q, want CREATED", result.Status)
	}
	if result.IssueURL != "https://github.com/owner/repo/issues/42" {
		t.Errorf("issueURL = %q", result.IssueURL)
	}
}

func TestParseAgentOutput_LegacyRejected(t *testing.T) {
	output := "After investigation.\n\n===TRIAGE_RESULT===\nREJECTED: Problem unrelated to this repo"
	result, err := ParseAgentOutput(output)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if result.Status != "REJECTED" {
		t.Errorf("status = %q", result.Status)
	}
}

func TestParseAgentOutput_LegacyError(t *testing.T) {
	output := "Tried to create issue.\n\n===TRIAGE_RESULT===\nERROR: gh issue create failed"
	result, err := ParseAgentOutput(output)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if result.Status != "ERROR" {
		t.Errorf("status = %q", result.Status)
	}
}

func TestParseAgentOutput_FallbackURL(t *testing.T) {
	output := "Created issue at https://github.com/owner/repo/issues/99 for tracking. Some more padding text to meet minimum length."
	result, err := ParseAgentOutput(output)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if result.Status != "CREATED" {
		t.Errorf("status = %q", result.Status)
	}
	if result.IssueURL != "https://github.com/owner/repo/issues/99" {
		t.Errorf("issueURL = %q", result.IssueURL)
	}
}

func TestParseAgentOutput_Empty(t *testing.T) {
	_, err := ParseAgentOutput("")
	if err == nil {
		t.Error("expected error on empty output")
	}
}

func TestParseAgentOutput_TooShort(t *testing.T) {
	_, err := ParseAgentOutput("short")
	if err == nil {
		t.Error("expected error on short output")
	}
}

func TestParseAgentOutput_NoResult(t *testing.T) {
	_, err := ParseAgentOutput("Some analysis that didn't produce a result or URL. Padding to meet minimum length requirement.")
	if err == nil {
		t.Error("expected error when no result")
	}
}

func TestParseAgentOutput_JSONWithTrailingContent(t *testing.T) {
	output := "Investigation complete.\n\n===TRIAGE_RESULT===\n" + `{
  "status": "CREATED",
  "title": "Bug title",
  "body": "Issue body",
  "labels": ["bug"],
  "confidence": "high",
  "files_found": 4,
  "open_questions": 1
}

---

## Summary

Some extra markdown the agent added after the JSON.`

	result, err := ParseAgentOutput(output)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if result.Status != "CREATED" {
		t.Errorf("status = %q, want CREATED", result.Status)
	}
	if result.Title != "Bug title" {
		t.Errorf("title = %q", result.Title)
	}
	if result.FilesFound != 4 {
		t.Errorf("files_found = %d, want 4", result.FilesFound)
	}
}
