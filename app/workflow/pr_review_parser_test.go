package workflow

import "testing"

func TestParseReviewOutput_Posted(t *testing.T) {
	out := "===REVIEW_RESULT===\n" + `{
  "status": "POSTED",
  "summary": "LGTM with minor nits",
  "comments_posted": 3,
  "comments_skipped": 1,
  "severity_summary": "minor"
}`
	r, err := ParseReviewOutput(out)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != "POSTED" || r.CommentsPosted != 3 || r.Severity != "minor" {
		t.Errorf("got %+v", r)
	}
}

func TestParseReviewOutput_Skipped(t *testing.T) {
	out := "===REVIEW_RESULT===\n" + `{"status": "SKIPPED", "summary": "lockfile only", "reason": "lockfile_only"}`
	r, err := ParseReviewOutput(out)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != "SKIPPED" || r.Reason != "lockfile_only" {
		t.Errorf("got %+v", r)
	}
}

func TestParseReviewOutput_Error(t *testing.T) {
	out := "===REVIEW_RESULT===\n" + `{"status": "ERROR", "error": "422 invalid head sha"}`
	r, err := ParseReviewOutput(out)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != "ERROR" || r.Error != "422 invalid head sha" {
		t.Errorf("got %+v", r)
	}
}

func TestParseReviewOutput_UnknownStatus(t *testing.T) {
	_, err := ParseReviewOutput("===REVIEW_RESULT===\n" + `{"status": "NOPE"}`)
	if err == nil {
		t.Error("unknown status must error")
	}
}
