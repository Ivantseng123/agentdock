package bot

import (
	"fmt"
	"strings"

	"slack-issue-bot/internal/config"
)

type ThreadMessage struct {
	User      string
	Timestamp string
	Text      string
}

type AttachmentInfo struct {
	Path string
	Name string
	Type string // "image", "text", "document"
}

type PromptInput struct {
	ThreadMessages   []ThreadMessage
	Attachments      []AttachmentInfo
	ExtraDescription string
	RepoPath         string
	Branch           string
	GitHubRepo       string   // "owner/repo"
	Channel          string   // Slack channel name
	Reporter         string   // display name
	Labels           []string // GitHub issue labels
	Prompt           config.PromptConfig
}

func BuildPrompt(input PromptInput) string {
	var sb strings.Builder

	sb.WriteString("Use the /triage-issue skill to investigate and create a GitHub issue.\n\n")

	// Thread context
	sb.WriteString("## Thread Context\n\n")
	for _, msg := range input.ThreadMessages {
		sb.WriteString(fmt.Sprintf("%s (%s):\n> %s\n\n", msg.User, msg.Timestamp, msg.Text))
	}

	// Extra description
	if input.ExtraDescription != "" {
		sb.WriteString("## Extra Description\n\n")
		sb.WriteString(fmt.Sprintf("> %s\n\n", input.ExtraDescription))
	}

	// Repository
	sb.WriteString("## Repository\n\n")
	sb.WriteString(fmt.Sprintf("Path: %s\n", input.RepoPath))
	if input.Branch != "" {
		sb.WriteString(fmt.Sprintf("Branch: %s\n", input.Branch))
	}
	sb.WriteString("\n")

	// Issue metadata for gh issue create
	sb.WriteString("## Issue Metadata\n\n")
	sb.WriteString(fmt.Sprintf("github_repo: %s\n", input.GitHubRepo))
	sb.WriteString(fmt.Sprintf("channel: %s\n", input.Channel))
	sb.WriteString(fmt.Sprintf("reporter: %s\n", input.Reporter))
	if input.Branch != "" {
		sb.WriteString(fmt.Sprintf("branch: %s\n", input.Branch))
	}
	if len(input.Labels) > 0 {
		sb.WriteString(fmt.Sprintf("labels: %s\n", strings.Join(input.Labels, ", ")))
	}
	sb.WriteString("\n")

	// Attachments
	if len(input.Attachments) > 0 {
		sb.WriteString("## Attachments\n\n")
		for _, att := range input.Attachments {
			hint := ""
			switch att.Type {
			case "image":
				hint = " (image — use your file reading tools to view)"
			case "text":
				hint = " (text — read directly)"
			case "document":
				hint = " (document)"
			}
			sb.WriteString(fmt.Sprintf("- %s%s\n", att.Path, hint))
		}
		sb.WriteString("\n")
	}

	// Language + extra rules
	if input.Prompt.Language != "" {
		sb.WriteString(fmt.Sprintf("Response language: %s\n", input.Prompt.Language))
	}
	if len(input.Prompt.ExtraRules) > 0 {
		sb.WriteString("\nAdditional rules:\n")
		for _, rule := range input.Prompt.ExtraRules {
			sb.WriteString(fmt.Sprintf("- %s\n", rule))
		}
	}

	return sb.String()
}
