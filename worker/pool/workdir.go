package pool

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/Ivantseng123/agentdock/shared/queue"
	"github.com/Ivantseng123/agentdock/shared/tracing"
)

// WorkDirProvider prepares and cleans up the working directory an agent
// runs against. Two implementations:
//   - RepoCloneProvider: wraps RepoProvider (clone + remove worktree)
//   - EmptyDirProvider: mkdir temp dir + RemoveAll
//
// executeJob selects between them based on whether the Job carries a CloneURL.
type WorkDirProvider interface {
	Prepare(job *queue.Job) (path string, err error)
	Cleanup(path string)
}

// RepoCloneProvider wraps the existing RepoProvider so jobs with CloneURL set
// continue to use the repo cache + git checkout path unchanged.
type RepoCloneProvider struct {
	Repo  RepoProvider
	Token string // github token
}

func (p *RepoCloneProvider) Prepare(job *queue.Job) (string, error) {
	return p.Repo.Prepare(job.CloneURL, job.Branch, p.Token)
}

func (p *RepoCloneProvider) Cleanup(path string) {
	if path == "" {
		return
	}
	_ = p.Repo.RemoveWorktree(path)
}

// EmptyDirProvider mkdirs a fresh temp directory per job. Used by Ask when
// the user didn't attach a repo.
type EmptyDirProvider struct{}

func (p *EmptyDirProvider) Prepare(job *queue.Job) (string, error) {
	dir, err := os.MkdirTemp("", fmt.Sprintf("ask-%s-*", job.ID))
	if err != nil {
		return "", fmt.Errorf("mkdir temp workdir: %w", err)
	}
	return dir, nil
}

func (p *EmptyDirProvider) Cleanup(path string) {
	if path == "" {
		return
	}
	_ = os.RemoveAll(path)
}

// selectProvider picks between RepoCloneProvider and EmptyDirProvider based
// on whether the Job has a CloneURL. The choice is by CloneURL rather than by
// TaskType to keep worker fully workflow-agnostic (spec Goal #6).
func selectProvider(job *queue.Job, repo RepoProvider, token string) WorkDirProvider {
	if job.CloneURL == "" {
		return &EmptyDirProvider{}
	}
	return &RepoCloneProvider{Repo: repo, Token: token}
}

// refsRootPath returns the directory in which ref worktrees live, derived
// from the primary worktree path.
//
// Refs sit INSIDE primary's worktree at `.refs/`. This contradicts the
// original spec §1 design (refs outside primary cwd), which the spec
// justified by avoiding `.gitignore` inheritance and cleanup nesting. That
// design was reversed during initial e2e testing because opencode's
// sandbox blocks any path outside cwd as `external_directory` — the
// agent literally cannot READ refs that live outside its working
// directory. The whole multi-repo feature collapses without inside-cwd
// access. The earlier `-refs` sibling layout left agents staring at
// "讓我檢查允許存取的目錄" before falling through to schema-fallback with
// a 154-byte truncated answer.
//
// `.gitignore` inheritance is a much smaller risk in practice: primary's
// `.gitignore` is unlikely to match `.refs/` (no real project uses that
// path), and ripgrep traverses subdirs by default. The original "spike to
// validate" assumption from spec §6 turned out to be the wrong
// assumption — sandbox boundaries dominate `.gitignore` propagation.
func refsRootPath(primaryPath string) string {
	return filepath.Join(primaryPath, ".refs")
}

// refDirName flattens owner/name → owner__name. GitHub disallows __ in repo
// names, so collisions across distinct refs are impossible. Used for the
// per-ref subdirectory name under refsRootPath.
func refDirName(repo string) string {
	return strings.ReplaceAll(repo, "/", "__")
}

// prepareRefs sequentially clones each ref into <refsRoot>/<owner>__<repo>/.
// Per-ref errors collect into `unavailable` (owner/name) and the loop
// continues — partial success is the contract (spec §4.3). Returns:
//
//   - successful: ref contexts with absolute paths, fed to PromptContext.RefRepos
//   - successfulPaths: same paths in slice form, used by guard + cleanup
//   - unavailable: owner/name list of failed refs, fed to PromptContext.UnavailableRefs
//   - refsRoot: the parent dir; "" when refs is empty (no mkdir performed)
//
// err returns non-nil only when the refs-root mkdir itself fails — per-ref
// failures are absorbed into `unavailable`.
func prepareRefs(ctx context.Context, provider RepoProvider, primaryPath, token string, refs []queue.RefRepo, logger *slog.Logger) (
	successful []queue.RefRepoContext,
	successfulPaths []string,
	unavailable []string,
	refsRoot string,
	err error,
) {
	if len(refs) == 0 {
		return nil, nil, nil, "", nil
	}
	refsRoot = refsRootPath(primaryPath)
	if mkErr := os.MkdirAll(refsRoot, 0755); mkErr != nil {
		return nil, nil, nil, "", fmt.Errorf("mkdir refs root: %w", mkErr)
	}
	for _, r := range refs {
		target := filepath.Join(refsRoot, refDirName(r.Repo))
		// One github.clone_repo span per ref — siblings of the primary
		// clone span, all children of worker.handle_job. repo_role attr
		// distinguishes them in the trace UI.
		refStart := time.Now()
		_, refSpan := tracer.Start(ctx, tracing.SpanGithubCloneRepo,
			trace.WithAttributes(
				attribute.String("repo_role", "ref"),
				attribute.String("repo", r.Repo),
				attribute.String("branch", r.Branch),
			),
		)
		pErr := provider.PrepareAt(r.CloneURL, r.Branch, token, target)
		refSpan.SetAttributes(attribute.Int64("duration_ms", time.Since(refStart).Milliseconds()))
		if pErr != nil {
			refSpan.RecordError(pErr)
			refSpan.SetStatus(codes.Error, "ref clone failed")
			refSpan.End()
			logger.Warn("ref clone failed; continuing with partial context",
				"phase", "處理中", "ref", r.Repo, "branch", r.Branch, "error", pErr)
			unavailable = append(unavailable, r.Repo)
			continue
		}
		refSpan.End()
		successful = append(successful, queue.RefRepoContext{
			Repo: r.Repo, Branch: r.Branch, Path: target,
		})
		successfulPaths = append(successfulPaths, target)
	}
	if len(refs) >= 5 {
		// Spec §3 / Q10: no hard cap, info-log at N≥5 for ops awareness.
		logger.Info("multi-ref ask with high count",
			"phase", "處理中",
			"count_total", len(refs),
			"count_success", len(successful),
			"count_unavailable", len(unavailable),
		)
	}
	return successful, successfulPaths, unavailable, refsRoot, nil
}

// cleanupRefs removes ref worktrees in reverse order then the refs-root dir.
// Best-effort: failures are silent because primary cleanup runs regardless,
// and a leftover ref dir gets reaped by the next PurgeStale.
func cleanupRefs(provider RepoProvider, paths []string, refsRoot string) {
	for i := len(paths) - 1; i >= 0; i-- {
		_ = provider.RemoveWorktree(paths[i])
	}
	if refsRoot != "" {
		_ = os.RemoveAll(refsRoot)
	}
}
