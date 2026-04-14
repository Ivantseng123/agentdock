package skill

import (
	"fmt"
	"path/filepath"
	"strings"

	"agentdock/internal/queue"
)

const (
	maxSkillDirSize = 1 * 1024 * 1024
	maxJobSize      = 5 * 1024 * 1024
)

var allowedExtensions = map[string]bool{
	".md": true, ".txt": true, ".yaml": true, ".yml": true,
	".json": true, ".example": true, ".tmpl": true,
}

func ValidateSkillFiles(files map[string][]byte) error {
	var totalSize int
	for relPath, content := range files {
		if strings.Contains(relPath, "..") || filepath.IsAbs(relPath) {
			return fmt.Errorf("invalid skill file path: %s", relPath)
		}
		ext := filepath.Ext(relPath)
		if ext == "" || !allowedExtensions[ext] {
			return fmt.Errorf("disallowed file type %q: %s", ext, relPath)
		}
		totalSize += len(content)
	}
	if totalSize > maxSkillDirSize {
		return fmt.Errorf("skill directory too large: %d bytes (max %d)", totalSize, maxSkillDirSize)
	}
	return nil
}

func ValidateJobSize(skills map[string]*queue.SkillPayload) error {
	var total int
	for _, sp := range skills {
		for _, content := range sp.Files {
			total += len(content)
		}
	}
	if total > maxJobSize {
		return fmt.Errorf("job skills too large: %d bytes (max %d)", total, maxJobSize)
	}
	return nil
}
