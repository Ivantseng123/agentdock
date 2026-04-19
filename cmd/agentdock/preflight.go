package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"syscall"

	"golang.org/x/term"

	"github.com/Ivantseng123/agentdock/internal/config"
	"github.com/Ivantseng123/agentdock/shared/crypto"
	"github.com/Ivantseng123/agentdock/shared/queue"
)

const maxRetries = 3

// PreflightScope distinguishes which subcommand is running preflight. The App
// scope additionally requires Slack bot + app-level tokens; Worker skips them.
type PreflightScope string

const (
	ScopeApp    PreflightScope = "app"
	ScopeWorker PreflightScope = "worker"
)

// runPreflight validates Redis, GitHub token, Slack tokens (App only), and
// agent CLI availability. In interactive mode (terminal + missing values),
// prompts the user. Returns a map of keys the user was prompted for so the
// caller can persist them via delta-only save-back.
func runPreflight(cfg *config.Config, scope PreflightScope) (map[string]any, error) {
	prompted := map[string]any{}
	interactive := term.IsTerminal(int(syscall.Stdin)) && needsInput(cfg, scope)

	fmt.Fprintln(stderr)

	if err := preflightRedis(cfg, interactive, prompted); err != nil {
		return prompted, err
	}
	if err := preflightGitHub(cfg, interactive, prompted); err != nil {
		return prompted, err
	}
	if err := preflightProviders(cfg, interactive, prompted); err != nil {
		return prompted, err
	}

	if scope == ScopeApp {
		if err := preflightSlackBot(cfg, interactive, prompted); err != nil {
			return prompted, err
		}
		if err := preflightSlackApp(cfg, interactive, prompted); err != nil {
			return prompted, err
		}
	}

	if err := preflightSecretKey(cfg, interactive, prompted, scope); err != nil {
		return prompted, err
	}

	if err := preflightAgentCLIs(cfg, interactive); err != nil {
		return prompted, err
	}

	fmt.Fprintf(stderr, "\n  Starting %s with: %s\n\n", scope, strings.Join(cfg.Providers, ", "))
	return prompted, nil
}

// needsInput returns true if any required config value (for the given scope)
// is empty, meaning preflight should enter interactive mode when attached to
// a terminal.
func needsInput(cfg *config.Config, scope PreflightScope) bool {
	base := cfg.Redis.Addr == "" || cfg.GitHub.Token == "" || len(cfg.Providers) == 0 || cfg.SecretKey == ""
	if scope == ScopeApp {
		return base || cfg.Slack.BotToken == "" || cfg.Slack.AppToken == ""
	}
	return base
}

func preflightRedis(cfg *config.Config, interactive bool, prompted map[string]any) error {
	if cfg.Redis.Addr == "" {
		if !interactive {
			return fmt.Errorf("REDIS_ADDR is required")
		}
		for attempt := 1; attempt <= maxRetries; attempt++ {
			addr := promptLine("Redis address: ")
			if addr == "" {
				printFail("Redis address is required")
				if attempt < maxRetries {
					continue
				}
				return fmt.Errorf("max retries exceeded for Redis address")
			}
			if err := checkRedis(addr); err != nil {
				printFail("Redis connect failed: %v (attempt %d/%d)", err, attempt, maxRetries)
				if attempt == maxRetries {
					return fmt.Errorf("max retries exceeded for Redis")
				}
				continue
			}
			cfg.Redis.Addr = addr
			prompted["redis.addr"] = addr
			printOK("Redis connected")
			return nil
		}
		return nil
	}
	if err := checkRedis(cfg.Redis.Addr); err != nil {
		printFail("Redis connect failed: %v", err)
		return err
	}
	printOK("Redis connected (%s)", cfg.Redis.Addr)
	return nil
}

func preflightGitHub(cfg *config.Config, interactive bool, prompted map[string]any) error {
	if cfg.GitHub.Token == "" {
		if !interactive {
			return fmt.Errorf("GITHUB_TOKEN is required")
		}
		fmt.Fprintln(stderr)
		fmt.Fprintln(stderr, "  GitHub token (ghp_... or github_pat_...):")
		fmt.Fprintln(stderr, "  Generate at: https://github.com/settings/tokens")
		fmt.Fprintln(stderr, "  Required permissions: Contents (Read), Issues (Write)")
		for attempt := 1; attempt <= maxRetries; attempt++ {
			token := promptHidden("Token: ")
			if token == "" {
				printFail("Token is required")
				if attempt < maxRetries {
					continue
				}
				return fmt.Errorf("max retries exceeded for GitHub token")
			}
			username, err := checkGitHubToken(token)
			if err != nil {
				printFail("%v (attempt %d/%d)", err, attempt, maxRetries)
				if attempt == maxRetries {
					return fmt.Errorf("max retries exceeded for GitHub token")
				}
				continue
			}
			cfg.GitHub.Token = token
			prompted["github.token"] = token
			printOK("Token valid (user: %s)", username)
			return nil
		}
		return nil
	}
	username, err := checkGitHubToken(cfg.GitHub.Token)
	if err != nil {
		printFail("GitHub token invalid: %v", err)
		return err
	}
	printOK("Token valid (user: %s)", username)
	return nil
}

func preflightProviders(cfg *config.Config, interactive bool, prompted map[string]any) error {
	if len(cfg.Providers) == 0 {
		if !interactive {
			return fmt.Errorf("PROVIDERS is required")
		}
		fmt.Fprintln(stderr)
		agents := sortedAgentNames(cfg)
		fmt.Fprintln(stderr, "  Available providers:")
		for i, name := range agents {
			fmt.Fprintf(stderr, "    %d) %s\n", i+1, name)
		}
		for attempt := 1; attempt <= maxRetries; attempt++ {
			input := promptLine("Select (comma-separated, e.g. 1,2): ")
			selected := parseSelection(input, agents)
			if len(selected) == 0 {
				printFail("At least one provider is required (attempt %d/%d)", attempt, maxRetries)
				if attempt == maxRetries {
					return fmt.Errorf("max retries exceeded for provider selection")
				}
				continue
			}
			cfg.Providers = selected
			prompted["providers"] = selected
			return nil
		}
	}
	return nil
}

func preflightSlackBot(cfg *config.Config, interactive bool, prompted map[string]any) error {
	if cfg.Slack.BotToken != "" {
		userID, err := checkSlackToken(cfg.Slack.BotToken)
		if err != nil {
			printFail("Slack bot token invalid: %v", err)
			return err
		}
		printOK("Slack bot token valid (user_id: %s)", userID)
		return nil
	}
	if !interactive {
		return fmt.Errorf("SLACK_BOT_TOKEN is required")
	}
	fmt.Fprintln(stderr)
	fmt.Fprintln(stderr, "  Slack bot token (xoxb-...):")
	for attempt := 1; attempt <= maxRetries; attempt++ {
		token := promptHidden("Token: ")
		if token == "" {
			printFail("Slack bot token is required")
			if attempt == maxRetries {
				return fmt.Errorf("max retries exceeded for Slack bot token")
			}
			continue
		}
		userID, err := checkSlackToken(token)
		if err != nil {
			printFail("%v (attempt %d/%d)", err, attempt, maxRetries)
			if attempt == maxRetries {
				return fmt.Errorf("max retries exceeded for Slack bot token")
			}
			continue
		}
		cfg.Slack.BotToken = token
		prompted["slack.bot_token"] = token
		printOK("Slack bot token valid (user_id: %s)", userID)
		return nil
	}
	return fmt.Errorf("unreachable")
}

func preflightSlackApp(cfg *config.Config, interactive bool, prompted map[string]any) error {
	if cfg.Slack.AppToken != "" {
		if !strings.HasPrefix(cfg.Slack.AppToken, "xapp-") {
			return fmt.Errorf("Slack app token must start with xapp-")
		}
		printOK("Slack app token format OK")
		return nil
	}
	if !interactive {
		return fmt.Errorf("SLACK_APP_TOKEN is required")
	}
	fmt.Fprintln(stderr)
	fmt.Fprintln(stderr, "  Slack app-level token (xapp-...):")
	for attempt := 1; attempt <= maxRetries; attempt++ {
		token := promptHidden("Token: ")
		if token == "" || !strings.HasPrefix(token, "xapp-") {
			printFail("must start with xapp- (attempt %d/%d)", attempt, maxRetries)
			if attempt == maxRetries {
				return fmt.Errorf("max retries exceeded for Slack app token")
			}
			continue
		}
		cfg.Slack.AppToken = token
		prompted["slack.app_token"] = token
		printOK("Slack app token format OK")
		return nil
	}
	return fmt.Errorf("unreachable")
}

func preflightSecretKey(cfg *config.Config, interactive bool, prompted map[string]any, scope PreflightScope) error {
	if cfg.SecretKey != "" {
		decoded, err := config.DecodeSecretKey(cfg.SecretKey)
		if err != nil {
			printFail("secret_key invalid: %v", err)
			return err
		}
		if scope == ScopeWorker {
			if err := verifyBeaconFromConfig(cfg, decoded); err != nil {
				printFail("secret_key 與 app 不匹配: %v", err)
				return err
			}
		}
		printOK("Secret key configured")
		return nil
	}
	if !interactive {
		return fmt.Errorf("SECRET_KEY is required — set secret_key in config or SECRET_KEY env var")
	}
	fmt.Fprintln(stderr)
	fmt.Fprintln(stderr, "  Secret key for encrypting secrets between app and workers.")
	fmt.Fprintln(stderr, "  Must be a 64-character hex string (32 bytes).")

	// Only app can auto-generate; worker must receive the key from app.
	if scope == ScopeApp {
		if promptYesNo("  Auto-generate a key?") {
			keyBytes := make([]byte, 32)
			if _, err := rand.Read(keyBytes); err != nil {
				return fmt.Errorf("generate key: %w", err)
			}
			hexKey := hex.EncodeToString(keyBytes)
			cfg.SecretKey = hexKey
			prompted["secret_key"] = hexKey
			fmt.Fprintf(stderr, "  Generated: %s\n", hexKey)
			fmt.Fprintln(stderr, "  ⚠ Copy this key to all worker configs.")
			printOK("Secret key generated (will be saved to config)")
			return nil
		}
	} else {
		fmt.Fprintln(stderr, "  Paste the secret key from the app config:")
	}

	for attempt := 1; attempt <= maxRetries; attempt++ {
		key := promptHidden("Secret key: ")
		decoded, err := config.DecodeSecretKey(key)
		if err != nil {
			printFail("%v (attempt %d/%d)", err, attempt, maxRetries)
			if attempt == maxRetries {
				return fmt.Errorf("max retries exceeded for secret key")
			}
			continue
		}
		// Worker: verify key matches app's beacon immediately.
		if scope == ScopeWorker {
			if err := verifyBeaconFromConfig(cfg, decoded); err != nil {
				printFail("secret_key 與 app 不匹配 (attempt %d/%d)", attempt, maxRetries)
				if attempt == maxRetries {
					return fmt.Errorf("max retries exceeded — key does not match app")
				}
				continue
			}
		}
		cfg.SecretKey = key
		prompted["secret_key"] = key
		printOK("Secret key valid")
		return nil
	}
	return nil
}

// verifyBeaconFromConfig connects to Redis and verifies the secret key
// matches the app's beacon. Uses cfg.Redis for connection details.
func verifyBeaconFromConfig(cfg *config.Config, secretKey []byte) error {
	rdb, err := queue.NewRedisClient(queue.RedisConfig{
		Addr:     cfg.Redis.Addr,
		Password: cfg.Redis.Password,
		DB:       cfg.Redis.DB,
		TLS:      cfg.Redis.TLS,
	})
	if err != nil {
		return fmt.Errorf("connect to Redis for beacon check: %w", err)
	}
	defer rdb.Close()
	return crypto.VerifyBeacon(context.Background(), rdb, secretKey)
}

func preflightAgentCLIs(cfg *config.Config, interactive bool) error {
	fmt.Fprintln(stderr)
	var validProviders []string
	for _, name := range cfg.Providers {
		agent, ok := cfg.Agents[name]
		if !ok {
			printWarn("%s: not configured in agents", name)
			continue
		}
		version, err := checkAgentCLI(agent.Command)
		if err != nil {
			printWarn("%s: %v", name, err)
			continue
		}
		printOK("%s %s", name, version)
		validProviders = append(validProviders, name)
	}

	if len(validProviders) == 0 {
		printFail("No providers available")
		return fmt.Errorf("all providers failed CLI check")
	}

	if len(validProviders) < len(cfg.Providers) {
		if interactive {
			if !promptYesNo("\n  Some providers are unavailable. Continue anyway?") {
				return fmt.Errorf("user cancelled")
			}
		}
		cfg.Providers = validProviders
	}
	return nil
}

// sortedAgentNames returns agent names from config in stable order.
func sortedAgentNames(cfg *config.Config) []string {
	names := make([]string, 0, len(cfg.Agents))
	for name := range cfg.Agents {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// parseSelection parses "1,2" style input into agent names.
func parseSelection(input string, agents []string) []string {
	var selected []string
	for _, part := range strings.Split(input, ",") {
		part = strings.TrimSpace(part)
		idx := 0
		if _, err := fmt.Sscanf(part, "%d", &idx); err == nil && idx >= 1 && idx <= len(agents) {
			selected = append(selected, agents[idx-1])
		}
	}
	return selected
}
