package prreview

import (
	"strings"
	"testing"
)

func TestFilterAndTruncate_AllValid(t *testing.T) {
	files := []PRFile{
		{Filename: "a.go", Patch: "@@ -1,1 +1,2 @@\n a\n+b\n"},
	}
	diff := parseDiffMap(files)
	r := ReviewJSON{
		Summary:         "ok",
		SeveritySummary: SummaryMinor,
		Comments: []CommentJSON{
			{Path: "a.go", Line: 2, Side: SideRight, Body: "nit", Severity: SeverityNit},
		},
	}
	posted, skips, trunc, _ := filterAndTruncate(&r, diff)
	if len(posted) != 1 || len(skips) != 0 {
		t.Errorf("want 1 posted, 0 skipped, got %d / %d", len(posted), len(skips))
	}
	if trunc != 0 {
		t.Errorf("want 0 truncated, got %d", trunc)
	}
}

func TestFilterAndTruncate_LineOutsideDiff(t *testing.T) {
	diff := parseDiffMap([]PRFile{{Filename: "a.go", Patch: "@@ -1,1 +1,2 @@\n a\n+b\n"}})
	r := ReviewJSON{
		Summary: "ok", SeveritySummary: SummaryMinor,
		Comments: []CommentJSON{
			{Path: "a.go", Line: 99, Side: SideRight, Body: "x", Severity: SeverityNit},
		},
	}
	posted, skips, _, _ := filterAndTruncate(&r, diff)
	if len(posted) != 0 || len(skips) != 1 {
		t.Fatalf("want 0 posted 1 skipped, got %d / %d", len(posted), len(skips))
	}
	if !strings.Contains(skips[0].Reason, "not in diff") {
		t.Errorf("reason text: got %q", skips[0].Reason)
	}
}

func TestFilterAndTruncate_FileNotInDiff(t *testing.T) {
	diff := parseDiffMap([]PRFile{{Filename: "a.go", Patch: "@@ -1 +1 @@\n a\n"}})
	r := ReviewJSON{
		Summary: "ok", SeveritySummary: SummaryMinor,
		Comments: []CommentJSON{
			{Path: "other.go", Line: 1, Side: SideRight, Body: "x", Severity: SeverityNit},
		},
	}
	posted, skips, _, _ := filterAndTruncate(&r, diff)
	if len(posted) != 0 || len(skips) != 1 {
		t.Fatalf("want 0 posted 1 skipped, got %d / %d", len(posted), len(skips))
	}
	if !strings.Contains(skips[0].Reason, "file not in diff") {
		t.Errorf("reason text: got %q", skips[0].Reason)
	}
}

func TestFilterAndTruncate_MultiLineSideMismatchSkipped(t *testing.T) {
	// Multi-line comment on LEFT where diff only has RIGHT lines should skip.
	diff := parseDiffMap([]PRFile{{Filename: "a.go", Patch: "@@ -1 +1,2 @@\n a\n+b\n"}})
	sl := 1
	ss := SideLeft
	r := ReviewJSON{
		Summary: "ok", SeveritySummary: SummaryMinor,
		Comments: []CommentJSON{
			{
				Path: "a.go", Line: 2, Side: SideLeft,
				StartLine: &sl, StartSide: &ss,
				Body: "x", Severity: SeverityNit,
			},
		},
	}
	posted, skips, _, _ := filterAndTruncate(&r, diff)
	if len(posted) != 0 || len(skips) != 1 {
		t.Fatalf("want skip, got %d / %d", len(posted), len(skips))
	}
}

func TestFilterAndTruncate_CommentBodyTruncated(t *testing.T) {
	diff := parseDiffMap([]PRFile{{Filename: "a.go", Patch: "@@ -1 +1,2 @@\n a\n+b\n"}})
	long := strings.Repeat("x", MaxCommentBody+200)
	r := ReviewJSON{
		Summary: "ok", SeveritySummary: SummaryMinor,
		Comments: []CommentJSON{
			{Path: "a.go", Line: 2, Side: SideRight, Body: long, Severity: SeverityNit},
		},
	}
	posted, _, trunc, _ := filterAndTruncate(&r, diff)
	if trunc != 1 {
		t.Errorf("want truncated_comments=1, got %d", trunc)
	}
	if len(posted) != 1 {
		t.Fatalf("want 1 posted, got %d", len(posted))
	}
	if !strings.HasSuffix(posted[0].Body, commentTruncSuffix) {
		t.Errorf("truncated body should end with suffix, got %q", posted[0].Body[len(posted[0].Body)-50:])
	}
}

func TestFilterAndTruncate_SummaryTruncated(t *testing.T) {
	long := strings.Repeat("y", MaxSummaryBody+200)
	r := ReviewJSON{
		Summary: long, SeveritySummary: SummaryClean,
		Comments: []CommentJSON{},
	}
	_, _, _, summaryTrunc := filterAndTruncate(&r, map[string]*validLines{})
	if !summaryTrunc {
		t.Errorf("summary should be truncated")
	}
	if !strings.HasSuffix(r.Summary, summaryTruncSuffix) {
		t.Errorf("truncated summary should end with suffix, got %q", r.Summary[len(r.Summary)-60:])
	}
}
