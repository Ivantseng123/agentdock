package main

import (
	"fmt"

	"github.com/Ivantseng123/agentdock/app"
	appconfig "github.com/Ivantseng123/agentdock/app/config"

	"github.com/spf13/cobra"
)

var appConfigPath string

var appCmd = &cobra.Command{
	Use:          "app",
	Short:        "Run the main Slack bot",
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		appCfg, _, err := loadAppConfig(cmd, appConfigPath)
		if err != nil {
			return err
		}
		if err := appconfig.Validate(appCfg); err != nil {
			return err
		}
		if _, err := appconfig.RunPreflight(appCfg); err != nil {
			return fmt.Errorf("preflight: %w", err)
		}

		handle, err := app.Run(appCfg)
		if err != nil {
			return err
		}
		return handle.Wait()
	},
}

func init() {
	app.Version = version
	app.Commit = commit
	app.Date = date

	appCmd.Flags().StringVarP(&appConfigPath, "config", "c", "", "path to app config file (default ~/.config/agentdock/app.yaml)")
	appconfig.RegisterFlags(appCmd)
	rootCmd.AddCommand(appCmd)
}
