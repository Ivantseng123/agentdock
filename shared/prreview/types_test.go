package prreview

import (
	"encoding/json"
	"testing"
)

func TestReviewJSON_SingleLineRoundTrip(t *testing.T) {
	original := ReviewJSON{
		Summary:         "LGTM.",
		SeveritySummary: SummaryClean,
		Comments: []CommentJSON{
			{
				Path:     "foo.go",
				Line:     10,
				Side:     SideRight,
				Body:     "nice",
				Severity: SeverityNit,
			},
		},
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got ReviewJSON
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.Comments[0].StartLine != nil || got.Comments[0].StartSide != nil {
		t.Errorf("expected start_line/start_side absent, got %v/%v",
			got.Comments[0].StartLine, got.Comments[0].StartSide)
	}
}

func TestReviewJSON_MultiLineRoundTrip(t *testing.T) {
	startLine := 10
	startSide := SideRight
	original := ReviewJSON{
		Summary:         "Multi-line concern.",
		SeveritySummary: SummaryMajor,
		Comments: []CommentJSON{
			{
				Path:      "bar.py",
				StartLine: &startLine,
				StartSide: &startSide,
				Line:      15,
				Side:      SideRight,
				Body:      "rewrite this block",
				Severity:  SeverityBlocker,
			},
		},
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got ReviewJSON
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.Comments[0].StartLine == nil || *got.Comments[0].StartLine != 10 {
		t.Errorf("start_line round-trip: got %v, want 10", got.Comments[0].StartLine)
	}
	if got.Comments[0].StartSide == nil || *got.Comments[0].StartSide != SideRight {
		t.Errorf("start_side round-trip: got %v, want RIGHT", got.Comments[0].StartSide)
	}
}

func TestPostResult_OmitEmpty(t *testing.T) {
	result := PostResult{Posted: 3, CommitID: "abc"}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(data)
	for _, field := range []string{`"dry_run"`, `"would_post"`, `"payload"`} {
		if contains(s, field) {
			t.Errorf("non-dry-run result should omit %s, got %s", field, s)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
