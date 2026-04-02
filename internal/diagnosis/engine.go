package diagnosis

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"slack-issue-bot/internal/llm"
)

type DiagnoseInput struct {
	Type     string
	Message  string
	RepoPath string
	Keywords []string
	Prompt   llm.PromptOptions
}

type Engine struct {
	llmProvider llm.Provider
	maxFiles    int
}

func NewEngine(provider llm.Provider, maxFiles int) *Engine {
	if maxFiles <= 0 {
		maxFiles = 10
	}
	return &Engine{
		llmProvider: provider,
		maxFiles:    maxFiles,
	}
}

func (e *Engine) Diagnose(ctx context.Context, input DiagnoseInput) (llm.DiagnoseResponse, error) {
	files, err := e.findRelevantFiles(input.RepoPath, input.Keywords)
	if err != nil {
		slog.Warn("failed to grep repo for relevant files", "error", err)
	}

	var repoFiles []llm.File

	if len(files) == 0 {
		// Keywords didn't match any files (common with non-English messages).
		// Provide repo directory structure as context instead.
		slog.Info("no keyword matches, providing repo tree as context")
		tree := e.repoTree(input.RepoPath)
		repoFiles = append(repoFiles, llm.File{
			Path:    "REPO_STRUCTURE.txt",
			Content: tree,
		})
	} else {
		for _, f := range files {
			fullPath := filepath.Join(input.RepoPath, f)
			content, err := os.ReadFile(fullPath)
			if err != nil {
				continue
			}
			lines := strings.Split(string(content), "\n")
			if len(lines) > 200 {
				lines = lines[:200]
			}
			repoFiles = append(repoFiles, llm.File{
				Path:    f,
				Content: strings.Join(lines, "\n"),
			})
		}
	}

	req := llm.DiagnoseRequest{
		Type:      input.Type,
		Message:   input.Message,
		RepoFiles: repoFiles,
		Prompt:    input.Prompt,
	}

	resp, err := e.llmProvider.Diagnose(ctx, req)
	if err != nil {
		return llm.DiagnoseResponse{}, fmt.Errorf("llm diagnose: %w", err)
	}
	return resp, nil
}

// FindFiles returns relevant file references without calling the LLM.
// Used in lite mode to produce a handoff spec.
func (e *Engine) FindFiles(input DiagnoseInput) []llm.FileRef {
	files, _ := e.findRelevantFiles(input.RepoPath, input.Keywords)
	var refs []llm.FileRef
	for _, f := range files {
		refs = append(refs, llm.FileRef{Path: f, Description: "matched keywords from Slack message"})
	}
	return refs
}

func (e *Engine) findRelevantFiles(repoPath string, keywords []string) ([]string, error) {
	seen := make(map[string]int)
	for _, kw := range keywords {
		cmd := exec.Command("git", "-C", repoPath, "grep", "-rl", "--no-color", kw)
		out, err := cmd.Output()
		if err != nil {
			continue
		}
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if line != "" && !shouldSkipFile(line) {
				seen[line]++
			}
		}
	}

	// Fallback: if git grep found nothing (e.g. not a git repo), walk the filesystem.
	if len(seen) == 0 {
		_ = filepath.Walk(repoPath, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			rel, err := filepath.Rel(repoPath, path)
			if err != nil || shouldSkipFile(rel) {
				return nil
			}
			content, err := os.ReadFile(path)
			if err != nil {
				return nil
			}
			lower := strings.ToLower(string(content))
			for _, kw := range keywords {
				if strings.Contains(lower, strings.ToLower(kw)) {
					seen[rel]++
				}
			}
			return nil
		})
	}

	type scored struct {
		path  string
		score int
	}
	var files []scored
	for p, s := range seen {
		files = append(files, scored{p, s})
	}
	for i := range files {
		for j := i + 1; j < len(files); j++ {
			if files[j].score > files[i].score {
				files[i], files[j] = files[j], files[i]
			}
		}
	}

	var result []string
	for i, f := range files {
		if i >= e.maxFiles {
			break
		}
		result = append(result, f.path)
	}
	return result, nil
}

// repoTree generates a directory listing of the repo (like `find . -type f`)
// to give the LLM structural context when keyword grep finds nothing.
func (e *Engine) repoTree(repoPath string) string {
	// Try `git ls-files` first (respects .gitignore)
	cmd := exec.Command("git", "-C", repoPath, "ls-files")
	out, err := cmd.Output()
	if err == nil && len(out) > 0 {
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		// Cap at 500 files to stay within LLM context
		if len(lines) > 500 {
			lines = append(lines[:500], fmt.Sprintf("... and %d more files", len(lines)-500))
		}
		return strings.Join(lines, "\n")
	}

	// Fallback: walk filesystem
	var lines []string
	filepath.Walk(repoPath, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(repoPath, path)
		if err != nil || shouldSkipFile(rel) {
			return nil
		}
		lines = append(lines, rel)
		if len(lines) >= 500 {
			return filepath.SkipAll
		}
		return nil
	})
	return strings.Join(lines, "\n")
}

func shouldSkipFile(path string) bool {
	skip := []string{".min.js", ".min.css", "vendor/", "node_modules/", ".lock", "go.sum", "package-lock"}
	for _, s := range skip {
		if strings.Contains(path, s) {
			return true
		}
	}
	return false
}
