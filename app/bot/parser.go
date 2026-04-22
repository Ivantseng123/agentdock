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

// Labels is a JSON-tolerant []string. Accepts array, single string, or null —
// some agent/model combos (observed with opencode + minimax) emit a bare string
// like "bug" instead of ["bug"], which previously failed TriageResult unmarshal
// and zeroed every other field.
type Labels []string

func (l *Labels) UnmarshalJSON(data []byte) error {
	if len(data) == 0 || string(data) == "null" {
		*l = nil
		return nil
	}
	var arr []string
	if err := json.Unmarshal(data, &arr); err == nil {
		*l = arr
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		if s == "" {
			*l = nil
		} else {
			*l = []string{s}
		}
		return nil
	}
	return fmt.Errorf("labels must be string or array, got %s", string(data))
}

// TriageResult is the parsed result from agent output.
type TriageResult struct {
	Status     string `json:"status"`
	IssueURL   string `json:"issue_url,omitempty"`
	Message    string `json:"message,omitempty"`
	Title      string `json:"title,omitempty"`
	Body       string `json:"body,omitempty"`
	Labels     Labels `json:"labels,omitempty"`
	Confidence string `json:"confidence,omitempty"`
	FilesFound int    `json:"files_found,omitempty"`
	Questions  int    `json:"open_questions,omitempty"`
}

// ParseAgentOutput extracts the triage result from agent stdout. Looks for
// ===TRIAGE_RESULT=== followed by a JSON object or a CREATED:/REJECTED:/ERROR:
// prefix. When the marker appears multiple times (e.g. opencode wraps the
// JSON between an opening and a closing marker as a fence), the parser walks
// them from last to first and returns the first segment that parses.
func ParseAgentOutput(output string) (TriageResult, error) {
	output = strings.TrimSpace(output)
	if len(output) < minOutputLength {
		return TriageResult{}, fmt.Errorf("agent output too short (%d chars)", len(output))
	}

	positions := markerPositions(output, resultSeparator)
	if len(positions) == 0 {
		// No result marker — try to find a GitHub issue URL anywhere in the output
		if url := extractIssueURL(output); url != "" {
			return TriageResult{Status: "CREATED", IssueURL: url}, nil
		}
		return TriageResult{}, fmt.Errorf("no triage result found in agent output")
	}

	// Walk markers from last to first. Preferring the final marker keeps the
	// "agent echoed the marker in a preamble example, then wrote the real
	// answer later" case working. Falling back to earlier markers handles the
	// fence case (opening + closing) where the last marker's segment is empty.
	for i := len(positions) - 1; i >= 0; i-- {
		start := positions[i] + len(resultSeparator)
		end := len(output)
		if i+1 < len(positions) {
			end = positions[i+1]
		}
		segment := strings.TrimSpace(output[start:end])
		if segment == "" {
			continue
		}
		tr, titleErr, matched := parseResultSegment(segment, output)
		if titleErr != nil {
			// CREATED with missing title — definitive error, don't silently
			// fall back to an earlier marker and risk a bogus match.
			return TriageResult{}, titleErr
		}
		if matched {
			return tr, nil
		}
	}

	// Final error message uses the last non-empty segment for context.
	lastSegment := ""
	for i := len(positions) - 1; i >= 0; i-- {
		start := positions[i] + len(resultSeparator)
		end := len(output)
		if i+1 < len(positions) {
			end = positions[i+1]
		}
		if s := strings.TrimSpace(output[start:end]); s != "" {
			lastSegment = s
			break
		}
	}
	return TriageResult{}, fmt.Errorf("unknown triage result: %s", lastSegment)
}

// markerPositions returns the byte offsets of every occurrence of marker in s.
func markerPositions(s, marker string) []int {
	var out []int
	offset := 0
	for {
		idx := strings.Index(s[offset:], marker)
		if idx == -1 {
			return out
		}
		out = append(out, offset+idx)
		offset += idx + len(marker)
	}
}

// parseResultSegment tries JSON first, then the legacy CREATED:/REJECTED:/ERROR:
// prefixes. Returns (result, titleErr, matched):
//   - matched=true, titleErr=nil → caller returns (result, nil).
//   - titleErr!=nil → CREATED JSON without a title; caller returns (zero, err)
//     immediately (do not fall back to earlier markers).
//   - matched=false, titleErr=nil → segment did not match; caller keeps
//     searching earlier markers.
func parseResultSegment(segment, fullOutput string) (TriageResult, error, bool) {
	if strings.HasPrefix(segment, "{") {
		jsonStr := extractJSON(segment)
		var tr TriageResult
		if err := json.Unmarshal([]byte(jsonStr), &tr); err == nil && tr.Status != "" {
			if tr.Status == "CREATED" && strings.TrimSpace(tr.Title) == "" {
				return TriageResult{}, fmt.Errorf("CREATED result missing required title"), false
			}
			return tr, nil, true
		}
	}

	if strings.HasPrefix(segment, "CREATED:") {
		url := strings.TrimSpace(strings.TrimPrefix(segment, "CREATED:"))
		if url == "" {
			url = extractIssueURL(fullOutput)
		}
		return TriageResult{Status: "CREATED", IssueURL: url}, nil, true
	}

	if strings.HasPrefix(segment, "REJECTED:") {
		msg := strings.TrimSpace(strings.TrimPrefix(segment, "REJECTED:"))
		return TriageResult{Status: "REJECTED", Message: msg}, nil, true
	}

	if strings.HasPrefix(segment, "ERROR:") {
		msg := strings.TrimSpace(strings.TrimPrefix(segment, "ERROR:"))
		return TriageResult{Status: "ERROR", Message: msg}, nil, true
	}

	return TriageResult{}, nil, false
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
