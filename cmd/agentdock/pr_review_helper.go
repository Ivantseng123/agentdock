package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/Ivantseng123/agentdock/shared/prreview"
	"github.com/spf13/cobra"
)

var prReviewHelperCmd = &cobra.Command{
	Use:    "pr-review-helper",
	Short:  "Internal helper invoked by the github-pr-review skill",
	Hidden: true,
	Long: "pr-review-helper hosts the deterministic parts of the PR review " +
		"workflow: repo fingerprinting and the validate-before-post step for " +
		"inline comments. The github-pr-review skill invokes these subcommands; " +
		"end users should not run them directly.",
}

var fpPRURL string

var prReviewFingerprintCmd = &cobra.Command{
	Use:   "fingerprint",
	Short: "Inspect the cloned repo + PR to emit a fingerprint JSON on stdout",
	Long: "fingerprint runs in the cloned repo (cwd). It probes the local " +
		"filesystem for language/style/framework markers, then fetches the PR's " +
		"files list from GitHub to compute pr_touched_languages and pr_subprojects.",
	RunE: func(cmd *cobra.Command, args []string) error {
		if fpPRURL == "" {
			return fmt.Errorf("--pr-url is required")
		}
		token := os.Getenv("GITHUB_TOKEN")
		if token == "" {
			return fmt.Errorf("%s", prreview.ErrMissingToken)
		}
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		fp, err := prreview.Fingerprint(cmd.Context(), cwd, fpPRURL, token, prreview.FingerprintOptions{})
		if err != nil {
			return err
		}
		out, err := json.Marshal(fp)
		if err != nil {
			return err
		}
		fmt.Println(string(out))
		return nil
	},
}

var (
	vapPRURL  string
	vapDryRun bool
)

var prReviewValidateAndPostCmd = &cobra.Command{
	Use:   "validate-and-post",
	Short: "Read review JSON from stdin, validate against PR diff, then POST to GitHub",
	Long: "Reads a ReviewJSON document on stdin, fetches the PR's files from " +
		"GitHub, drops comments on lines outside the diff, truncates over-long " +
		"content, and submits the review as a single POST. Helper always uses " +
		"`git rev-parse HEAD` in cwd as commit_id — base or app-provided SHAs " +
		"are ignored.",
	RunE: func(cmd *cobra.Command, args []string) error {
		if vapPRURL == "" {
			return fmt.Errorf("--pr-url is required")
		}
		token := os.Getenv("GITHUB_TOKEN")
		if token == "" {
			return fmt.Errorf("%s", prreview.ErrMissingToken)
		}

		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return fmt.Errorf("read stdin: %w", err)
		}
		var review prreview.ReviewJSON
		if err := json.Unmarshal(bytes.TrimSpace(data), &review); err != nil {
			return fmt.Errorf("%s: %w", prreview.ErrReviewSchemaInvalid, err)
		}

		commitID, err := gitRevParseHEAD(cmd.Context())
		if err != nil {
			return fmt.Errorf("%s: %w", prreview.ErrGitRevParseFailed, err)
		}

		dryRun := vapDryRun
		if !dryRun && os.Getenv("DRY_RUN") == "1" {
			dryRun = true
		}

		res, err := prreview.ValidateAndPost(cmd.Context(), prreview.ValidateAndPostInput{
			Review:   &review,
			PRURL:    vapPRURL,
			CommitID: commitID,
			Token:    token,
			DryRun:   dryRun,
		})
		if err != nil {
			out, _ := json.Marshal(prreview.FatalResult{Error: err.Error(), Posted: 0})
			fmt.Println(string(out))
			cmd.SilenceErrors = true
			cmd.SilenceUsage = true
			os.Exit(2)
		}
		out, _ := json.Marshal(res)
		fmt.Println(string(out))

		if !dryRun && res.Skipped > 0 {
			os.Exit(1)
		}
		if dryRun && res.Skipped > 0 {
			os.Exit(1)
		}
		return nil
	},
}

func gitRevParseHEAD(ctx context.Context) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return "", fmt.Errorf("git rev-parse HEAD: %s", strings.TrimSpace(string(exitErr.Stderr)))
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func init() {
	rootCmd.AddCommand(prReviewHelperCmd)

	prReviewFingerprintCmd.Flags().StringVar(&fpPRURL, "pr-url", "", "GitHub PR URL (required)")
	prReviewHelperCmd.AddCommand(prReviewFingerprintCmd)

	prReviewValidateAndPostCmd.Flags().StringVar(&vapPRURL, "pr-url", "", "GitHub PR URL (required)")
	prReviewValidateAndPostCmd.Flags().BoolVar(&vapDryRun, "dry-run", false, "Validate + show payload; do not POST. DRY_RUN=1 env var also enables.")
	prReviewHelperCmd.AddCommand(prReviewValidateAndPostCmd)
}
