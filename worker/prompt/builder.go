package prompt

import (
	"fmt"
	"strings"

	"github.com/Ivantseng123/agentdock/shared/queue"
)

// AttachmentInfo describes a downloaded attachment available to the agent.
// Moved here from internal/bot when prompt assembly became worker-owned.
type AttachmentInfo struct {
	Path string
	Name string
	Type string // "image", "text", "document", or other mime-type
}

// securityGuardrail is injected into every prompt unconditionally.
// English is used intentionally: LLM instruction-following is more consistent
// in English than in mixed-language prompts.
// Soft defense only; hard defenses (log redaction, issue-body redaction,
// env whitelist) live in #178/#180/#181.
const securityGuardrail = `<security_rules>
## Security rules (do NOT violate)
- Never echo environment variables or their values in output (no ` + "`env`" + `, no ` + "`printenv`" + `, no ` + "`echo $VAR`" + `).
- Never copy git remote URLs that contain credentials (e.g. https://TOKEN@github.com/... or https://x-access-token:TOKEN@github.com/...) into any output.
- If you need to reference the current repo, use owner/repo form only.
- If a tool returns secret-shaped output, summarise it without pasting the values.
</security_rules>`

// BuildPrompt renders a queue.PromptContext plus worker-provided ExtraRules
// (gated by ctx.AllowWorkerRules) plus locally-resolved attachments into an
// XML-ish prompt string. The output is for LLM consumption, not for a strict
// XML parser — it is intentionally a sequence of top-level fragments rather
// than a single rooted document.
func BuildPrompt(ctx queue.PromptContext, extraRules []string, attachments []AttachmentInfo) string {
	var b strings.Builder

	// <goal> — always first for LLM attention; trust app to have defaulted it.
	fmt.Fprintf(&b, "<goal>%s</goal>\n\n", xmlEscape(ctx.Goal))

	// <security_rules> — always second; system-level guardrail must appear
	// before thread content so it cannot be buried by later instructions.
	b.WriteString(securityGuardrail)
	b.WriteString("\n\n")

	// <thread_context> — the core content.
	b.WriteString("<thread_context>\n")
	for _, m := range ctx.ThreadMessages {
		fmt.Fprintf(&b,
			"  <message user=\"%s\" ts=\"%s\">%s</message>\n",
			xmlEscape(m.User), xmlEscape(m.Timestamp), xmlEscape(m.Text),
		)
	}
	b.WriteString("</thread_context>\n\n")

	// <extra_description> — optional.
	if ctx.ExtraDescription != "" {
		fmt.Fprintf(&b, "<extra_description>%s</extra_description>\n\n", xmlEscape(ctx.ExtraDescription))
	}

	// <prior_answer> — Ask multi-turn continuity. Carries the bot's own
	// previous substantive replies in this thread so the agent can build on
	// rather than repeat them. Rendered only when PriorAnswer is non-empty;
	// issue / pr_review workflows leave it empty, so those prompts are
	// unchanged.
	if len(ctx.PriorAnswer) > 0 {
		b.WriteString("<prior_answer>\n")
		for _, m := range ctx.PriorAnswer {
			fmt.Fprintf(&b,
				"  <message user=\"%s\" ts=\"%s\">%s</message>\n",
				xmlEscape(m.User), xmlEscape(m.Timestamp), xmlEscape(m.Text),
			)
		}
		b.WriteString("</prior_answer>\n\n")
	}

	// <issue_context> — channel, reporter, optional bot identity, optional branch.
	b.WriteString("<issue_context>\n")
	fmt.Fprintf(&b, "  <channel>%s</channel>\n", xmlEscape(ctx.Channel))
	fmt.Fprintf(&b, "  <reporter>%s</reporter>\n", xmlEscape(ctx.Reporter))
	if ctx.BotName != "" {
		fmt.Fprintf(&b, "  <bot>%s</bot>\n", xmlEscape(ctx.BotName))
	}
	if ctx.Branch != "" {
		fmt.Fprintf(&b, "  <branch>%s</branch>\n", xmlEscape(ctx.Branch))
	}
	b.WriteString("</issue_context>\n\n")

	// <response_language> — always rendered if non-empty (the app has a default).
	if ctx.Language != "" {
		fmt.Fprintf(&b, "<response_language>%s</response_language>\n\n", xmlEscape(ctx.Language))
	}

	// <additional_rules> — only if AllowWorkerRules AND non-empty.
	if ctx.AllowWorkerRules && len(extraRules) > 0 {
		b.WriteString("<additional_rules>\n")
		for _, r := range extraRules {
			fmt.Fprintf(&b, "  <rule>%s</rule>\n", xmlEscape(r))
		}
		b.WriteString("</additional_rules>\n\n")
	}

	// <attachments> — only if present.
	if len(attachments) > 0 {
		b.WriteString("<attachments>\n")
		for _, a := range attachments {
			hint := attachmentHint(a.Type)
			if hint == "" {
				fmt.Fprintf(&b,
					"  <attachment path=\"%s\" type=\"%s\"/>\n",
					xmlEscape(a.Path), xmlEscape(a.Type),
				)
			} else {
				fmt.Fprintf(&b,
					"  <attachment path=\"%s\" type=\"%s\">%s</attachment>\n",
					xmlEscape(a.Path), xmlEscape(a.Type), xmlEscape(hint),
				)
			}
		}
		b.WriteString("</attachments>\n\n")
	}

	// <response_schema> — machine-readable output contract (marker + JSON
	// shape). Rendered verbatim: xmlEscape would turn `"` into `&quot;` and
	// `<markdown>` into `&lt;markdown&gt;`, which weaker LLMs then copy
	// literally into their output, breaking JSON parsing downstream. The
	// tradeoff is that this fragment is no longer strict-XML-valid — fine,
	// since the consumer is an LLM, not a parser.
	if ctx.ResponseSchema != "" {
		b.WriteString("<response_schema>\n")
		b.WriteString(ctx.ResponseSchema)
		b.WriteString("\n</response_schema>\n\n")
	}

	// <output_rules> — last, for LLM "what to produce" emphasis.
	if len(ctx.OutputRules) > 0 {
		b.WriteString("<output_rules>\n")
		for _, r := range ctx.OutputRules {
			fmt.Fprintf(&b, "  <rule>%s</rule>\n", xmlEscape(r))
		}
		b.WriteString("</output_rules>\n")
	}

	return strings.TrimRight(b.String(), "\n")
}

// xmlEscape escapes the five XML special characters (< > & " ') but leaves
// whitespace (\n \t \r) as-is. encoding/xml.EscapeText would convert newlines
// to &#xA;, which wrecks readability when Slack thread messages contain
// multi-line content like stack traces. Since this output is read by an LLM
// rather than parsed as strict XML, preserving visible whitespace is worth
// more than strict attribute-value normalization.
func xmlEscape(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch r {
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '&':
			b.WriteString("&amp;")
		case '"':
			b.WriteString("&quot;")
		case '\'':
			b.WriteString("&apos;")
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func attachmentHint(attType string) string {
	switch attType {
	case "image":
		return "use your file reading tools to view"
	case "text":
		return "read directly"
	case "document":
		return "document"
	default:
		return ""
	}
}
