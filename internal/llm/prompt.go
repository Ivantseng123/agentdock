package llm

import (
	"fmt"
	"strings"
)

// PromptOptions holds user-configurable prompt settings from config.
type PromptOptions struct {
	Language   string
	ExtraRules []string
}

func SystemPrompt(diagType string, opts PromptOptions) string {
	var base string
	if diagType == "bug" {
		base = `You are a senior software engineer diagnosing bugs. Given a bug report and relevant source code files, analyze the potential root cause.

Respond in JSON format:
{
  "summary": "Brief description of the likely root cause",
  "files": [{"path": "file/path.go", "line_number": 42, "description": "Why this location is relevant"}],
  "suggestions": ["Suggested fix 1", "Suggested fix 2"]
}

Be concise. Focus on the most likely cause. List ALL related file paths with full paths.`
	} else {
		base = `You are a senior software engineer analyzing feature requests. Given a feature request and the current codebase, identify where and how to implement it.

Respond in JSON format:
{
  "summary": "What existing functionality is related",
  "files": [{"path": "file/path.go", "line_number": 42, "description": "Why this is a good place to add the feature"}],
  "suggestions": ["Implementation approach 1", "Implementation approach 2"],
  "complexity": "low|medium|high"
}

Be concise. Focus on actionable locations. List ALL related file paths with full paths.`
	}

	// Append language instruction
	if opts.Language != "" {
		base += fmt.Sprintf("\n\nIMPORTANT: You MUST respond entirely in %s. All text in the JSON values must be in %s.", opts.Language, opts.Language)
	}

	// Append user-defined extra rules
	for _, rule := range opts.ExtraRules {
		base += "\n" + rule
	}

	return base
}

func BuildPrompt(diagType, message string, repoFiles []File) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("## %s Report\n\n", capitalize(diagType)))
	sb.WriteString(fmt.Sprintf("**Message:** %s\n\n", message))

	if len(repoFiles) > 0 {
		hasTree := len(repoFiles) == 1 && repoFiles[0].Path == "REPO_STRUCTURE.txt"
		if hasTree {
			sb.WriteString("## Repository File Structure\n\n")
			sb.WriteString("No specific files matched the keywords. Below is the full file listing of the repository. Use this to identify which files are most likely related to the request.\n\n")
			sb.WriteString(fmt.Sprintf("```\n%s\n```\n\n", repoFiles[0].Content))
		} else {
			sb.WriteString("## Relevant Source Files\n\n")
			for _, f := range repoFiles {
				sb.WriteString(fmt.Sprintf("### %s\n```\n%s\n```\n\n", f.Path, f.Content))
			}
		}
	}
	return sb.String()
}

func capitalize(s string) string {
	if len(s) == 0 {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
