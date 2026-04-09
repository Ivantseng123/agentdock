package bot

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

const (
	metadataSeparator = "===TRIAGE_METADATA==="
	maxBodyLength     = 65000
	minOutputLength   = 50
	maxTitleLength    = 80
)

type AgentMetadata struct {
	IssueType      string    `json:"issue_type"`
	Confidence     string    `json:"confidence"`
	Files          []FileRef `json:"files"`
	OpenQuestions  []string  `json:"open_questions"`
	SuggestedTitle string    `json:"suggested_title"`
}

type FileRef struct {
	Path      string `json:"path"`
	Line      int    `json:"line"`
	Relevance string `json:"relevance"`
}

type ParsedOutput struct {
	MarkdownBody string
	Metadata     AgentMetadata
	Degraded     bool
}

func ParseAgentOutput(output string) (ParsedOutput, error) {
	output = strings.TrimSpace(output)
	if len(output) < minOutputLength {
		return ParsedOutput{}, fmt.Errorf("agent output too short (%d chars, minimum %d)", len(output), minOutputLength)
	}

	idx := strings.LastIndex(output, metadataSeparator)
	if idx == -1 {
		return ParsedOutput{
			MarkdownBody: output,
			Metadata:     defaultMetadata(),
			Degraded:     true,
		}, nil
	}

	body := strings.TrimSpace(output[:idx])
	jsonPart := strings.TrimSpace(output[idx+len(metadataSeparator):])

	var meta AgentMetadata
	if err := json.Unmarshal([]byte(jsonPart), &meta); err != nil {
		return ParsedOutput{
			MarkdownBody: body,
			Metadata:     defaultMetadata(),
			Degraded:     true,
		}, nil
	}

	return ParsedOutput{
		MarkdownBody: body,
		Metadata:     meta,
		Degraded:     false,
	}, nil
}

func defaultMetadata() AgentMetadata {
	return AgentMetadata{Confidence: "medium"}
}

var (
	scriptStyleRegex = regexp.MustCompile(`(?i)<(script|style)[^>]*>[\s\S]*?</(script|style)>`)
	htmlTagRegex     = regexp.MustCompile(`<[^>]*>`)
)

func SanitizeBody(body string) string {
	body = scriptStyleRegex.ReplaceAllString(body, "")
	body = htmlTagRegex.ReplaceAllString(body, "")
	if len(body) > maxBodyLength {
		body = body[:maxBodyLength]
	}
	return body
}

func ResolveTitle(suggestedTitle, markdownBody, firstMessage string) string {
	title := ""
	switch {
	case suggestedTitle != "":
		title = suggestedTitle
	case markdownBody != "":
		first := strings.SplitN(markdownBody, "\n", 2)[0]
		title = strings.TrimLeft(first, "# ")
	case firstMessage != "":
		title = strings.SplitN(firstMessage, "\n", 2)[0]
	default:
		return "Untitled issue"
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return "Untitled issue"
	}
	if len(title) > maxTitleLength {
		title = title[:maxTitleLength-3] + "..."
	}
	return title
}

var issueTypeToLabel = map[string]string{
	"bug":         "bug",
	"feature":     "enhancement",
	"improvement": "enhancement",
	"question":    "question",
}

func ResolveLabels(issueType string, defaultLabels []string) []string {
	var labels []string
	if label, ok := issueTypeToLabel[issueType]; ok {
		labels = append(labels, label)
	}
	labels = append(labels, defaultLabels...)
	return labels
}

func FormatIssueBody(channel, reporter, branch, agentBody string) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("**Channel**: #%s\n", channel))
	sb.WriteString(fmt.Sprintf("**Reporter**: %s\n", reporter))
	if branch != "" {
		sb.WriteString(fmt.Sprintf("**Branch**: %s\n", branch))
	}
	sb.WriteString("\n---\n\n")
	sb.WriteString(agentBody)
	return sb.String()
}
