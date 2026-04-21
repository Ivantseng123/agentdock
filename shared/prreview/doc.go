// Package prreview implements GitHub pull-request review analysis and posting
// for the github-pr-review skill. It is invoked by the agentdock pr-review-helper
// subcommand on the worker.
//
// Callers provide a review JSON via stdin (see ReviewJSON). The package
// validates line numbers against the PR's actual diff, truncates oversized
// content, and posts a single COMMENT-event review to GitHub. Rate-limit
// handling honors GitHub's Retry-After header with an exponential fallback.
package prreview
