package skill

import (
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type SkillsFileConfig struct {
	Skills map[string]*SkillConfig `yaml:"skills"`
	Cache  CacheConfig             `yaml:"cache"`
}

type SkillConfig struct {
	Type    string        `yaml:"type"`    // "local" | "remote"
	Path    string        `yaml:"path"`    // local: disk path
	Package string        `yaml:"package"` // remote: npm package name
	Version string        `yaml:"version"` // remote: version spec (default "latest")
	Timeout time.Duration `yaml:"timeout"` // remote: fetch timeout (default 30s)
}

type CacheConfig struct {
	TTL time.Duration `yaml:"ttl"`
}

func LoadSkillsConfig(path string) (*SkillsFileConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var cfg SkillsFileConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}

	applySkillsDefaults(&cfg)
	return &cfg, nil
}

func applySkillsDefaults(cfg *SkillsFileConfig) {
	for _, s := range cfg.Skills {
		if s.Type == "remote" {
			if s.Version == "" {
				s.Version = "latest"
			}
			if s.Timeout <= 0 {
				s.Timeout = 30 * time.Second
			}
		}
	}
	if cfg.Cache.TTL <= 0 {
		cfg.Cache.TTL = 5 * time.Minute
	}
}
