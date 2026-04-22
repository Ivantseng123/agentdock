package config

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// flagToKey maps cobra flag names (dash-case) to koanf dotted keys matching
// yaml tags on Config. Flags not in this map are ignored by
// BuildFlagOverrideMap (e.g. --help, --version, --config).
var flagToKey = map[string]string{
	"log-level":                "log_level",
	"redis-addr":               "redis.addr",
	"redis-password":           "redis.password",
	"redis-db":                 "redis.db",
	"redis-tls":                "redis.tls",
	"github-token":             "github.token",
	"mantis-base-url":          "mantis.base_url",
	"mantis-api-token":         "mantis.api_token",
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
	"attachments-store":        "attachments.store",
	"attachments-temp-dir":     "attachments.temp_dir",
	"attachments-ttl":          "attachments.ttl",
	"skills-config":            "skills_config",
	"slack-bot-token":          "slack.bot_token",
	"slack-app-token":          "slack.app_token",
	"server-port":              "server.port",
	"auto-bind":                "auto_bind",
	"max-thread-messages":      "max_thread_messages",
	"semaphore-timeout":        "semaphore_timeout",
	"rate-limit-per-user":      "rate_limit.per_user",
	"rate-limit-per-channel":   "rate_limit.per_channel",
	"rate-limit-window":        "rate_limit.window",
}

// RegisterFlags wires every app-scope flag onto cmd.Flags(). Worker flags
// live in worker/config.RegisterFlags — keeping the two scopes apart means
// `agentdock app --help` doesn't advertise worker-only flags, and vice versa.
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

	f.String("mantis-base-url", "", "Mantis base URL")
	f.String("mantis-api-token", "", "Mantis API token")

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

	f.String("attachments-store", "", "attachment storage backend")
	f.String("attachments-temp-dir", "", "temp directory for downloaded attachments")
	f.Duration("attachments-ttl", 0, "attachment retention TTL")

	f.String("skills-config", "", "path to skills.yaml")

	f.String("slack-bot-token", "", "Slack bot token (xoxb-)")
	f.String("slack-app-token", "", "Slack app-level token (xapp-)")
	f.Int("server-port", 0, "HTTP server port for /healthz and /jobs")
	f.Bool("auto-bind", false, "auto-register channels on join")
	f.Int("max-thread-messages", 0, "max thread messages to read for context")
	f.Duration("semaphore-timeout", 0, "worker semaphore acquire timeout")
	f.Int("rate-limit-per-user", 0, "max triggers per user per window")
	f.Int("rate-limit-per-channel", 0, "max triggers per channel per window")
	f.Duration("rate-limit-window", 0, "rate limit sliding window")
}

// BuildFlagOverrideMap walks cmd.Flags() and returns a koanf-style map keyed
// by dotted path for every flag the user explicitly set. Flags not in
// flagToKey are skipped silently.
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

// queueTransportFlag is a pflag.Value that accepts the set of supported queue
// transports. Currently only "redis"; future backends (e.g. github runner)
// widen the switch without reworking the flag surface.
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

// logLevelFlag is a pflag.Value that accepts debug/info/warn/error.
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
