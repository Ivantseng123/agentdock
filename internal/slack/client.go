package slack

import (
	"bytes"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"agentdock/internal/metrics"

	"github.com/slack-go/slack"
)

type Client struct {
	api    *slack.Client
	logger *slog.Logger
}

// ImageData holds a downloaded image for vision processing.
type ImageData struct {
	Name      string // original filename
	MimeType  string // "image/png", "image/jpeg"
	Data      []byte // raw image bytes
	Permalink string // Slack permalink for fallback/issue body
}

// FetchedMessage contains the text and extracted images from a Slack message.
type FetchedMessage struct {
	Text   string      // message text + inlined text/xlsx content
	Images []ImageData // jpg/png image bytes for vision
}

const (
	maxImageSize  = 20 * 1024 * 1024 // 20 MB
	maxImageCount = 5
)

func NewClient(botToken string, logger *slog.Logger) *Client {
	return &Client{
		api:    slack.New(botToken),
		logger: logger,
	}
}

func (c *Client) API() *slack.Client {
	return c.api
}

// FetchMessage retrieves a message and enriches it with file attachment content.
// Text files are downloaded and inlined. Vision images are downloaded for LLM use.
// xlsx files are parsed to TSV. All other files are noted with filename + permalink.
func (c *Client) FetchMessage(channelID, messageTS string) (FetchedMessage, error) {
	start := time.Now()
	defer func() {
		metrics.ExternalDuration.WithLabelValues("slack", "fetch_message").Observe(time.Since(start).Seconds())
	}()
	params := &slack.GetConversationHistoryParameters{
		ChannelID: channelID,
		Latest:    messageTS,
		Inclusive: true,
		Limit:     1,
	}
	history, err := c.api.GetConversationHistory(params)
	if err != nil {
		metrics.ExternalErrorsTotal.WithLabelValues("slack", "fetch_message").Inc()
		return FetchedMessage{}, fmt.Errorf("fetch message: %w", err)
	}
	if len(history.Messages) == 0 {
		metrics.ExternalErrorsTotal.WithLabelValues("slack", "fetch_message").Inc()
		return FetchedMessage{}, fmt.Errorf("message not found at ts=%s", messageTS)
	}

	msg := history.Messages[0]
	text := msg.Text
	var images []ImageData

	for _, f := range msg.Files {
		if isTextFile(f.Filetype, f.Mimetype) {
			content, dlErr := c.downloadFile(f.URLPrivateDownload)
			if dlErr != nil {
				c.logger.Warn("Slack 檔案下載失敗", "phase", "失敗", "name", f.Name, "error", dlErr)
				text += fmt.Sprintf("\n\n[附件: %s](%s)", f.Name, f.Permalink)
				continue
			}
			lines := strings.Split(content, "\n")
			if len(lines) > 500 {
				content = strings.Join(lines[:500], "\n") + "\n... [truncated]"
			}
			text += fmt.Sprintf("\n\n--- 附件: %s ---\n```\n%s\n```", f.Name, content)
		} else if f.Filetype == "xlsx" {
			data, dlErr := c.downloadBytes(f.URLPrivateDownload)
			if dlErr != nil {
				c.logger.Warn("XLSX 下載失敗", "phase", "失敗", "name", f.Name, "error", dlErr)
				text += fmt.Sprintf("\n\n[附件: %s](%s)", f.Name, f.Permalink)
				continue
			}
			parsed, parseErr := parseXlsx(data, defaultMaxXlsxRows)
			if parseErr != nil {
				c.logger.Warn("XLSX 解析失敗", "phase", "失敗", "name", f.Name, "error", parseErr)
				text += fmt.Sprintf("\n\n[附件: %s](%s)", f.Name, f.Permalink)
				continue
			}
			text += fmt.Sprintf("\n\n--- 附件: %s ---\n%s", f.Name, parsed)
		} else if isVisionImage(f.Filetype) && len(images) < maxImageCount {
			data, dlErr := c.downloadBytes(f.URLPrivateDownload)
			if dlErr != nil {
				c.logger.Warn("圖片下載失敗", "phase", "失敗", "name", f.Name, "error", dlErr)
				text += fmt.Sprintf("\n\n[圖片: %s](%s)", f.Name, f.Permalink)
				continue
			}
			if len(data) > maxImageSize {
				c.logger.Warn("圖片過大，跳過", "phase", "失敗", "name", f.Name, "size", len(data))
				text += fmt.Sprintf("\n\n[圖片: %s](%s)", f.Name, f.Permalink)
				continue
			}
			images = append(images, ImageData{
				Name:      f.Name,
				MimeType:  f.Mimetype,
				Data:      data,
				Permalink: f.Permalink,
			})
			text += fmt.Sprintf("\n\n[圖片: %s](%s)", f.Name, f.Permalink)
		} else if isImageFile(f.Filetype, f.Mimetype) {
			text += fmt.Sprintf("\n\n[圖片: %s](%s)", f.Name, f.Permalink)
		} else {
			text += fmt.Sprintf("\n\n[附件: %s](%s)", f.Name, f.Permalink)
		}
	}

	return FetchedMessage{Text: text, Images: images}, nil
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

func (c *Client) downloadBytes(dlURL string) ([]byte, error) {
	if dlURL == "" {
		return nil, fmt.Errorf("empty download URL")
	}
	var buf bytes.Buffer
	if err := c.api.GetFile(dlURL, &buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
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

func isVisionImage(filetype string) bool {
	return filetype == "png" || filetype == "jpg" || filetype == "jpeg"
}

func (c *Client) ResolveUser(userID string) string {
	user, err := c.api.GetUserInfo(userID)
	if err != nil {
		c.logger.Warn("使用者名稱解析失敗", "phase", "失敗", "user_id", userID, "error", err)
		return userID
	}
	if user.Profile.DisplayName != "" {
		return user.Profile.DisplayName
	}
	return user.RealName
}

// PostMessage sends a text message. If threadTS is non-empty, replies in that thread.
func (c *Client) PostMessage(channelID, text, threadTS string) error {
	start := time.Now()
	opts := []slack.MsgOption{slack.MsgOptionText(text, false)}
	if threadTS != "" {
		opts = append(opts, slack.MsgOptionTS(threadTS))
	}
	_, _, err := c.api.PostMessage(channelID, opts...)
	metrics.ExternalDuration.WithLabelValues("slack", "post_message").Observe(time.Since(start).Seconds())
	if err != nil {
		metrics.ExternalErrorsTotal.WithLabelValues("slack", "post_message").Inc()
		return fmt.Errorf("post message: %w", err)
	}
	return nil
}

func (c *Client) GetChannelName(channelID string) string {
	info, err := c.api.GetConversationInfo(&slack.GetConversationInfoInput{
		ChannelID: channelID,
	})
	if err != nil {
		c.logger.Warn("頻道名稱解析失敗", "phase", "失敗", "channel_id", channelID, "error", err)
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

// PostMessageWithButton sends a message with a single action button in the thread.
// Returns the message timestamp.
func (c *Client) PostMessageWithButton(channelID, text, threadTS, actionID, buttonText, value string) (string, error) {
	btnBlock := slack.NewActionBlock("cancel_actions",
		slack.NewButtonBlockElement(actionID, value,
			slack.NewTextBlockObject("plain_text", buttonText, false, false)),
	)
	textBlock := slack.NewSectionBlock(
		slack.NewTextBlockObject("mrkdwn", text, false, false), nil, nil)

	opts := []slack.MsgOption{
		slack.MsgOptionBlocks(textBlock, btnBlock),
	}
	if threadTS != "" {
		opts = append(opts, slack.MsgOptionTS(threadTS))
	}
	start := time.Now()
	_, ts, err := c.api.PostMessage(channelID, opts...)
	metrics.ExternalDuration.WithLabelValues("slack", "post_message").Observe(time.Since(start).Seconds())
	if err != nil {
		metrics.ExternalErrorsTotal.WithLabelValues("slack", "post_message").Inc()
		return "", fmt.Errorf("post message with button: %w", err)
	}
	return ts, nil
}

// UpdateMessage replaces an existing message (used to clear buttons after selection).
func (c *Client) UpdateMessage(channelID, messageTS, text string) error {
	start := time.Now()
	_, _, _, err := c.api.UpdateMessage(channelID, messageTS,
		slack.MsgOptionText(text, false),
	)
	metrics.ExternalDuration.WithLabelValues("slack", "post_message").Observe(time.Since(start).Seconds())
	if err != nil {
		metrics.ExternalErrorsTotal.WithLabelValues("slack", "post_message").Inc()
		return fmt.Errorf("update message: %w", err)
	}
	return nil
}

// ThreadRawMessage is a raw message from a Slack thread.
type ThreadRawMessage struct {
	User      string
	Text      string
	Timestamp string
	Files     []slack.File
}

// FetchThreadContext reads all messages in a thread up to the trigger point.
func (c *Client) FetchThreadContext(channelID, threadTS, triggerTS, botUserID string, limit int) ([]ThreadRawMessage, error) {
	start := time.Now()
	if limit <= 0 {
		limit = 50
	}

	var allMessages []slack.Message
	cursor := ""

	for {
		params := &slack.GetConversationRepliesParameters{
			ChannelID: channelID,
			Timestamp: threadTS,
			Cursor:    cursor,
			Limit:     200,
		}

		msgs, hasMore, nextCursor, err := c.api.GetConversationReplies(params)
		if err != nil {
			return nil, fmt.Errorf("conversations.replies: %w", err)
		}

		allMessages = append(allMessages, msgs...)

		if !hasMore || len(allMessages) >= limit {
			break
		}
		cursor = nextCursor
	}

	result := filterThreadMessages(allMessages, triggerTS, botUserID)
	c.logger.Debug("訊息串內容已讀取", "phase", "處理中", "channel_id", channelID, "message_count", len(result), "duration_ms", time.Since(start).Milliseconds())
	return result, nil
}

// filterThreadMessages filters out bot messages and messages at/after the trigger.
func filterThreadMessages(messages []slack.Message, triggerTS, botUserID string) []ThreadRawMessage {
	var result []ThreadRawMessage
	for _, m := range messages {
		if m.Timestamp >= triggerTS {
			continue
		}
		if m.BotID != "" || m.User == botUserID {
			continue
		}
		result = append(result, ThreadRawMessage{
			User:      m.User,
			Text:      m.Text,
			Timestamp: m.Timestamp,
			Files:     m.Files,
		})
	}
	return result
}

// AttachmentDownload is the result of downloading a single attachment.
type AttachmentDownload struct {
	Name   string
	Path   string
	Type   string // "image", "text", "document"
	Failed bool
}

// DownloadAttachments downloads thread attachments to a temp dir.
func (c *Client) DownloadAttachments(messages []ThreadRawMessage, tempDir string) []AttachmentDownload {
	var attachments []AttachmentDownload

	for _, msg := range messages {
		for _, f := range msg.Files {
			dlURL := f.URLPrivateDownload
			if dlURL == "" {
				dlURL = f.URLPrivate
			}
			c.logger.Debug("下載附件", "phase", "處理中", "name", f.Name, "url_private_download", f.URLPrivateDownload, "url_private", f.URLPrivate, "size", f.Size)
			data, err := c.downloadBytes(dlURL)
			if err != nil {
				c.logger.Warn("附件下載失敗", "phase", "失敗", "name", f.Name, "error", err)
				attachments = append(attachments, AttachmentDownload{
					Name:   f.Name,
					Type:   classifyAttachment(f.Filetype, f.Mimetype),
					Failed: true,
				})
				continue
			}
			c.logger.Debug("附件已下載", "phase", "處理中", "name", f.Name, "expected_size", f.Size, "actual_size", len(data))

			path := filepath.Join(tempDir, f.Name)
			if err := os.WriteFile(path, data, 0644); err != nil {
				c.logger.Warn("附件寫入失敗", "phase", "失敗", "name", f.Name, "error", err)
				continue
			}

			attachments = append(attachments, AttachmentDownload{
				Name: f.Name,
				Path: path,
				Type: classifyAttachment(f.Filetype, f.Mimetype),
			})
		}
	}
	return attachments
}

// classifyAttachment determines the type of a Slack file.
func classifyAttachment(filetype, mimetype string) string {
	if isImageFile(filetype, mimetype) {
		return "image"
	}
	if isTextFile(filetype, mimetype) {
		return "text"
	}
	return "document"
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
