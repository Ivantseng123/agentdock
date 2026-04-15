package bot

import (
	"encoding/json"
	"fmt"
	"strings"
)

const (
	resultSeparator = "===TRIAGE_RESULT==="
	minOutputLength = 10
)

// TriageResult is the parsed result from agent output.
type TriageResult struct {
	Status     string   `json:"status"`
	IssueURL   string   `json:"issue_url,omitempty"`
	Message    string   `json:"message,omitempty"`
	Title      string   `json:"title,omitempty"`
	Body       string   `json:"body,omitempty"`
	Labels     []string `json:"labels,omitempty"`
	Confidence string   `json:"confidence,omitempty"`
	FilesFound int      `json:"files_found,omitempty"`
	Questions  int      `json:"open_questions,omitempty"`
}

// ParseAgentOutput extracts the triage result from agent stdout.
// Looks for ===TRIAGE_RESULT=== followed by CREATED:/REJECTED:/ERROR:
func ParseAgentOutput(output string) (TriageResult, error) {
	output = strings.TrimSpace(output)
	if len(output) < minOutputLength {
		return TriageResult{}, fmt.Errorf("agent output too short (%d chars)", len(output))
	}

	idx := strings.LastIndex(output, resultSeparator)
	if idx == -1 {
		// No result marker — try to find a GitHub issue URL anywhere in the output
		if url := extractIssueURL(output); url != "" {
			return TriageResult{Status: "CREATED", IssueURL: url}, nil
		}
		return TriageResult{}, fmt.Errorf("no triage result found in agent output")
	}

	result := strings.TrimSpace(output[idx+len(resultSeparator):])

	// Try JSON format first.
	if strings.HasPrefix(result, "{") {
		jsonStr := extractJSON(result)
		var tr TriageResult
		if err := json.Unmarshal([]byte(jsonStr), &tr); err == nil && tr.Status != "" {
			return tr, nil
		}
	}

	// Legacy format.
	if strings.HasPrefix(result, "CREATED:") {
		url := strings.TrimSpace(strings.TrimPrefix(result, "CREATED:"))
		if url == "" {
			url = extractIssueURL(output)
		}
		return TriageResult{Status: "CREATED", IssueURL: url}, nil
	}

	if strings.HasPrefix(result, "REJECTED:") {
		msg := strings.TrimSpace(strings.TrimPrefix(result, "REJECTED:"))
		return TriageResult{Status: "REJECTED", Message: msg}, nil
	}

	if strings.HasPrefix(result, "ERROR:") {
		msg := strings.TrimSpace(strings.TrimPrefix(result, "ERROR:"))
		return TriageResult{Status: "ERROR", Message: msg}, nil
	}

	return TriageResult{}, fmt.Errorf("unknown triage result: %s", result)
}

// extractJSON finds the first top-level JSON object in text by matching braces.
// This handles cases where the agent appends extra content after the JSON.
func extractJSON(text string) string {
	depth := 0
	start := strings.Index(text, "{")
	if start < 0 {
		return text
	}
	inString := false
	escaped := false
	for i := start; i < len(text); i++ {
		ch := text[i]
		if escaped {
			escaped = false
			continue
		}
		if ch == '\\' && inString {
			escaped = true
			continue
		}
		if ch == '"' {
			inString = !inString
			continue
		}
		if inString {
			continue
		}
		if ch == '{' {
			depth++
		} else if ch == '}' {
			depth--
			if depth == 0 {
				return text[start : i+1]
			}
		}
	}
	return text // unbalanced, return as-is
}

// extractIssueURL finds a GitHub issue URL in text.
func extractIssueURL(text string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if strings.Contains(line, "github.com/") && strings.Contains(line, "/issues/") {
			// Extract URL from the line
			for _, word := range strings.Fields(line) {
				if strings.HasPrefix(word, "https://github.com/") && strings.Contains(word, "/issues/") {
					return word
				}
			}
		}
	}
	return ""
}
