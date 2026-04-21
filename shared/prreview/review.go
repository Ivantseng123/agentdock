package prreview

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
