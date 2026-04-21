package prreview

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

var _ = bytes.NewReader

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

func TestValidateAndPost_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/pulls/42/files") && r.Method == "GET":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `[{"filename":"a.go","status":"modified","patch":"@@ -1 +1,2 @@\n a\n+b\n"}]`)
		case strings.Contains(r.URL.Path, "/pulls/42/reviews") && r.Method == "POST":
			body, _ := io.ReadAll(r.Body)
			var got CreateReviewReq
			_ = json.Unmarshal(body, &got)
			if got.Event != "COMMENT" {
				t.Errorf("event: want COMMENT, got %q", got.Event)
			}
			if got.CommitID != "deadbeef" {
				t.Errorf("commit_id: want deadbeef, got %q", got.CommitID)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id": 12345}`)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	r := ReviewJSON{
		Summary: "ok", SeveritySummary: SummaryMinor,
		Comments: []CommentJSON{
			{Path: "a.go", Line: 2, Side: SideRight, Body: "nit", Severity: SeverityNit},
		},
	}
	res, err := ValidateAndPost(context.Background(), ValidateAndPostInput{
		Review:   &r,
		PRURL:    "https://github.com/x/y/pull/42",
		CommitID: "deadbeef",
		Token:    "tok",
		APIBase:  srv.URL,
	})
	if err != nil {
		t.Fatalf("ValidateAndPost: %v", err)
	}
	if res.Posted != 1 {
		t.Errorf("posted: want 1, got %d", res.Posted)
	}
	if res.ReviewID != 12345 {
		t.Errorf("review_id: want 12345, got %d", res.ReviewID)
	}
	if res.CommitID != "deadbeef" {
		t.Errorf("commit_id: want deadbeef, got %q", res.CommitID)
	}
	if res.DryRun {
		t.Error("expected DryRun=false")
	}
}

func TestValidateAndPost_DryRun(t *testing.T) {
	var postHits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/pulls/42/files") && r.Method == "GET" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `[{"filename":"a.go","status":"modified","patch":"@@ -1 +1,2 @@\n a\n+b\n"}]`)
			return
		}
		if r.Method == "POST" {
			postHits++
		}
		t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()

	r := ReviewJSON{
		Summary: "ok", SeveritySummary: SummaryMinor,
		Comments: []CommentJSON{
			{Path: "a.go", Line: 2, Side: SideRight, Body: "nit", Severity: SeverityNit},
		},
	}
	res, err := ValidateAndPost(context.Background(), ValidateAndPostInput{
		Review:   &r,
		PRURL:    "https://github.com/x/y/pull/42",
		CommitID: "deadbeef",
		Token:    "tok",
		APIBase:  srv.URL,
		DryRun:   true,
	})
	if err != nil {
		t.Fatalf("ValidateAndPost(DryRun): %v", err)
	}
	if !res.DryRun {
		t.Error("want DryRun=true")
	}
	if res.WouldPost != 1 {
		t.Errorf("would_post: want 1, got %d", res.WouldPost)
	}
	if res.Payload == nil {
		t.Error("Payload should be populated in dry-run")
	}
	if postHits != 0 {
		t.Errorf("POST should never be called in dry-run, got %d", postHits)
	}
}

func TestValidateAndPost_StaleCommit422(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/files") {
			_, _ = io.WriteString(w, `[{"filename":"a.go","status":"modified","patch":"@@ -1 +1,2 @@\n a\n+b\n"}]`)
			return
		}
		w.WriteHeader(422)
		_, _ = io.WriteString(w, `{"message":"commit_id not in PR"}`)
	}))
	defer srv.Close()

	r := ReviewJSON{
		Summary: "ok", SeveritySummary: SummaryMinor,
		Comments: []CommentJSON{
			{Path: "a.go", Line: 2, Side: SideRight, Body: "nit", Severity: SeverityNit},
		},
	}
	_, err := ValidateAndPost(context.Background(), ValidateAndPostInput{
		Review:   &r,
		PRURL:    "https://github.com/x/y/pull/42",
		CommitID: "stale",
		Token:    "tok",
		APIBase:  srv.URL,
	})
	if err == nil {
		t.Fatal("want error on 422")
	}
	if !strings.Contains(err.Error(), ErrGitHubStaleCommit) {
		t.Errorf("want stale-commit error, got %v", err)
	}
}

func TestValidateAndPost_Unauth401(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
	}))
	defer srv.Close()

	r := ReviewJSON{Summary: "ok", SeveritySummary: SummaryClean, Comments: []CommentJSON{}}
	_, err := ValidateAndPost(context.Background(), ValidateAndPostInput{
		Review:   &r,
		PRURL:    "https://github.com/x/y/pull/42",
		CommitID: "x",
		Token:    "bad",
		APIBase:  srv.URL,
	})
	if err == nil || !strings.Contains(err.Error(), ErrGitHubUnauth) {
		t.Errorf("want unauth error, got %v", err)
	}
}
