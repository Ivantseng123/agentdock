package bot

import (
	"strings"
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

func TestParseAgentOutput_LabelsAsString(t *testing.T) {
	// Some agents (observed with opencode + minimax) emit labels as a single
	// string instead of an array. Parser must accept it and preserve the rest
	// of the struct — prior behavior was a type error that zeroed everything.
	output := "Investigation done.\n\n===TRIAGE_RESULT===\n" + `{
  "status": "CREATED",
  "title": "T",
  "body": "B",
  "labels": "bug",
  "confidence": "high",
  "files_found": 3
}`
	result, err := ParseAgentOutput(output)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if result.Status != "CREATED" || result.Title != "T" || result.Confidence != "high" || result.FilesFound != 3 {
		t.Errorf("non-labels fields lost: %+v", result)
	}
	if len(result.Labels) != 1 || result.Labels[0] != "bug" {
		t.Errorf("labels = %v, want [bug]", result.Labels)
	}
}

func TestParseAgentOutput_LabelsAsNullOrMissing(t *testing.T) {
	cases := []string{
		`{"status":"CREATED","title":"T","body":"B","labels":null,"confidence":"high"}`,
		`{"status":"CREATED","title":"T","body":"B","confidence":"high"}`,
	}
	for i, jsonBody := range cases {
		output := "x\n\n===TRIAGE_RESULT===\n" + jsonBody
		result, err := ParseAgentOutput(output)
		if err != nil {
			t.Fatalf("case %d parse failed: %v", i, err)
		}
		if result.Status != "CREATED" || result.Title != "T" {
			t.Errorf("case %d: fields lost: %+v", i, result)
		}
		if result.Labels != nil && len(result.Labels) != 0 {
			t.Errorf("case %d: labels should be nil/empty, got %v", i, result.Labels)
		}
	}
}

func TestParseAgentOutput_LabelsEmptyString(t *testing.T) {
	output := "x\n\n===TRIAGE_RESULT===\n" + `{"status":"CREATED","title":"T","body":"B","labels":""}`
	result, err := ParseAgentOutput(output)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if len(result.Labels) != 0 {
		t.Errorf("empty string should yield no labels, got %v", result.Labels)
	}
}

func TestParseAgentOutput_CreatedRequiresTitle_Empty(t *testing.T) {
	// Agent emits Status=CREATED but an empty title — previously the bot
	// happily sent the request and GitHub rejected with 422 "title can't be
	// blank". Parser must reject it up-front.
	output := "x\n\n===TRIAGE_RESULT===\n" + `{"status":"CREATED","title":"","body":"b","labels":["bug"]}`
	_, err := ParseAgentOutput(output)
	if err == nil {
		t.Fatal("expected error for CREATED with empty title")
	}
	if !strings.Contains(err.Error(), "title") {
		t.Errorf("error should mention title, got: %v", err)
	}
}

func TestParseAgentOutput_CreatedRequiresTitle_Missing(t *testing.T) {
	output := "x\n\n===TRIAGE_RESULT===\n" + `{"status":"CREATED","body":"b"}`
	_, err := ParseAgentOutput(output)
	if err == nil {
		t.Fatal("expected error for CREATED without title field")
	}
}

func TestParseAgentOutput_CreatedRequiresTitle_WhitespaceOnly(t *testing.T) {
	output := "x\n\n===TRIAGE_RESULT===\n" + `{"status":"CREATED","title":"   \n\t","body":"b"}`
	_, err := ParseAgentOutput(output)
	if err == nil {
		t.Fatal("expected error for CREATED with whitespace-only title")
	}
}

func TestParseAgentOutput_CreatedLegacyURLNoTitleOK(t *testing.T) {
	// Legacy "CREATED: <url>" path doesn't need a title — the issue already
	// exists, CreateIssue won't be called, so no validation should apply.
	output := "done.\n\n===TRIAGE_RESULT===\nCREATED: https://github.com/o/r/issues/1"
	result, err := ParseAgentOutput(output)
	if err != nil {
		t.Fatalf("legacy CREATED should pass: %v", err)
	}
	if result.Status != "CREATED" || result.IssueURL == "" {
		t.Errorf("unexpected result: %+v", result)
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

func TestParseAgentOutput_DoubleMarkerFence(t *testing.T) {
	// Opencode (observed with minimax-m2.5-free) wraps the JSON between an
	// opening AND a closing ===TRIAGE_RESULT=== marker, treating it as a
	// fenced block. LastIndex would land on the closing marker with empty
	// content — parser must fall back to an earlier marker and pull the
	// JSON from between.
	output := "Based on my investigation of Mantis #36321...\n\n===TRIAGE_RESULT===\n" + `{
  "status": "CREATED",
  "title": "[已解決] 報價單列印顯示正本副本文字",
  "body": "## Problem\n報價單列印時錯誤顯示正副本文字",
  "labels": ["bug"],
  "confidence": "high",
  "files_found": 12,
  "open_questions": 0
}
===TRIAGE_RESULT===`

	result, err := ParseAgentOutput(output)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if result.Status != "CREATED" {
		t.Errorf("status = %q, want CREATED", result.Status)
	}
	if result.Title != "[已解決] 報價單列印顯示正本副本文字" {
		t.Errorf("title lost: %q", result.Title)
	}
	if result.Confidence != "high" {
		t.Errorf("confidence = %q, want high", result.Confidence)
	}
	if result.FilesFound != 12 {
		t.Errorf("files_found = %d, want 12", result.FilesFound)
	}
}

func TestParseAgentOutput_PreambleEchoThenRealResult(t *testing.T) {
	// Guard the existing LastIndex-preferred behavior: if an agent echoes
	// the marker in a preamble example and THEN writes the real answer after
	// a second marker, the final answer wins. Regression test for anyone
	// trying to naively swap LastIndex → FirstIndex.
	output := "Example: ===TRIAGE_RESULT===\nCREATED: https://example.com\n\n" +
		"Now doing the real work.\n\n===TRIAGE_RESULT===\n" + `{
  "status": "REJECTED",
  "message": "Problem unrelated to this repo"
}`

	result, err := ParseAgentOutput(output)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if result.Status != "REJECTED" {
		t.Errorf("status = %q, want REJECTED (from the LAST marker, not the echoed one)", result.Status)
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
