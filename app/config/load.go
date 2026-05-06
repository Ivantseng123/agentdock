package config

import (
	"fmt"
	"os"
	"reflect"

	"github.com/Ivantseng123/agentdock/shared/configloader"
	"github.com/knadh/koanf/providers/confmap"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
	"github.com/spf13/cobra"
)

// BuildKoanf builds two koanf instances and unmarshals into Config.
//
//   - kEff: defaults -> file -> env -> flags. Source of truth for the running
//     process.
//   - kSave: defaults -> file -> flags. Used for save-back so env values don't
//     leak into app.yaml.
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
			configloader.WarnUnknownKeys(kEff, reflect.TypeOf(Config{}))
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
	ApplyDefaults(&cfg)

	return &cfg, kEff, kSave, configloader.DeltaInfo{
		FileExisted:     fileExisted,
		HadFlagOverride: len(flagMap) > 0,
	}, nil
}

