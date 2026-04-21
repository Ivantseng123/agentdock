package prreview

const (
	ErrGitHubUnauth        = "GitHub token invalid or expired"
	ErrGitHubForbidden     = "Insufficient GitHub token scope (need PR write)"
	ErrGitHubNotFound      = "PR not found (404)"
	ErrGitHubStaleCommit   = "PR head moved during review (422); please re-trigger with current SHA"
	ErrGitHubRateLimit     = "GitHub rate-limited after 3 attempts (max 30s); please re-trigger later"
	ErrGitHubWallTime      = "GitHub rate-limited; wall time exceeded"
	ErrReviewSchemaInvalid = "review schema invalid"
	ErrMissingPRURL        = "PR_URL required"
	ErrMissingToken        = "GITHUB_TOKEN required"
	ErrGitRevParseFailed   = "git rev-parse HEAD failed"
)
