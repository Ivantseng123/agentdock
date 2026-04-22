package slack

import (
	"bytes"
	"fmt"
	"html"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/Ivantseng123/agentdock/shared/metrics"

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

var slackUserIDPattern = regexp.MustCompile(`^[UW][A-Z0-9]+$`)

// isSlackUserID reports whether s matches the shape of a Slack user ID
// (uppercase U or W followed by alphanumeric uppercase). Used to short-
// circuit API calls for strings we know aren't resolvable.
func isSlackUserID(s string) bool {
	return slackUserIDPattern.MatchString(s)
}

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
	if !isSlackUserID(userID) {
		// Not a Slack-shaped ID (bot display name, already-resolved name,
		// etc.). Skip the API call and return as-is.
		return userID
	}
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
	_, err := c.PostMessageWithTS(channelID, text, threadTS)
	return err
}

// UploadFile uploads a text/markdown file into the given thread with an
// initial comment. Used for answer bodies that exceed Slack's comfortable
// read length — the file preview renders the content and users can expand
// or download it. threadTS must be non-empty; the file is posted as a
// thread reply.
func (c *Client) UploadFile(channelID, threadTS, filename, title, content, initialComment string) error {
	start := time.Now()
	_, err := c.api.UploadFile(slack.UploadFileParameters{
		Channel:         channelID,
		ThreadTimestamp: threadTS,
		Filename:        filename,
		Title:           title,
		Content:         content,
		FileSize:        len(content),
		InitialComment:  initialComment,
	})
	metrics.ExternalDuration.WithLabelValues("slack", "upload_file").Observe(time.Since(start).Seconds())
	if err != nil {
		metrics.ExternalErrorsTotal.WithLabelValues("slack", "upload_file").Inc()
		return fmt.Errorf("upload file: %w", err)
	}
	return nil
}

// PostMessageWithTS posts a message and returns its timestamp so callers can
// later UpdateMessage/UpdateMessageWithButton to edit it in place.
func (c *Client) PostMessageWithTS(channelID, text, threadTS string) (string, error) {
	start := time.Now()
	opts := []slack.MsgOption{slack.MsgOptionText(text, false)}
	if threadTS != "" {
		opts = append(opts, slack.MsgOptionTS(threadTS))
	}
	_, ts, err := c.api.PostMessage(channelID, opts...)
	metrics.ExternalDuration.WithLabelValues("slack", "post_message").Observe(time.Since(start).Seconds())
	if err != nil {
		metrics.ExternalErrorsTotal.WithLabelValues("slack", "post_message").Inc()
		return "", fmt.Errorf("post message: %w", err)
	}
	return ts, nil
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
// labels[i] is the button text; values[i] is the payload returned as
// action.Value on click. When values is nil/short, the label doubles as
// the value (legacy behaviour).
// Returns the message timestamp.
func (c *Client) PostSelector(channelID, prompt, actionPrefix string, labels, values []string, threadTS string) (string, error) {
	var buttons []slack.BlockElement
	for i, label := range labels {
		val := label
		if i < len(values) {
			val = values[i]
		}
		buttons = append(buttons, slack.NewButtonBlockElement(
			fmt.Sprintf("%s_%d", actionPrefix, i),
			val,
			slack.NewTextBlockObject(slack.PlainTextType, label, false, false),
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

// PostSelectorWithBack sends a button selector with an optional trailing back
// button. If backActionID is empty, behaves identically to PostSelector. The
// back button is rendered rightmost (farthest from main option buttons) and
// uses default button style.
func (c *Client) PostSelectorWithBack(
	channelID, prompt, actionPrefix string,
	labels, values []string,
	threadTS string,
	backActionID, backLabel string,
) (string, error) {
	var buttons []slack.BlockElement
	for i, label := range labels {
		val := label
		if i < len(values) {
			val = values[i]
		}
		buttons = append(buttons, slack.NewButtonBlockElement(
			fmt.Sprintf("%s_%d", actionPrefix, i),
			val,
			slack.NewTextBlockObject(slack.PlainTextType, label, false, false),
		))
	}
	if backActionID != "" {
		buttons = append(buttons, slack.NewButtonBlockElement(
			backActionID,
			backActionID, // value equals actionID so router doesn't need SelectedOption
			slack.NewTextBlockObject(slack.PlainTextType, backLabel, false, false),
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
		return "", fmt.Errorf("post selector with back: %w", err)
	}
	return ts, nil
}

// PostExternalSelector sends a message with a type-ahead searchable dropdown.
// When cancelActionID and cancelLabel are non-empty, adds a cancel button
// alongside the dropdown so users can bail out of the selection. Returns the
// message timestamp.
func (c *Client) PostExternalSelector(channelID, prompt, actionID, placeholder, threadTS, cancelActionID, cancelLabel string) (string, error) {
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

	elements := []slack.BlockElement{extSelect}
	if cancelActionID != "" && cancelLabel != "" {
		cancelBtn := slack.NewButtonBlockElement(
			cancelActionID,
			cancelLabel,
			slack.NewTextBlockObject(slack.PlainTextType, cancelLabel, false, false),
		)
		elements = append(elements, cancelBtn)
	}
	actions := slack.NewActionBlock(actionID+"_block", elements...)

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

// OpenTextInputModal opens a modal with a single multiline text input.
// metadata is stored in the view's private_metadata so the submit handler
// can resolve the originating pending entry.
func (c *Client) OpenTextInputModal(triggerID, title, label, inputName, metadata string) error {
	textInput := slack.NewPlainTextInputBlockElement(
		slack.NewTextBlockObject(slack.PlainTextType, "請輸入...", false, false),
		inputName,
	)
	textInput.Multiline = true

	inputBlock := slack.NewInputBlock(
		inputName+"_block",
		slack.NewTextBlockObject(slack.PlainTextType, label, false, false),
		nil, textInput,
	)
	inputBlock.Optional = false

	modalView := slack.ModalViewRequest{
		Type:            slack.VTModal,
		Title:           slack.NewTextBlockObject(slack.PlainTextType, title, false, false),
		Submit:          slack.NewTextBlockObject(slack.PlainTextType, "送出", false, false),
		Close:           slack.NewTextBlockObject(slack.PlainTextType, "取消", false, false),
		Blocks:          slack.Blocks{BlockSet: []slack.Block{inputBlock}},
		PrivateMetadata: metadata,
	}
	_, err := c.api.OpenView(triggerID, modalView)
	if err != nil {
		return fmt.Errorf("open text input modal: %w", err)
	}
	return nil
}

// OpenDescriptionModal opens the description modal by delegating to
// OpenTextInputModal. The optional-empty flavour from the pre-Phase-6
// implementation is intentionally dropped (spec §modal-generalisation).
func (c *Client) OpenDescriptionModal(triggerID, selectorMsgTS string) error {
	return c.OpenTextInputModal(triggerID, "補充說明", "補充說明", "description_input", selectorMsgTS)
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

// UpdateMessageWithButton replaces a message's text while preserving a single
// action button (mirrors PostMessageWithButton's block structure). Used for the
// status message where the cancel button must stay visible across updates.
func (c *Client) UpdateMessageWithButton(
	channelID, messageTS, text, actionID, buttonText, value string,
) error {
	btnBlock := slack.NewActionBlock("cancel_actions",
		slack.NewButtonBlockElement(actionID, value,
			slack.NewTextBlockObject("plain_text", buttonText, false, false)),
	)
	textBlock := slack.NewSectionBlock(
		slack.NewTextBlockObject("mrkdwn", text, false, false), nil, nil)

	start := time.Now()
	_, _, _, err := c.api.UpdateMessage(channelID, messageTS,
		slack.MsgOptionBlocks(textBlock, btnBlock),
	)
	metrics.ExternalDuration.WithLabelValues("slack", "post_message").Observe(time.Since(start).Seconds())
	if err != nil {
		metrics.ExternalErrorsTotal.WithLabelValues("slack", "post_message").Inc()
		return fmt.Errorf("update message with button: %w", err)
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

// FetchThreadContext reads all messages in a thread up to the trigger
// point, filtering out our own bot's posts. botUserID and botID are
// both checked because edge cases (custom username, thread broadcast,
// new block API) can leave one field mismatched.
func (c *Client) FetchThreadContext(channelID, threadTS, triggerTS, botUserID, botID string, limit int) ([]ThreadRawMessage, error) {
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

	result := filterThreadMessages(allMessages, triggerTS, botUserID, botID)
	c.logger.Debug("訊息串內容已讀取", "phase", "處理中", "channel_id", channelID, "message_count", len(result), "duration_ms", time.Since(start).Milliseconds())
	return result, nil
}

// filterThreadMessages keeps messages from other participants (human or
// external bots) and drops our own bot's posts (identified by botUserID
// or botID). Bot messages get their text reconstructed from blocks or
// attachments when m.Text is empty; messages whose reconstructed text is
// also empty are dropped entirely. User messages whose content is only a
// @bot mention of us (no other text) are also dropped — those are triggers
// the user sent that got deduped, so they carry no signal for the agent.
// Bot display names are prefixed with "bot:" in the User field so
// downstream prompts can tell them apart from humans.
func filterThreadMessages(messages []slack.Message, triggerTS, botUserID, botID string) []ThreadRawMessage {
	var result []ThreadRawMessage
	for _, m := range messages {
		if m.Timestamp >= triggerTS {
			continue
		}
		if botUserID != "" && m.User == botUserID {
			continue
		}
		if botID != "" && m.BotID == botID {
			continue
		}
		text := extractMessageText(m)
		if m.BotID != "" && text == "" {
			// Pure interactive / reaction-only bot message — no signal for triage.
			continue
		}
		if botUserID != "" && isOnlySelfMention(text, botUserID) {
			// User message that's just "<@bot>" with no actual question — prompt noise.
			continue
		}
		user := m.User
		if m.BotID != "" {
			if name := resolveBotDisplayName(m); name != "" {
				user = "bot:" + name
			}
		}
		result = append(result, ThreadRawMessage{
			User:      user,
			Text:      text,
			Timestamp: m.Timestamp,
			Files:     m.Files,
		})
	}
	return result
}

// isOnlySelfMention reports whether text contains nothing but zero or more
// "<@botUserID>" mentions (and whitespace). Strips every occurrence and
// checks what remains — "<@bot>" / " <@bot> " / "<@bot> <@bot>" all count
// as noise; "<@bot> hello" / "<@otherbot>" do not.
func isOnlySelfMention(text, botUserID string) bool {
	if text == "" {
		return false
	}
	mention := "<@" + botUserID + ">"
	stripped := strings.TrimSpace(strings.ReplaceAll(text, mention, ""))
	return stripped == ""
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

// extractMessageText returns m.Text if non-empty, otherwise reconstructs
// text from blocks (modern integrations) or attachments (legacy). Returns
// "" only when no renderable text is present.
//
// Slack pre-encodes <, >, & as HTML entities (&lt;, &gt;, &amp;) so they
// don't collide with mention/link syntax. We decode here at the adapter
// boundary so downstream consumers (workflow, prompt builder) work with
// plain text — otherwise the worker's xmlEscape runs a second round and
// dependency chains like "exceljs -> archiver" reach the LLM as
// "exceljs -&amp;gt; archiver".
func extractMessageText(m slack.Message) string {
	var text string
	switch {
	case strings.TrimSpace(m.Text) != "":
		text = m.Text
	default:
		if s := extractFromBlocks(m.Blocks.BlockSet); s != "" {
			text = s
		} else if s := extractFromAttachments(m.Attachments); s != "" {
			text = s
		}
	}
	if text == "" {
		return ""
	}
	return html.UnescapeString(text)
}

// extractFromBlocks walks block kit content pulling text from
// text-bearing block types. Interactive / image blocks are ignored.
func extractFromBlocks(blocks []slack.Block) string {
	var parts []string
	for _, b := range blocks {
		switch bb := b.(type) {
		case *slack.SectionBlock:
			if bb.Text != nil && bb.Text.Text != "" {
				parts = append(parts, bb.Text.Text)
			}
			for _, f := range bb.Fields {
				if f != nil && f.Text != "" {
					parts = append(parts, f.Text)
				}
			}
		case *slack.HeaderBlock:
			if bb.Text != nil && bb.Text.Text != "" {
				parts = append(parts, bb.Text.Text)
			}
		case *slack.ContextBlock:
			for _, e := range bb.ContextElements.Elements {
				if tb, ok := e.(*slack.TextBlockObject); ok && tb.Text != "" {
					parts = append(parts, tb.Text)
				}
			}
		case *slack.RichTextBlock:
			for _, el := range bb.Elements {
				var sectionElems []slack.RichTextSectionElement
				switch v := el.(type) {
				case *slack.RichTextSection:
					sectionElems = v.Elements
				case *slack.RichTextQuote:
					sectionElems = v.Elements
				case *slack.RichTextPreformatted:
					sectionElems = v.Elements
				}
				for _, inner := range sectionElems {
					if te, ok := inner.(*slack.RichTextSectionTextElement); ok && te.Text != "" {
						parts = append(parts, te.Text)
					}
				}
			}
		}
	}
	return strings.Join(parts, "\n")
}

// extractFromAttachments renders legacy-API attachment content as plain
// text. Each attachment contributes its Pretext / Title / Text / Fallback
// plus any Fields; multiple attachments are joined with a blank line.
func extractFromAttachments(atts []slack.Attachment) string {
	var segments []string
	for _, a := range atts {
		var parts []string
		if a.Pretext != "" {
			parts = append(parts, a.Pretext)
		}
		if a.Title != "" {
			parts = append(parts, a.Title)
		}
		if a.Text != "" {
			parts = append(parts, a.Text)
		} else if a.Fallback != "" {
			parts = append(parts, a.Fallback)
		}
		for _, f := range a.Fields {
			if f.Title != "" || f.Value != "" {
				parts = append(parts, fmt.Sprintf("*%s*: %s", f.Title, f.Value))
			}
		}
		if len(parts) > 0 {
			segments = append(segments, strings.Join(parts, "\n"))
		}
	}
	return strings.Join(segments, "\n\n")
}

// resolveBotDisplayName picks the best human-friendly name for a bot
// message, preferring BotProfile.Name (what Slack's UI shows) over
// Username (integration-set) and falling back to BotID.
func resolveBotDisplayName(m slack.Message) string {
	if m.BotProfile != nil && m.BotProfile.Name != "" {
		return m.BotProfile.Name
	}
	if m.Username != "" {
		return m.Username
	}
	if m.BotID != "" {
		return m.BotID
	}
	return ""
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
