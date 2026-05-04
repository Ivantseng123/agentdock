package config

import (
	"os"
	"strconv"
	"strings"
)

// EnvOverrideMap returns a koanf-friendly map of env var values used by the
// app module. Unset env vars are absent from the result.
func EnvOverrideMap() map[string]any {
	out := map[string]any{}
	if v := os.Getenv("SLACK_BOT_TOKEN"); v != "" {
		out["slack.bot_token"] = v
	}
	if v := os.Getenv("SLACK_APP_TOKEN"); v != "" {
		out["slack.app_token"] = v
	}
	if v := os.Getenv("GITHUB_TOKEN"); v != "" {
		out["github.token"] = v
	}
	if v := os.Getenv("GITHUB_APP_APP_ID"); v != "" {
		// Bad int silently skipped — preflight catches missing AppID
		// with a clear field-level error message.
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			out["github.app.app_id"] = n
		}
	}
	if v := os.Getenv("GITHUB_APP_INSTALLATION_ID"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			out["github.app.installation_id"] = n
		}
	}
	if v := os.Getenv("GITHUB_APP_PRIVATE_KEY_PATH"); v != "" {
		out["github.app.private_key_path"] = v
	}
	if v := os.Getenv("MANTIS_API_TOKEN"); v != "" {
		out["mantis.api_token"] = v
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
	return out
}

// scanSecretEnvVars picks up AGENTDOCK_SECRET_* env vars.
func scanSecretEnvVars() map[string]string {
	const prefix = "AGENTDOCK_SECRET_"
	out := make(map[string]string)
	for _, env := range os.Environ() {
		if idx := strings.Index(env, "="); idx > 0 {
			key := env[:idx]
			if strings.HasPrefix(key, prefix) {
				name := key[len(prefix):]
				if name != "" {
					out[name] = env[idx+1:]
				}
			}
		}
	}
	return out
}
