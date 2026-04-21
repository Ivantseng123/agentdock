package workflow

import (
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
