package worker

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"agentdock/internal/bot"
	"agentdock/internal/queue"
)

// Runner abstracts agent execution (for testing).
type Runner interface {
	Run(ctx context.Context, workDir, prompt string, opts bot.RunOptions) (string, error)
}

// RepoProvider abstracts repo clone/checkout (for testing).
type RepoProvider interface {
	Prepare(cloneURL, branch string) (string, error)
}

type executionDeps struct {
	attachments queue.AttachmentStore
	repoCache   RepoProvider
	runner      Runner
	store       queue.JobStore
	skillDirs   []string
}

func executeJob(ctx context.Context, job *queue.Job, deps executionDeps, opts bot.RunOptions) *queue.JobResult {
	startedAt := time.Now()
	logger := slog.With("job_id", job.ID, "repo", job.Repo)

	// Resolve attachments (blocks until Prepare completes on app side).
	var attachments []queue.AttachmentReady
	if len(job.Attachments) > 0 {
		logger.Info("resolving attachments", "count", len(job.Attachments))
		var err error
		attachments, err = deps.attachments.Resolve(ctx, job.ID)
		if err != nil {
			return failedResult(job, startedAt, fmt.Errorf("attachments failed: %w", err))
		}
	}

	// Clone/fetch repo.
	logger.Info("preparing repo", "branch", job.Branch)
	repoPath, err := deps.repoCache.Prepare(job.CloneURL, job.Branch)
	if err != nil {
		return failedResult(job, startedAt, fmt.Errorf("repo prepare failed: %w", err))
	}
	logger.Info("repo ready", "path", repoPath)

	// Copy attachments to repo workspace.
	for _, att := range attachments {
		if att.URL != "" {
			_ = att // For local file:// URLs, path is already accessible.
		}
	}

	// Mount skills to all agent skill directories.
	if len(job.Skills) > 0 {
		logger.Info("mounting skills", "count", len(job.Skills), "skill_dirs", deps.skillDirs)
		for _, sd := range deps.skillDirs {
			if err := mountSkills(repoPath, job.Skills, sd); err != nil {
				return failedResult(job, startedAt, fmt.Errorf("skill mount failed: %w", err))
			}
			defer cleanupSkills(repoPath, job.Skills, sd)
		}
	} else {
		logger.Warn("no skills in job payload")
	}

	// Execute agent.
	deps.store.UpdateStatus(job.ID, queue.JobRunning)
	logger.Info("executing agent")
	output, err := deps.runner.Run(ctx, repoPath, job.Prompt, opts)
	if err != nil {
		return failedResult(job, startedAt, err)
	}
	logger.Info("agent finished", "output_len", len(output))

	// Parse agent output.
	parsed, err := bot.ParseAgentOutput(output)
	if err != nil {
		truncated := output
		if len(truncated) > 2000 {
			truncated = truncated[:2000] + "…(truncated)"
		}
		logger.Warn("parse failed, dumping raw output", "output", truncated)
		return failedResult(job, startedAt, fmt.Errorf("parse failed: %w", err))
	}
	logger.Info("parse succeeded", "status", parsed.Status, "confidence", parsed.Confidence, "files_found", parsed.FilesFound)

	return &queue.JobResult{
		JobID:      job.ID,
		Status:     "completed",
		Title:      parsed.Title,
		Body:       parsed.Body,
		Labels:     parsed.Labels,
		Confidence: parsed.Confidence,
		FilesFound: parsed.FilesFound,
		Questions:  parsed.Questions,
		RawOutput:  output,
		StartedAt:  startedAt,
		FinishedAt: time.Now(),
	}
}

func failedResult(job *queue.Job, startedAt time.Time, err error) *queue.JobResult {
	return &queue.JobResult{
		JobID:      job.ID,
		Status:     "failed",
		Error:      err.Error(),
		StartedAt:  startedAt,
		FinishedAt: time.Now(),
	}
}

func mountSkills(repoPath string, skills map[string]*queue.SkillPayload, skillDir string) error {
	if skillDir == "" {
		return nil
	}
	for name, payload := range skills {
		for relPath, content := range payload.Files {
			if strings.Contains(relPath, "..") || filepath.IsAbs(relPath) {
				return fmt.Errorf("invalid skill file path: %s", relPath)
			}
			fullPath := filepath.Join(repoPath, skillDir, name, relPath)
			if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
				return err
			}
			if err := os.WriteFile(fullPath, content, 0644); err != nil {
				return err
			}
		}
	}
	return nil
}

func cleanupSkills(repoPath string, skills map[string]*queue.SkillPayload, skillDir string) {
	if skillDir == "" {
		return
	}
	dir := filepath.Join(repoPath, skillDir)
	for name := range skills {
		os.RemoveAll(filepath.Join(dir, name))
	}
	os.Remove(dir) // only succeeds if empty (safe)
}
