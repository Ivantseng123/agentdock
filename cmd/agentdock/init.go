package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	appconfig "github.com/Ivantseng123/agentdock/app/config"
	"github.com/Ivantseng123/agentdock/shared/configloader"
	"github.com/Ivantseng123/agentdock/shared/connectivity"
	"github.com/Ivantseng123/agentdock/shared/prompt"
	workerconfig "github.com/Ivantseng123/agentdock/worker/config"

	"github.com/spf13/cobra"
	"golang.org/x/term"
	"gopkg.in/yaml.v3"
)

var (
	initConfigPath  string
	initForce       bool
	initInteractive bool
)

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Generate a starter config file",
	Long: `Initialize agentdock configuration. Use 'init app' for the app config
or 'init worker' for the worker config. Running 'agentdock init' on its own
prints this help.`,
}

var initAppCmd = &cobra.Command{
	Use:   "app",
	Short: "Generate a starter app config file",
	RunE: func(cmd *cobra.Command, args []string) error {
		path, err := resolveAppConfigPath(initConfigPath)
		if err != nil {
			return err
		}
		return runInitApp(path, initInteractive, initForce)
	},
}

var initWorkerCmd = &cobra.Command{
	Use:   "worker",
	Short: "Generate a starter worker config file",
	RunE: func(cmd *cobra.Command, args []string) error {
		path, err := resolveWorkerConfigPath(initConfigPath)
		if err != nil {
			return err
		}
		return runInitWorker(path, initInteractive, initForce)
	},
}

func init() {
	for _, c := range []*cobra.Command{initAppCmd, initWorkerCmd} {
		c.Flags().StringVarP(&initConfigPath, "config", "c", "", "path for the new config file")
		c.Flags().BoolVar(&initForce, "force", false, "overwrite if file exists")
		c.Flags().BoolVarP(&initInteractive, "interactive", "i", false, "prompt for required values")
	}
	initCmd.AddCommand(initAppCmd, initWorkerCmd)
	rootCmd.AddCommand(initCmd)
}

// runInitApp writes a starter app.yaml. When interactive, prompts for Slack /
// GitHub / Redis / secret_key values and validates each via shared/connectivity.
func runInitApp(path string, interactive, force bool) error {
	if interactive && !term.IsTerminal(int(syscall.Stdin)) {
		return fmt.Errorf("--interactive requires a terminal (stdin is not a TTY)")
	}
	if _, err := os.Stat(path); err == nil && !force {
		return fmt.Errorf("config already exists at %s; pass --force to overwrite", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	var cfg appconfig.Config
	data, _ := yaml.Marshal(appconfig.DefaultsMap())
	_ = yaml.Unmarshal(data, &cfg)

	if interactive {
		if err := promptAppInit(&cfg); err != nil {
			return err
		}
	}

	out, err := marshalAppYAML(&cfg, path)
	if err != nil {
		return err
	}
	return configloader.AtomicWrite(path, out, 0o600)
}

// runInitWorker writes a starter worker.yaml with built-in agents pre-populated.
func runInitWorker(path string, interactive, force bool) error {
	if interactive && !term.IsTerminal(int(syscall.Stdin)) {
		return fmt.Errorf("--interactive requires a terminal (stdin is not a TTY)")
	}
	if _, err := os.Stat(path); err == nil && !force {
		return fmt.Errorf("config already exists at %s; pass --force to overwrite", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	var cfg workerconfig.Config
	data, _ := yaml.Marshal(workerconfig.DefaultsMap())
	_ = yaml.Unmarshal(data, &cfg)
	cfg.Agents = map[string]workerconfig.AgentConfig{}
	for k, v := range workerconfig.BuiltinAgents {
		cfg.Agents[k] = v
	}

	if interactive {
		if err := promptWorkerInit(&cfg); err != nil {
			return err
		}
	}

	out, err := marshalWorkerYAML(&cfg, path)
	if err != nil {
		return err
	}
	return configloader.AtomicWrite(path, out, 0o600)
}

func marshalAppYAML(cfg *appconfig.Config, path string) ([]byte, error) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".json":
		return json.MarshalIndent(cfg, "", "  ")
	}
	raw, err := yaml.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	text := string(raw)
	if cfg.Slack.BotToken == "" {
		text = insertBefore(text, "slack:", "# REQUIRED for `agentdock app`: Slack bot+app tokens")
	}
	if cfg.GitHub.Token == "" {
		text = insertBefore(text, "github:", "# REQUIRED for both subcommands: GitHub token")
	}
	if cfg.Redis.Addr == "" {
		text = insertBefore(text, "redis:", "# REQUIRED when queue.transport=redis: Redis address")
	}
	// queue.store picks the JobStore backend. "mem" is fine for unit tests
	// and small single-instance deployments; "redis" is recommended for
	// production so in-flight job state survives app restarts (#123).
	// Anchor on "store: mem" (not bare "store:") — attachments also has a
	// "store" field in the same yaml and a bare anchor would match it first.
	text = insertBefore(text, "    store: mem",
		"    # JobStore backend. mem: in-process, lost on restart (test / small deployments).\n"+
			"    # redis: persisted via RedisJobStore, survives app restart (recommended for production).")
	text = "# Generated by `agentdock init app`. See agentdock app --help for flag overrides.\n" + text
	return []byte(text), nil
}

func marshalWorkerYAML(cfg *workerconfig.Config, path string) ([]byte, error) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".json":
		return json.MarshalIndent(cfg, "", "  ")
	}
	raw, err := yaml.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	text := string(raw)
	if cfg.GitHub.Token == "" {
		text = insertBefore(text, "github:", "# REQUIRED: GitHub token")
	}
	if cfg.Redis.Addr == "" {
		text = insertBefore(text, "redis:", "# REQUIRED when queue.transport=redis: Redis address")
	}
	if cfg.SecretKey == "" {
		text = insertBefore(text, "secret_key:", "# REQUIRED: copy the secret_key value from the app config")
	}
	if len(cfg.Providers) == 0 {
		text = insertBefore(text, "providers:", "# REQUIRED: list of agent names to try, in order")
	}
	text = "# Generated by `agentdock init worker`. See agentdock worker --help for flag overrides.\n" + text
	return []byte(text), nil
}

func insertBefore(s, anchor, comment string) string {
	if strings.Contains(s, anchor) {
		return strings.Replace(s, anchor, comment+"\n"+anchor, 1)
	}
	return s
}

func promptAppInit(cfg *appconfig.Config) error {
	fmt.Fprintln(prompt.Stderr)

	fmt.Fprintln(prompt.Stderr, "  Slack bot token (xoxb-...):")
	for attempt := 1; attempt <= 3; attempt++ {
		tok := prompt.Hidden("Token: ")
		if tok == "" {
			prompt.Fail("Slack bot token is required")
			continue
		}
		identity, err := connectivity.CheckSlackToken(tok)
		if err != nil {
			prompt.Fail("%v (attempt %d/3)", err, attempt)
			continue
		}
		cfg.Slack.BotToken = tok
		prompt.OK("Slack bot token valid (user_id: %s)", identity.UserID)
		break
	}

	fmt.Fprintln(prompt.Stderr)
	fmt.Fprintln(prompt.Stderr, "  Slack app-level token (xapp-...):")
	for attempt := 1; attempt <= 3; attempt++ {
		tok := prompt.Hidden("Token: ")
		if !strings.HasPrefix(tok, "xapp-") {
			prompt.Fail("must start with xapp- (attempt %d/3)", attempt)
			continue
		}
		cfg.Slack.AppToken = tok
		prompt.OK("Slack app token format OK")
		break
	}

	fmt.Fprintln(prompt.Stderr)
	fmt.Fprintln(prompt.Stderr, "  GitHub token (ghp_... or github_pat_...):")
	for attempt := 1; attempt <= 3; attempt++ {
		tok := prompt.Hidden("Token: ")
		if tok == "" {
			prompt.Fail("GitHub token is required")
			continue
		}
		username, err := connectivity.CheckGitHubToken(tok)
		if err != nil {
			prompt.Fail("%v (attempt %d/3)", err, attempt)
			continue
		}
		cfg.GitHub.Token = tok
		prompt.OK("Token valid (user: %s)", username)
		break
	}

	fmt.Fprintln(prompt.Stderr)
	fmt.Fprintln(prompt.Stderr, "  Redis is required (queue transport).")
	for attempt := 1; attempt <= 3; attempt++ {
		addr := prompt.Line("Redis address: ")
		if addr == "" {
			prompt.Fail("Redis address is required (attempt %d/3)", attempt)
			continue
		}
		if err := connectivity.CheckRedis(addr, "", 0, false); err != nil {
			prompt.Fail("Redis connect failed: %v (attempt %d/3)", err, attempt)
			continue
		}
		cfg.Redis.Addr = addr
		cfg.Queue.Transport = "redis"
		prompt.OK("Redis connected")
		break
	}

	fmt.Fprintln(prompt.Stderr)
	fmt.Fprintln(prompt.Stderr, "  Secret key (AES-256, 64 hex chars):")
	if prompt.YesNo("  Auto-generate a key?") {
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return fmt.Errorf("generate key: %w", err)
		}
		cfg.SecretKey = hex.EncodeToString(key)
		prompt.OK("Secret key generated — copy this into worker.yaml:\n  %s", cfg.SecretKey)
	}

	fmt.Fprintln(prompt.Stderr)
	fmt.Fprintln(prompt.Stderr, "  Mantis enrichment (optional) — lets the agent fetch Mantis issue details.")
	if prompt.YesNoDefault("  Enable Mantis?", false) {
		if err := promptMantis(cfg); err != nil {
			return err
		}
	}
	return nil
}

// promptMantis collects Mantis base_url + api_token, validates via
// connectivity.CheckMantis. 3 retries then offers to skip.
func promptMantis(cfg *appconfig.Config) error {
	fmt.Fprintln(prompt.Stderr)
	fmt.Fprintln(prompt.Stderr, "  Mantis base URL (e.g. https://mantis.example.com):")
	var baseURL string
	for attempt := 1; attempt <= 3; attempt++ {
		baseURL = strings.TrimRight(prompt.Line("URL: "), "/")
		if baseURL == "" {
			prompt.Fail("URL is required (attempt %d/3)", attempt)
			continue
		}
		break
	}
	if baseURL == "" {
		return nil
	}

	fmt.Fprintln(prompt.Stderr)
	fmt.Fprintln(prompt.Stderr, "  Mantis API token:")
	for attempt := 1; attempt <= 3; attempt++ {
		token := prompt.Hidden("Token: ")
		if token == "" {
			prompt.Fail("token is required (attempt %d/3)", attempt)
			continue
		}
		n, err := connectivity.CheckMantis(baseURL, token)
		if err != nil {
			prompt.Fail("%v (attempt %d/3)", err, attempt)
			if attempt == 3 {
				_ = prompt.YesNoDefault("  Skip Mantis setup?", true)
				return nil
			}
			continue
		}
		cfg.Mantis.BaseURL = baseURL
		cfg.Mantis.APIToken = token
		prompt.OK("Mantis connected (%d projects accessible)", n)
		return nil
	}
	return nil
}

func promptWorkerInit(cfg *workerconfig.Config) error {
	fmt.Fprintln(prompt.Stderr)

	fmt.Fprintln(prompt.Stderr, "  GitHub token (ghp_... or github_pat_...):")
	for attempt := 1; attempt <= 3; attempt++ {
		tok := prompt.Hidden("Token: ")
		if tok == "" {
			prompt.Fail("GitHub token is required")
			continue
		}
		username, err := connectivity.CheckGitHubToken(tok)
		if err != nil {
			prompt.Fail("%v (attempt %d/3)", err, attempt)
			continue
		}
		cfg.GitHub.Token = tok
		prompt.OK("Token valid (user: %s)", username)
		break
	}

	fmt.Fprintln(prompt.Stderr)
	for attempt := 1; attempt <= 3; attempt++ {
		addr := prompt.Line("Redis address: ")
		if addr == "" {
			prompt.Fail("Redis address is required")
			continue
		}
		if err := connectivity.CheckRedis(addr, "", 0, false); err != nil {
			prompt.Fail("Redis connect failed: %v (attempt %d/3)", err, attempt)
			continue
		}
		cfg.Redis.Addr = addr
		cfg.Queue.Transport = "redis"
		prompt.OK("Redis connected")
		break
	}

	fmt.Fprintln(prompt.Stderr)
	fmt.Fprintln(prompt.Stderr, "  Secret key (paste from app config — must match):")
	for attempt := 1; attempt <= 3; attempt++ {
		key := prompt.Hidden("Secret key: ")
		if _, err := hex.DecodeString(key); err != nil || len(key) != 64 {
			prompt.Fail("must be 64 hex chars (attempt %d/3)", attempt)
			continue
		}
		cfg.SecretKey = key
		prompt.OK("Secret key accepted")
		break
	}

	fmt.Fprintln(prompt.Stderr)
	names := make([]string, 0, len(cfg.Agents))
	for name := range cfg.Agents {
		names = append(names, name)
	}
	fmt.Fprintln(prompt.Stderr, "  Available providers:")
	for i, name := range names {
		fmt.Fprintf(prompt.Stderr, "    %d) %s\n", i+1, name)
	}
	for attempt := 1; attempt <= 3; attempt++ {
		input := prompt.Line("Select (comma-separated, e.g. 1,2): ")
		var selected []string
		for _, part := range strings.Split(input, ",") {
			part = strings.TrimSpace(part)
			idx := 0
			if _, err := fmt.Sscanf(part, "%d", &idx); err == nil && idx >= 1 && idx <= len(names) {
				selected = append(selected, names[idx-1])
			}
		}
		if len(selected) == 0 {
			prompt.Fail("At least one provider is required (attempt %d/3)", attempt)
			continue
		}
		cfg.Providers = selected
		break
	}
	return nil
}
