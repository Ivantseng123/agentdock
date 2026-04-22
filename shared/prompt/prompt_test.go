package prompt

import (
	"bytes"
	"strings"
	"testing"
)

func TestOK_PrintsGreenCheckmark(t *testing.T) {
	var buf bytes.Buffer
	origStderr := Stderr
	Stderr = &buf
	defer func() { Stderr = origStderr }()

	OK("hello %s", "world")

	out := buf.String()
	if !strings.Contains(out, "hello world") {
		t.Errorf("output missing message: %q", out)
	}
	if !strings.Contains(out, "\033[32m") {
		t.Errorf("output missing green color: %q", out)
	}
}

func TestFail_PrintsRedCross(t *testing.T) {
	var buf bytes.Buffer
	origStderr := Stderr
	Stderr = &buf
	defer func() { Stderr = origStderr }()

	Fail("bad: %d", 42)

	out := buf.String()
	if !strings.Contains(out, "bad: 42") {
		t.Errorf("output missing message: %q", out)
	}
	if !strings.Contains(out, "\033[31m") {
		t.Errorf("output missing red color: %q", out)
	}
}

func TestCheckAgentCLI_MissingBinaryReturnsError(t *testing.T) {
	_, err := CheckAgentCLI("definitely-not-a-real-binary-12345")
	if err == nil {
		t.Error("expected error for missing binary")
	}
}

func TestYesNoDefault_EmptyInputUsesDefault(t *testing.T) {
	origStdin := Stdin
	defer func() { Stdin = origStdin }()

	cases := []struct {
		name       string
		input      string
		defaultYes bool
		want       bool
	}{
		{"empty_default_yes", "\n", true, true},
		{"empty_default_no", "\n", false, false},
		{"y_default_no", "y\n", false, true},
		{"n_default_yes", "n\n", true, false},
		{"yes_default_no", "yes\n", false, true},
		{"capital_Y_default_no", "Y\n", false, true},
		{"whitespace_empty_default_no", "   \n", false, false},
	}

	var buf bytes.Buffer
	origStderr := Stderr
	Stderr = &buf
	defer func() { Stderr = origStderr }()

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			Stdin = strings.NewReader(tc.input)
			got := YesNoDefault("prompt", tc.defaultYes)
			if got != tc.want {
				t.Errorf("YesNoDefault(%q, default=%v) = %v, want %v", tc.input, tc.defaultYes, got, tc.want)
			}
		})
	}
}

func TestYesNoDefault_SuffixMatchesDefault(t *testing.T) {
	origStdin := Stdin
	Stdin = strings.NewReader("\n")
	defer func() { Stdin = origStdin }()

	var buf bytes.Buffer
	origStderr := Stderr
	Stderr = &buf
	defer func() { Stderr = origStderr }()

	YesNoDefault("enable feature?", false)
	out := buf.String()
	if !strings.Contains(out, "[y/N]") {
		t.Errorf("default=false prompt should show [y/N], got: %q", out)
	}

	buf.Reset()
	Stdin = strings.NewReader("\n")
	YesNoDefault("enable feature?", true)
	out = buf.String()
	if !strings.Contains(out, "[Y/n]") {
		t.Errorf("default=true prompt should show [Y/n], got: %q", out)
	}
}
