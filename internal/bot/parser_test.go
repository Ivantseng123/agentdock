package bot

import (
	"testing"
)

func TestParseAgentOutput_Created(t *testing.T) {
	output := `Some analysis output here...

The issue has been created successfully.

===TRIAGE_RESULT===
CREATED: https://github.com/owner/repo/issues/42`

	result, err := ParseAgentOutput(output)
	if err != nil {
		t.Fatalf("ParseAgentOutput failed: %v", err)
	}
	if result.Status != "CREATED" {
		t.Errorf("status = %q, want CREATED", result.Status)
	}
	if result.IssueURL != "https://github.com/owner/repo/issues/42" {
		t.Errorf("issueURL = %q", result.IssueURL)
	}
}

func TestParseAgentOutput_Rejected(t *testing.T) {
	output := `After investigation, this problem is not related to the codebase.

===TRIAGE_RESULT===
REJECTED: Could not find relevant code, problem likely unrelated to this repo`

	result, err := ParseAgentOutput(output)
	if err != nil {
		t.Fatalf("ParseAgentOutput failed: %v", err)
	}
	if result.Status != "REJECTED" {
		t.Errorf("status = %q, want REJECTED", result.Status)
	}
	if result.Message == "" {
		t.Error("message should not be empty")
	}
}

func TestParseAgentOutput_Error(t *testing.T) {
	output := `Tried to create the issue but it failed.

===TRIAGE_RESULT===
ERROR: gh issue create failed: 401 Bad credentials`

	result, err := ParseAgentOutput(output)
	if err != nil {
		t.Fatalf("ParseAgentOutput failed: %v", err)
	}
	if result.Status != "ERROR" {
		t.Errorf("status = %q, want ERROR", result.Status)
	}
	if result.Message == "" {
		t.Error("message should not be empty")
	}
}

func TestParseAgentOutput_NoMarker_FallbackURL(t *testing.T) {
	output := `Analysis complete. Created issue at https://github.com/owner/repo/issues/99 for tracking. Some more text to meet the minimum length requirement.`

	result, err := ParseAgentOutput(output)
	if err != nil {
		t.Fatalf("ParseAgentOutput failed: %v", err)
	}
	if result.Status != "CREATED" {
		t.Errorf("status = %q, want CREATED", result.Status)
	}
	if result.IssueURL != "https://github.com/owner/repo/issues/99" {
		t.Errorf("issueURL = %q", result.IssueURL)
	}
}

func TestParseAgentOutput_NoMarker_NoURL(t *testing.T) {
	output := "Some analysis that didn't produce a result or URL. Padding to meet minimum length requirement for the parser."

	_, err := ParseAgentOutput(output)
	if err == nil {
		t.Error("expected error when no result marker and no URL")
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
		t.Error("expected error on output under 50 chars")
	}
}

func TestExtractIssueURL(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"plain url", "https://github.com/owner/repo/issues/42", "https://github.com/owner/repo/issues/42"},
		{"in sentence", "Created issue at https://github.com/owner/repo/issues/42 successfully", "https://github.com/owner/repo/issues/42"},
		{"no url", "no github url here", ""},
		{"partial url", "https://github.com/owner/repo", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractIssueURL(tt.input)
			if got != tt.want {
				t.Errorf("extractIssueURL = %q, want %q", got, tt.want)
			}
		})
	}
}
