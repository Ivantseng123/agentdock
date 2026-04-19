package skill

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type SkillFiles struct {
	Name  string
	Files map[string][]byte
}

func FetchPackage(ctx context.Context, pkg, version string) ([]*SkillFiles, error) {
	tmpDir, err := os.MkdirTemp("", "agentdock-skill-*")
	if err != nil {
		return nil, fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	arg := pkg + "@" + version
	cmd := exec.CommandContext(ctx, "npm", "install", "--prefix", tmpDir, "--no-save", arg)
	cmd.Dir = tmpDir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("npm install %s failed: %w\n%s", arg, err, string(output))
	}

	pkgPath, err := resolvePackagePath(tmpDir, pkg)
	if err != nil {
		return nil, fmt.Errorf("resolve package path: %w", err)
	}

	return scanPackageSkills(pkgPath)
}

func resolvePackagePath(baseDir, pkg string) (string, error) {
	parts := strings.Split(pkg, "/")
	elems := append([]string{baseDir, "node_modules"}, parts...)
	pkgDir := filepath.Join(elems...)

	if _, err := os.Stat(pkgDir); err != nil {
		return "", fmt.Errorf("package not found at %s: %w", pkgDir, err)
	}
	return pkgDir, nil
}

func scanPackageSkills(pkgDir string) ([]*SkillFiles, error) {
	skillsDir := filepath.Join(pkgDir, "skills")
	entries, err := os.ReadDir(skillsDir)
	if err != nil {
		return nil, fmt.Errorf("read skills directory: %w", err)
	}

	var result []*SkillFiles
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		skillDir := filepath.Join(skillsDir, entry.Name())

		if _, err := os.Stat(filepath.Join(skillDir, "SKILL.md")); err != nil {
			continue
		}

		files, err := readDirRecursive(skillDir, "")
		if err != nil {
			return nil, fmt.Errorf("read skill %s: %w", entry.Name(), err)
		}

		result = append(result, &SkillFiles{
			Name:  entry.Name(),
			Files: files,
		})
	}

	if len(result) == 0 {
		return nil, fmt.Errorf("no skills found in %s", skillsDir)
	}
	return result, nil
}

func readDirRecursive(baseDir, prefix string) (map[string][]byte, error) {
	files := make(map[string][]byte)
	dir := baseDir
	if prefix != "" {
		dir = filepath.Join(baseDir, prefix)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	for _, entry := range entries {
		relPath := entry.Name()
		if prefix != "" {
			relPath = prefix + "/" + entry.Name()
		}

		if entry.IsDir() {
			sub, err := readDirRecursive(baseDir, relPath)
			if err != nil {
				return nil, err
			}
			for k, v := range sub {
				files[k] = v
			}
			continue
		}

		info, err := entry.Info()
		if err != nil {
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 {
			continue
		}

		content, err := os.ReadFile(filepath.Join(baseDir, relPath))
		if err != nil {
			return nil, err
		}
		files[relPath] = content
	}
	return files, nil
}
