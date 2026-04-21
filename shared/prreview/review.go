package prreview

import (
	"context"
	"fmt"
)

// ValidateAndPostInput aggregates every parameter ValidateAndPost needs. Keeps
// the cobra wiring trivial.
type ValidateAndPostInput struct {
	Review   *ReviewJSON
	PRURL    string
	CommitID string
	Token    string
	APIBase  string
	DryRun   bool
}

// ValidateAndPost runs schema validation, fetches the PR diff, filters /
// truncates comments against the diff, assembles the POST payload, and either
// sends it (real mode) or returns it (dry run).
func ValidateAndPost(ctx context.Context, in ValidateAndPostInput) (*PostResult, error) {
	if in.Review == nil {
		return nil, fmt.Errorf("%s: review nil", ErrReviewSchemaInvalid)
	}
	if in.PRURL == "" {
		return nil, fmt.Errorf("%s", ErrMissingPRURL)
	}
	if in.Token == "" {
		return nil, fmt.Errorf("%s", ErrMissingToken)
	}
	if err := Validate(in.Review); err != nil {
		return nil, fmt.Errorf("%s: %w", ErrReviewSchemaInvalid, err)
	}

	apiBase := in.APIBase
	if apiBase == "" {
		apiBase = "https://api.github.com"
	}

	files, err := listDiffFiles(ctx, apiBase, in.PRURL, in.Token, DefaultMaxWallTime)
	if err != nil {
		return nil, err
	}
	diff := parseDiffMap(files)

	inline, skips, trunc, summaryTrunc := filterAndTruncate(in.Review, diff)

	payload := &CreateReviewReq{
		CommitID: in.CommitID,
		Body:     in.Review.Summary,
		Event:    "COMMENT",
		Comments: inline,
	}

	res := &PostResult{
		Skipped:           len(skips),
		TruncatedComments: trunc,
		SummaryTruncated:  summaryTrunc,
		SkipReasons:       skips,
		CommitID:          in.CommitID,
	}

	if in.DryRun {
		res.DryRun = true
		res.WouldPost = len(inline)
		res.Payload = payload
		return res, nil
	}

	id, err := createReview(ctx, apiBase, in.PRURL, in.Token, payload, DefaultMaxWallTime)
	if err != nil {
		return nil, err
	}
	res.Posted = len(inline)
	res.ReviewID = id
	return res, nil
}

// filterAndTruncate takes the validated review + diff map and produces the
// list of comments that will actually go into the POST payload, plus the
// SkipReasons for anything dropped. It also truncates over-long comment
// bodies and the summary, mutating r.Summary in place (helper is one-shot,
// so this is fine and simpler than copying the struct).
func filterAndTruncate(r *ReviewJSON, diff map[string]*validLines) (
	posted []CreateReviewReqInline, skipped []SkipReason, truncatedComments int, summaryTruncated bool,
) {
	if len(r.Summary) > MaxSummaryBody {
		r.Summary = r.Summary[:MaxSummaryBody] + summaryTruncSuffix
		summaryTruncated = true
	}

	for _, c := range r.Comments {
		valid, ok := diff[c.Path]
		if !ok {
			skipped = append(skipped, SkipReason{Path: c.Path, Line: c.Line, Reason: "file not in diff"})
			continue
		}
		if c.StartLine != nil {
			if !valid.has(*c.StartLine, string(*c.StartSide)) {
				skipped = append(skipped, SkipReason{Path: c.Path, Line: *c.StartLine, Reason: "start_line/side not in diff"})
				continue
			}
		}
		if !valid.has(c.Line, string(c.Side)) {
			skipped = append(skipped, SkipReason{Path: c.Path, Line: c.Line, Reason: "line/side not in diff"})
			continue
		}

		body := c.Body
		if len(body) > MaxCommentBody {
			body = body[:MaxCommentBody] + commentTruncSuffix
			truncatedComments++
		}
		posted = append(posted, CreateReviewReqInline{
			Path:      c.Path,
			Line:      c.Line,
			Side:      string(c.Side),
			Body:      body,
			StartLine: c.StartLine,
			StartSide: c.StartSide,
		})
	}
	return posted, skipped, truncatedComments, summaryTruncated
}
