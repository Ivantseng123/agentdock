package main

import (
	"encoding/json"
	"fmt"
	"os"

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

func init() {
	rootCmd.AddCommand(prReviewHelperCmd)

	prReviewFingerprintCmd.Flags().StringVar(&fpPRURL, "pr-url", "", "GitHub PR URL (required)")
	prReviewHelperCmd.AddCommand(prReviewFingerprintCmd)
}
