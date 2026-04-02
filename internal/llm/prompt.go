package llm

import (
	"fmt"
	"strings"
)

func SystemPrompt(diagType string) string {
	if diagType == "bug" {
		return `You are a senior software engineer diagnosing bugs. Given a bug report and relevant source code files, analyze the potential root cause.

Respond in JSON format:
{
  "summary": "Brief description of the likely root cause",
  "files": [{"path": "file/path.go", "line_number": 42, "description": "Why this location is relevant"}],
  "suggestions": ["Suggested fix 1", "Suggested fix 2"]
}

Be concise. Focus on the most likely cause. Include 2-5 relevant files max.`
	}
	return `You are a senior software engineer analyzing feature requests. Given a feature request and the current codebase, identify where and how to implement it.

Respond in JSON format:
{
  "summary": "What existing functionality is related",
  "files": [{"path": "file/path.go", "line_number": 42, "description": "Why this is a good place to add the feature"}],
  "suggestions": ["Implementation approach 1", "Implementation approach 2"],
  "complexity": "low|medium|high"
}

Be concise. Focus on actionable locations. Include 2-5 relevant files max.`
}

func BuildPrompt(diagType, message string, repoFiles []File) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("## %s Report\n\n", capitalize(diagType)))
	sb.WriteString(fmt.Sprintf("**Message:** %s\n\n", message))

	if len(repoFiles) > 0 {
		sb.WriteString("## Relevant Source Files\n\n")
		for _, f := range repoFiles {
			sb.WriteString(fmt.Sprintf("### %s\n```\n%s\n```\n\n", f.Path, f.Content))
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
