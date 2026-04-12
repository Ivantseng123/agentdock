package worker

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"slack-issue-bot/internal/bot"
	"slack-issue-bot/internal/queue"
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

	// Resolve attachments (blocks until Prepare completes on app side).
	var attachments []queue.AttachmentReady
	if len(job.Attachments) > 0 {
		var err error
		attachments, err = deps.attachments.Resolve(ctx, job.ID)
		if err != nil {
			return failedResult(job, startedAt, fmt.Errorf("attachments failed: %w", err))
		}
	}

	// Clone/fetch repo.
	repoPath, err := deps.repoCache.Prepare(job.CloneURL, job.Branch)
	if err != nil {
		return failedResult(job, startedAt, fmt.Errorf("repo prepare failed: %w", err))
	}

	// Copy attachments to repo workspace.
	for _, att := range attachments {
		if att.URL != "" {
			_ = att // For local file:// URLs, path is already accessible.
		}
	}

	// Mount skills to all agent skill directories.
	if len(job.Skills) > 0 {
		for _, sd := range deps.skillDirs {
			if err := mountSkills(repoPath, job.Skills, sd); err != nil {
				return failedResult(job, startedAt, fmt.Errorf("skill mount failed: %w", err))
			}
			defer cleanupSkills(repoPath, job.Skills, sd)
		}
	}

	// Execute agent.
	deps.store.UpdateStatus(job.ID, queue.JobRunning)
	output, err := deps.runner.Run(ctx, repoPath, job.Prompt, opts)
	if err != nil {
		return failedResult(job, startedAt, err)
	}

	// Parse agent output.
	parsed, err := bot.ParseAgentOutput(output)
	if err != nil {
		return failedResult(job, startedAt, fmt.Errorf("parse failed: %w", err))
	}

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

func mountSkills(repoPath string, skills map[string]string, skillDir string) error {
	if skillDir == "" {
		return nil
	}
	for name, content := range skills {
		skillSubDir := filepath.Join(repoPath, skillDir, name)
		if err := os.MkdirAll(skillSubDir, 0755); err != nil {
			return err
		}
		path := filepath.Join(skillSubDir, "SKILL.md")
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			return err
		}
	}
	return nil
}

func cleanupSkills(repoPath string, skills map[string]string, skillDir string) {
	if skillDir == "" {
		return
	}
	dir := filepath.Join(repoPath, skillDir)
	for name := range skills {
		os.RemoveAll(filepath.Join(dir, name))
	}
	os.Remove(dir) // only succeeds if empty (safe)
}
