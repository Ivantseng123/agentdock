package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDetectVersion_Success(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-cli")
	os.WriteFile(script, []byte(`#!/bin/sh
echo "fake-cli 1.2.3"
echo "build: abcdef"
`), 0755)

	got, err := detectVersion(context.Background(), script)
	if err != nil {
		t.Fatalf("detectVersion failed: %v", err)
	}
	if got != "fake-cli 1.2.3" {
		t.Errorf("got %q, want %q (first line only)", got, "fake-cli 1.2.3")
	}
}

func TestDetectVersion_NotFound(t *testing.T) {
	_, err := detectVersion(context.Background(), "/nonexistent/cli-binary")
	if err == nil {
		t.Fatal("expected error for missing binary")
	}
	if !strings.Contains(err.Error(), "exec") {
		t.Errorf("err should mention exec, got: %v", err)
	}
}

func TestDetectVersion_NonZeroExit(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "broken-cli")
	os.WriteFile(script, []byte("#!/bin/sh\necho oops\nexit 2\n"), 0755)

	_, err := detectVersion(context.Background(), script)
	if err == nil {
		t.Fatal("expected error for non-zero exit")
	}
}

func TestDetectVersion_EmptyOutput(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "silent-cli")
	os.WriteFile(script, []byte("#!/bin/sh\nexit 0\n"), 0755)

	_, err := detectVersion(context.Background(), script)
	if err == nil {
		t.Fatal("expected error for empty output")
	}
	if !strings.Contains(err.Error(), "empty output") {
		t.Errorf("err should mention empty output, got: %v", err)
	}
}

func TestDetectVersion_RespectsTimeout(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "slow-cli")
	os.WriteFile(script, []byte("#!/bin/sh\nsleep 10\necho slow\n"), 0755)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := detectVersion(ctx, script)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error from timeout")
	}
	// Caller-supplied ctx (200ms) is shorter than versionDetectTimeout (5s) —
	// the caller's deadline must dominate.
	if elapsed > 2*time.Second {
		t.Errorf("detectVersion did not respect caller ctx: elapsed=%v", elapsed)
	}
}
