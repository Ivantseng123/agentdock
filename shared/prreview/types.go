package prreview

const (
	MaxCommentBody = 4096
	MaxSummaryBody = 2048

	commentTruncSuffix = "\n\n_…(comment truncated)_"
	summaryTruncSuffix = "\n\n_(summary truncated; see inline comments)_"
)

type Severity string

const (
	SeverityBlocker    Severity = "blocker"
	SeveritySuggestion Severity = "suggestion"
	SeverityNit        Severity = "nit"
)

type SeveritySummary string

const (
	SummaryClean SeveritySummary = "clean"
	SummaryMinor SeveritySummary = "minor"
	SummaryMajor SeveritySummary = "major"
)

type Side string

const (
	SideLeft  Side = "LEFT"
	SideRight Side = "RIGHT"
)

type CommentJSON struct {
	Path      string   `json:"path"`
	Line      int      `json:"line"`
	Side      Side     `json:"side"`
	Body      string   `json:"body"`
	Severity  Severity `json:"severity"`
	StartLine *int     `json:"start_line,omitempty"`
	StartSide *Side    `json:"start_side,omitempty"`
}

type ReviewJSON struct {
	Summary         string          `json:"summary"`
	SeveritySummary SeveritySummary `json:"severity_summary"`
	Comments        []CommentJSON   `json:"comments"`
}

type SkipReason struct {
	Path   string `json:"path"`
	Line   int    `json:"line"`
	Reason string `json:"reason"`
}

type PostResult struct {
	Posted            int              `json:"posted,omitempty"`
	WouldPost         int              `json:"would_post,omitempty"`
	Skipped           int              `json:"skipped"`
	TruncatedComments int              `json:"truncated_comments"`
	SummaryTruncated  bool             `json:"summary_truncated"`
	SkipReasons       []SkipReason     `json:"skip_reasons"`
	ReviewID          int64            `json:"review_id,omitempty"`
	CommitID          string           `json:"commit_id"`
	DryRun            bool             `json:"dry_run,omitempty"`
	Payload           *CreateReviewReq `json:"payload,omitempty"`
}

type FatalResult struct {
	Error  string `json:"error"`
	Posted int    `json:"posted"`
}

type CreateReviewReq struct {
	CommitID string                  `json:"commit_id"`
	Body     string                  `json:"body"`
	Event    string                  `json:"event"`
	Comments []CreateReviewReqInline `json:"comments"`
}

type CreateReviewReqInline struct {
	Path      string `json:"path"`
	Line      int    `json:"line"`
	Side      string `json:"side"`
	Body      string `json:"body"`
	StartLine *int   `json:"start_line,omitempty"`
	StartSide *Side  `json:"start_side,omitempty"`
}

type FingerprintResult struct {
	Language           string   `json:"language"`
	Confidence         string   `json:"confidence"`
	StyleSources       []string `json:"style_sources"`
	TestRunner         string   `json:"test_runner,omitempty"`
	Framework          string   `json:"framework,omitempty"`
	PRTouchedLanguages []string `json:"pr_touched_languages"`
	PRSubprojects      []string `json:"pr_subprojects"`
}
