package config

import (
	"os"
	"strings"
)

// EnvOverrideMap returns a koanf-friendly map of env var values used by the
// worker module. Unset env vars are absent from the result.
func EnvOverrideMap() map[string]any {
	out := map[string]any{}
	if v := os.Getenv("GITHUB_TOKEN"); v != "" {
		out["github.token"] = v
	}
	if v := os.Getenv("REDIS_ADDR"); v != "" {
		out["redis.addr"] = v
	}
	if v := os.Getenv("REDIS_PASSWORD"); v != "" {
		out["redis.password"] = v
	}
	if v := os.Getenv("SECRET_KEY"); v != "" {
		out["secret_key"] = v
	}
	if v := os.Getenv("PROVIDERS"); v != "" {
		var providers []string
		for _, p := range strings.Split(v, ",") {
			if p = strings.TrimSpace(p); p != "" {
				providers = append(providers, p)
			}
		}
		if len(providers) > 0 {
			out["providers"] = providers
		}
	}
	// OTEL_EXPORTER_OTLP_ENDPOINT is the OTel-standard env name. Bind into
	// our tracing.otlp_endpoint path so env wins over YAML during koanf merge.
	if v := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"); v != "" {
		out["tracing.otlp_endpoint"] = v
	}
	return out
}

