package workflow

import (
	"strings"
	"testing"
)

func TestParseAskOutput_Valid(t *testing.T) {
	output := "Some thinking...\n\n===ASK_RESULT===\n" + `{"answer":"the answer is 42","confidence":"high"}`
	r, err := ParseAskOutput(output)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if r.Answer != "the answer is 42" {
		t.Errorf("answer = %q", r.Answer)
	}
	if r.Confidence != "high" {
		t.Errorf("confidence = %q", r.Confidence)
	}
	if r.ResultSource != ResultSourceSchema {
		t.Errorf("ResultSource = %q, want %q", r.ResultSource, ResultSourceSchema)
	}
}

// TestParseAskOutput_MarkerMissing_FallbackToMarkerMissing exercises the
// missing-marker fallback: agent omits the ===ASK_RESULT=== wrapper but
// raw stdout passes the syntactic gate. Spec §Failure Categories.
func TestParseAskOutput_MarkerMissing_FallbackToMarkerMissing(t *testing.T) {
	output := "Here is the answer to your question."
	r, err := ParseAskOutput(output)
	if err != nil {
		t.Fatalf("expected fallback success, got error: %v", err)
	}
	if r.Answer != output {
		t.Errorf("Answer = %q, want %q", r.Answer, output)
	}
	if r.ResultSource != ResultSourceFallbackMarkerMissing {
		t.Errorf("ResultSource = %q, want %q", r.ResultSource, ResultSourceFallbackMarkerMissing)
	}
}

func TestParseAskOutput_MarkerMissing_EmptyFails(t *testing.T) {
	if _, err := ParseAskOutput(""); err == nil {
		t.Error("expected error on empty stdout")
	}
}

func TestParseAskOutput_MarkerMissing_WhitespaceOnlyFails(t *testing.T) {
	if _, err := ParseAskOutput("   \n\t  \n"); err == nil {
		t.Error("expected error on whitespace-only stdout")
	}
}

func TestParseAskOutput_MarkerMissing_TooShortFails(t *testing.T) {
	if _, err := ParseAskOutput("hi"); err == nil {
		t.Error("expected error on too-short stdout")
	}
}

// TestParseAskOutput_EmptyAnswer_FallsBackToEmptyAnswer covers the case
// where the JSON parses but the answer field is empty. Under the
// extended fallback, we surface the raw stdout (incl. the empty-answer
// JSON) so the user can see what the agent actually returned.
func TestParseAskOutput_EmptyAnswer_FallsBackToEmptyAnswer(t *testing.T) {
	output := "===ASK_RESULT===\n" + `{"answer":""}`
	r, err := ParseAskOutput(output)
	if err != nil {
		t.Fatalf("expected fallback success, got error: %v", err)
	}
	if r.ResultSource != ResultSourceFallbackEmptyAnswer {
		t.Errorf("ResultSource = %q, want %q", r.ResultSource, ResultSourceFallbackEmptyAnswer)
	}
	if !strings.Contains(r.Answer, "===ASK_RESULT===") {
		t.Errorf("fallback Answer should be the raw stdout; got %q", r.Answer)
	}
}

// TestParseAskOutput_MalformedJSON_FallsBackToUnmarshal pins the
// marker-present-but-unparseable case. Old behaviour failed closed; new
// contract surfaces the raw stdout with fallback_unmarshal so the user
// still sees the output.
func TestParseAskOutput_MalformedJSON_FallsBackToUnmarshal(t *testing.T) {
	output := "===ASK_RESULT===\n{not json"
	r, err := ParseAskOutput(output)
	if err != nil {
		t.Fatalf("expected fallback success, got error: %v", err)
	}
	if r.ResultSource != ResultSourceFallbackUnmarshal {
		t.Errorf("ResultSource = %q, want %q", r.ResultSource, ResultSourceFallbackUnmarshal)
	}
	if r.Answer != output {
		t.Errorf("fallback Answer should equal raw stdout; got %q", r.Answer)
	}
}

// TestParseAskOutput_MarkerInStringValue_FallsBackToUnmarshal covers the
// 2026-04-25 incident shape where the agent emits a marker followed by
// JSON whose string value also contains the marker keyword. extractJSON
// is brace-counting + string-aware, but segmentAfterMarker still splits
// on every marker occurrence, leaving the JSON segment truncated. New
// contract: that truncation routes to fallback_unmarshal instead of
// failing closed. Spec §Failure Categories lists this as one of the
// patterns the unmarshal fallback covers.
func TestParseAskOutput_MarkerInStringValue_FallsBackToUnmarshal(t *testing.T) {
	output := "===ASK_RESULT===\n" + `{"answer": "Schema reference: ===ASK_RESULT=== marker is required."}`
	r, err := ParseAskOutput(output)
	if err != nil {
		t.Fatalf("expected fallback success, got error: %v", err)
	}
	if r.ResultSource != ResultSourceFallbackUnmarshal {
		t.Errorf("ResultSource = %q, want %q", r.ResultSource, ResultSourceFallbackUnmarshal)
	}
}

// TestParseAskOutput_MarkerInBodyNoJSON_FallsBackToSegmentsNoJSON pins the
// 2026-04-26 incident where the agent answered in markdown that字面引用
// the marker keyword in body text without ever emitting a JSON object.
// Old behaviour returned "expected JSON object after marker" → :x: 解析失敗.
// New contract: fallback_segments_no_json with the markdown surfaced.
func TestParseAskOutput_MarkerInBodyNoJSON_FallsBackToSegmentsNoJSON(t *testing.T) {
	output := "*簡答*\n答案。結尾附上 `===ASK_RESULT===` JSON 區塊。\n\n*依據*\n以 `===ASK_RESULT===` 結尾。"
	r, err := ParseAskOutput(output)
	if err != nil {
		t.Fatalf("expected fallback success, got error: %v", err)
	}
	if r.ResultSource != ResultSourceFallbackSegmentsNoJSON {
		t.Errorf("ResultSource = %q, want %q", r.ResultSource, ResultSourceFallbackSegmentsNoJSON)
	}
}

func TestParseAskOutput_MultipleMarkers_LastWins(t *testing.T) {
	output := "===ASK_RESULT===\n" + `{"answer":"first"}` + "\n\n===ASK_RESULT===\n" + `{"answer":"last"}`
	r, err := ParseAskOutput(output)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if !strings.Contains(r.Answer, "last") {
		t.Errorf("expected last marker to win, got %q", r.Answer)
	}
}

// TestParseAskOutput_FenceMarkers guards the opencode fence pattern where
// the payload sits between an opening and closing marker. Without the
// fence-aware walk the last marker's segment is empty and parsing errors
// out before reaching the real payload.
func TestParseAskOutput_FenceMarkers(t *testing.T) {
	output := "===ASK_RESULT===\n" + `{"answer":"real answer","confidence":"high"}` + "\n===ASK_RESULT==="
	r, err := ParseAskOutput(output)
	if err != nil {
		t.Fatalf("fence pattern must parse: %v", err)
	}
	if r.Answer != "real answer" || r.Confidence != "high" {
		t.Errorf("got %+v", r)
	}
}
