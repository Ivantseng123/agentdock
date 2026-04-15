package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"
	"golang.org/x/term"
)

var (
	stderr  = os.Stderr
	scanner = bufio.NewScanner(os.Stdin)
)

// checkRedis verifies connectivity to a Redis server at addr.
func checkRedis(addr string) error {
	if addr == "" {
		return errors.New("address is empty")
	}
	client := redis.NewClient(&redis.Options{Addr: addr})
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	return client.Ping(ctx).Err()
}

// checkGitHubToken verifies the token can authenticate and has repository access.
// It returns the authenticated user's login name on success.
func checkGitHubToken(token string) (string, error) {
	if token == "" {
		return "", errors.New("token is empty")
	}

	httpClient := &http.Client{Timeout: 10 * time.Second}
	doReq := func(url string) (*http.Response, error) {
		req, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/vnd.github+json")
		return httpClient.Do(req)
	}

	// Step 1: verify identity.
	resp, err := doReq("https://api.github.com/user")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return "", errors.New("invalid or expired token")
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub /user returned HTTP %d", resp.StatusCode)
	}

	var user struct {
		Login string `json:"login"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return "", fmt.Errorf("decode /user response: %w", err)
	}
	login := user.Login

	// Step 2: verify repository access.
	resp2, err := doReq("https://api.github.com/user/repos?per_page=1")
	if err != nil {
		return "", err
	}
	defer resp2.Body.Close()

	if resp2.StatusCode == http.StatusForbidden || resp2.StatusCode == http.StatusNotFound {
		return "", fmt.Errorf("token lacks repository access (user: %s)", login)
	}
	if resp2.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub /user/repos returned HTTP %d", resp2.StatusCode)
	}

	return login, nil
}

// checkAgentCLI verifies that the named CLI binary is available and returns
// the first line of its --version output (stdout+stderr combined).
func checkAgentCLI(command string) (string, error) {
	cmd := exec.Command(command, "--version")

	out, err := cmd.CombinedOutput()
	if err != nil {
		var execErr *exec.Error
		if errors.As(err, &execErr) {
			return "", execErr
		}
		// Non-zero exit is fine — many CLIs exit non-zero for --version.
	}

	localScanner := bufio.NewScanner(bytes.NewReader(out))
	if localScanner.Scan() {
		return strings.TrimSpace(localScanner.Text()), nil
	}
	return "", nil
}

// printOK prints a success line to stderr.
func printOK(format string, args ...any) {
	fmt.Fprintf(stderr, "  \033[32m✓\033[0m %s\n", fmt.Sprintf(format, args...))
}

// printFail prints a failure line to stderr.
func printFail(format string, args ...any) {
	fmt.Fprintf(stderr, "  \033[31m✗\033[0m %s\n", fmt.Sprintf(format, args...))
}

// printWarn prints a warning line to stderr.
func printWarn(format string, args ...any) {
	fmt.Fprintf(stderr, "  \033[33m⚠\033[0m %s\n", fmt.Sprintf(format, args...))
}

// promptLine prints a prompt and reads a line of text input.
func promptLine(prompt string) string {
	fmt.Fprintf(stderr, "  %s", prompt)
	if scanner.Scan() {
		return strings.TrimSpace(scanner.Text())
	}
	return ""
}

// promptHidden prints a prompt and reads input without echo (for secrets).
func promptHidden(prompt string) string {
	fmt.Fprintf(stderr, "  %s", prompt)
	b, err := term.ReadPassword(int(syscall.Stdin))
	fmt.Fprintln(stderr) // newline after hidden input
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// promptYesNo prints a yes/no prompt. Default is yes.
func promptYesNo(prompt string) bool {
	answer := promptLine(fmt.Sprintf("%s [Y/n]: ", prompt))
	return answer == "" || strings.ToLower(answer) == "y" || strings.ToLower(answer) == "yes"
}
