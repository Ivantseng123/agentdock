package prreview

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// FingerprintOptions lets callers (tests) override the GitHub API base URL.
// Production callers pass the zero value; APIBase defaults to https://api.github.com.
type FingerprintOptions struct {
	APIBase string
}

// Fingerprint returns a full FingerprintResult combining local probes with
// PR-aware fields fetched from the GitHub API.
func Fingerprint(ctx context.Context, repoDir, prURL, token string, opts FingerprintOptions) (*FingerprintResult, error) {
	fp, err := fingerprintLocal(repoDir)
	if err != nil {
		return nil, err
	}

	apiBase := opts.APIBase
	if apiBase == "" {
		apiBase = "https://api.github.com"
	}
	files, err := listDiffFiles(ctx, apiBase, prURL, token, DefaultMaxWallTime)
	if err != nil {
		// Surface empty PR-aware fields on API error; caller decides.
		return fp, nil
	}

	fp.PRTouchedLanguages = prTouchedLanguages(files)
	fp.PRSubprojects = prSubprojects(repoDir, files)
	return fp, nil
}

func prTouchedLanguages(files []PRFile) []string {
	extLang := map[string]string{
		".go": "go", ".py": "python", ".ts": "ts", ".tsx": "ts",
		".js": "js", ".jsx": "js", ".rs": "rust", ".rb": "ruby",
		".java": "java", ".md": "markdown", ".yml": "yaml", ".yaml": "yaml",
		".toml": "toml", ".json": "json",
	}
	seen := map[string]bool{}
	out := []string{}
	for _, f := range files {
		ext := strings.ToLower(filepath.Ext(f.Filename))
		if l, ok := extLang[ext]; ok && !seen[l] {
			seen[l] = true
			out = append(out, l)
		}
	}
	return out
}

func prSubprojects(repoDir string, files []PRFile) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, f := range files {
		dir := filepath.Dir(f.Filename)
		for dir != "." && dir != "/" && dir != "" {
			if hasManifest(filepath.Join(repoDir, dir)) {
				if !seen[dir] {
					seen[dir] = true
					out = append(out, dir)
				}
				break
			}
			dir = filepath.Dir(dir)
		}
	}
	return out
}

func hasManifest(dir string) bool {
	for _, m := range []string{"go.mod", "package.json", "pyproject.toml", "Cargo.toml", "Gemfile", "pom.xml", "build.gradle"} {
		if fileExists(filepath.Join(dir, m)) {
			return true
		}
	}
	return false
}

// fingerprintLocal inspects a cloned repo on disk and returns the language,
// style sources, test runner, and framework best guesses. Missing files are
// not errors — they surface as zero values in FingerprintResult.
func fingerprintLocal(repoDir string) (*FingerprintResult, error) {
	fp := &FingerprintResult{
		StyleSources:       []string{},
		PRTouchedLanguages: []string{},
		PRSubprojects:      []string{},
	}

	manifest, lang := detectManifest(repoDir)
	extCounts := countExtensions(repoDir)

	switch {
	case lang != "" && extCounts[lang] > 0:
		fp.Language = lang
		fp.Confidence = "high"
	case lang != "":
		fp.Language = lang
		fp.Confidence = "medium"
	case len(extCounts) > 0:
		fp.Language = dominantExt(extCounts)
		fp.Confidence = "low"
	default:
		fp.Confidence = "low"
	}

	if manifest == "package.json" && hasDep(repoDir, "typescript") {
		fp.Language = "ts"
	}

	fp.StyleSources = detectStyleSources(repoDir)
	fp.TestRunner = detectTestRunner(repoDir, fp.Language)
	fp.Framework = detectFramework(repoDir)

	return fp, nil
}

func detectManifest(repoDir string) (manifest, lang string) {
	cands := []struct {
		file string
		lang string
	}{
		{"go.mod", "go"},
		{"package.json", "js"},
		{"pyproject.toml", "python"},
		{"setup.py", "python"},
		{"Cargo.toml", "rust"},
		{"Gemfile", "ruby"},
		{"pom.xml", "java"},
		{"build.gradle", "java"},
		{"build.gradle.kts", "java"},
	}
	for _, c := range cands {
		if fileExists(filepath.Join(repoDir, c.file)) {
			return c.file, c.lang
		}
	}
	return "", ""
}

func countExtensions(repoDir string) map[string]int {
	m := map[string]int{}
	extLang := map[string]string{
		".go":   "go",
		".py":   "python",
		".ts":   "ts",
		".tsx":  "ts",
		".js":   "js",
		".jsx":  "js",
		".rs":   "rust",
		".rb":   "ruby",
		".java": "java",
	}
	_ = filepath.WalkDir(repoDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if name == ".git" || name == "node_modules" || name == "vendor" ||
				name == "target" || name == "dist" || name == "build" {
				return fs.SkipDir
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if l, ok := extLang[ext]; ok {
			m[l]++
		}
		return nil
	})
	return m
}

func dominantExt(counts map[string]int) string {
	var best string
	var bestN int
	for k, n := range counts {
		if n > bestN {
			best = k
			bestN = n
		}
	}
	return best
}

func hasDep(repoDir, depName string) bool {
	data, err := os.ReadFile(filepath.Join(repoDir, "package.json"))
	if err != nil {
		return false
	}
	var pkg struct {
		Dependencies    map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		return false
	}
	if _, ok := pkg.Dependencies[depName]; ok {
		return true
	}
	if _, ok := pkg.DevDependencies[depName]; ok {
		return true
	}
	return false
}

func detectStyleSources(repoDir string) []string {
	cands := []string{
		"CLAUDE.md", "AGENTS.md", "CONTRIBUTING.md",
		".editorconfig",
		".golangci.yml", ".golangci.yaml",
		".eslintrc", ".eslintrc.js", ".eslintrc.json", ".eslintrc.yaml", ".eslintrc.yml",
		"ruff.toml", ".ruff.toml",
		"rustfmt.toml",
		".rubocop.yml",
		".prettierrc", ".prettierrc.json", ".prettierrc.yaml", ".prettierrc.yml",
	}
	out := []string{}
	for _, f := range cands {
		if fileExists(filepath.Join(repoDir, f)) {
			out = append(out, f)
		}
	}
	return out
}

func detectTestRunner(repoDir, lang string) string {
	switch lang {
	case "go":
		return "go test"
	case "python":
		return "pytest"
	case "rust":
		return "cargo test"
	case "ruby":
		return "rspec"
	case "java":
		return "mvn test"
	case "js", "ts":
		data, err := os.ReadFile(filepath.Join(repoDir, "package.json"))
		if err != nil {
			return "npm test"
		}
		var pkg struct {
			Scripts map[string]string `json:"scripts"`
		}
		if err := json.Unmarshal(data, &pkg); err == nil {
			if s, ok := pkg.Scripts["test"]; ok && s != "" {
				return s
			}
		}
		return "npm test"
	}
	return ""
}

func detectFramework(repoDir string) string {
	if data, err := os.ReadFile(filepath.Join(repoDir, "package.json")); err == nil {
		var pkg struct {
			Dependencies    map[string]string `json:"dependencies"`
			DevDependencies map[string]string `json:"devDependencies"`
		}
		if err := json.Unmarshal(data, &pkg); err == nil {
			for _, fw := range []string{"next", "react", "vue", "svelte", "nuxt", "express", "fastify"} {
				if _, ok := pkg.Dependencies[fw]; ok {
					return fw
				}
				if _, ok := pkg.DevDependencies[fw]; ok {
					return fw
				}
			}
		}
	}
	if data, err := os.ReadFile(filepath.Join(repoDir, "pyproject.toml")); err == nil {
		s := string(data)
		for _, fw := range []string{"fastapi", "django", "flask"} {
			if strings.Contains(s, fw) {
				return fw
			}
		}
	}
	if data, err := os.ReadFile(filepath.Join(repoDir, "go.mod")); err == nil {
		s := string(data)
		for _, fw := range []string{"gin-gonic/gin", "labstack/echo", "gofiber/fiber"} {
			if strings.Contains(s, fw) {
				switch {
				case strings.Contains(fw, "gin"):
					return "gin"
				case strings.Contains(fw, "echo"):
					return "echo"
				case strings.Contains(fw, "fiber"):
					return "fiber"
				}
			}
		}
	}
	return ""
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
