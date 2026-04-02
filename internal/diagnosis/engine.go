package diagnosis

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"time"

	"slack-issue-bot/internal/llm"
)

// DiagnoseInput holds the parameters for a diagnosis request.
type DiagnoseInput struct {
	Type     string
	Message  string
	RepoPath string
	Keywords []string
	Prompt   llm.PromptOptions
}

// EngineConfig configures the diagnosis engine.
type EngineConfig struct {
	MaxFiles  int
	MaxTurns  int
	MaxTokens int
	CacheTTL  time.Duration
}

// Engine runs agent-loop diagnosis against a repository.
type Engine struct {
	chain     llm.ConversationProvider
	tools     []Tool
	cache     *Cache
	maxFiles  int
	maxTurns  int
	maxTokens int
}

// NewEngine creates a diagnosis engine with the given conversation provider
// and configuration.
func NewEngine(chain llm.ConversationProvider, cfg EngineConfig) *Engine {
	if cfg.MaxFiles <= 0 {
		cfg.MaxFiles = 10
	}
	if cfg.MaxTurns <= 0 {
		cfg.MaxTurns = 5
	}
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = 100000
	}
	if cfg.CacheTTL <= 0 {
		cfg.CacheTTL = 30 * time.Minute
	}

	return &Engine{
		chain:     chain,
		tools:     AllTools(),
		cache:     NewCache(cfg.CacheTTL),
		maxFiles:  cfg.MaxFiles,
		maxTurns:  cfg.MaxTurns,
		maxTokens: cfg.MaxTokens,
	}
}

// Diagnose runs the agent-loop diagnosis. Results are cached by message + prompt.
func (e *Engine) Diagnose(ctx context.Context, input DiagnoseInput) (llm.DiagnoseResponse, error) {
	cacheKey := e.cache.Key(input.RepoPath, "", input.Message,
		input.Prompt.Language, input.Prompt.ExtraRules)

	if cached, ok := e.cache.Get(cacheKey); ok {
		slog.Info("diagnosis cache hit", "repo", input.RepoPath)
		return cached, nil
	}

	resp, err := RunLoop(ctx, e.chain, e.tools, LoopInput{
		Type:      input.Type,
		Message:   input.Message,
		RepoPath:  input.RepoPath,
		Keywords:  input.Keywords,
		Prompt:    input.Prompt,
		MaxTurns:  e.maxTurns,
		MaxTokens: e.maxTokens,
	})
	if err != nil {
		return llm.DiagnoseResponse{}, fmt.Errorf("diagnosis: %w", err)
	}

	e.cache.Set(cacheKey, resp)
	return resp, nil
}

// FindFiles returns relevant file references without calling the LLM.
// Used in lite mode to produce a handoff spec (grep-only).
func (e *Engine) FindFiles(input DiagnoseInput) []llm.FileRef {
	files, _ := grepFiles(input.RepoPath, input.Keywords, e.maxFiles)
	var refs []llm.FileRef
	for _, f := range files {
		refs = append(refs, llm.FileRef{Path: f, Description: "matched keywords from Slack message"})
	}
	return refs
}

// Stop terminates the cache cleanup goroutine.
func (e *Engine) Stop() {
	if e.cache != nil {
		e.cache.Stop()
	}
}

// --- Helpers ---

// grepFiles searches the repo for files matching any of the given terms.
func grepFiles(repoPath string, terms []string, maxFiles int) ([]string, error) {
	if maxFiles <= 0 {
		maxFiles = 10
	}

	seen := make(map[string]int)
	for _, term := range terms {
		cmd := exec.Command("git", "-C", repoPath, "grep", "-rli", "--no-color", term)
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
		if i >= maxFiles {
			break
		}
		result = append(result, f.path)
	}
	return result, nil
}

// parseStringArray extracts a JSON string array from LLM text output.
func parseStringArray(text string) ([]string, error) {
	text = strings.TrimSpace(text)
	var arr []string

	// Direct parse.
	if err := json.Unmarshal([]byte(text), &arr); err == nil && len(arr) > 0 {
		return arr, nil
	}

	// Extract from code block.
	if idx := strings.Index(text, "```"); idx != -1 {
		start := idx + 3
		if nl := strings.Index(text[start:], "\n"); nl != -1 {
			start += nl + 1
		}
		if end := strings.Index(text[start:], "```"); end != -1 {
			if err := json.Unmarshal([]byte(strings.TrimSpace(text[start:start+end])), &arr); err == nil && len(arr) > 0 {
				return arr, nil
			}
		}
	}

	// Find array anywhere.
	if idx := strings.Index(text, "["); idx != -1 {
		if end := strings.LastIndex(text, "]"); end > idx {
			if err := json.Unmarshal([]byte(text[idx:end+1]), &arr); err == nil && len(arr) > 0 {
				return arr, nil
			}
		}
	}

	return nil, fmt.Errorf("could not parse string array from: %s", truncate(text, 100))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func shouldSkipFile(path string) bool {
	skip := []string{".min.js", ".min.css", "vendor/", "node_modules/", ".lock", "go.sum", "package-lock", ".class", ".jar", "target/", "build/", ".git/"}
	for _, s := range skip {
		if strings.Contains(path, s) {
			return true
		}
	}
	return false
}
