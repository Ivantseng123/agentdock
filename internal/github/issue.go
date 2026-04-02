package github

import (
	"context"
	"fmt"
	"strings"

	gh "github.com/google/go-github/v60/github"
	"slack-issue-bot/internal/llm"
)

type IssueInput struct {
	Type        string
	TitlePrefix string
	Channel     string
	Reporter    string
	Message     string
	Labels      []string
	Diagnosis   llm.DiagnoseResponse
	RepoOwner   string // for GitHub file links
	RepoName    string
	Branch      string
}

type IssueClient struct {
	client *gh.Client
}

func NewIssueClient(token string) *IssueClient {
	return &IssueClient{
		client: gh.NewClient(nil).WithAuthToken(token),
	}
}

func (ic *IssueClient) CreateIssue(ctx context.Context, owner, repo string, input IssueInput) (string, error) {
	title := buildTitle(input)
	body := FormatIssueBody(input)

	var labels []string
	labels = append(labels, input.Labels...)

	issue, _, err := ic.client.Issues.Create(ctx, owner, repo, &gh.IssueRequest{
		Title:  gh.String(title),
		Body:   gh.String(body),
		Labels: &labels,
	})
	if err != nil {
		return "", fmt.Errorf("create issue: %w", err)
	}

	return issue.GetHTMLURL(), nil
}

func buildTitle(input IssueInput) string {
	title := input.Message
	if idx := strings.IndexAny(title, "\n\r"); idx != -1 {
		title = title[:idx]
	}
	if len(title) > 80 {
		title = title[:77] + "..."
	}
	if input.TitlePrefix != "" {
		title = input.TitlePrefix + " " + title
	}
	return title
}

func FormatIssueBody(input IssueInput) string {
	var sb strings.Builder

	// Source — reporter as plain text (no @ tag)
	sb.WriteString(fmt.Sprintf("**Channel:** %s | **Reporter:** %s\n\n", input.Channel, input.Reporter))
	sb.WriteString(fmt.Sprintf("> %s\n\n", input.Message))

	hasDiagnosis := input.Diagnosis.Summary != ""

	if !hasDiagnosis {
		// Lite mode
		if len(input.Diagnosis.Files) > 0 {
			sb.WriteString("### Related Files\n\n")
			for _, f := range input.Diagnosis.Files {
				writeFileRef(&sb, f, input.RepoOwner, input.RepoName, input.Branch)
			}
			sb.WriteString("\n")
		}
		sb.WriteString("---\n\n")
		sb.WriteString("_No AI diagnosis was run. Use these file paths with your own AI to investigate._\n")
		return sb.String()
	}

	// Full mode — triage card
	sb.WriteString("### AI Triage\n\n")
	sb.WriteString(input.Diagnosis.Summary + "\n\n")

	if len(input.Diagnosis.Files) > 0 {
		sb.WriteString("### Related Files\n\n")
		for _, f := range input.Diagnosis.Files {
			writeFileRef(&sb, f, input.RepoOwner, input.RepoName, input.Branch)
		}
		sb.WriteString("\n")
	}

	if len(input.Diagnosis.Suggestions) > 0 {
		sb.WriteString("### Direction\n\n")
		for _, s := range input.Diagnosis.Suggestions {
			sb.WriteString(fmt.Sprintf("- %s\n", s))
		}
		sb.WriteString("\n")
	}

	if len(input.Diagnosis.OpenQuestions) > 0 {
		sb.WriteString("### Needs Clarification\n\n")
		for _, q := range input.Diagnosis.OpenQuestions {
			sb.WriteString(fmt.Sprintf("- %s\n", q))
		}
	}

	return sb.String()
}

// writeFileRef writes a file reference as a GitHub permalink if repo info is available.
// Shows just the filename as link text for readability.
func writeFileRef(sb *strings.Builder, f llm.FileRef, owner, repo, branch string) {
	path := f.Path
	// Skip context files (README, CLAUDE.md etc. prefixed with [context])
	if strings.HasPrefix(path, "[context]") {
		return
	}

	// Extract just the filename for display
	fileName := path
	if idx := strings.LastIndex(path, "/"); idx != -1 {
		fileName = path[idx+1:]
	}

	if owner != "" && repo != "" {
		ref := branch
		if ref == "" {
			ref = "main"
		}
		url := fmt.Sprintf("https://github.com/%s/%s/blob/%s/%s", owner, repo, ref, path)
		if f.LineNumber > 0 {
			url += fmt.Sprintf("#L%d", f.LineNumber)
		}
		sb.WriteString(fmt.Sprintf("- [`%s`](%s)", fileName, url))
	} else {
		if f.LineNumber > 0 {
			sb.WriteString(fmt.Sprintf("- `%s:%d`", fileName, f.LineNumber))
		} else {
			sb.WriteString(fmt.Sprintf("- `%s`", fileName))
		}
	}
	if f.Description != "" {
		sb.WriteString(fmt.Sprintf(" — %s", f.Description))
	}
	sb.WriteString("\n")
}
