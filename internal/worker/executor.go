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
	RemoveWorktree(worktreePath string) error
	CleanAll() error
	PurgeStale() error
}

type executionDeps struct {
	attachments queue.AttachmentStore
	repoCache   RepoProvider
	runner      Runner
	store       queue.JobStore
	skillDirs   []string
}

func executeJob(ctx context.Context, job *queue.Job, deps executionDeps, opts bot.RunOptions, logger *slog.Logger) *queue.JobResult {
	startedAt := time.Now()
	logger = logger.With("job_id", job.ID, "repo", job.Repo)

	// Resolve attachments (blocks until Prepare completes on app side).
	var attachments []queue.AttachmentReady
	if len(job.Attachments) > 0 {
		logger.Info("解析附件中", "phase", "處理中", "count", len(job.Attachments))
		var err error
		attachments, err = deps.attachments.Resolve(ctx, job.ID)
		if err != nil {
			return failedResult(job, startedAt, fmt.Errorf("attachments failed: %w", err), "")
		}
	}

	// Clone/fetch repo.
	logger.Info("準備 repo 中", "phase", "處理中", "branch", job.Branch)
	prepareStart := time.Now()
	repoPath, err := deps.repoCache.Prepare(job.CloneURL, job.Branch)
	if err != nil {
		return failedResult(job, startedAt, fmt.Errorf("repo prepare failed: %w", err), "")
	}
	prepareSeconds := time.Since(prepareStart).Seconds()
	logger.Info("Repo 已就緒", "phase", "處理中", "path", repoPath, "prepare_seconds", prepareSeconds)

	// Write attachments into worktree — cleaned up together with RemoveWorktree.
	prompt := job.Prompt
	if len(attachments) > 0 {
		attachDir := filepath.Join(repoPath, ".attachments")
		attachInfos, err := writeAttachments(attachments, attachDir)
		if err != nil {
			logger.Warn("附件寫入失敗，繼續執行", "phase", "處理中", "error", err)
		} else {
			prompt = bot.AppendAttachmentSection(prompt, attachInfos)
			logger.Info("附件已寫入", "phase", "處理中", "count", len(attachInfos), "dir", attachDir)
		}
	}

	// Mount skills to all agent skill directories.
	if len(job.Skills) > 0 {
		logger.Info("掛載 skill 中", "phase", "處理中", "count", len(job.Skills), "skill_dirs", deps.skillDirs)
		for _, sd := range deps.skillDirs {
			if err := mountSkills(repoPath, job.Skills, sd); err != nil {
				return failedResult(job, startedAt, fmt.Errorf("skill mount failed: %w", err), repoPath)
			}
			defer cleanupSkills(repoPath, job.Skills, sd)
		}
	} else {
		logger.Warn("工作中無 skill payload", "phase", "處理中")
	}

	// Execute agent.
	deps.store.UpdateStatus(job.ID, queue.JobRunning)
	logger.Info("執行 agent 中", "phase", "處理中")
	output, err := deps.runner.Run(ctx, repoPath, prompt, opts)
	if err != nil {
		return failedResult(job, startedAt, err, repoPath)
	}
	logger.Info("Agent 執行完成", "phase", "完成", "output_len", len(output))

	// Parse agent output.
	parsed, err := bot.ParseAgentOutput(output)
	if err != nil {
		truncated := output
		if len(truncated) > 2000 {
			truncated = truncated[:2000] + "…(truncated)"
		}
		logger.Warn("解析失敗，輸出原始內容", "phase", "失敗", "output", truncated)
		return failedResult(job, startedAt, fmt.Errorf("parse failed: %w", err), repoPath)
	}
	logger.Info("解析成功", "phase", "完成", "status", parsed.Status, "confidence", parsed.Confidence, "files_found", parsed.FilesFound)

	return &queue.JobResult{
		JobID:          job.ID,
		Status:         "completed",
		Title:          parsed.Title,
		Body:           parsed.Body,
		Labels:         parsed.Labels,
		Confidence:     parsed.Confidence,
		FilesFound:     parsed.FilesFound,
		Questions:      parsed.Questions,
		RawOutput:      output,
		RepoPath:       repoPath,
		StartedAt:      startedAt,
		FinishedAt:     time.Now(),
		PrepareSeconds: prepareSeconds,
	}
}

func writeAttachments(attachments []queue.AttachmentReady, dir string) ([]bot.AttachmentInfo, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create attachment dir: %w", err)
	}

	seen := make(map[string]int)
	var infos []bot.AttachmentInfo

	for _, att := range attachments {
		filename := att.Filename
		if count, exists := seen[filename]; exists {
			ext := filepath.Ext(filename)
			base := strings.TrimSuffix(filename, ext)
			filename = fmt.Sprintf("%s_%d%s", base, count+1, ext)
		}
		seen[att.Filename]++

		path := filepath.Join(dir, filename)
		if err := os.WriteFile(path, att.Data, 0644); err != nil {
			return nil, fmt.Errorf("write attachment %s: %w", filename, err)
		}
		infos = append(infos, bot.AttachmentInfo{
			Path: path,
			Name: filename,
			Type: att.MimeType,
		})
	}
	return infos, nil
}

func failedResult(job *queue.Job, startedAt time.Time, err error, repoPath string) *queue.JobResult {
	return &queue.JobResult{
		JobID:      job.ID,
		Status:     "failed",
		Error:      err.Error(),
		RepoPath:   repoPath,
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
