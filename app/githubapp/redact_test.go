package githubapp

import (
	"strings"
	"testing"
)

func TestRedactGitHubBody(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string // substring that must NOT appear after redaction
	}{
		{"installation token", `{"token":"ghs_abcdefghij1234567890"}`, "ghs_abcdefghij1234567890"},
		{"classic PAT", `seen ghp_xyz0987654321zyxwvu in body`, "ghp_xyz0987654321zyxwvu"},
		{"fine-grained PAT", `prefix github_pat_11ABCDEFG_aaaa1234567890 suffix`, "github_pat_11ABCDEFG_aaaa1234567890"},
		{"bearer header", `Authorization: Bearer eyJhbGciOiJSUzI1NiJ9.eyJpc3MiOjF9.sig`, "Bearer eyJhbGciOiJSUzI1NiJ9"},
		{"oauth token", `gho_abcdefghij1234567890`, "gho_abcdefghij1234567890"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := redactGitHubBody(tc.in)
			if strings.Contains(got, tc.want) {
				t.Errorf("redactGitHubBody(%q) = %q, must not contain %q", tc.in, got, tc.want)
			}
			if !strings.Contains(got, "[REDACTED]") {
				t.Errorf("redactGitHubBody(%q) = %q, expected [REDACTED] marker", tc.in, got)
			}
		})
	}
}

func TestRedactGitHubBody_LeavesUnrelatedAlone(t *testing.T) {
	in := `{"message":"Resource protected by org SAML enforcement"}`
	got := redactGitHubBody(in)
	if got != in {
		t.Errorf("redactGitHubBody changed safe input: %q → %q", in, got)
	}
}

func TestRedactGitHubBody_ShortBearerValueLeftAlone(t *testing.T) {
	// 10-char minimum guards against eating "Bearer x" in error
	// messages that aren't actually credentials.
	in := `Bearer x`
	got := redactGitHubBody(in)
	if got != in {
		t.Errorf("redactGitHubBody redacted too-short bearer value: %q → %q", in, got)
	}
}
