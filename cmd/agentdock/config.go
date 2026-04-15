package main

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"agentdock/internal/config"

	"github.com/knadh/koanf/parsers/json"
	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/confmap"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
	"github.com/spf13/cobra"
)

// DeltaInfo records whether the config file existed and whether the user
// passed any flag overrides. Phase 6 uses it to decide whether to trigger
// save-back.
type DeltaInfo struct {
	FileExisted     bool
	HadFlagOverride bool
}

// buildKoanf builds two koanf instances:
//
//   - kEff: defaults -> file -> env -> flags. Used to materialize the *config.Config
//     the bot actually runs with.
//   - kSave: defaults -> file -> flags. Used for save-back so env values don't
//     leak into config.yaml (D1).
//
// Both layers use yaml tags on config.Config for the confmap provider's key
// shape, and the returned *config.Config is unmarshaled from kEff.
func buildKoanf(cmd *cobra.Command, configPath string) (*config.Config, *koanf.Koanf, *koanf.Koanf, DeltaInfo, error) {
	kEff := koanf.New(".")
	kSave := koanf.New(".")

	// L0 defaults — applied to both.
	defaults := config.DefaultsMap()
	_ = kEff.Load(confmap.Provider(defaults, "."), nil)
	_ = kSave.Load(confmap.Provider(defaults, "."), nil)

	// L1 file — applied to both, only if it exists.
	var fileExisted bool
	if configPath != "" {
		if _, err := os.Stat(configPath); err == nil {
			fileExisted = true
			parser, err := pickParser(configPath)
			if err != nil {
				return nil, nil, nil, DeltaInfo{}, err
			}
			if err := kEff.Load(file.Provider(configPath), parser); err != nil {
				return nil, nil, nil, DeltaInfo{}, fmt.Errorf("load %s: %w", configPath, err)
			}
			if err := kSave.Load(file.Provider(configPath), parser); err != nil {
				return nil, nil, nil, DeltaInfo{}, fmt.Errorf("load %s: %w", configPath, err)
			}
		} else if !os.IsNotExist(err) {
			return nil, nil, nil, DeltaInfo{}, fmt.Errorf("stat %s: %w", configPath, err)
		}
	}

	// L2 env — kEff only. Env must not round-trip into config.yaml on save.
	envMap := config.EnvOverrideMap()
	_ = kEff.Load(confmap.Provider(envMap, "."), nil)

	// L3 flags — both. Explicit user intent, safe to persist.
	flagMap := buildFlagOverrideMap(cmd)
	_ = kEff.Load(confmap.Provider(flagMap, "."), nil)
	_ = kSave.Load(confmap.Provider(flagMap, "."), nil)

	var cfg config.Config
	if err := kEff.UnmarshalWithConf("", &cfg, koanf.UnmarshalConf{Tag: "yaml"}); err != nil {
		return nil, nil, nil, DeltaInfo{}, fmt.Errorf("unmarshal: %w", err)
	}

	mergeBuiltinAgents(&cfg)

	return &cfg, kEff, kSave, DeltaInfo{
		FileExisted:     fileExisted,
		HadFlagOverride: len(flagMap) > 0,
	}, nil
}

// pickParser chooses a koanf parser based on file extension. Only .yaml, .yml,
// and .json are supported (D2).
func pickParser(path string) (koanf.Parser, error) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".yaml", ".yml":
		return yaml.Parser(), nil
	case ".json":
		return json.Parser(), nil
	default:
		return nil, fmt.Errorf("unsupported config format: %s; only .yaml/.yml/.json supported", filepath.Ext(path))
	}
}

// mergeBuiltinAgents fills in any built-in agent entries the user didn't
// override. Runtime fallback only — `agentdock init` does not write these to
// disk (D16).
func mergeBuiltinAgents(cfg *config.Config) {
	if cfg.Agents == nil {
		cfg.Agents = map[string]config.AgentConfig{}
	}
	for name, agent := range config.BuiltinAgents {
		if _, exists := cfg.Agents[name]; !exists {
			cfg.Agents[name] = agent
		}
	}
}

// ctxKey is a private type for cmd.Context values so tests and other packages
// can't accidentally collide.
type ctxKey int

const (
	ctxKeyConfig ctxKey = iota
	ctxKeyKSave
	ctxKeyDelta
)

func cfgFromCtx(ctx context.Context) *config.Config {
	return ctx.Value(ctxKeyConfig).(*config.Config)
}

func kSaveFromCtx(ctx context.Context) *koanf.Koanf {
	return ctx.Value(ctxKeyKSave).(*koanf.Koanf)
}

func deltaFromCtx(ctx context.Context) DeltaInfo {
	return ctx.Value(ctxKeyDelta).(DeltaInfo)
}

// loadAndStash resolves the config path, builds the koanf layer chain, runs
// scope-aware preflight (prompting for any missing values), persists any
// delta back to disk, and stashes the resulting *config.Config, kSave, and
// DeltaInfo into cmd.Context. Intended to be wired as PersistentPreRunE on
// subcommands that need the loaded config.
//
// If the caller explicitly passed a config path that doesn't exist, it
// returns a guided error pointing at `agentdock init`. Empty path falls back
// to the default location silently (fine for first-run).
func loadAndStash(cmd *cobra.Command, configPath string, scope PreflightScope) error {
	resolved, err := resolveConfigPath(configPath)
	if err != nil {
		return err
	}
	cfg, _, kSave, delta, err := buildKoanf(cmd, resolved)
	if err != nil {
		return err
	}
	if configPath != "" && !delta.FileExisted {
		return fmt.Errorf("config file not found: %s; run 'agentdock init -c %s' first", resolved, resolved)
	}
	if err := validate(cfg); err != nil {
		return err
	}
	prompted, err := runPreflight(cfg, scope)
	if err != nil {
		return fmt.Errorf("preflight: %w", err)
	}
	if _, err := saveConfig(kSave, resolved, prompted, delta); err != nil {
		slog.Warn("config save failed", "path", resolved, "error", err)
	}

	ctx := cmd.Context()
	ctx = context.WithValue(ctx, ctxKeyConfig, cfg)
	ctx = context.WithValue(ctx, ctxKeyKSave, kSave)
	ctx = context.WithValue(ctx, ctxKeyDelta, delta)
	cmd.SetContext(ctx)
	return nil
}

// saveConfig writes kSave to path if any delta condition is met (D13):
//
//	A. preflight prompted any value (prompted non-empty), or
//	B. flag override happened (delta.HadFlagOverride), or
//	C. config file didn't exist (!delta.FileExisted).
//
// Skips the write when the marshaled output is byte-identical to the
// existing file. Returns (written, error). Save failures are non-fatal; the
// caller should warn-log and continue.
func saveConfig(kSave *koanf.Koanf, path string, prompted map[string]any, delta DeltaInfo) (bool, error) {
	shouldWrite := len(prompted) > 0 || delta.HadFlagOverride || !delta.FileExisted
	if !shouldWrite {
		return false, nil
	}

	for k, v := range prompted {
		if err := kSave.Set(k, v); err != nil {
			return false, fmt.Errorf("kSave.Set(%s): %w", k, err)
		}
	}

	parser, err := pickParser(path)
	if err != nil {
		return false, err
	}
	data, err := kSave.Marshal(parser)
	if err != nil {
		return false, fmt.Errorf("marshal: %w", err)
	}

	if existing, err := os.ReadFile(path); err == nil && bytes.Equal(existing, data) {
		return false, nil
	}

	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return false, fmt.Errorf("mkdir: %w", err)
	}
	if err := atomicWrite(path, data, 0600); err != nil {
		return false, fmt.Errorf("write: %w", err)
	}
	return true, nil
}

// resolveConfigPath expands ~/ and returns an absolute path. Empty input
// falls back to the literal default ~/.config/agentdock/config.yaml (D2).
func resolveConfigPath(in string) (string, error) {
	if in == "" {
		in = "~/.config/agentdock/config.yaml"
	}
	if strings.HasPrefix(in, "~/") || in == "~" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve ~: %w", err)
		}
		in = filepath.Join(home, strings.TrimPrefix(in, "~/"))
	}
	return filepath.Abs(in)
}
