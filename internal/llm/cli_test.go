package llm

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestCLIProvider_Name(t *testing.T) {
	p := NewCLIProvider("test-cli", "echo", nil, 10*time.Second)
	if p.Name() != "test-cli" {
		t.Errorf("expected name test-cli, got %s", p.Name())
	}
}

func TestCLIProvider_Diagnose_ParsesJSON(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skip on windows")
	}

	// Create a fake CLI script that outputs valid JSON
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-ai")
	err := os.WriteFile(script, []byte(`#!/bin/sh
echo '{"summary":"found the bug","files":[{"path":"main.go","line_number":10,"description":"here"}],"suggestions":["fix it"]}'
`), 0755)
	if err != nil {
		t.Fatal(err)
	}

	p := NewCLIProvider("fake", script, nil, 10*time.Second)
	resp, err := p.Diagnose(context.Background(), DiagnoseRequest{
		Type:    "bug",
		Message: "something broke",
	})
	if err != nil {
		t.Fatalf("Diagnose failed: %v", err)
	}
	if resp.Summary != "found the bug" {
		t.Errorf("expected 'found the bug', got %q", resp.Summary)
	}
	if len(resp.Files) != 1 || resp.Files[0].Path != "main.go" {
		t.Errorf("unexpected files: %+v", resp.Files)
	}
	if len(resp.Suggestions) != 1 || resp.Suggestions[0] != "fix it" {
		t.Errorf("unexpected suggestions: %v", resp.Suggestions)
	}
}

func TestCLIProvider_Diagnose_FallbackRawText(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skip on windows")
	}

	dir := t.TempDir()
	script := filepath.Join(dir, "plain-ai")
	err := os.WriteFile(script, []byte(`#!/bin/sh
echo "The bug is likely in the auth module."
`), 0755)
	if err != nil {
		t.Fatal(err)
	}

	p := NewCLIProvider("plain", script, nil, 10*time.Second)
	resp, err := p.Diagnose(context.Background(), DiagnoseRequest{
		Type:    "bug",
		Message: "login broken",
	})
	if err != nil {
		t.Fatalf("Diagnose failed: %v", err)
	}
	// Non-JSON output should be returned as raw summary
	if resp.Summary == "" {
		t.Error("expected non-empty summary from raw text fallback")
	}
}

func TestCLIProvider_Diagnose_CommandNotFound(t *testing.T) {
	p := NewCLIProvider("missing", "nonexistent-cli-tool-xyz", nil, 10*time.Second)
	_, err := p.Diagnose(context.Background(), DiagnoseRequest{
		Type:    "bug",
		Message: "test",
	})
	if err == nil {
		t.Error("expected error for missing command")
	}
}

func TestCLIProvider_Diagnose_Timeout(t *testing.T) {
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("sleep not found")
	}

	p := NewCLIProvider("slow", "sleep", []string{"10"}, 100*time.Millisecond)
	_, err := p.Diagnose(context.Background(), DiagnoseRequest{
		Type:    "bug",
		Message: "test",
	})
	if err == nil {
		t.Error("expected timeout error")
	}
}
