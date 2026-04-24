package connectivity

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// minGitMajor / minGitMinor is the oldest git release that honours the
// GIT_CONFIG_COUNT / GIT_CONFIG_KEY_N / GIT_CONFIG_VALUE_N env-var triple used
// by shared/github/repo.go to inject HTTP auth without persisting the PAT in
// .git/config (#179). Older git silently drops those vars, so private-repo
// fetch/clone fails with HTTP 401 and no useful pointer back at the cause.
const (
	minGitMajor = 2
	minGitMinor = 31 // git 2.31 shipped GIT_CONFIG_COUNT in March 2021
)

// CheckGitVersion verifies the local git binary is recent enough for the
// env-based auth mechanism. Called from worker preflight so operators running
// on a stale host get a clear error instead of a silent auth failure later.
func CheckGitVersion() error {
	out, err := exec.Command("git", "--version").Output()
	if err != nil {
		return fmt.Errorf("git binary not found or unreadable: %w", err)
	}
	raw := strings.TrimSpace(string(out))
	major, minor, err := parseGitVersion(raw)
	if err != nil {
		return err
	}
	if major < minGitMajor || (major == minGitMajor && minor < minGitMinor) {
		return fmt.Errorf("git %d.%d is too old; need ≥ %d.%d for env-based auth (see #179)",
			major, minor, minGitMajor, minGitMinor)
	}
	return nil
}

// parseGitVersion pulls the major / minor from a `git --version` output line,
// which looks like "git version 2.50.1" or "git version 2.50.1 (Apple Git-155)".
// Only major and minor are consumed; patch and vendor suffix are ignored.
func parseGitVersion(raw string) (int, int, error) {
	fields := strings.Fields(raw)
	if len(fields) < 3 {
		return 0, 0, fmt.Errorf("unrecognised git --version output: %q", raw)
	}
	parts := strings.SplitN(fields[2], ".", 3)
	if len(parts) < 2 {
		return 0, 0, fmt.Errorf("unrecognised git version number: %q", fields[2])
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, fmt.Errorf("parse git major version %q: %w", parts[0], err)
	}
	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, 0, fmt.Errorf("parse git minor version %q: %w", parts[1], err)
	}
	return major, minor, nil
}
