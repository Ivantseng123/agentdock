package workflow

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

const askMarker = "===ASK_RESULT==="

// ResultSource flags transport-quality of an AskResult. Schema is the
// happy path; any fallback_* value indicates the parser had to recover
// from an agent that didn't follow the schema. Spec
// 2026-04-26-ask-fallback-extension-design.md §Failure Categories.
const (
	ResultSourceSchema                 = "schema"
	ResultSourceFallbackMarkerMissing  = "fallback_marker_missing"
	ResultSourceFallbackSegmentsNoJSON = "fallback_segments_no_json"
	ResultSourceFallbackUnmarshal      = "fallback_unmarshal"
	ResultSourceFallbackEmptyAnswer    = "fallback_empty_answer"
)

// askFallbackMinLength is the minimum rune count below which raw stdout
// is rejected as a fallback answer. Syntactic gate only; no content
// classification.
const askFallbackMinLength = 10

// AskResult is the parsed ===ASK_RESULT=== JSON, plus a transport-quality
// marker (ResultSource) populated by ParseAskOutput.
type AskResult struct {
	Answer       string `json:"answer"`
	Confidence   string `json:"confidence,omitempty"`
	ResultSource string `json:"-"`
}

// ParseAskOutput extracts the ASK_RESULT marker block and unmarshals its
// JSON body. Returns error only when stdout fails the syntactic gate
// (truly empty / below min length); every other failure shape (marker
// missing, segments without JSON, unmarshal failure, empty answer)
// returns success with the corresponding fallback_* ResultSource so the
// handler can prepend a transparency banner. Reverses §Design Decisions
// #2 of 2026-04-25-workflow-output-boundary-design.md.
func ParseAskOutput(output string) (AskResult, error) {
	output = strings.TrimSpace(output)
	segments := segmentAfterMarker(output, askMarker)
	if len(segments) == 0 {
		return fallbackOrFail(output, ResultSourceFallbackMarkerMissing)
	}
	lastReason := ResultSourceFallbackSegmentsNoJSON
	for _, seg := range segments {
		if !strings.HasPrefix(seg, "{") {
			continue
		}
		jsonStr := extractJSON(seg)
		var r AskResult
		if err := json.Unmarshal([]byte(jsonStr), &r); err != nil {
			lastReason = ResultSourceFallbackUnmarshal
			continue
		}
		if strings.TrimSpace(r.Answer) == "" {
			lastReason = ResultSourceFallbackEmptyAnswer
			continue
		}
		r.ResultSource = ResultSourceSchema
		return r, nil
	}
	return fallbackOrFail(output, lastReason)
}

// fallbackOrFail packages raw stdout as a fallback Answer when the
// syntactic gate passes; otherwise reports the parse failure for the
// caller to surface as ":x: Agent 沒有產生任何答案".
func fallbackOrFail(output, reason string) (AskResult, error) {
	if looksLikePlainAnswer(output) {
		return AskResult{Answer: output, ResultSource: reason}, nil
	}
	return AskResult{}, fmt.Errorf("ask output empty or below min length")
}

// looksLikePlainAnswer is the syntactic gate for fallback. Not a content
// classifier — the handler attaches a transparency banner regardless.
func looksLikePlainAnswer(s string) bool {
	return utf8.RuneCountInString(strings.TrimSpace(s)) >= askFallbackMinLength
}
