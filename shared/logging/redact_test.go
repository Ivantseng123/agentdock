package logging

import (
	"strings"
	"testing"
)

func TestRedact_EmptySecrets(t *testing.T) {
	text := "output contains no secrets"
	got := Redact(text, nil)
	if got != text {
		t.Errorf("empty secrets: got %q, want %q", got, text)
	}
	got = Redact(text, map[string]string{})
	if got != text {
		t.Errorf("empty map: got %q, want %q", got, text)
	}
}

func TestRedact_Normal(t *testing.T) {
	secrets := map[string]string{
		"GH_TOKEN": "ghp_supersecrettoken123",
	}
	text := "agent output: token=ghp_supersecrettoken123 done"
	got := Redact(text, secrets)
	want := "agent output: token=*** done"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRedact_ShortValueSkipped(t *testing.T) {
	// Values shorter than minRedactLength must NOT be redacted.
	secrets := map[string]string{
		"FLAG": "abc", // only 3 bytes
	}
	text := "flag=abc result=ok"
	got := Redact(text, secrets)
	if got != text {
		t.Errorf("short value should not be redacted: got %q, want %q", got, text)
	}
}

func TestRedact_MultiSecret(t *testing.T) {
	secrets := map[string]string{
		"GH_TOKEN":    "ghp_supersecrettoken123",
		"MANTIS_TOKEN": "mantis-super-secret",
	}
	text := "tok=ghp_supersecrettoken123 mantis=mantis-super-secret end"
	got := Redact(text, secrets)
	if strings.Contains(got, "ghp_supersecrettoken123") {
		t.Errorf("GH_TOKEN not redacted in %q", got)
	}
	if strings.Contains(got, "mantis-super-secret") {
		t.Errorf("MANTIS_TOKEN not redacted in %q", got)
	}
}

func TestRedact_NoSecretInText(t *testing.T) {
	// Regression: text without any secret must be byte-for-byte identical.
	secrets := map[string]string{
		"GH_TOKEN": "ghp_supersecrettoken123",
	}
	text := "totally normal log line without any secret"
	got := Redact(text, secrets)
	if got != text {
		t.Errorf("unmodified text changed: got %q, want %q", got, text)
	}
}
