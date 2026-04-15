package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitNonInteractive_YAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := runInit(path, false, false); err != nil {
		t.Fatalf("runInit: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if !strings.Contains(content, "# REQUIRED") {
		t.Error("YAML output should contain # REQUIRED comments")
	}
	if !strings.Contains(content, "claude:") {
		t.Error("YAML output should contain claude agent block")
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Errorf("file mode = %o, want 0600", info.Mode().Perm())
	}
}

func TestInitNonInteractive_JSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := runInit(path, false, false); err != nil {
		t.Fatalf("runInit: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	if strings.Contains(string(data), "# REQUIRED") {
		t.Error("JSON output should NOT contain comments")
	}
}

func TestInitNonInteractive_RejectsExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("existing"), 0600); err != nil {
		t.Fatal(err)
	}
	err := runInit(path, false, false)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("expected 'already exists' error, got %v", err)
	}
}

func TestInitNonInteractive_ForceOverwrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	os.WriteFile(path, []byte("existing"), 0600)
	if err := runInit(path, false, true); err != nil {
		t.Fatalf("runInit force: %v", err)
	}
	data, _ := os.ReadFile(path)
	if string(data) == "existing" {
		t.Error("existing content should have been overwritten")
	}
}

func TestAtomicWrite_RemovesStaleTmp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	tmp := path + ".tmp"

	// Simulate stale .tmp from a previous failed write with lax permissions.
	if err := os.WriteFile(tmp, []byte("stale"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := atomicWrite(path, []byte("fresh"), 0600); err != nil {
		t.Fatalf("atomicWrite: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("file mode = %o, want 0600", info.Mode().Perm())
	}

	// .tmp should not linger.
	if _, err := os.Stat(tmp); err == nil {
		t.Error(".tmp file should not exist after successful atomicWrite")
	}
}
