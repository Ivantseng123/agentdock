package main

import (
	"github.com/Ivantseng123/agentdock/app"

	"github.com/spf13/cobra"
)

var appConfigPath string

var appCmd = &cobra.Command{
	Use:          "app",
	Short:        "Run the main Slack bot",
	SilenceUsage: true,
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		return loadAndStash(cmd, appConfigPath, ScopeApp)
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		return app.Run(cfgFromCtx(cmd.Context()))
	},
}

func init() {
	// Propagate build info from cmd (goreleaser sets main.version at link time)
	// into the app package so startup logs report the correct build.
	app.Version = version
	app.Commit = commit
	app.Date = date

	appCmd.Flags().StringVarP(&appConfigPath, "config", "c", "", "path to config file (default ~/.config/agentdock/config.yaml)")
	rootCmd.AddCommand(appCmd)
	rootCmd.AddCommand(workerCmd)
	addAppFlags(appCmd)
}
