package pool

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	// ParseAgentOutput still in internal/bot — will move to worker-visible
	// location when Phase 3 restructures the bot package.
	"github.com/Ivantseng123/agentdock/internal/bot"
	"github.com/Ivantseng123/agentdock/shared/crypto"
	"github.com/Ivantseng123/agentdock/shared/queue"
	"github.com/Ivantseng123/agentdock/worker/agent"
	"github.com/Ivantseng123/agentdock/worker/prompt"
)

// Runner abstracts agent execution (for testing).
type Runner interface {
	Run(ctx context.Context, workDir, prompt string, opts agent.RunOptions) (string, error)
}

// RepoProvider abstracts repo clone/checkout (for testing).
type RepoProvider interface {
	Prepare(cloneURL, branch, token string) (string, error)
	RemoveWorktree(worktreePath string) error
	CleanAll() error
	PurgeStale() error
}

type executionDeps struct {
	attachments   queue.AttachmentStore
	repoCache     RepoProvider
	runner        Runner
	store         queue.JobStore
	skillDirs     []string
	secretKey     []byte
	workerSecrets map[string]string
	extraRules    []string
}

func executeJob(ctx context.Context, job *queue.Job, deps executionDeps, opts agent.RunOptions, logger *slog.Logger) *queue.JobResult {
	startedAt := time.Now()
	logger = logger.With("job_id", job.ID, "repo", job.Repo)

	// Resolve attachments (blocks until Prepare completes on app side).
	var attachments []queue.AttachmentReady
	if len(job.Attachments) > 0 {
		logger.Info("解析附件中", "phase", "處理中", "count", len(job.Attachments))
		var err error
		attachments, err = deps.attachments.Resolve(ctx, job.ID)
		if err != nil {
			return classifyResult(job, startedAt, fmt.Errorf("attachments failed: %w", err), "", ctx, deps.store)
		}
	}

	// Decrypt and merge secrets.
	var mergedSecrets map[string]string
	if len(job.EncryptedSecrets) > 0 {
		if len(deps.secretKey) == 0 {
			return classifyResult(job, startedAt, fmt.Errorf("job has encrypted secrets but worker has no secret_key configured"), "", ctx, deps.store)
		}
		decrypted, err := crypto.Decrypt(deps.secretKey, job.EncryptedSecrets)
		if err != nil {
			return classifyResult(job, startedAt, fmt.Errorf("decrypt secrets: %w", err), "", ctx, deps.store)
		}
		var appSecrets map[string]string
		if err := json.Unmarshal(decrypted, &appSecrets); err != nil {
			return classifyResult(job, startedAt, fmt.Errorf("unmarshal secrets: %w", err), "", ctx, deps.store)
		}
		mergedSecrets = appSecrets
	}
	// Overlay worker secrets (worker wins)
	if len(deps.workerSecrets) > 0 {
		if mergedSecrets == nil {
			mergedSecrets = make(map[string]string)
		}
		for k, v := range deps.workerSecrets {
			mergedSecrets[k] = v
		}
	}

	// Clone/fetch repo.
	logger.Info("準備 repo 中", "phase", "處理中", "branch", job.Branch)
	prepareStart := time.Now()
	ghToken := ""
	if mergedSecrets != nil {
		ghToken = mergedSecrets["GH_TOKEN"]
	}
	if err := ctx.Err(); err != nil {
		return classifyResult(job, startedAt, err, "", ctx, deps.store)
	}
	repoPath, err := deps.repoCache.Prepare(job.CloneURL, job.Branch, ghToken)
	if err != nil {
		return classifyResult(job, startedAt, fmt.Errorf("repo prepare failed: %w", err), "", ctx, deps.store)
	}
	prepareSeconds := time.Since(prepareStart).Seconds()
	logger.Info("Repo 已就緒", "phase", "處理中", "path", repoPath, "prepare_seconds", prepareSeconds)

	// Defensive: new schema requires PromptContext. drain-and-cut means old
	// Job.Prompt-only jobs shouldn't exist, but fail clearly if one slips through.
	if job.PromptContext == nil {
		return failedResult(job, startedAt, fmt.Errorf("malformed job: missing prompt_context"), repoPath)
	}

	// Write attachments into worktree — cleaned up together with RemoveWorktree.
	var attachInfos []prompt.AttachmentInfo
	if len(attachments) > 0 {
		attachDir := filepath.Join(repoPath, ".attachments")
		var err error
		attachInfos, err = writeAttachments(attachments, attachDir)
		if err != nil {
			logger.Warn("附件寫入失敗，繼續執行", "phase", "處理中", "error", err)
		} else {
			logger.Info("附件已寫入", "phase", "處理中", "count", len(attachInfos), "dir", attachDir)
		}
	}

	// Build XML prompt from structured context + worker-owned extra rules.
	// Local variable named promptXML (not prompt) to avoid shadowing the
	// imported prompt package — keeps prompt.X callable later in the scope.
	promptXML := prompt.BuildPrompt(*job.PromptContext, deps.extraRules, attachInfos)
	logger.Info("Prompt 已組裝", "phase", "處理中", "length", len(promptXML))
	logger.Debug("Prompt XML 內容", "phase", "處理中", "prompt", promptXML)

	// Mount skills to all agent skill directories.
	if len(job.Skills) > 0 {
		logger.Info("掛載 skill 中", "phase", "處理中", "count", len(job.Skills), "skill_dirs", deps.skillDirs)
		for _, sd := range deps.skillDirs {
			if err := mountSkills(repoPath, job.Skills, sd); err != nil {
				return classifyResult(job, startedAt, fmt.Errorf("skill mount failed: %w", err), repoPath, ctx, deps.store)
			}
			defer cleanupSkills(repoPath, job.Skills, sd)
		}
	} else {
		logger.Warn("工作中無 skill payload", "phase", "處理中")
	}

	// Execute agent.
	deps.store.UpdateStatus(job.ID, queue.JobRunning)
	logger.Info("執行 agent 中", "phase", "處理中")
	opts.Secrets = mergedSecrets
	output, err := deps.runner.Run(ctx, repoPath, promptXML, opts)
	if err != nil {
		return classifyResult(job, startedAt, err, repoPath, ctx, deps.store)
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
		return classifyResult(job, startedAt, fmt.Errorf("parse failed: %w", err), repoPath, ctx, deps.store)
	}
	logger.Info("解析成功", "phase", "完成", "status", parsed.Status, "confidence", parsed.Confidence, "files_found", parsed.FilesFound)

	// Agent said "not our bug" — short-circuit into the existing "low
	// confidence skipped" lane in result_listener. Dropping parsed.Status
	// here is what caused empty-title 422s: the worker turned every parse
	// success into Status="completed" with empty Title, and the listener
	// then tried to create an issue with that.
	if parsed.Status == "REJECTED" {
		return &queue.JobResult{
			JobID:          job.ID,
			Status:         "completed",
			Confidence:     "low",
			Message:        parsed.Message,
			RawOutput:      output,
			RepoPath:       repoPath,
			StartedAt:      startedAt,
			FinishedAt:     time.Now(),
			PrepareSeconds: prepareSeconds,
		}
	}

	// Agent self-reported ERROR — route to failure so the user gets a retry
	// button and a clear reason instead of a silent 422.
	if parsed.Status == "ERROR" {
		msg := parsed.Message
		if msg == "" {
			msg = "agent reported ERROR without message"
		}
		return failedResult(job, startedAt, fmt.Errorf("agent error: %s", msg), repoPath)
	}

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

func writeAttachments(attachments []queue.AttachmentReady, dir string) ([]prompt.AttachmentInfo, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create attachment dir: %w", err)
	}

	seen := make(map[string]int)
	var infos []prompt.AttachmentInfo

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
		infos = append(infos, prompt.AttachmentInfo{
			Path: path,
			Name: filename,
			Type: att.MimeType,
		})
	}
	return infos, nil
}

func classifyResult(job *queue.Job, startedAt time.Time, err error, repoPath string, ctx context.Context, store queue.JobStore) *queue.JobResult {
	if ctx.Err() == context.Canceled {
		if state, lookupErr := store.Get(job.ID); lookupErr == nil && state.Status == queue.JobCancelled {
			return cancelledResult(job, startedAt, repoPath)
		}
	}
	return failedResult(job, startedAt, err, repoPath)
}

func cancelledResult(job *queue.Job, startedAt time.Time, repoPath string) *queue.JobResult {
	return &queue.JobResult{
		JobID:      job.ID,
		Status:     "cancelled",
		RepoPath:   repoPath,
		StartedAt:  startedAt,
		FinishedAt: time.Now(),
	}
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
