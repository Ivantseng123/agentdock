package githubapp

import "regexp"

// tokenPattern matches GitHub-issued tokens and Bearer-prefixed values
// that might appear in upstream response bodies (e.g. a misbehaving
// proxy echoing the request's Authorization header back, or a debug
// page interpolating one). When such a body is folded into an error
// or log line, we don't want the token to ride along.
//
// Covers: ghs_ (installation), ghp_ (classic PAT), github_pat_
// (fine-grained PAT), gho_ (oauth), ghu_ (user-to-server), ghr_
// (refresh), and `Bearer <opaque>` headers.
var tokenPattern = regexp.MustCompile(
	`(?i)(ghs_|ghp_|github_pat_|gho_|ghu_|ghr_)[A-Za-z0-9_]{10,}` +
		`|Bearer\s+[A-Za-z0-9._-]{10,}`,
)

// redactGitHubBody returns s with any token-like substrings replaced
// by "[REDACTED]". Used before embedding upstream response bodies in
// error messages or log lines.
func redactGitHubBody(s string) string {
	return tokenPattern.ReplaceAllString(s, "[REDACTED]")
}
