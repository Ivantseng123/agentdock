package bot

import (
	"testing"
)

func TestParseAgentOutput_FullOutput(t *testing.T) {
	output := `## Summary

Login page spins forever after submit.

## Related Code

- src/api/auth/login.ts:45

===TRIAGE_METADATA===
{
  "issue_type": "bug",
  "confidence": "high",
  "files": [{"path": "src/api/auth/login.ts", "line": 45, "relevance": "login handler"}],
  "open_questions": ["Does this affect all users?"],
  "suggested_title": "Login page infinite loading"
}`

	result, err := ParseAgentOutput(output)
	if err != nil {
		t.Fatalf("ParseAgentOutput failed: %v", err)
	}
	if result.Metadata.IssueType != "bug" {
		t.Errorf("issue_type = %q", result.Metadata.IssueType)
	}
	if result.Metadata.Confidence != "high" {
		t.Errorf("confidence = %q", result.Metadata.Confidence)
	}
	if len(result.Metadata.Files) != 1 {
		t.Fatalf("files count = %d", len(result.Metadata.Files))
	}
	if result.Metadata.Files[0].Path != "src/api/auth/login.ts" {
		t.Errorf("file path = %q", result.Metadata.Files[0].Path)
	}
	if result.Metadata.SuggestedTitle != "Login page infinite loading" {
		t.Errorf("suggested_title = %q", result.Metadata.SuggestedTitle)
	}
	if result.MarkdownBody == "" {
		t.Error("markdown body is empty")
	}
	if result.MarkdownBody != "## Summary\n\nLogin page spins forever after submit.\n\n## Related Code\n\n- src/api/auth/login.ts:45" {
		t.Errorf("markdown body = %q", result.MarkdownBody)
	}
}

func TestParseAgentOutput_NoMetadata(t *testing.T) {
	output := "## Summary\n\nJust a plain markdown body with no metadata."
	result, err := ParseAgentOutput(output)
	if err != nil {
		t.Fatalf("ParseAgentOutput failed: %v", err)
	}
	if result.MarkdownBody != output {
		t.Errorf("body should be full output")
	}
	if result.Metadata.Confidence != "medium" {
		t.Errorf("default confidence = %q, want medium", result.Metadata.Confidence)
	}
	if result.Degraded != true {
		t.Error("should be degraded when no metadata")
	}
}

func TestParseAgentOutput_InvalidJSON(t *testing.T) {
	output := "## Summary\n\nBody here.\n\n===TRIAGE_METADATA===\n{invalid json}"
	result, err := ParseAgentOutput(output)
	if err != nil {
		t.Fatalf("ParseAgentOutput failed: %v", err)
	}
	if result.MarkdownBody != "## Summary\n\nBody here." {
		t.Errorf("body = %q", result.MarkdownBody)
	}
	if result.Degraded != true {
		t.Error("should be degraded on invalid JSON")
	}
}

func TestParseAgentOutput_EmptyOutput(t *testing.T) {
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

func TestParseAgentOutput_LastSeparatorUsed(t *testing.T) {
	output := `The output format uses ===TRIAGE_METADATA=== as separator.

## Real content here

===TRIAGE_METADATA===
{"issue_type": "bug", "confidence": "high", "files": [], "open_questions": [], "suggested_title": "test"}`

	result, err := ParseAgentOutput(output)
	if err != nil {
		t.Fatalf("ParseAgentOutput failed: %v", err)
	}
	if result.Metadata.IssueType != "bug" {
		t.Errorf("issue_type = %q", result.Metadata.IssueType)
	}
	if result.MarkdownBody == "" {
		t.Error("markdown body should not be empty")
	}
}

func TestSanitizeBody(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"strips html", "Hello <script>alert('xss')</script> world", "Hello  world"},
		{"keeps markdown", "## Title\n\n**bold** text", "## Title\n\n**bold** text"},
		{"strips nested tags", "<div><p>text</p></div>", "text"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SanitizeBody(tt.input)
			if got != tt.want {
				t.Errorf("SanitizeBody(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestSanitizeBody_MaxLength(t *testing.T) {
	long := make([]byte, 70000)
	for i := range long {
		long[i] = 'a'
	}
	result := SanitizeBody(string(long))
	if len(result) > maxBodyLength {
		t.Errorf("len = %d, want <= %d", len(result), maxBodyLength)
	}
}

func TestResolveTitle(t *testing.T) {
	tests := []struct {
		name           string
		suggestedTitle string
		markdownBody   string
		firstMessage   string
		want           string
	}{
		{"from suggested", "Login bug", "", "", "Login bug"},
		{"from markdown", "", "## Login page broken\n\ndetails", "", "Login page broken"},
		{"from message", "", "", "the login page is broken", "the login page is broken"},
		{"fallback", "", "", "", "Untitled issue"},
		{"truncate", string(make([]byte, 100)), "", "", string(make([]byte, 77)) + "..."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveTitle(tt.suggestedTitle, tt.markdownBody, tt.firstMessage)
			if got != tt.want {
				t.Errorf("ResolveTitle = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResolveLabels(t *testing.T) {
	tests := []struct {
		issueType     string
		defaultLabels []string
		wantLen       int
	}{
		{"bug", []string{"from-slack"}, 2},
		{"feature", []string{"from-slack"}, 2},
		{"improvement", nil, 1},
		{"question", nil, 1},
		{"unknown", []string{"from-slack"}, 1},
		{"", nil, 0},
	}
	for _, tt := range tests {
		t.Run(tt.issueType, func(t *testing.T) {
			got := ResolveLabels(tt.issueType, tt.defaultLabels)
			if len(got) != tt.wantLen {
				t.Errorf("ResolveLabels(%q, %v) = %v (len %d), want len %d",
					tt.issueType, tt.defaultLabels, got, len(got), tt.wantLen)
			}
		})
	}
}
