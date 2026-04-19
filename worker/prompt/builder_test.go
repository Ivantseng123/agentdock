package prompt

import (
	"strings"
	"testing"

	"github.com/Ivantseng123/agentdock/shared/queue"
)

func TestBuildPrompt_Basic(t *testing.T) {
	ctx := queue.PromptContext{
		ThreadMessages: []queue.ThreadMessage{
			{User: "Alice", Timestamp: "2026-04-09 10:30", Text: "login broken"},
		},
		Channel:     "general",
		Reporter:    "Alice",
		Language:    "zh-TW",
		Goal:        "triage this",
		OutputRules: []string{"short reply"},
	}
	got := BuildPrompt(ctx, nil, nil)

	wants := []string{
		`<goal>triage this</goal>`,
		`<message user="Alice" ts="2026-04-09 10:30">login broken</message>`,
		`<channel>general</channel>`,
		`<reporter>Alice</reporter>`,
		`<response_language>zh-TW</response_language>`,
		`<rule>short reply</rule>`,
	}
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("missing fragment %q in:\n%s", w, got)
		}
	}
}

func TestBuildPrompt_Ordering_GoalFirst_OutputRulesLast(t *testing.T) {
	ctx := queue.PromptContext{
		ThreadMessages: []queue.ThreadMessage{{User: "A", Timestamp: "1", Text: "t"}},
		Channel:        "c",
		Reporter:       "r",
		Language:       "en",
		Goal:           "g",
		OutputRules:    []string{"o"},
	}
	got := BuildPrompt(ctx, nil, nil)

	goalIdx := strings.Index(got, "<goal>")
	ctxIdx := strings.Index(got, "<thread_context>")
	rulesIdx := strings.Index(got, "<output_rules>")

	if goalIdx == -1 || ctxIdx == -1 || rulesIdx == -1 {
		t.Fatalf("missing sections: goal=%d thread=%d rules=%d", goalIdx, ctxIdx, rulesIdx)
	}
	if goalIdx != 0 {
		t.Errorf("expected <goal> at index 0, got %d", goalIdx)
	}
	if !(goalIdx < ctxIdx && ctxIdx < rulesIdx) {
		t.Errorf("expected goal < thread < output_rules, got goal=%d thread=%d rules=%d", goalIdx, ctxIdx, rulesIdx)
	}
	if !strings.HasSuffix(got, "</output_rules>") {
		t.Errorf("expected output_rules to be last section, got suffix: %q", got[max(0, len(got)-60):])
	}
}

func TestBuildPrompt_OptionalOmitted_ExtraDescriptionAndBranch(t *testing.T) {
	ctx := queue.PromptContext{
		ThreadMessages: []queue.ThreadMessage{{User: "A", Timestamp: "1", Text: "t"}},
		Channel:        "c",
		Reporter:       "r",
		Language:       "en",
		Goal:           "g",
	}
	got := BuildPrompt(ctx, nil, nil)

	if strings.Contains(got, "<extra_description>") {
		t.Errorf("expected no <extra_description>, got:\n%s", got)
	}
	if strings.Contains(got, "<branch>") {
		t.Errorf("expected no <branch>, got:\n%s", got)
	}
}

func TestBuildPrompt_OptionalOmitted_NoAttachments(t *testing.T) {
	ctx := queue.PromptContext{
		ThreadMessages: []queue.ThreadMessage{{User: "A", Timestamp: "1", Text: "t"}},
		Goal:           "g",
	}
	got := BuildPrompt(ctx, nil, nil)
	if strings.Contains(got, "<attachments>") {
		t.Errorf("expected no <attachments> when attachments nil, got:\n%s", got)
	}
}

func TestBuildPrompt_OptionalOmitted_EmptyOutputRules(t *testing.T) {
	ctx := queue.PromptContext{
		ThreadMessages: []queue.ThreadMessage{{User: "A", Timestamp: "1", Text: "t"}},
		Goal:           "g",
		OutputRules:    nil,
	}
	got := BuildPrompt(ctx, nil, nil)
	if strings.Contains(got, "<output_rules>") {
		t.Errorf("expected no <output_rules> when empty, got:\n%s", got)
	}
}

func TestBuildPrompt_WorkerRulesToggle_AllowFalse_NoRules(t *testing.T) {
	ctx := queue.PromptContext{
		ThreadMessages:   []queue.ThreadMessage{{User: "A", Timestamp: "1", Text: "t"}},
		Goal:             "g",
		AllowWorkerRules: false,
	}
	got := BuildPrompt(ctx, []string{"rule1"}, nil)
	if strings.Contains(got, "<additional_rules>") {
		t.Errorf("expected no <additional_rules> when AllowWorkerRules=false, got:\n%s", got)
	}
}

func TestBuildPrompt_WorkerRulesToggle_AllowTrue_EmptyRules_NoSection(t *testing.T) {
	ctx := queue.PromptContext{
		ThreadMessages:   []queue.ThreadMessage{{User: "A", Timestamp: "1", Text: "t"}},
		Goal:             "g",
		AllowWorkerRules: true,
	}
	got := BuildPrompt(ctx, nil, nil)
	if strings.Contains(got, "<additional_rules>") {
		t.Errorf("expected no <additional_rules> when ExtraRules empty, got:\n%s", got)
	}
}

func TestBuildPrompt_WorkerRulesToggle_AllowTrue_WithRules_Rendered(t *testing.T) {
	ctx := queue.PromptContext{
		ThreadMessages:   []queue.ThreadMessage{{User: "A", Timestamp: "1", Text: "t"}},
		Goal:             "g",
		AllowWorkerRules: true,
	}
	got := BuildPrompt(ctx, []string{"no guess", "real files only"}, nil)
	if !strings.Contains(got, "<additional_rules>") {
		t.Errorf("expected <additional_rules>, got:\n%s", got)
	}
	if !strings.Contains(got, "<rule>no guess</rule>") {
		t.Errorf("missing rule1 in:\n%s", got)
	}
	if !strings.Contains(got, "<rule>real files only</rule>") {
		t.Errorf("missing rule2 in:\n%s", got)
	}
}

func TestBuildPrompt_XMLEscaping(t *testing.T) {
	ctx := queue.PromptContext{
		ThreadMessages: []queue.ThreadMessage{
			{User: `<Alice & "Bob">`, Timestamp: "1", Text: `<script>alert("x")</script>`},
		},
		Channel:     "c",
		Reporter:    "r",
		Goal:        "g",
		OutputRules: []string{"< 100 chars"},
	}
	got := BuildPrompt(ctx, nil, nil)

	if strings.Contains(got, "<script>") {
		t.Errorf("unescaped <script> in output:\n%s", got)
	}
	if !strings.Contains(got, "&lt;script&gt;") {
		t.Errorf("expected escaped <script>, got:\n%s", got)
	}
	if !strings.Contains(got, "&amp;") {
		t.Errorf("expected escaped &amp;, got:\n%s", got)
	}
	if !strings.Contains(got, "&quot;") {
		t.Errorf("expected escaped &quot; for double quotes, got:\n%s", got)
	}
	if !strings.Contains(got, "&lt; 100 chars") {
		t.Errorf("expected escaped '< 100 chars', got:\n%s", got)
	}
}

// TestBuildPrompt_PreservesWhitespace guards against accidentally using
// encoding/xml.EscapeText (which converts \n to &#xA;). Slack thread messages
// often contain multi-line stack traces; those newlines must reach the LLM
// verbatim, not as entity references.
func TestBuildPrompt_PreservesWhitespace(t *testing.T) {
	ctx := queue.PromptContext{
		ThreadMessages: []queue.ThreadMessage{
			{User: "Alice", Timestamp: "1", Text: "line1\nline2\n\tindented"},
		},
		Channel:  "c",
		Reporter: "r",
		Goal:     "g",
	}
	got := BuildPrompt(ctx, nil, nil)

	if !strings.Contains(got, "line1\nline2\n\tindented") {
		t.Errorf("expected raw newlines/tabs preserved in message text, got:\n%s", got)
	}
	if strings.Contains(got, "&#xA;") || strings.Contains(got, "&#10;") {
		t.Errorf("newline was entity-encoded (should be raw), got:\n%s", got)
	}
	if strings.Contains(got, "&#x9;") || strings.Contains(got, "&#9;") {
		t.Errorf("tab was entity-encoded (should be raw), got:\n%s", got)
	}
}

func TestBuildPrompt_Attachments_ImageTextDocument(t *testing.T) {
	ctx := queue.PromptContext{
		ThreadMessages: []queue.ThreadMessage{{User: "A", Timestamp: "1", Text: "t"}},
		Goal:           "g",
	}
	atts := []AttachmentInfo{
		{Path: "/tmp/a.png", Name: "a.png", Type: "image"},
		{Path: "/tmp/b.log", Name: "b.log", Type: "text"},
		{Path: "/tmp/c.pdf", Name: "c.pdf", Type: "document"},
	}
	got := BuildPrompt(ctx, nil, atts)
	if !strings.Contains(got, `<attachment path="/tmp/a.png" type="image">use your file reading tools to view</attachment>`) {
		t.Errorf("image hint wrong, got:\n%s", got)
	}
	if !strings.Contains(got, `<attachment path="/tmp/b.log" type="text">read directly</attachment>`) {
		t.Errorf("text hint wrong, got:\n%s", got)
	}
	if !strings.Contains(got, `<attachment path="/tmp/c.pdf" type="document">document</attachment>`) {
		t.Errorf("document hint wrong, got:\n%s", got)
	}
}

func TestBuildPrompt_Attachments_UnknownTypeSelfClosing(t *testing.T) {
	ctx := queue.PromptContext{
		ThreadMessages: []queue.ThreadMessage{{User: "A", Timestamp: "1", Text: "t"}},
		Goal:           "g",
	}
	atts := []AttachmentInfo{
		{Path: "/tmp/z.bin", Name: "z.bin", Type: "binary"},
	}
	got := BuildPrompt(ctx, nil, atts)
	if !strings.Contains(got, `<attachment path="/tmp/z.bin" type="binary"/>`) {
		t.Errorf("unknown type should render self-closing, got:\n%s", got)
	}
}

func TestBuildPrompt_OutputRulesArray_MultipleRendered(t *testing.T) {
	ctx := queue.PromptContext{
		ThreadMessages: []queue.ThreadMessage{{User: "A", Timestamp: "1", Text: "t"}},
		Goal:           "g",
		OutputRules:    []string{"one-liner", "< 100 chars", "slack-friendly"},
	}
	got := BuildPrompt(ctx, nil, nil)
	for _, r := range []string{"one-liner", "slack-friendly"} {
		if !strings.Contains(got, "<rule>"+r+"</rule>") {
			t.Errorf("missing rule %q in output_rules:\n%s", r, got)
		}
	}
}
