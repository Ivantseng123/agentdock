package bot

import (
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"github.com/Ivantseng123/agentdock/app/mantis"
)

var urlRegex = regexp.MustCompile(`https?://[^\s<>|]+`)

// enrichMessage expands URLs in the message with their content.
// - Mantis URLs: fetch title + description via API
// - Other URLs: kept as-is
func enrichMessage(message string, mantisClient *mantis.Client) string {
	if mantisClient == nil || !mantisClient.IsConfigured() {
		return message
	}

	urls := urlRegex.FindAllString(message, -1)
	if len(urls) == 0 {
		return message
	}

	var appendix strings.Builder
	for _, url := range urls {
		// Clean Slack URL formatting: <url|label> → url
		cleanURL := url
		if idx := strings.Index(cleanURL, "|"); idx != -1 {
			cleanURL = cleanURL[:idx]
		}
		cleanURL = strings.Trim(cleanURL, "<>")

		if !mantisClient.IsMantisURL(cleanURL) {
			continue
		}

		issueID := mantis.ExtractIssueID(cleanURL)
		if issueID == "" {
			continue
		}

		title, desc, err := mantisClient.FetchIssueSummary(issueID)
		if err != nil {
			slog.Warn("Mantis issue 擴充失敗", "phase", "失敗", "id", issueID, "error", err)
			continue
		}

		slog.Info("Mantis issue 已擴充", "phase", "完成", "id", issueID, "title", title)
		appendix.WriteString(fmt.Sprintf("\n\n--- Mantis #%s: %s ---\n%s\n[原始連結](%s)", issueID, title, desc, cleanURL))
	}

	if appendix.Len() > 0 {
		return message + appendix.String()
	}
	return message
}
