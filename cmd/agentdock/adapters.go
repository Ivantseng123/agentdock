package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/Ivantseng123/agentdock/internal/bot"
	ghclient "github.com/Ivantseng123/agentdock/shared/github"
	slackclient "github.com/Ivantseng123/agentdock/internal/slack"
)

// agentRunnerAdapter wraps AgentRunner to satisfy pool.Runner interface.
type agentRunnerAdapter struct {
	runner *bot.AgentRunner
}

func (a *agentRunnerAdapter) Run(ctx context.Context, workDir, prompt string, opts bot.RunOptions) (string, error) {
	return a.runner.Run(ctx, slog.Default(), workDir, prompt, opts)
}

// repoCacheAdapter wraps RepoCache to satisfy pool.RepoProvider interface.
type repoCacheAdapter struct {
	cache *ghclient.RepoCache
}

func (a *repoCacheAdapter) Prepare(cloneURL, branch, token string) (string, error) {
	barePath, err := a.cache.EnsureRepo(cloneURL, token)
	if err != nil {
		return "", err
	}
	wtBase := a.cache.WorktreeDir()
	if err := os.MkdirAll(wtBase, 0755); err != nil {
		return "", fmt.Errorf("create worktree base dir: %w", err)
	}
	worktreePath, err := os.MkdirTemp(wtBase, "triage-repo-*")
	if err != nil {
		return "", fmt.Errorf("create worktree temp dir: %w", err)
	}
	// MkdirTemp creates the dir; git worktree add needs it to not exist.
	os.Remove(worktreePath)
	if err := a.cache.AddWorktree(barePath, branch, worktreePath); err != nil {
		return "", err
	}
	return worktreePath, nil
}

func (a *repoCacheAdapter) RemoveWorktree(path string) error {
	return a.cache.RemoveWorktree(path)
}

func (a *repoCacheAdapter) CleanAll() error {
	return a.cache.CleanAll()
}

func (a *repoCacheAdapter) PurgeStale() error {
	return a.cache.PurgeStale()
}

// slackPosterAdapter wraps slackclient.Client to satisfy bot.SlackPoster interface.
// SlackPoster.PostMessage has no return value, but Client.PostMessage returns error.
type slackPosterAdapter struct {
	client *slackclient.Client
	logger *slog.Logger
}

func (a *slackPosterAdapter) PostMessage(channelID, text, threadTS string) {
	if err := a.client.PostMessage(channelID, text, threadTS); err != nil {
		a.logger.Warn("發送訊息失敗", "phase", "失敗", "channel_id", channelID, "error", err)
	}
}

func (a *slackPosterAdapter) UpdateMessage(channelID, messageTS, text string) {
	if err := a.client.UpdateMessage(channelID, messageTS, text); err != nil {
		a.logger.Warn("更新訊息失敗", "phase", "失敗", "channel_id", channelID, "error", err)
	}
}

func (a *slackPosterAdapter) PostMessageWithButton(channelID, text, threadTS, actionID, buttonText, value string) (string, error) {
	return a.client.PostMessageWithButton(channelID, text, threadTS, actionID, buttonText, value)
}

func parseLogLevel(level string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
