package skill

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFixture(t *testing.T, dir, relPath, content string) {
	t.Helper()
	full := filepath.Join(dir, relPath)
	if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestScanPackageSkills_SingleSkill(t *testing.T) {
	tmpDir := t.TempDir()
	writeFixture(t, tmpDir, "skills/code-review/SKILL.md", "# Code Review")
	writeFixture(t, tmpDir, "skills/code-review/examples/ex1.md", "example 1")
	writeFixture(t, tmpDir, "package.json", `{"name":"@team/review"}`)

	skills, err := scanPackageSkills(tmpDir)
	if err != nil {
		t.Fatalf("scanPackageSkills: %v", err)
	}
	if len(skills) != 1 {
		t.Fatalf("got %d skills, want 1", len(skills))
	}
	if skills[0].Name != "code-review" {
		t.Errorf("name = %q", skills[0].Name)
	}
	if string(skills[0].Files["SKILL.md"]) != "# Code Review" {
		t.Errorf("SKILL.md content = %q", string(skills[0].Files["SKILL.md"]))
	}
	if string(skills[0].Files["examples/ex1.md"]) != "example 1" {
		t.Errorf("examples/ex1.md = %q", string(skills[0].Files["examples/ex1.md"]))
	}
}

func TestScanPackageSkills_MultipleSkills(t *testing.T) {
	tmpDir := t.TempDir()
	writeFixture(t, tmpDir, "skills/skill-a/SKILL.md", "# Skill A")
	writeFixture(t, tmpDir, "skills/skill-b/SKILL.md", "# Skill B")
	writeFixture(t, tmpDir, "skills/skill-b/refs/api.yaml", "openapi: 3.0")
	writeFixture(t, tmpDir, "package.json", `{"name":"@team/multi"}`)

	skills, err := scanPackageSkills(tmpDir)
	if err != nil {
		t.Fatalf("scanPackageSkills: %v", err)
	}
	if len(skills) != 2 {
		t.Fatalf("got %d skills, want 2", len(skills))
	}
	names := map[string]bool{}
	for _, s := range skills {
		names[s.Name] = true
	}
	if !names["skill-a"] || !names["skill-b"] {
		t.Errorf("skill names = %v", names)
	}
}

func TestScanPackageSkills_SkipsWithoutSkillMD(t *testing.T) {
	tmpDir := t.TempDir()
	writeFixture(t, tmpDir, "skills/has-skill/SKILL.md", "# Valid")
	writeFixture(t, tmpDir, "skills/no-skill/README.md", "# Not a skill")
	writeFixture(t, tmpDir, "package.json", `{}`)

	skills, err := scanPackageSkills(tmpDir)
	if err != nil {
		t.Fatalf("scanPackageSkills: %v", err)
	}
	if len(skills) != 1 {
		t.Fatalf("got %d skills, want 1", len(skills))
	}
	if skills[0].Name != "has-skill" {
		t.Errorf("name = %q", skills[0].Name)
	}
}

func TestScanPackageSkills_NoSkillsDir(t *testing.T) {
	tmpDir := t.TempDir()
	writeFixture(t, tmpDir, "package.json", `{}`)

	_, err := scanPackageSkills(tmpDir)
	if err == nil {
		t.Fatal("expected error when skills/ directory is missing")
	}
}

func TestScanPackageSkills_SkipsBadExtension(t *testing.T) {
	tmpDir := t.TempDir()
	writeFixture(t, tmpDir, "skills/my-skill/SKILL.md", "# Valid")
	writeFixture(t, tmpDir, "skills/my-skill/hack.sh", "#!/bin/bash")
	writeFixture(t, tmpDir, "package.json", `{}`)

	skills, err := scanPackageSkills(tmpDir)
	if err != nil {
		t.Fatalf("scanPackageSkills: %v", err)
	}
	if len(skills) != 1 {
		t.Fatalf("got %d skills", len(skills))
	}
	if _, ok := skills[0].Files["hack.sh"]; !ok {
		t.Error("expected hack.sh to be read (validation is separate)")
	}
}

func TestResolvePackagePath_Scoped(t *testing.T) {
	tmpDir := t.TempDir()
	pkgDir := filepath.Join(tmpDir, "node_modules", "@team", "review")
	os.MkdirAll(pkgDir, 0755)
	writeFixture(t, pkgDir, "skills/s1/SKILL.md", "ok")
	writeFixture(t, pkgDir, "package.json", `{}`)

	path, err := resolvePackagePath(tmpDir, "@team/review")
	if err != nil {
		t.Fatalf("resolvePackagePath: %v", err)
	}
	if path != pkgDir {
		t.Errorf("path = %q, want %q", path, pkgDir)
	}
}

func TestResolvePackagePath_Unscoped(t *testing.T) {
	tmpDir := t.TempDir()
	pkgDir := filepath.Join(tmpDir, "node_modules", "my-skill")
	os.MkdirAll(pkgDir, 0755)

	path, err := resolvePackagePath(tmpDir, "my-skill")
	if err != nil {
		t.Fatalf("resolvePackagePath: %v", err)
	}
	if path != pkgDir {
		t.Errorf("path = %q, want %q", path, pkgDir)
	}
}
