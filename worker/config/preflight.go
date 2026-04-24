package config

import (
	"fmt"
	"sort"
	"strings"

	"github.com/Ivantseng123/agentdock/shared/connectivity"
	"github.com/Ivantseng123/agentdock/shared/crypto"
	"github.com/Ivantseng123/agentdock/shared/prompt"
	"github.com/Ivantseng123/agentdock/shared/queue"
)

const maxRetries = 3

// RunPreflight validates worker-scope requirements (Redis + beacon, secret_key,
// optional GitHub override, providers, agent CLI availability). Redis/secret_key
// come first because they establish the encrypted-secret delivery path — once
// that's in place, the app provides GH_TOKEN via each job and a worker-local
// github.token is only needed to override it. Interactive prompts fire only on
// a terminal with missing required values.
func RunPreflight(cfg *Config) (map[string]any, error) {
	prompted := map[string]any{}
	interactive := prompt.IsTerminal() && needsInput(cfg)

	fmt.Fprintln(prompt.Stderr)

	// Gate on git binary version up front: every other check can pass and
	// the worker still hit a silent HTTP 401 at first fetch if the local git
	// predates GIT_CONFIG_COUNT (introduced 2.31). Fail fast with a pointer
	// instead of watching jobs fail later with no explanation (#179).
	if err := preflightGit(); err != nil {
		prompt.Fail("%v", err)
		return prompted, err
	}

	if cfg.Queue.Transport == "redis" {
		if err := preflightRedis(cfg, interactive, prompted); err != nil {
			return prompted, err
		}
		if err := preflightSecretKey(cfg, interactive, prompted); err != nil {
			return prompted, err
		}
	}

	if err := preflightGitHub(cfg, interactive, prompted); err != nil {
		return prompted, err
	}

	if err := preflightProviders(cfg, interactive, prompted); err != nil {
		return prompted, err
	}
	if err := preflightAgentCLIs(cfg, interactive); err != nil {
		return prompted, err
	}

	fmt.Fprintf(prompt.Stderr, "\n  Starting worker with: %s\n\n", strings.Join(cfg.Providers, ", "))
	return prompted, nil
}

func needsInput(cfg *Config) bool {
	if cfg.Queue.Transport == "redis" {
		return cfg.Redis.Addr == "" || cfg.SecretKey == "" || len(cfg.Providers) == 0
	}
	return cfg.GitHub.Token == "" || len(cfg.Providers) == 0
}

func preflightGit() error {
	if err := connectivity.CheckGitVersion(); err != nil {
		return err
	}
	prompt.OK("Git binary supports env-based auth")
	return nil
}

func preflightGitHub(cfg *Config, interactive bool, prompted map[string]any) error {
	if cfg.GitHub.Token != "" {
		username, err := connectivity.CheckGitHubToken(cfg.GitHub.Token)
		if err != nil {
			prompt.Fail("GitHub token invalid: %v", err)
			return err
		}
		prompt.OK("Token valid (user: %s)", username)
		return nil
	}
	// Empty worker github.token is fine when app delivers GH_TOKEN via
	// encrypted job secrets (redis transport + secret_key path). The worker
	// token is only a per-worker override.
	if cfg.Queue.Transport == "redis" && cfg.SecretKey != "" {
		prompt.OK("GitHub token will be provided by app via encrypted secrets")
		return nil
	}
	if !interactive {
		return fmt.Errorf("GITHUB_TOKEN is required")
	}
	fmt.Fprintln(prompt.Stderr)
	fmt.Fprintln(prompt.Stderr, "  GitHub token (ghp_... or github_pat_...):")
	fmt.Fprintln(prompt.Stderr, "  Generate at: https://github.com/settings/tokens")
	for attempt := 1; attempt <= maxRetries; attempt++ {
		token := prompt.Hidden("Token: ")
		if token == "" {
			prompt.Fail("Token is required")
			if attempt == maxRetries {
				return fmt.Errorf("max retries exceeded for GitHub token")
			}
			continue
		}
		username, err := connectivity.CheckGitHubToken(token)
		if err != nil {
			prompt.Fail("%v (attempt %d/%d)", err, attempt, maxRetries)
			if attempt == maxRetries {
				return fmt.Errorf("max retries exceeded for GitHub token")
			}
			continue
		}
		cfg.GitHub.Token = token
		prompted["github.token"] = token
		prompt.OK("Token valid (user: %s)", username)
		return nil
	}
	return fmt.Errorf("unreachable")
}

func preflightRedis(cfg *Config, interactive bool, prompted map[string]any) error {
	if cfg.Redis.Addr != "" {
		if err := connectivity.CheckRedis(cfg.Redis.Addr, cfg.Redis.Password, cfg.Redis.DB, cfg.Redis.TLS); err != nil {
			prompt.Fail("Redis connect failed: %v", err)
			return err
		}
		prompt.OK("Redis connected (%s)", cfg.Redis.Addr)
		return nil
	}
	if !interactive {
		return fmt.Errorf("REDIS_ADDR is required")
	}
	for attempt := 1; attempt <= maxRetries; attempt++ {
		addr := prompt.Line("Redis address: ")
		if addr == "" {
			prompt.Fail("Redis address is required")
			if attempt == maxRetries {
				return fmt.Errorf("max retries exceeded for Redis address")
			}
			continue
		}
		if err := connectivity.CheckRedis(addr, "", 0, false); err != nil {
			prompt.Fail("Redis connect failed: %v (attempt %d/%d)", err, attempt, maxRetries)
			if attempt == maxRetries {
				return fmt.Errorf("max retries exceeded for Redis")
			}
			continue
		}
		cfg.Redis.Addr = addr
		prompted["redis.addr"] = addr
		prompt.OK("Redis connected")
		return nil
	}
	return fmt.Errorf("unreachable")
}

func preflightSecretKey(cfg *Config, interactive bool, prompted map[string]any) error {
	if cfg.SecretKey != "" {
		decoded, err := crypto.DecodeSecretKey(cfg.SecretKey)
		if err != nil {
			prompt.Fail("secret_key invalid: %v", err)
			return err
		}
		if err := verifyBeacon(cfg, decoded); err != nil {
			prompt.Fail("secret_key 與 app 不匹配: %v", err)
			return err
		}
		prompt.OK("Secret key configured")
		return nil
	}
	if !interactive {
		return fmt.Errorf("SECRET_KEY is required — set secret_key in config or SECRET_KEY env var")
	}
	fmt.Fprintln(prompt.Stderr)
	fmt.Fprintln(prompt.Stderr, "  Secret key for decrypting secrets from app.")
	fmt.Fprintln(prompt.Stderr, "  Paste the secret key from the app config:")
	for attempt := 1; attempt <= maxRetries; attempt++ {
		key := prompt.Hidden("Secret key: ")
		decoded, err := crypto.DecodeSecretKey(key)
		if err != nil {
			prompt.Fail("%v (attempt %d/%d)", err, attempt, maxRetries)
			if attempt == maxRetries {
				return fmt.Errorf("max retries exceeded for secret key")
			}
			continue
		}
		if err := verifyBeacon(cfg, decoded); err != nil {
			prompt.Fail("secret_key 與 app 不匹配 (attempt %d/%d)", attempt, maxRetries)
			if attempt == maxRetries {
				return fmt.Errorf("max retries exceeded — key does not match app")
			}
			continue
		}
		cfg.SecretKey = key
		prompted["secret_key"] = key
		prompt.OK("Secret key valid")
		return nil
	}
	return fmt.Errorf("unreachable")
}

func verifyBeacon(cfg *Config, secretKey []byte) error {
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
	return connectivity.VerifySecretBeacon(rdb, secretKey)
}

func preflightProviders(cfg *Config, interactive bool, prompted map[string]any) error {
	if len(cfg.Providers) > 0 {
		return nil
	}
	if !interactive {
		return fmt.Errorf("PROVIDERS is required")
	}
	fmt.Fprintln(prompt.Stderr)
	agents := sortedAgentNames(cfg)
	fmt.Fprintln(prompt.Stderr, "  Available providers:")
	for i, name := range agents {
		fmt.Fprintf(prompt.Stderr, "    %d) %s\n", i+1, name)
	}
	for attempt := 1; attempt <= maxRetries; attempt++ {
		input := prompt.Line("Select (comma-separated, e.g. 1,2): ")
		selected := parseSelection(input, agents)
		if len(selected) == 0 {
			prompt.Fail("At least one provider is required (attempt %d/%d)", attempt, maxRetries)
			if attempt == maxRetries {
				return fmt.Errorf("max retries exceeded for provider selection")
			}
			continue
		}
		cfg.Providers = selected
		prompted["providers"] = selected
		return nil
	}
	return fmt.Errorf("unreachable")
}

func preflightAgentCLIs(cfg *Config, interactive bool) error {
	fmt.Fprintln(prompt.Stderr)
	var validProviders []string
	for _, name := range cfg.Providers {
		agent, ok := cfg.Agents[name]
		if !ok {
			prompt.Warn("%s: not configured in agents", name)
			continue
		}
		version, err := prompt.CheckAgentCLI(agent.Command)
		if err != nil {
			prompt.Warn("%s: %v", name, err)
			continue
		}
		prompt.OK("%s %s", name, version)
		validProviders = append(validProviders, name)
	}

	if len(validProviders) == 0 {
		prompt.Fail("No providers available")
		return fmt.Errorf("all providers failed CLI check")
	}
	if len(validProviders) < len(cfg.Providers) {
		if interactive {
			if !prompt.YesNo("\n  Some providers are unavailable. Continue anyway?") {
				return fmt.Errorf("user cancelled")
			}
		}
		cfg.Providers = validProviders
	}
	return nil
}

// sortedAgentNames returns agent names from config in stable order.
func sortedAgentNames(cfg *Config) []string {
	names := make([]string, 0, len(cfg.Agents))
	for name := range cfg.Agents {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// parseSelection parses "1,2" input into agent names by index.
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
