// Package prompt provides small helpers for interactive CLI prompts: coloured
// status lines (OK / FAIL / WARN), line and hidden-input readers, a yes/no
// prompt, and a CLI-version probe. Both app/config/preflight and
// worker/config/preflight import this package so they don't have to duplicate
// the stderr plumbing.
package prompt

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"golang.org/x/term"
)

// Stderr is the writer status lines and prompts go to. Tests may replace it.
var Stderr io.Writer = os.Stderr

// Stdin is the input source for prompts. Tests may replace it to drive
// scripted interactions.
var Stdin io.Reader = os.Stdin

// IsTerminal reports whether stdin is a terminal. Indirected through a
// package-level var so tests can override it.
var IsTerminal = func() bool {
	return term.IsTerminal(int(syscall.Stdin))
}

// OK prints a green checkmark line.
func OK(format string, args ...any) {
	fmt.Fprintf(Stderr, "  \033[32m✓\033[0m %s\n", fmt.Sprintf(format, args...))
}

// Fail prints a red cross line.
func Fail(format string, args ...any) {
	fmt.Fprintf(Stderr, "  \033[31m✗\033[0m %s\n", fmt.Sprintf(format, args...))
}

// Warn prints a yellow warning line.
func Warn(format string, args ...any) {
	fmt.Fprintf(Stderr, "  \033[33m⚠\033[0m %s\n", fmt.Sprintf(format, args...))
}

// Line prints a prompt and reads a single trimmed line of input.
func Line(prompt string) string {
	fmt.Fprintf(Stderr, "  %s", prompt)
	sc := bufio.NewScanner(Stdin)
	if sc.Scan() {
		return strings.TrimSpace(sc.Text())
	}
	return ""
}

// Hidden prints a prompt and reads input without echo (for secrets).
func Hidden(prompt string) string {
	fmt.Fprintf(Stderr, "  %s", prompt)
	b, err := term.ReadPassword(int(syscall.Stdin))
	fmt.Fprintln(Stderr)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// YesNoDefault prints a yes/no prompt with an explicit default. Pressing
// Enter returns defaultYes. The [Y/n] / [y/N] suffix reflects the default.
func YesNoDefault(prompt string, defaultYes bool) bool {
	suffix := "[y/N]"
	if defaultYes {
		suffix = "[Y/n]"
	}
	answer := Line(fmt.Sprintf("%s %s: ", prompt, suffix))
	if answer == "" {
		return defaultYes
	}
	lower := strings.ToLower(answer)
	return lower == "y" || lower == "yes"
}

// YesNo prints a yes/no prompt with a "yes" default.
func YesNo(prompt string) bool {
	return YesNoDefault(prompt, true)
}

// CheckAgentCLI runs `<command> --version` and returns the first line of
// stdout+stderr. Returns a typed *exec.Error when the binary is missing so
// callers can distinguish "not installed" from "bad exit code" (many CLIs
// exit non-zero on --version and that is OK).
func CheckAgentCLI(command string) (string, error) {
	cmd := exec.Command(command, "--version")
	out, err := cmd.CombinedOutput()
	if err != nil {
		var execErr *exec.Error
		if errors.As(err, &execErr) {
			return "", execErr
		}
	}
	sc := bufio.NewScanner(bytes.NewReader(out))
	if sc.Scan() {
		return strings.TrimSpace(sc.Text()), nil
	}
	return "", nil
}
