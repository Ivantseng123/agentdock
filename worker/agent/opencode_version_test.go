package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMinimumOpencodeVersionParses(t *testing.T) {
	if _, err := parseOpencodeSemver(MinimumOpencodeVersion); err != nil {
		t.Fatalf("MinimumOpencodeVersion %q failed to parse: %v", MinimumOpencodeVersion, err)
	}
}

func TestParseOpencodeSemver(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    [3]int
		wantErr bool
	}{
		{"floor", "1.14.41", [3]int{1, 14, 41}, false},
		{"zeros", "0.0.0", [3]int{0, 0, 0}, false},
		{"two-digit-major", "10.20.30", [3]int{10, 20, 30}, false},
		{"trim-whitespace", "  1.14.41\n", [3]int{1, 14, 41}, false},
		{"two-segments", "1.14", [3]int{}, true},
		{"four-segments", "1.14.41.0", [3]int{}, true},
		{"non-numeric", "1.14.x", [3]int{}, true},
		{"empty", "", [3]int{}, true},
		{"banner", "claude 1.14.41", [3]int{}, true},
		{"rc-suffix", "1.14.41-rc.1", [3]int{}, true},
		{"build-suffix", "1.14.41+build.7", [3]int{}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseOpencodeSemver(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseOpencodeSemver(%q) = %v, want error", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseOpencodeSemver(%q) unexpected error: %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("parseOpencodeSemver(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestCompareToMinimumOpencodeVersion(t *testing.T) {
	cases := []struct {
		name     string
		detected string
		wantErr  bool
	}{
		{"equal-to-floor", MinimumOpencodeVersion, false},
		{"patch-above", "1.14.42", false},
		{"minor-above", "1.15.0", false},
		{"major-above", "2.0.0", false},
		{"patch-below", "1.14.40", true},
		{"minor-below", "1.13.99", true},
		{"major-below", "0.99.99", true},
		{"malformed", "not-a-version", true},
		{"two-segments", "1.14", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := compareToMinimumOpencodeVersion(tc.detected)
			if tc.wantErr && err == nil {
				t.Errorf("compareToMinimumOpencodeVersion(%q) = nil, want error", tc.detected)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("compareToMinimumOpencodeVersion(%q) = %v, want nil", tc.detected, err)
			}
		})
	}
}

func TestCompareToMinimumOpencodeVersion_BelowFloorErrorMessage(t *testing.T) {
	err := compareToMinimumOpencodeVersion("1.14.40")
	if err == nil {
		t.Fatal("expected error for below-floor version")
	}
	msg := err.Error()
	if !strings.Contains(msg, "1.14.40") {
		t.Errorf("error %q missing detected version", msg)
	}
	if !strings.Contains(msg, MinimumOpencodeVersion) {
		t.Errorf("error %q missing required version", msg)
	}
	if !strings.Contains(msg, "opencode-server-mode-poc-report.md") {
		t.Errorf("error %q missing pointer to POC report", msg)
	}
}

func TestCheckOpencodeVersion_BinaryMissing(t *testing.T) {
	_, err := CheckOpencodeVersion(context.Background(), "/nonexistent/binary/path/opencode-does-not-exist")
	if err == nil {
		t.Fatal("expected error for missing binary, got nil")
	}
	if !strings.Contains(err.Error(), "probe") {
		t.Errorf("error %q does not look like a probe failure: missing 'probe' marker", err.Error())
	}
}

func TestCheckOpencodeVersion_FakeBinaryHappyPath(t *testing.T) {
	dir := t.TempDir()
	binPath := filepath.Join(dir, "opencode")
	script := "#!/bin/sh\necho " + MinimumOpencodeVersion + "\n"
	if err := os.WriteFile(binPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake binary: %v", err)
	}
	detected, err := CheckOpencodeVersion(context.Background(), binPath)
	if err != nil {
		t.Fatalf("CheckOpencodeVersion(floor) unexpected error: %v", err)
	}
	if detected != MinimumOpencodeVersion {
		t.Errorf("detected = %q, want %q", detected, MinimumOpencodeVersion)
	}
}

func TestCheckOpencodeVersion_FakeBinaryBelowFloor(t *testing.T) {
	dir := t.TempDir()
	binPath := filepath.Join(dir, "opencode")
	script := "#!/bin/sh\necho 1.14.40\n"
	if err := os.WriteFile(binPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake binary: %v", err)
	}
	detected, err := CheckOpencodeVersion(context.Background(), binPath)
	if err == nil {
		t.Fatalf("expected below-floor error, got nil (detected=%q)", detected)
	}
	if detected != "1.14.40" {
		t.Errorf("detected = %q, want %q", detected, "1.14.40")
	}
	if !strings.Contains(err.Error(), "低於 server-mode 最低版本") {
		t.Errorf("error %q missing Chinese below-floor marker", err.Error())
	}
}

func TestCheckOpencodeVersion_FakeBinaryMalformed(t *testing.T) {
	dir := t.TempDir()
	binPath := filepath.Join(dir, "opencode")
	script := "#!/bin/sh\necho not-a-semver\n"
	if err := os.WriteFile(binPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake binary: %v", err)
	}
	_, err := CheckOpencodeVersion(context.Background(), binPath)
	if err == nil {
		t.Fatal("expected malformed-output error, got nil")
	}
	if !strings.Contains(err.Error(), "opencode 版本格式無法解析") {
		t.Errorf("error %q does not surface Chinese parse-failure message", err.Error())
	}
}
