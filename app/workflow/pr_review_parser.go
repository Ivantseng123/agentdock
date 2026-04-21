package workflow

import (
	"encoding/json"
	"fmt"
	"strings"
)

const reviewMarker = "===REVIEW_RESULT==="

// ReviewResult is the three-state ===REVIEW_RESULT=== JSON from the
// github-pr-review skill. See 2026-04-21-github-pr-review-skill-design.md
// §Result marker contract.
type ReviewResult struct {
	Status          string `json:"status"` // POSTED | SKIPPED | ERROR
	Summary         string `json:"summary,omitempty"`
	CommentsPosted  int    `json:"comments_posted,omitempty"`
	CommentsSkipped int    `json:"comments_skipped,omitempty"`
	Severity        string `json:"severity_summary,omitempty"` // clean|minor|major
	Reason          string `json:"reason,omitempty"`           // for SKIPPED
	Error           string `json:"error,omitempty"`            // for ERROR
}

func ParseReviewOutput(output string) (ReviewResult, error) {
	output = strings.TrimSpace(output)
	idx := strings.LastIndex(output, reviewMarker)
	if idx == -1 {
		return ReviewResult{}, fmt.Errorf("%s marker not found", reviewMarker)
	}
	body := strings.TrimSpace(output[idx+len(reviewMarker):])
	jsonStr := extractJSON(body)
	var r ReviewResult
	if err := json.Unmarshal([]byte(jsonStr), &r); err != nil {
		return ReviewResult{}, fmt.Errorf("unmarshal: %w", err)
	}
	switch r.Status {
	case "POSTED", "SKIPPED", "ERROR":
		return r, nil
	default:
		return ReviewResult{}, fmt.Errorf("unknown review status %q", r.Status)
	}
}
