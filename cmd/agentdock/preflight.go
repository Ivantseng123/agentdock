package main

import (
	"fmt"
	"sort"
	"strings"
	"syscall"

	"golang.org/x/term"

	"agentdock/internal/config"
)

const maxRetries = 3

// runPreflight validates Redis, GitHub token, and agent CLI availability.
// In interactive mode (terminal + missing values), prompts the user.
func runPreflight(cfg *config.Config) error {
	interactive := term.IsTerminal(int(syscall.Stdin)) && needsInput(cfg)

	fmt.Fprintln(stderr)

	// --- Redis ---
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
			printOK("Redis connected")
			break
		}
	} else {
		if err := checkRedis(cfg.Redis.Addr); err != nil {
			printFail("Redis connect failed: %v", err)
			return err
		}
		printOK("Redis connected (%s)", cfg.Redis.Addr)
	}

	// --- GitHub Token ---
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
			printOK("Token valid (user: %s)", username)
			break
		}
	} else {
		username, err := checkGitHubToken(cfg.GitHub.Token)
		if err != nil {
			printFail("GitHub token invalid: %v", err)
			return err
		}
		printOK("Token valid (user: %s)", username)
	}

	// --- Providers ---
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
			break
		}
	}

	// --- Agent CLI version check ---
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

	fmt.Fprintf(stderr, "\n  Starting worker with: %s\n\n", strings.Join(cfg.Providers, ", "))
	return nil
}

// needsInput returns true if any required config value is empty.
func needsInput(cfg *config.Config) bool {
	return cfg.Redis.Addr == "" || cfg.GitHub.Token == "" || len(cfg.Providers) == 0
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
