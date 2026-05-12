package configloader

import (
	"os"
	"strings"
)

// ScanSecretEnvVars returns env vars whose name starts with prefix, keyed by
// the suffix after prefix (e.g. AGENTDOCK_SECRET_FOO=bar with prefix
// "AGENTDOCK_SECRET_" yields {"FOO": "bar"}).
func ScanSecretEnvVars(prefix string) map[string]string {
	out := make(map[string]string)
	for _, env := range os.Environ() {
		idx := strings.Index(env, "=")
		if idx <= 0 {
			continue
		}
		key := env[:idx]
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		name := key[len(prefix):]
		if name == "" {
			continue
		}
		out[name] = env[idx+1:]
	}
	return out
}
