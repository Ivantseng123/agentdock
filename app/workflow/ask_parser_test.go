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
}

func TestParseAskOutput_MarkerMissing(t *testing.T) {
	output := `{"answer":"no marker"}`
	if _, err := ParseAskOutput(output); err == nil {
		t.Error("expected error when marker missing")
	}
}

func TestParseAskOutput_EmptyAnswer(t *testing.T) {
	output := "===ASK_RESULT===\n" + `{"answer":""}`
	if _, err := ParseAskOutput(output); err == nil {
		t.Error("expected error when answer empty")
	}
}

func TestParseAskOutput_MalformedJSON(t *testing.T) {
	output := "===ASK_RESULT===\n{not json"
	if _, err := ParseAskOutput(output); err == nil {
		t.Error("expected error on malformed JSON")
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
