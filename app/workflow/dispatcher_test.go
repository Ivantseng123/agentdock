package workflow

import "testing"

func TestParseTrigger(t *testing.T) {
	tests := []struct {
		name      string
		text      string
		wantVerb  string
		wantArgs  string
		wantKnown bool
	}{
		{"empty", "", "", "", false},
		{"verb only ask", "ask", "ask", "", true},
		{"verb + args", "ask what does X do?", "ask", "what does X do?", true},
		{"case-insensitive ASK", "ASK question", "ask", "question", true},
		{"case-insensitive Ask", "Ask Q", "ask", "Q", true},
		{"review with url", "review https://github.com/foo/bar/pull/123", "review", "https://github.com/foo/bar/pull/123", true},
		{"review with slack-wrapped url", "review <https://github.com/foo/bar/pull/123>", "review", "https://github.com/foo/bar/pull/123", true},
		{"issue verb", "issue foo/bar", "issue", "foo/bar", true},
		{"no verb repo-shaped", "foo/bar", "", "foo/bar", false},
		{"no verb repo@branch", "foo/bar@dev", "", "foo/bar@dev", false},
		{"unknown verb treated as unknown", "askme please", "askme", "please", false},
		{"slack mention tag stripped", "<@U123> ask Q", "ask", "Q", true},
		{"trailing whitespace", "ask Q  ", "ask", "Q", true},
		{"nested mentions", "<@U1> <@U2> ask Q", "ask", "Q", true},
		{"channel broadcast then mention", "<!channel> <@U123> ask Q", "ask", "Q", true},
		{"here broadcast", "<!here> review <URL>", "review", "<URL>", true},
		{"mention only, no verb", "<@U123>", "", "", false},
		{"triage alone", "/triage", "", "", false},
		{"triage with trailing space", "/triage ", "", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseTrigger(tc.text)
			if got.Verb != tc.wantVerb {
				t.Errorf("Verb = %q, want %q", got.Verb, tc.wantVerb)
			}
			if got.Args != tc.wantArgs {
				t.Errorf("Args = %q, want %q", got.Args, tc.wantArgs)
			}
			if got.KnownVerb != tc.wantKnown {
				t.Errorf("KnownVerb = %v, want %v", got.KnownVerb, tc.wantKnown)
			}
		})
	}
}

func TestLooksLikeRepo(t *testing.T) {
	cases := map[string]bool{
		"":             false,
		"foo/bar":      true,
		"foo/bar@main": true,
		"foo":          false,
		"foo bar":      false,
		"https://x/y":  false, // multi-slash (colon in scheme also disqualifies)
		"a/b/c":        false, // more than one "/"
		"foo/bar@":     false, // empty branch
		"foo/":         false, // trailing slash
		"/foo":         false, // leading slash
	}
	for in, want := range cases {
		t.Run(in, func(t *testing.T) {
			if got := LooksLikeRepo(in); got != want {
				t.Errorf("LooksLikeRepo(%q) = %v, want %v", in, got, want)
			}
		})
	}
}
