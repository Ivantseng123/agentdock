package config

import (
	"fmt"
	"log/slog"
	"os"
	"reflect"
	"slices"

	"github.com/Ivantseng123/agentdock/shared/configloader"
	"github.com/knadh/koanf/providers/confmap"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
	"github.com/spf13/cobra"
)

// BuildKoanf builds two koanf instances and unmarshals into Config.
func BuildKoanf(cmd *cobra.Command, configPath string) (*Config, *koanf.Koanf, *koanf.Koanf, configloader.DeltaInfo, error) {
	kEff := koanf.New(".")
	kSave := koanf.New(".")

	defaults := DefaultsMap()
	_ = kEff.Load(confmap.Provider(defaults, "."), nil)
	_ = kSave.Load(confmap.Provider(defaults, "."), nil)

	var fileExisted bool
	if configPath != "" {
		if _, err := os.Stat(configPath); err == nil {
			fileExisted = true
			parser, err := configloader.PickParser(configPath)
			if err != nil {
				return nil, nil, nil, configloader.DeltaInfo{}, err
			}
			if err := kEff.Load(file.Provider(configPath), parser); err != nil {
				return nil, nil, nil, configloader.DeltaInfo{}, fmt.Errorf("load %s: %w", configPath, err)
			}
			if err := kSave.Load(file.Provider(configPath), parser); err != nil {
				return nil, nil, nil, configloader.DeltaInfo{}, fmt.Errorf("load %s: %w", configPath, err)
			}
			warnUnknownKeys(kEff)
		} else if !os.IsNotExist(err) {
			return nil, nil, nil, configloader.DeltaInfo{}, fmt.Errorf("stat %s: %w", configPath, err)
		}
	}

	envMap := EnvOverrideMap()
	_ = kEff.Load(confmap.Provider(envMap, "."), nil)

	flagMap := BuildFlagOverrideMap(cmd)
	_ = kEff.Load(confmap.Provider(flagMap, "."), nil)
	_ = kSave.Load(confmap.Provider(flagMap, "."), nil)

	var cfg Config
	if err := kEff.UnmarshalWithConf("", &cfg, koanf.UnmarshalConf{Tag: "yaml"}); err != nil {
		return nil, nil, nil, configloader.DeltaInfo{}, fmt.Errorf("unmarshal: %w", err)
	}
	// Order matters: mergeBuiltinAgents first so partial user entries (e.g.
	// only `extra_args`) inherit built-in timeout/skill_dir BEFORE
	// ApplyDefaults's generic 5m per-agent fallback kicks in.
	mergeBuiltinAgents(&cfg)
	ApplyDefaults(&cfg)

	return &cfg, kEff, kSave, configloader.DeltaInfo{
		FileExisted:     fileExisted,
		HadFlagOverride: len(flagMap) > 0,
	}, nil
}

// mergeBuiltinAgents fills in any built-in agent entries the user didn't
// override. This is the primary source of agent defaults: `agentdock init
// worker` no longer snapshots BuiltinAgents, so every startup picks up the
// latest values from the current binary. User-defined entries take precedence.
//
// Three user cases per built-in name:
//
//  1. No entry at all → copy the built-in verbatim.
//  2. Entry with ONLY `extra_args` set (no `command`, no `args`) → treat as
//     "keep built-in, layer user's extra_args". The user's other zero-valued
//     fields (timeout, skill_dir, stream) also inherit from the built-in.
//  3. Entry with `command` or `args` override (a genuine full override). User
//     wins; their override slice is used as-is. If the user ALSO set
//     `extra_args` but their args override has no `{extra_args}` token, the
//     extra_args are silently dropped — we emit a startup warn so the
//     operator knows their flag never reached the CLI.
func mergeBuiltinAgents(cfg *Config) {
	if cfg.Agents == nil {
		cfg.Agents = map[string]AgentConfig{}
	}
	for name, builtin := range BuiltinAgents {
		user, exists := cfg.Agents[name]
		if !exists {
			cfg.Agents[name] = builtin
			continue
		}
		// Partial entry: user wrote `extra_args` (and/or `timeout`, `skill_dir`,
		// `stream`) but NOT `command` or `args`. Treat as layered override.
		if user.Command == "" && len(user.Args) == 0 {
			merged := builtin
			if user.Timeout > 0 {
				merged.Timeout = user.Timeout
			}
			if user.SkillDir != "" {
				merged.SkillDir = user.SkillDir
			}
			// Stream is a bool; only override if user explicitly set something
			// non-zero. We can't distinguish "false" from "unset" here, so
			// stream stays at the built-in unless the user also set command/args.
			merged.ExtraArgs = user.ExtraArgs
			cfg.Agents[name] = merged
			continue
		}
		// Full override path. Warn if user set extra_args but their args
		// override doesn't have the {extra_args} token — the flag won't reach
		// the CLI, which is almost certainly a user mistake.
		if len(user.ExtraArgs) > 0 && !slices.Contains(user.Args, ExtraArgsToken) {
			slog.Warn("extra_args 被忽略：args 覆寫未包含 {extra_args} token",
				"component", "config",
				"phase", "載入",
				"agent", name,
				"extra_args", user.ExtraArgs,
			)
		}
	}
}

// warnUnknownKeys logs warnings for koanf keys that don't match the Config
// schema. Map-valued fields (agents, secrets) are skipped.
func warnUnknownKeys(k *koanf.Koanf) {
	valid := map[string]bool{}
	mapKeys := map[string]bool{}
	configloader.WalkYAMLPathsKeyOnly(reflect.TypeOf(Config{}), "", valid, mapKeys)
	for _, key := range configloader.UnknownKeys(k, valid, mapKeys) {
		slog.Warn("未知設定鍵", "phase", "失敗", "key", key)
	}
}
