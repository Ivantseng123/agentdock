package main

import (
	"fmt"

	"github.com/Ivantseng123/agentdock/worker"
	workerconfig "github.com/Ivantseng123/agentdock/worker/config"

	"github.com/spf13/cobra"
)

var workerCmdConfigPath string

var workerCmd = &cobra.Command{
	Use:          "worker",
	Short:        "Run a worker process (Redis mode)",
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		wcfg, _, err := loadWorkerConfig(cmd, workerCmdConfigPath)
		if err != nil {
			return err
		}
		if err := workerconfig.Validate(wcfg); err != nil {
			return err
		}
		if _, err := workerconfig.RunPreflight(wcfg); err != nil {
			return fmt.Errorf("preflight: %w", err)
		}
		return worker.Run(wcfg)
	},
}

func init() {
	worker.Version = version
	worker.Commit = commit
	worker.Date = date

	workerCmd.Flags().StringVarP(&workerCmdConfigPath, "config", "c", "", "path to worker config file (default ~/.config/agentdock/worker.yaml)")
	workerconfig.RegisterFlags(workerCmd)
	rootCmd.AddCommand(workerCmd)
}
