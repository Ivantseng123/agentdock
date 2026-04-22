package config

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/Ivantseng123/agentdock/shared/connectivity"
	"github.com/Ivantseng123/agentdock/shared/crypto"
	"github.com/Ivantseng123/agentdock/shared/prompt"
)

const maxRetries = 3

// RunPreflight validates tokens / redis / secret key and, when running on a
// terminal with missing values, prompts for them. Returns the set of keys the
// caller should persist via delta-only save-back.
//
// App scope: Slack bot + app tokens are required; GitHub + Redis + SecretKey
// are prompted for in redis mode.
func RunPreflight(cfg *Config) (map[string]any, error) {
	prompted := map[string]any{}
	interactive := prompt.IsTerminal() && needsInput(cfg)

	fmt.Fprintln(prompt.Stderr)

	if err := preflightSlackBot(cfg, interactive, prompted); err != nil {
		return prompted, err
	}
	if err := preflightSlackApp(cfg, interactive, prompted); err != nil {
		return prompted, err
	}
	if err := preflightGitHub(cfg, interactive, prompted); err != nil {
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

	return prompted, nil
}

func needsInput(cfg *Config) bool {
	base := cfg.Slack.BotToken == "" || cfg.Slack.AppToken == "" || cfg.GitHub.Token == ""
	if cfg.Queue.Transport == "redis" {
		return base || cfg.Redis.Addr == "" || cfg.SecretKey == ""
	}
	return base
}

func preflightSlackBot(cfg *Config, interactive bool, prompted map[string]any) error {
	if cfg.Slack.BotToken != "" {
		identity, err := connectivity.CheckSlackToken(cfg.Slack.BotToken)
		if err != nil {
			prompt.Fail("Slack bot token invalid: %v", err)
			return err
		}
		prompt.OK("Slack bot token valid (user_id: %s)", identity.UserID)
		prompted["slack.bot_user_id"] = identity.UserID
		prompted["slack.bot_id"] = identity.BotID
		prompted["slack.bot_username"] = identity.Username
		return nil
	}
	if !interactive {
		return fmt.Errorf("SLACK_BOT_TOKEN is required")
	}
	fmt.Fprintln(prompt.Stderr)
	fmt.Fprintln(prompt.Stderr, "  Slack bot token (xoxb-...):")
	for attempt := 1; attempt <= maxRetries; attempt++ {
		token := prompt.Hidden("Token: ")
		if token == "" {
			prompt.Fail("Slack bot token is required")
			if attempt == maxRetries {
				return fmt.Errorf("max retries exceeded for Slack bot token")
			}
			continue
		}
		identity, err := connectivity.CheckSlackToken(token)
		if err != nil {
			prompt.Fail("%v (attempt %d/%d)", err, attempt, maxRetries)
			if attempt == maxRetries {
				return fmt.Errorf("max retries exceeded for Slack bot token")
			}
			continue
		}
		cfg.Slack.BotToken = token
		prompted["slack.bot_token"] = token
		prompted["slack.bot_user_id"] = identity.UserID
		prompted["slack.bot_id"] = identity.BotID
		prompted["slack.bot_username"] = identity.Username
		prompt.OK("Slack bot token valid (user_id: %s)", identity.UserID)
		return nil
	}
	return fmt.Errorf("unreachable")
}

func preflightSlackApp(cfg *Config, interactive bool, prompted map[string]any) error {
	if cfg.Slack.AppToken != "" {
		if !strings.HasPrefix(cfg.Slack.AppToken, "xapp-") {
			return fmt.Errorf("Slack app token must start with xapp-")
		}
		prompt.OK("Slack app token format OK")
		return nil
	}
	if !interactive {
		return fmt.Errorf("SLACK_APP_TOKEN is required")
	}
	fmt.Fprintln(prompt.Stderr)
	fmt.Fprintln(prompt.Stderr, "  Slack app-level token (xapp-...):")
	for attempt := 1; attempt <= maxRetries; attempt++ {
		token := prompt.Hidden("Token: ")
		if token == "" || !strings.HasPrefix(token, "xapp-") {
			prompt.Fail("must start with xapp- (attempt %d/%d)", attempt, maxRetries)
			if attempt == maxRetries {
				return fmt.Errorf("max retries exceeded for Slack app token")
			}
			continue
		}
		cfg.Slack.AppToken = token
		prompted["slack.app_token"] = token
		prompt.OK("Slack app token format OK")
		return nil
	}
	return fmt.Errorf("unreachable")
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
	if !interactive {
		return fmt.Errorf("GITHUB_TOKEN is required")
	}
	fmt.Fprintln(prompt.Stderr)
	fmt.Fprintln(prompt.Stderr, "  GitHub token (ghp_... or github_pat_...):")
	fmt.Fprintln(prompt.Stderr, "  Generate at: https://github.com/settings/tokens")
	fmt.Fprintln(prompt.Stderr, "  Required permissions: Contents (Read), Issues (Write)")
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
		if _, err := crypto.DecodeSecretKey(cfg.SecretKey); err != nil {
			prompt.Fail("secret_key invalid: %v", err)
			return err
		}
		prompt.OK("Secret key configured")
		return nil
	}
	if !interactive {
		return fmt.Errorf("SECRET_KEY is required — set secret_key in config or SECRET_KEY env var")
	}
	fmt.Fprintln(prompt.Stderr)
	fmt.Fprintln(prompt.Stderr, "  Secret key for encrypting secrets between app and workers.")
	fmt.Fprintln(prompt.Stderr, "  Must be a 64-character hex string (32 bytes).")

	if prompt.YesNo("  Auto-generate a key?") {
		keyBytes := make([]byte, 32)
		if _, err := rand.Read(keyBytes); err != nil {
			return fmt.Errorf("generate key: %w", err)
		}
		hexKey := hex.EncodeToString(keyBytes)
		cfg.SecretKey = hexKey
		prompted["secret_key"] = hexKey
		fmt.Fprintf(prompt.Stderr, "  Generated: %s\n", hexKey)
		fmt.Fprintln(prompt.Stderr, "  ⚠ Copy this key to all worker configs.")
		prompt.OK("Secret key generated (will be saved to config)")
		return nil
	}

	for attempt := 1; attempt <= maxRetries; attempt++ {
		key := prompt.Hidden("Secret key: ")
		if _, err := crypto.DecodeSecretKey(key); err != nil {
			prompt.Fail("%v (attempt %d/%d)", err, attempt, maxRetries)
			if attempt == maxRetries {
				return fmt.Errorf("max retries exceeded for secret key")
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
