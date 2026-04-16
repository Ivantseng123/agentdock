package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

var rootCmd = &cobra.Command{
	Use:     "agentdock",
	Short:   "AgentDock — Slack-driven LLM agent orchestrator",
	Long:    "AgentDock listens to Slack events, dispatches work to external LLM agents, and delivers results back to your team.",
	Version: fmt.Sprintf("%s (commit %s, built %s)", version, commit, date),
}

func init() {
	addPersistentFlags(rootCmd)
}

// Execute runs the root command. Called from main().
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}
