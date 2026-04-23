package config

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

var flagToKey = map[string]string{
	"log-level":                "log_level",
	"redis-addr":               "redis.addr",
	"redis-password":           "redis.password",
	"redis-db":                 "redis.db",
	"redis-tls":                "redis.tls",
	"github-token":             "github.token",
	"queue-capacity":           "queue.capacity",
	"queue-transport":          "queue.transport",
	"queue-job-timeout":        "queue.job_timeout",
	"queue-agent-idle-timeout": "queue.agent_idle_timeout",
	"queue-prepare-timeout":    "queue.prepare_timeout",
	"queue-status-interval":    "queue.status_interval",
	"logging-dir":              "logging.dir",
	"logging-level":            "logging.level",
	"logging-retention-days":   "logging.retention_days",
	"logging-agent-output-dir": "logging.agent_output_dir",
	"repo-cache-dir":           "repo_cache.dir",
	"repo-cache-max-age":       "repo_cache.max_age",
	"workers":                  "count",
	"providers":                "providers",
}

// RegisterFlags wires every worker-scope flag onto cmd.Flags().
func RegisterFlags(cmd *cobra.Command) {
	f := cmd.Flags()

	{
		var v logLevelFlag
		f.Var(&v, "log-level", "log level: debug|info|warn|error")
	}

	f.String("redis-addr", "", "Redis address (host:port)")
	f.String("redis-password", "", "Redis password")
	f.Int("redis-db", 0, "Redis DB index")
	f.Bool("redis-tls", false, "use TLS for Redis connection")

	f.String("github-token", "", "GitHub API token")

	f.Int("queue-capacity", 0, "queue buffer capacity")
	{
		var v queueTransportFlag
		f.Var(&v, "queue-transport", "queue transport: redis")
	}
	f.Duration("queue-job-timeout", 0, "max wall-clock time per job")
	f.Duration("queue-agent-idle-timeout", 0, "max idle time for agent without output")
	f.Duration("queue-prepare-timeout", 0, "max time for prepare phase (clone/checkout)")
	f.Duration("queue-status-interval", 0, "interval between job status heartbeats")

	f.String("logging-dir", "", "directory for rotated log files")
	{
		var v logLevelFlag
		f.Var(&v, "logging-level", "file log level: debug|info|warn|error")
	}
	f.Int("logging-retention-days", 0, "days to retain rotated log files")
	f.String("logging-agent-output-dir", "", "directory for raw agent stdout/stderr")

	f.String("repo-cache-dir", "", "directory for cached git clones")
	f.Duration("repo-cache-max-age", 0, "max age before repo cache is refreshed")

	f.Int("workers", 0, "number of worker goroutines (maps to count)")
	f.StringSlice("providers", nil, "ordered provider chain (comma-separated)")
}

// BuildFlagOverrideMap walks cmd.Flags() and returns a koanf-style map of
// every flag the user explicitly set.
func BuildFlagOverrideMap(cmd *cobra.Command) map[string]any {
	out := map[string]any{}
	cmd.Flags().Visit(func(f *pflag.Flag) {
		key, ok := flagToKey[f.Name]
		if !ok {
			return
		}
		switch f.Value.Type() {
		case "string":
			if v, err := cmd.Flags().GetString(f.Name); err == nil {
				out[key] = v
			}
		case "int":
			if v, err := cmd.Flags().GetInt(f.Name); err == nil {
				out[key] = v
			}
		case "bool":
			if v, err := cmd.Flags().GetBool(f.Name); err == nil {
				out[key] = v
			}
		case "duration":
			if v, err := cmd.Flags().GetDuration(f.Name); err == nil {
				out[key] = v
			}
		case "stringSlice":
			if v, err := cmd.Flags().GetStringSlice(f.Name); err == nil {
				out[key] = v
			}
		case "queue-transport", "log-level":
			out[key] = f.Value.String()
		}
	})
	return out
}

// queueTransportFlag narrows accepted transports to the set supported today.
// New backends join the switch without touching the flag surface.
type queueTransportFlag string

func (q *queueTransportFlag) String() string { return string(*q) }
func (q *queueTransportFlag) Type() string   { return "queue-transport" }
func (q *queueTransportFlag) Set(v string) error {
	switch v {
	case "redis":
		*q = queueTransportFlag(v)
		return nil
	}
	return fmt.Errorf("must be one of [redis]")
}

type logLevelFlag string

func (l *logLevelFlag) String() string { return string(*l) }
func (l *logLevelFlag) Type() string   { return "log-level" }
func (l *logLevelFlag) Set(v string) error {
	switch strings.ToLower(v) {
	case "debug", "info", "warn", "warning", "error":
		*l = logLevelFlag(v)
		return nil
	}
	return fmt.Errorf("must be one of [debug info warn error]")
}
