package bot

import (
	"fmt"
	"strings"
)

const (
	resultSeparator = "===TRIAGE_RESULT==="
	minOutputLength = 50
)

// TriageResult is the parsed result from agent output.
type TriageResult struct {
	Status   string // "CREATED", "REJECTED", "ERROR"
	IssueURL string // only set when Status == "CREATED"
	Message  string // rejection reason or error message
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
