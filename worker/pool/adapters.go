package pool

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	ghclient "github.com/Ivantseng123/agentdock/shared/github"
	"github.com/Ivantseng123/agentdock/worker/agent"
)

// AgentRunnerAdapter wraps *agent.Runner to satisfy the Runner interface.
// It injects slog.Default() as the logger agent.Runner.Run requires.
type AgentRunnerAdapter struct {
	Runner *agent.Runner
}

func (a *AgentRunnerAdapter) Run(ctx context.Context, workDir, prompt string, opts agent.RunOptions) (string, error) {
	return a.Runner.Run(ctx, slog.Default(), workDir, prompt, opts)
}

// RepoCacheAdapter wraps *ghclient.RepoCache to satisfy the RepoProvider interface.
type RepoCacheAdapter struct {
	Cache *ghclient.RepoCache
}

func (a *RepoCacheAdapter) Prepare(cloneURL, branch, token string) (string, error) {
	barePath, err := a.Cache.EnsureRepo(cloneURL, token)
	if err != nil {
		return "", err
	}
	wtBase := a.Cache.WorktreeDir()
	if err := os.MkdirAll(wtBase, 0755); err != nil {
		return "", fmt.Errorf("create worktree base dir: %w", err)
	}
	worktreePath, err := os.MkdirTemp(wtBase, "triage-repo-*")
	if err != nil {
		return "", fmt.Errorf("create worktree temp dir: %w", err)
	}
	// MkdirTemp creates the dir; git worktree add needs it to not exist.
	os.Remove(worktreePath)
	if err := a.Cache.AddWorktree(barePath, branch, worktreePath, token); err != nil {
		return "", err
	}
	return worktreePath, nil
}

// PrepareAt clones into targetPath rather than the cache's default worktree
// dir. Used for ref repos so worker can co-locate them with the primary
// worktree under `<primary>-refs/<owner>__<repo>`.
//
// Caller is responsible for the PARENT of targetPath existing; this method
// removes targetPath itself first because git worktree add requires the
// target dir not to exist.
func (a *RepoCacheAdapter) PrepareAt(cloneURL, branch, token, targetPath string) error {
	barePath, err := a.Cache.EnsureRepo(cloneURL, token)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(targetPath); err != nil {
		return fmt.Errorf("clear ref target %s: %w", targetPath, err)
	}
	return a.Cache.AddWorktree(barePath, branch, targetPath, token)
}

func (a *RepoCacheAdapter) RemoveWorktree(path string) error { return a.Cache.RemoveWorktree(path) }
func (a *RepoCacheAdapter) CleanAll() error                  { return a.Cache.CleanAll() }
func (a *RepoCacheAdapter) PurgeStale() error                { return a.Cache.PurgeStale() }
