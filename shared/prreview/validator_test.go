package prreview

import (
	"strings"
	"testing"
)

func TestValidate_OKSingleLine(t *testing.T) {
	r := ReviewJSON{
		Summary:         "good",
		SeveritySummary: SummaryMinor,
		Comments: []CommentJSON{
			{Path: "a.go", Line: 5, Side: SideRight, Body: "x", Severity: SeverityNit},
		},
	}
	if err := Validate(&r); err != nil {
		t.Fatalf("want nil, got %v", err)
	}
}

func TestValidate_OKMultiLine(t *testing.T) {
	sl := 3
	ss := SideRight
	r := ReviewJSON{
		Summary:         "good",
		SeveritySummary: SummaryMinor,
		Comments: []CommentJSON{
			{
				Path: "a.go", Line: 5, Side: SideRight, StartLine: &sl, StartSide: &ss,
				Body: "x", Severity: SeverityNit,
			},
		},
	}
	if err := Validate(&r); err != nil {
		t.Fatalf("want nil, got %v", err)
	}
}

func TestValidate_OKEmptyComments(t *testing.T) {
	r := ReviewJSON{Summary: "nothing to report", SeveritySummary: SummaryClean, Comments: []CommentJSON{}}
	if err := Validate(&r); err != nil {
		t.Fatalf("want nil, got %v", err)
	}
}

func TestValidate_MissingSummary(t *testing.T) {
	r := ReviewJSON{SeveritySummary: SummaryClean, Comments: []CommentJSON{}}
	err := Validate(&r)
	if err == nil || !strings.Contains(err.Error(), "summary") {
		t.Fatalf("want summary error, got %v", err)
	}
}

func TestValidate_BadSeveritySummary(t *testing.T) {
	r := ReviewJSON{Summary: "s", SeveritySummary: "bogus", Comments: []CommentJSON{}}
	err := Validate(&r)
	if err == nil || !strings.Contains(err.Error(), "severity_summary") {
		t.Fatalf("want severity_summary error, got %v", err)
	}
}

func TestValidate_BadCommentSeverity(t *testing.T) {
	r := ReviewJSON{
		Summary: "s", SeveritySummary: SummaryMinor,
		Comments: []CommentJSON{
			{Path: "a.go", Line: 1, Side: SideRight, Body: "x", Severity: "bogus"},
		},
	}
	err := Validate(&r)
	if err == nil || !strings.Contains(err.Error(), "severity") {
		t.Fatalf("want severity error, got %v", err)
	}
}

func TestValidate_BadSide(t *testing.T) {
	r := ReviewJSON{
		Summary: "s", SeveritySummary: SummaryMinor,
		Comments: []CommentJSON{
			{Path: "a.go", Line: 1, Side: "MIDDLE", Body: "x", Severity: SeverityNit},
		},
	}
	err := Validate(&r)
	if err == nil || !strings.Contains(err.Error(), "side") {
		t.Fatalf("want side error, got %v", err)
	}
}

func TestValidate_MultiLineHalfDefined(t *testing.T) {
	sl := 3
	r := ReviewJSON{
		Summary: "s", SeveritySummary: SummaryMinor,
		Comments: []CommentJSON{
			{Path: "a.go", Line: 5, Side: SideRight, StartLine: &sl, Body: "x", Severity: SeverityNit},
		},
	}
	err := Validate(&r)
	if err == nil || !strings.Contains(err.Error(), "start_line and start_side") {
		t.Fatalf("want half-defined error, got %v", err)
	}
}

func TestValidate_MultiLineMismatchedSides(t *testing.T) {
	sl := 3
	ss := SideLeft
	r := ReviewJSON{
		Summary: "s", SeveritySummary: SummaryMinor,
		Comments: []CommentJSON{
			{
				Path: "a.go", Line: 5, Side: SideRight, StartLine: &sl, StartSide: &ss,
				Body: "x", Severity: SeverityNit,
			},
		},
	}
	err := Validate(&r)
	if err == nil || !strings.Contains(err.Error(), "must match") {
		t.Fatalf("want mismatched-side error, got %v", err)
	}
}

func TestValidate_MultiLineStartAfterLine(t *testing.T) {
	sl := 10
	ss := SideRight
	r := ReviewJSON{
		Summary: "s", SeveritySummary: SummaryMinor,
		Comments: []CommentJSON{
			{
				Path: "a.go", Line: 5, Side: SideRight, StartLine: &sl, StartSide: &ss,
				Body: "x", Severity: SeverityNit,
			},
		},
	}
	err := Validate(&r)
	if err == nil || !strings.Contains(err.Error(), "start_line") {
		t.Fatalf("want start>line error, got %v", err)
	}
}
