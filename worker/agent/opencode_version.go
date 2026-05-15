// Package agent — opencode_version.go is the opencode-server-mode
// capability check. It has a deliberately different policy from
// version.go's LogVersions:
//
//   - LogVersions is warn-and-continue for every agent at boot. Some
//     CLIs predate the --version convention, so a probe failure must
//     NOT crash the worker (see version.go doc comment).
//
//   - CheckOpencodeVersion is blocking, opencode-only, server-mode-only.
//     Below floor or probe failure → caller exits non-zero. Server mode
//     wires HTTP/SSE contracts whose semantics depend on the
//     POC-validated baseline (1.14.41); running on an older binary
//     would silently degrade ask answers.
//
// The parser handles opencode's bare-semver `--version` output
// (e.g. `1.14.41\n`) which differs from claude / codex which print a
// `<name> <version>` banner. version.go's detectVersion returns the
// trimmed first line, so for opencode that line is itself the version.
// Three-segment numeric compare; no `golang.org/x/mod/semver` (spec N4).
package agent

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// MinimumOpencodeVersion is the lower-bound floor for opencode in
// `cfg.Opencode.Mode == "server"`. Backed by the POC report on main
// (docs/specs/opencode-server-mode-poc-report.md, PR #258). Bumping
// requires re-running POC `-all -replay-count=100` against the
// candidate build and publishing a refreshed report; see spec
// § Bumping the floor.
const MinimumOpencodeVersion = "1.14.41"

// CheckOpencodeVersion probes `binaryPath --version` (via the same
// detectVersion used by LogVersions) and compares the result against
// MinimumOpencodeVersion. Returns the detected version on success.
//
// In server mode, callers must treat any error as a boot-blocker. Probe
// failure (binary missing / non-zero exit / malformed output) AND
// below-floor mismatch are both surfaced as errors here — stricter than
// LogVersions's warn-only policy by design. Spawn mode must not call
// this; LogVersions's warn-and-continue path is sufficient there.
func CheckOpencodeVersion(ctx context.Context, binaryPath string) (string, error) {
	detected, err := detectVersion(ctx, binaryPath)
	if err != nil {
		return "", fmt.Errorf("probe %s --version: %w", binaryPath, err)
	}
	if err := compareToMinimumOpencodeVersion(detected); err != nil {
		return detected, err
	}
	return detected, nil
}

func compareToMinimumOpencodeVersion(detected string) error {
	d, err := parseOpencodeSemver(detected)
	if err != nil {
		return fmt.Errorf("opencode 版本格式無法解析 (detected=%q, required=%q, 見 docs/specs/opencode-server-mode-poc-report.md): %w",
			detected, MinimumOpencodeVersion, err)
	}
	m, err := parseOpencodeSemver(MinimumOpencodeVersion)
	if err != nil {
		// Unreachable in production — the constant is checked at parse
		// time in TestMinimumOpencodeVersionParses. Surface as a plain
		// error so a future typo in the constant fails the unit test.
		return fmt.Errorf("MinimumOpencodeVersion 常數格式錯誤: %w", err)
	}
	for i := 0; i < 3; i++ {
		if d[i] > m[i] {
			return nil
		}
		if d[i] < m[i] {
			return fmt.Errorf("opencode %s 低於 server-mode 最低版本 %s (見 docs/specs/opencode-server-mode-poc-report.md)",
				detected, MinimumOpencodeVersion)
		}
	}
	return nil
}

func parseOpencodeSemver(s string) ([3]int, error) {
	var out [3]int
	s = strings.TrimSpace(s)
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return out, fmt.Errorf("expected MAJOR.MINOR.PATCH, got %q", s)
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return out, fmt.Errorf("non-numeric segment %q in %q", p, s)
		}
		if n < 0 {
			return out, fmt.Errorf("negative segment %d in %q", n, s)
		}
		out[i] = n
	}
	return out, nil
}
