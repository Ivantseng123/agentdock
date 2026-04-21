package prreview

import (
	"fmt"
)

// Validate enforces the ReviewJSON schema documented in
// docs/superpowers/specs/2026-04-21-github-pr-review-skill-design.md
// §Review JSON schema. Returns a human-readable error on the first failure;
// callers surface this through the helper's stderr on exit 2.
func Validate(r *ReviewJSON) error {
	if r == nil {
		return fmt.Errorf("review is nil")
	}
	if r.Summary == "" {
		return fmt.Errorf("summary must be non-empty")
	}
	switch r.SeveritySummary {
	case SummaryClean, SummaryMinor, SummaryMajor:
	default:
		return fmt.Errorf("severity_summary must be clean|minor|major, got %q", r.SeveritySummary)
	}
	if r.Comments == nil {
		return fmt.Errorf("comments must be an array (may be empty)")
	}
	for i, c := range r.Comments {
		if err := validateComment(&c); err != nil {
			return fmt.Errorf("comments[%d]: %w", i, err)
		}
	}
	return nil
}

func validateComment(c *CommentJSON) error {
	if c.Path == "" {
		return fmt.Errorf("path required")
	}
	if c.Line <= 0 {
		return fmt.Errorf("line must be > 0")
	}
	if c.Side != SideLeft && c.Side != SideRight {
		return fmt.Errorf("side must be LEFT|RIGHT, got %q", c.Side)
	}
	if c.Body == "" {
		return fmt.Errorf("body required")
	}
	switch c.Severity {
	case SeverityBlocker, SeveritySuggestion, SeverityNit:
	default:
		return fmt.Errorf("severity must be blocker|suggestion|nit, got %q", c.Severity)
	}
	if (c.StartLine == nil) != (c.StartSide == nil) {
		return fmt.Errorf("start_line and start_side must both be present or both absent")
	}
	if c.StartLine != nil {
		if *c.StartSide != c.Side {
			return fmt.Errorf("start_side must match side for multi-line comments")
		}
		if *c.StartLine > c.Line {
			return fmt.Errorf("start_line must be <= line for multi-line comments")
		}
		if *c.StartLine <= 0 {
			return fmt.Errorf("start_line must be > 0")
		}
	}
	return nil
}
