package workflow

import (
	"encoding/json"
	"fmt"
	"strings"
)

const askMarker = "===ASK_RESULT==="

// AskResult is the parsed ===ASK_RESULT=== JSON.
type AskResult struct {
	Answer     string `json:"answer"`
	Confidence string `json:"confidence,omitempty"`
}

// ParseAskOutput extracts the last ASK_RESULT marker block and unmarshals
// its JSON body. Rejects empty answers.
func ParseAskOutput(output string) (AskResult, error) {
	output = strings.TrimSpace(output)
	idx := strings.LastIndex(output, askMarker)
	if idx == -1 {
		return AskResult{}, fmt.Errorf("%s marker not found", askMarker)
	}
	body := strings.TrimSpace(output[idx+len(askMarker):])
	if !strings.HasPrefix(body, "{") {
		return AskResult{}, fmt.Errorf("expected JSON object after marker")
	}
	jsonStr := extractJSON(body) // reused from issue_parser.go
	var r AskResult
	if err := json.Unmarshal([]byte(jsonStr), &r); err != nil {
		return AskResult{}, fmt.Errorf("unmarshal: %w", err)
	}
	if strings.TrimSpace(r.Answer) == "" {
		return AskResult{}, fmt.Errorf("answer must not be empty")
	}
	return r, nil
}
