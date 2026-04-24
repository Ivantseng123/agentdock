package slack

import (
	"strings"
	"testing"

	"github.com/slack-go/slack"
)

// TestIsBotAnswerMessage_TableRows mirrors the §1 classification table
// from issue #151 row-by-row. Each case documents which row it comes
// from so future edits to the predicate trace back to the spec.
func TestIsBotAnswerMessage_TableRows(t *testing.T) {
	substantive := strings.Repeat("a", botAnswerMinChars+10)

	cases := []struct {
		name string
		text string
		want bool
		row  string
	}{
		// Row 1: substantive markdown answer → include.
		{"substantive_long_answer", substantive, true, "substantive answer"},
		{"substantive_with_mrkdwn_bold", "*簡答*\nYour code sends a malformed request. Check the auth header format — it should be `Bearer <token>`, not `Token <token>`.", true, "substantive answer"},

		// Row 2: uploaded answer file — file-stream content still passes the
		// text predicate when Slack surfaces it via blocks/attachments. (The
		// pure no-text file-upload case is filtered at the caller by
		// extractMessageText returning "".)
		{"answer_md_content_via_blocks", "## Follow-up on DB migration\n\n" + substantive, true, "uploaded answer"},

		// Row 3: selector prompts — drop.
		{"selector_attach_repo", ":question: 要附加 Repository 嗎？", false, "selector prompt"},
		{"selector_which_repo", ":point_right: Which repo?", false, "selector prompt"},
		{"selector_description", ":pencil2: 要補充說明想讓 agent 做什麼嗎？", false, "selector prompt"},

		// Row 4: user-pick acks — drop.
		{"ack_user_pick", ":white_check_mark: foo/bar", false, "user-pick ack"},

		// Row 5: skip / back breadcrumbs — drop.
		{"skip_breadcrumb", ":fast_forward: 跳過補充說明", false, "skip breadcrumb"},
		{"back_breadcrumb", ":leftwards_arrow_with_hook: 返回 attach 選擇", false, "back breadcrumb"},

		// Row 6: status lines — drop.
		{"status_analyzing", ":mag: 分析 codebase 中...", false, "status line"},
		{"status_thinking", ":thinking_face: 思考中...", false, "status line"},

		// Row 7: user's 補充說明 quote — drop. (User verbatim, not a bot
		// answer; the modal's extra_description already carries this.)
		{"user_memo_echo", ":memo: 補充說明: fix the login bug please", false, "user-echo"},

		// Extras beyond the table: error lines and short acks.
		{"failure_x", ":x: 思考失敗：timeout", false, "error line"},
		{"warning_busy", ":warning: 系統忙碌，請稍後再試", false, "warning line"},
		{"too_short_plain_text", "收到", false, "below min length"},
		{"empty_string", "", false, "empty"},
		{"whitespace_only", "   \n\t ", false, "whitespace only"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isBotAnswerMessage(tc.text)
			if got != tc.want {
				t.Errorf("isBotAnswerMessage(%q) = %v, want %v (row: %s)",
					truncate(tc.text, 60), got, tc.want, tc.row)
			}
		})
	}
}

// TestIsBotAnswerMessage_MinLengthEdge is the guard for substantive-but-
// short bot replies just above/below the threshold.
func TestIsBotAnswerMessage_MinLengthEdge(t *testing.T) {
	justBelow := strings.Repeat("x", botAnswerMinChars-1)
	atThreshold := strings.Repeat("x", botAnswerMinChars)
	if isBotAnswerMessage(justBelow) {
		t.Errorf("expected false at len=%d (below threshold)", len(justBelow))
	}
	if !isBotAnswerMessage(atThreshold) {
		t.Errorf("expected true at len=%d (at threshold)", len(atThreshold))
	}
}

func TestFilterPriorBotAnswers_ReturnsOnlySelfBotPosts(t *testing.T) {
	long := strings.Repeat("a", botAnswerMinChars+10)
	messages := []slack.Message{
		// Human message — must not be counted, even though it's substantive.
		{Msg: slack.Msg{User: "U001", Text: long, Timestamp: "1000.0"}},
		// External bot (e.g. GitHub) with substantive content — skip: only
		// our bot's own answers count as "prior answer".
		{Msg: slack.Msg{
			User: "", Text: long, Timestamp: "1001.0",
			BotID: "B_GH", BotProfile: &slack.BotProfile{Name: "GitHub"},
		}},
		// Self via BotID, substantive — keep.
		{Msg: slack.Msg{
			User: "UBOT", Text: long, Timestamp: "1002.0",
			BotID: "B_SELF", BotProfile: &slack.BotProfile{Name: "ai_trigger_issue_bot"},
		}},
		// Self via BotID, selector prompt — drop (non-answer).
		{Msg: slack.Msg{
			User: "UBOT", Text: ":question: 要附加 Repository 嗎？", Timestamp: "1003.0",
			BotID: "B_SELF",
		}},
		// Self after triggerTS — drop.
		{Msg: slack.Msg{
			User: "UBOT", Text: long, Timestamp: "9999.0",
			BotID: "B_SELF",
		}},
	}

	result := filterPriorBotAnswers(messages, "5000.0", "UBOT", "B_SELF")
	if len(result) != 1 {
		t.Fatalf("expected 1 qualifying message, got %d", len(result))
	}
	if result[0].Timestamp != "1002.0" {
		t.Errorf("wrong message kept: ts=%q", result[0].Timestamp)
	}
	if result[0].User != "bot:ai_trigger_issue_bot" {
		t.Errorf("bot display name not prefixed: %q", result[0].User)
	}
}

func TestFilterPriorBotAnswers_EmptyIdentityReturnsNil(t *testing.T) {
	// Without a bot identity nothing is a "self" post. Defensive — callers
	// should always supply at least one identity axis.
	messages := []slack.Message{
		{Msg: slack.Msg{User: "UBOT", Text: strings.Repeat("a", 100), Timestamp: "1000.0",
			BotID: "B_SELF"}},
	}
	result := filterPriorBotAnswers(messages, "9999.0", "", "")
	if result != nil {
		t.Errorf("expected nil result with empty identity, got %+v", result)
	}
}

func TestFilterPriorBotAnswers_UsesBlocksWhenTextEmpty(t *testing.T) {
	// Messages posted via Block Kit have empty .Text but substantive blocks.
	long := strings.Repeat("a", botAnswerMinChars+10)
	blocks := slack.Blocks{BlockSet: []slack.Block{
		slack.NewSectionBlock(
			slack.NewTextBlockObject("mrkdwn", long, false, false),
			nil, nil,
		),
	}}
	messages := []slack.Message{
		{Msg: slack.Msg{
			User: "UBOT", Timestamp: "1000.0",
			BotID:      "B_SELF",
			BotProfile: &slack.BotProfile{Name: "ai_trigger_issue_bot"},
			Blocks:     blocks,
		}},
	}
	result := filterPriorBotAnswers(messages, "9999.0", "UBOT", "B_SELF")
	if len(result) != 1 {
		t.Fatalf("expected blocks-sourced text to qualify, got %d", len(result))
	}
	if !strings.Contains(result[0].Text, long) {
		t.Errorf("text mismatch: %q", truncate(result[0].Text, 60))
	}
}

func TestFilterPriorBotAnswers_ReturnsChronologicalOrder(t *testing.T) {
	long1 := "answer one " + strings.Repeat("a", botAnswerMinChars)
	long2 := "answer two " + strings.Repeat("b", botAnswerMinChars)
	messages := []slack.Message{
		{Msg: slack.Msg{
			User: "UBOT", Text: long1, Timestamp: "1000.0",
			BotID: "B_SELF",
		}},
		{Msg: slack.Msg{
			User: "UBOT", Text: long2, Timestamp: "2000.0",
			BotID: "B_SELF",
		}},
	}
	result := filterPriorBotAnswers(messages, "9999.0", "UBOT", "B_SELF")
	if len(result) != 2 {
		t.Fatalf("expected 2 qualifying, got %d", len(result))
	}
	if result[0].Timestamp != "1000.0" || result[1].Timestamp != "2000.0" {
		t.Errorf("order broken: got ts %q then %q", result[0].Timestamp, result[1].Timestamp)
	}
	// Caller (FetchPriorBotAnswer) takes the last element — ts=2000.0.
}

// truncate is a test helper: trims a long string for readable error output.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
