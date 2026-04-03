package slack

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/slack-go/slack"
)

type Client struct {
	api *slack.Client
}

func NewClient(botToken string) *Client {
	return &Client{
		api: slack.New(botToken),
	}
}

func (c *Client) API() *slack.Client {
	return c.api
}

// FetchMessage retrieves a message and enriches it with file attachment content.
// Text files are downloaded and inlined. Images are noted with filename + permalink.
func (c *Client) FetchMessage(channelID, messageTS string) (string, error) {
	params := &slack.GetConversationHistoryParameters{
		ChannelID: channelID,
		Latest:    messageTS,
		Inclusive: true,
		Limit:     1,
	}
	history, err := c.api.GetConversationHistory(params)
	if err != nil {
		return "", fmt.Errorf("fetch message: %w", err)
	}
	if len(history.Messages) == 0 {
		return "", fmt.Errorf("message not found at ts=%s", messageTS)
	}

	msg := history.Messages[0]
	text := msg.Text

	// Process file attachments
	for _, f := range msg.Files {
		if isTextFile(f.Filetype, f.Mimetype) {
			content, dlErr := c.downloadFile(f.URLPrivateDownload)
			if dlErr != nil {
				slog.Warn("failed to download slack file", "name", f.Name, "error", dlErr)
				text += fmt.Sprintf("\n\n[附件: %s](%s)", f.Name, f.Permalink)
				continue
			}
			// Cap at 500 lines
			lines := strings.Split(content, "\n")
			if len(lines) > 500 {
				content = strings.Join(lines[:500], "\n") + "\n... [truncated]"
			}
			text += fmt.Sprintf("\n\n--- 附件: %s ---\n```\n%s\n```", f.Name, content)
		} else if isImageFile(f.Filetype, f.Mimetype) {
			text += fmt.Sprintf("\n\n[圖片: %s](%s)", f.Name, f.Permalink)
		} else {
			text += fmt.Sprintf("\n\n[附件: %s](%s)", f.Name, f.Permalink)
		}
	}

	return text, nil
}

func (c *Client) downloadFile(url string) (string, error) {
	if url == "" {
		return "", fmt.Errorf("empty download URL")
	}
	buf := new(strings.Builder)
	err := c.api.GetFile(url, buf)
	if err != nil {
		return "", err
	}
	return buf.String(), nil
}

func isTextFile(filetype, mimetype string) bool {
	textTypes := []string{"text", "csv", "tsv", "log", "json", "xml", "yaml", "yml",
		"html", "css", "javascript", "python", "go", "java", "ruby", "shell",
		"markdown", "sql", "plain", "snippet"}
	for _, t := range textTypes {
		if strings.Contains(filetype, t) || strings.Contains(mimetype, "text/") {
			return true
		}
	}
	return false
}

func isImageFile(filetype, mimetype string) bool {
	return strings.HasPrefix(mimetype, "image/") ||
		filetype == "png" || filetype == "jpg" || filetype == "jpeg" ||
		filetype == "gif" || filetype == "webp" || filetype == "svg"
}

func (c *Client) ResolveUser(userID string) string {
	user, err := c.api.GetUserInfo(userID)
	if err != nil {
		slog.Warn("failed to resolve user", "userID", userID, "error", err)
		return userID
	}
	if user.Profile.DisplayName != "" {
		return user.Profile.DisplayName
	}
	return user.RealName
}

// PostMessage sends a text message. If threadTS is non-empty, replies in that thread.
func (c *Client) PostMessage(channelID, text, threadTS string) error {
	opts := []slack.MsgOption{slack.MsgOptionText(text, false)}
	if threadTS != "" {
		opts = append(opts, slack.MsgOptionTS(threadTS))
	}
	_, _, err := c.api.PostMessage(channelID, opts...)
	if err != nil {
		return fmt.Errorf("post message: %w", err)
	}
	return nil
}

func (c *Client) GetChannelName(channelID string) string {
	info, err := c.api.GetConversationInfo(&slack.GetConversationInfoInput{
		ChannelID: channelID,
	})
	if err != nil {
		slog.Warn("failed to resolve channel name", "channelID", channelID, "error", err)
		return channelID
	}
	return "#" + info.Name
}

// PostSelector sends a message with clickable buttons.
// If threadTS is non-empty, posts in that thread.
// Returns the message timestamp.
func (c *Client) PostSelector(channelID, prompt, actionPrefix string, options []string, threadTS string) (string, error) {
	var buttons []slack.BlockElement
	for i, opt := range options {
		buttons = append(buttons, slack.NewButtonBlockElement(
			fmt.Sprintf("%s_%d", actionPrefix, i),
			opt,
			slack.NewTextBlockObject(slack.PlainTextType, opt, false, false),
		))
	}

	section := slack.NewSectionBlock(
		slack.NewTextBlockObject(slack.MarkdownType, prompt, false, false),
		nil, nil,
	)
	actions := slack.NewActionBlock(actionPrefix, buttons...)

	opts := []slack.MsgOption{slack.MsgOptionBlocks(section, actions)}
	if threadTS != "" {
		opts = append(opts, slack.MsgOptionTS(threadTS))
	}

	_, ts, err := c.api.PostMessage(channelID, opts...)
	if err != nil {
		return "", fmt.Errorf("post selector: %w", err)
	}
	return ts, nil
}

// PostExternalSelector sends a message with a type-ahead searchable dropdown.
// Returns the message timestamp.
func (c *Client) PostExternalSelector(channelID, prompt, actionID, placeholder, threadTS string) (string, error) {
	section := slack.NewSectionBlock(
		slack.NewTextBlockObject(slack.MarkdownType, prompt, false, false),
		nil, nil,
	)

	extSelect := slack.NewOptionsSelectBlockElement(
		slack.OptTypeExternal,
		slack.NewTextBlockObject(slack.PlainTextType, placeholder, false, false),
		actionID,
	)
	extSelect.MinQueryLength = new(int) // 0 = show options immediately
	*extSelect.MinQueryLength = 0

	actions := slack.NewActionBlock(actionID+"_block", extSelect)

	opts := []slack.MsgOption{slack.MsgOptionBlocks(section, actions)}
	if threadTS != "" {
		opts = append(opts, slack.MsgOptionTS(threadTS))
	}

	_, ts, err := c.api.PostMessage(channelID, opts...)
	if err != nil {
		return "", fmt.Errorf("post external selector: %w", err)
	}
	return ts, nil
}

// OpenDescriptionModal opens a modal with a text input for extra description.
// selectorMsgTS is stored as private_metadata so we can find the pending issue on submit.
func (c *Client) OpenDescriptionModal(triggerID, selectorMsgTS string) error {
	textInput := slack.NewPlainTextInputBlockElement(
		slack.NewTextBlockObject(slack.PlainTextType, "輸入補充說明...", false, false),
		"description_input",
	)
	textInput.Multiline = true

	inputBlock := slack.NewInputBlock(
		"description_block",
		slack.NewTextBlockObject(slack.PlainTextType, "補充說明", false, false),
		nil,
		textInput,
	)
	inputBlock.Optional = true

	modalView := slack.ModalViewRequest{
		Type:            slack.VTModal,
		Title:           slack.NewTextBlockObject(slack.PlainTextType, "補充說明", false, false),
		Submit:          slack.NewTextBlockObject(slack.PlainTextType, "送出", false, false),
		Close:           slack.NewTextBlockObject(slack.PlainTextType, "跳過", false, false),
		Blocks:          slack.Blocks{BlockSet: []slack.Block{inputBlock}},
		PrivateMetadata: selectorMsgTS,
	}

	_, err := c.api.OpenView(triggerID, modalView)
	if err != nil {
		return fmt.Errorf("open modal: %w", err)
	}
	return nil
}

// UpdateMessage replaces an existing message (used to clear buttons after selection).
func (c *Client) UpdateMessage(channelID, messageTS, text string) error {
	_, _, _, err := c.api.UpdateMessage(channelID, messageTS,
		slack.MsgOptionText(text, false),
	)
	if err != nil {
		return fmt.Errorf("update message: %w", err)
	}
	return nil
}

func ExtractKeywords(message string) []string {
	stopWords := map[string]bool{
		"the": true, "a": true, "an": true, "is": true, "are": true,
		"was": true, "were": true, "be": true, "been": true, "being": true,
		"have": true, "has": true, "had": true, "do": true, "does": true,
		"did": true, "will": true, "would": true, "could": true, "should": true,
		"may": true, "might": true, "must": true, "shall": true,
		"not": true, "no": true, "nor": true, "and": true, "or": true,
		"but": true, "if": true, "then": true, "else": true, "when": true,
		"at": true, "by": true, "for": true, "with": true, "about": true,
		"to": true, "from": true, "in": true, "on": true, "of": true,
		"up": true, "out": true, "off": true, "over": true, "under": true,
		"after": true, "before": true, "between": true, "through": true,
		"it": true, "its": true, "this": true, "that": true, "these": true,
		"those": true, "i": true, "you": true, "he": true, "she": true,
		"we": true, "they": true, "me": true, "him": true, "her": true,
		"us": true, "them": true, "my": true, "your": true, "his": true,
		"our": true, "their": true, "shows": true, "user": true,
	}

	words := strings.Fields(strings.ToLower(message))
	var keywords []string
	for _, w := range words {
		w = strings.Trim(w, ".,!?;:'\"()[]{}")
		if len(w) < 3 {
			continue
		}
		if stopWords[w] {
			continue
		}
		keywords = append(keywords, w)
	}

	if len(keywords) > 10 {
		keywords = keywords[:10]
	}
	return keywords
}
