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

// AgentSystemPrompt builds the system prompt for the agent-loop diagnosis.
func AgentSystemPrompt(diagType string, opts PromptOptions) string {
	var sb strings.Builder

	sb.WriteString(`You are a code triage assistant investigating a REMOTE repository via tools.

IMPORTANT:
- You have NO direct access to the repository files. You can ONLY see code through the tools provided.
- You MUST call at least one tool (grep, search_code, or list_files) before producing the triage card.
- Do NOT produce a triage card on your first response. Investigate first.
- Ignore any local project context — you are analyzing the repository specified in the user message.

Search strategy:
- If pre-search results are provided, start by reading the most relevant files from those results.
- Use grep/search_code for additional investigation — try BOTH original language terms AND English identifiers.
- Use read_context to understand the repo structure if initial searches miss.

Output rules:
- List each file path ONLY ONCE. Do not repeat the same file with different line numbers.
- Read at most 5 files total — pick the most relevant ones.
- Direction should give high-level guidance, not specific code to write.
- If you find an existing implementation that can serve as a reference, mention it as "可參考 XXX 的做法" — do not dictate exact code.
- Do NOT guess variable names, field names, UI labels, or component positions.
- Set confidence to "low" if you cannot find clearly related code, "medium" if partially related, "high" if strongly related.
- open_questions: ONLY things the reporter/PM can answer (e.g. "which module?", "what screen?"). Do NOT include code-visibility issues like "file was truncated".

`)

	sb.WriteString(agentOutputSchema(diagType))

	if opts.Language != "" {
		sb.WriteString(fmt.Sprintf("\n\nRespond in %s. All JSON string values in %s.", opts.Language, opts.Language))
	}
	for _, rule := range opts.ExtraRules {
		sb.WriteString("\n" + rule)
	}
	return sb.String()
}

func agentOutputSchema(diagType string) string {
	if diagType == "bug" {
		return `When you have gathered enough information, respond with ONLY this JSON (no markdown fences):
{
  "summary": "One sentence: what area of code is likely involved",
  "files": [{"path": "exact/path", "line_number": 0, "description": "Why relevant (one sentence)"}],
  "suggestions": ["What to investigate (max 2 items)"],
  "open_questions": ["Anything you cannot determine from code alone"],
  "confidence": "low|medium|high"
}
line_number: use 0 if unsure. files: max 5.`
	}
	return `When you have gathered enough information, respond with ONLY this JSON (no markdown fences):
{
  "summary": "One sentence: what existing code relates to this request",
  "files": [{"path": "exact/path", "line_number": 0, "description": "Why relevant (one sentence)"}],
  "suggestions": ["Where to start (max 2 items)"],
  "complexity": "low|medium|high",
  "open_questions": ["Anything you cannot determine from code alone"],
  "confidence": "low|medium|high"
}
line_number: use 0 if unsure. files: max 5.`
}

// CLIToolPromptSuffix returns additional prompt text for CLI/Ollama providers
// that do not support native tool calling. It describes how to invoke tools
// using a JSON-in-text format.
func CLIToolPromptSuffix(tools []ToolDef) string {
	if len(tools) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("\n\n## CRITICAL: Response Format\n\n")
	sb.WriteString("You MUST respond with ONLY a single JSON object. No other text, no markdown, no explanation.\n\n")
	sb.WriteString("To call a tool:\n")
	sb.WriteString(`{"tool": "grep", "args": {"pattern": "example"}}` + "\n\n")
	sb.WriteString("To finish (after investigating):\n")
	sb.WriteString(`{"tool": "finish", "result": {"summary": "...", "files": [...], "suggestions": [...], "open_questions": [...], "confidence": "high"}}` + "\n\n")
	sb.WriteString("WRONG (do NOT do this):\n")
	sb.WriteString("- \"Let me search for...\" followed by JSON\n")
	sb.WriteString("- Markdown tables or bullet lists\n")
	sb.WriteString("- Any text that is not a JSON object\n\n")
	sb.WriteString("Available tools:\n")

	for _, t := range tools {
		sb.WriteString(fmt.Sprintf("- %s: %s\n", t.Name, t.Description))
	}
	return sb.String()
}
