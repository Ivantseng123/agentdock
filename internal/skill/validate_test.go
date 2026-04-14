package skill

import (
	"strings"
	"testing"

	"agentdock/internal/queue"
)

func TestValidateSkillFiles_Valid(t *testing.T) {
	files := map[string][]byte{
		"SKILL.md":             []byte("# My Skill"),
		"examples/example1.md": []byte("example"),
		"references/spec.yaml": []byte("key: value"),
		"config.json":          []byte("{}"),
		"template.tmpl":        []byte("{{.Name}}"),
		"notes.txt":            []byte("notes"),
		"data.yml":             []byte("a: 1"),
		"sample.example":       []byte("sample"),
	}
	if err := ValidateSkillFiles(files); err != nil {
		t.Errorf("expected valid, got: %v", err)
	}
}

func TestValidateSkillFiles_BadExtension(t *testing.T) {
	files := map[string][]byte{
		"SKILL.md": []byte("# My Skill"),
		"hack.sh":  []byte("#!/bin/bash\nrm -rf /"),
	}
	err := ValidateSkillFiles(files)
	if err == nil {
		t.Fatal("expected error for .sh file")
	}
	if !strings.Contains(err.Error(), "hack.sh") {
		t.Errorf("error should mention filename: %v", err)
	}
}

func TestValidateSkillFiles_PathTraversal(t *testing.T) {
	files := map[string][]byte{
		"SKILL.md":      []byte("ok"),
		"../etc/passwd": []byte("bad"),
	}
	err := ValidateSkillFiles(files)
	if err == nil {
		t.Fatal("expected error for path traversal")
	}
}

func TestValidateSkillFiles_AbsolutePath(t *testing.T) {
	files := map[string][]byte{
		"/etc/shadow": []byte("bad"),
	}
	err := ValidateSkillFiles(files)
	if err == nil {
		t.Fatal("expected error for absolute path")
	}
}

func TestValidateSkillFiles_TooLarge(t *testing.T) {
	files := map[string][]byte{
		"SKILL.md": make([]byte, 1*1024*1024+1),
	}
	err := ValidateSkillFiles(files)
	if err == nil {
		t.Fatal("expected error for oversized skill")
	}
}

func TestValidateJobSize_Under5MB(t *testing.T) {
	skills := map[string]*queue.SkillPayload{
		"a": {Files: map[string][]byte{"SKILL.md": make([]byte, 1*1024*1024)}},
		"b": {Files: map[string][]byte{"SKILL.md": make([]byte, 1*1024*1024)}},
	}
	if err := ValidateJobSize(skills); err != nil {
		t.Errorf("expected valid, got: %v", err)
	}
}

func TestValidateJobSize_Over5MB(t *testing.T) {
	skills := map[string]*queue.SkillPayload{
		"a": {Files: map[string][]byte{"SKILL.md": make([]byte, 3*1024*1024)}},
		"b": {Files: map[string][]byte{"SKILL.md": make([]byte, 3*1024*1024)}},
	}
	err := ValidateJobSize(skills)
	if err == nil {
		t.Fatal("expected error for oversized job")
	}
}
