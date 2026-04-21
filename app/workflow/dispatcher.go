package workflow

import (
	"context"
	"log/slog"
	"strings"
)

// KnownVerbs enumerates verbs recognised by the dispatcher. Adding a verb
// here is not enough — the corresponding workflow must also be registered.
var KnownVerbs = []string{"issue", "ask", "review"}

// TriggerParse is the result of running ParseTrigger on an @bot mention.
// Verb is always lowercase for case-insensitive matching.
type TriggerParse struct {
	Verb      string // lowercase; "" if no verb (legacy bare-repo or empty)
	Args      string // remainder after verb + whitespace; unwrapped from Slack <...>
	KnownVerb bool   // true iff Verb is in KnownVerbs
}

// ParseTrigger extracts the verb and args from a mention's raw text. It:
//   - Strips leading Slack control tokens (<@U...>, <!channel>, <!here>, ...)
//   - Strips the legacy /triage prefix
//   - Lowercases the first token to match verbs case-insensitively
//   - Strips Slack URL auto-wrapping (<...>) from the remaining args
//   - Sets KnownVerb iff the verb matches one of KnownVerbs
func ParseTrigger(text string) TriggerParse {
	text = strings.TrimSpace(text)
	// Strip all leading Slack control tokens: <@U...>, <!channel>, <!here>,
	// etc. Slack delivers these in message.text whenever the user prefixes
	// the bot mention with one (e.g. "@here @bot ask Q" → "<!here> <@BOT> ask Q").
	// Only the bot mention itself signals we should even run — everything
	// before the verb is noise.
	for strings.HasPrefix(text, "<@") || strings.HasPrefix(text, "<!") {
		closeIdx := strings.Index(text, ">")
		if closeIdx < 0 {
			break
		}
		text = strings.TrimSpace(text[closeIdx+1:])
	}
	// Strip legacy /triage prefix
	text = strings.TrimSpace(strings.TrimPrefix(text, "/triage"))

	if text == "" {
		return TriggerParse{}
	}

	// Split into first token + rest
	var first, rest string
	if sp := strings.IndexAny(text, " \t"); sp >= 0 {
		first = text[:sp]
		rest = strings.TrimSpace(text[sp+1:])
	} else {
		first = text
		rest = ""
	}

	verb := strings.ToLower(first)
	rest = stripSlackURLWrap(rest)

	for _, kv := range KnownVerbs {
		if verb == kv {
			return TriggerParse{Verb: verb, Args: rest, KnownVerb: true}
		}
	}

	// Unknown first token. Decide whether it should be treated as legacy
	// bare-repo ("foo/bar") or as an unknown verb.
	if LooksLikeRepo(first) {
		// Bare repo — empty verb, whole text as args.
		return TriggerParse{Verb: "", Args: stripSlackURLWrap(text), KnownVerb: false}
	}
	// Unknown verb — surface the typed verb so dispatcher can tell the user.
	return TriggerParse{Verb: verb, Args: rest, KnownVerb: false}
}

// LooksLikeRepo returns true iff s matches owner/repo or owner/repo@branch.
// Used by the dispatcher to keep `@bot foo/bar` routing to Issue (legacy).
func LooksLikeRepo(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	// Reject anything that looks like a URL.
	if strings.Contains(s, "://") {
		return false
	}
	// Reject trailing '@' (empty branch is not meaningful here).
	if strings.HasSuffix(s, "@") {
		return false
	}
	// Split off optional @branch.
	if at := strings.IndexByte(s, '@'); at >= 0 {
		s = s[:at]
	}
	// Must contain exactly one "/"
	return strings.Count(s, "/") == 1 && !strings.HasPrefix(s, "/") && !strings.HasSuffix(s, "/")
}

func stripSlackURLWrap(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && s[0] == '<' && s[len(s)-1] == '>' {
		inner := s[1 : len(s)-1]
		// Slack wraps URLs and sometimes appends "|display" — drop that part.
		if pipe := strings.IndexByte(inner, '|'); pipe >= 0 {
			inner = inner[:pipe]
		}
		if strings.HasPrefix(inner, "http://") || strings.HasPrefix(inner, "https://") {
			return inner
		}
	}
	return s
}

// Dispatcher routes parsed triggers to the right Workflow via the Registry.
// Constructed once at app startup; safe to call Dispatch concurrently.
type Dispatcher struct {
	registry *Registry
	slack    SlackPort
	logger   *slog.Logger
}

// NewDispatcher wires a dispatcher around a populated registry and a
// SlackPort. Panics if registry is nil.
func NewDispatcher(reg *Registry, slack SlackPort, logger *slog.Logger) *Dispatcher {
	if reg == nil {
		panic("workflow: NewDispatcher called with nil registry")
	}
	return &Dispatcher{registry: reg, slack: slack, logger: logger}
}

// Dispatch parses the trigger event and routes it to the matching workflow.
// Unknown verbs / no-verb-no-args cases return a D-selector NextStep.
// Returns the initial Pending (the dispatcher fills SelectorTS after the
// caller posts the selector/modal) and the NextStep to execute.
func (d *Dispatcher) Dispatch(ctx context.Context, ev TriggerEvent) (*Pending, NextStep, error) {
	tp := ParseTrigger(ev.Text)

	if tp.Verb == "" && tp.Args == "" {
		// Plain @bot with no args → D-selector.
		return d.postDSelector(ev, "")
	}
	if tp.KnownVerb {
		wf, ok := d.registry.Get(tp.Verb)
		if !ok {
			// Verb declared known but no workflow registered — registry
			// misconfiguration; fail loudly.
			return nil, NextStep{Kind: NextStepError, ErrorText: "workflow " + tp.Verb + " not registered"}, nil
		}
		step, err := wf.Trigger(ctx, ev, tp.Args)
		if err != nil {
			return nil, NextStep{Kind: NextStepError, ErrorText: err.Error()}, err
		}
		if step.Pending != nil {
			step.Pending.TaskType = wf.Type()
		}
		return step.Pending, step, nil
	}
	// Not a known verb. Either legacy bare-repo (LooksLikeRepo) → Issue,
	// or unknown verb → D-selector with warning.
	if tp.Verb == "" && LooksLikeRepo(tp.Args) {
		wf, _ := d.registry.Get("issue")
		if wf == nil {
			return nil, NextStep{Kind: NextStepError, ErrorText: "issue workflow not registered"}, nil
		}
		step, err := wf.Trigger(ctx, ev, tp.Args)
		if err != nil {
			return nil, NextStep{Kind: NextStepError, ErrorText: err.Error()}, err
		}
		if step.Pending != nil {
			step.Pending.TaskType = "issue"
		}
		return step.Pending, step, nil
	}

	// Unknown verb — D-selector with warning.
	warning := ""
	if tp.Verb != "" {
		warning = ":warning: 不認得 `" + tp.Verb + "`，請選一個："
	}
	return d.postDSelector(ev, warning)
}

// postDSelector returns a NextStep that renders the three-button selector.
// warning prepends a :warning: line (empty string = no warning).
func (d *Dispatcher) postDSelector(ev TriggerEvent, warning string) (*Pending, NextStep, error) {
	prompt := warning
	if prompt != "" {
		prompt += "\n"
	}
	prompt += ":point_right: 你想做什麼？"

	pending := &Pending{
		ChannelID: ev.ChannelID,
		ThreadTS:  ev.ThreadTS,
		TriggerTS: ev.TriggerTS,
		UserID:    ev.UserID,
		Phase:     "d_selector",
	}
	step := NextStep{
		Kind:           NextStepPostSelector,
		SelectorPrompt: prompt,
		SelectorActions: []SelectorAction{
			{ActionID: "d_selector", Label: "📝 建 Issue", Value: "issue"},
			{ActionID: "d_selector", Label: "❓ 問問題", Value: "ask"},
			{ActionID: "d_selector", Label: "🔍 Review PR", Value: "pr_review"},
		},
		Pending: pending,
	}
	return pending, step, nil
}

// HandleSelection routes a button-click or modal-submit to the owning workflow.
// For D-selector clicks, synthesises a fresh TriggerEvent and re-enters
// the workflow's Trigger.
func (d *Dispatcher) HandleSelection(ctx context.Context, p *Pending, value string) (NextStep, error) {
	if p.Phase == "d_selector" {
		// Value is one of the registered task types. Treat like a synthetic
		// @bot <verb> with no args.
		wf, ok := d.registry.Get(value)
		if !ok {
			return NextStep{Kind: NextStepError, ErrorText: "unknown workflow: " + value}, nil
		}
		ev := TriggerEvent{ChannelID: p.ChannelID, ThreadTS: p.ThreadTS, TriggerTS: p.TriggerTS, UserID: p.UserID}
		step, err := wf.Trigger(ctx, ev, "")
		if err != nil {
			return NextStep{Kind: NextStepError, ErrorText: err.Error()}, err
		}
		if step.Pending != nil {
			step.Pending.TaskType = wf.Type()
		}
		return step, nil
	}

	wf, ok := d.registry.Get(p.TaskType)
	if !ok {
		return NextStep{Kind: NextStepError, ErrorText: "unknown task_type: " + p.TaskType}, nil
	}
	return wf.Selection(ctx, p, value)
}
