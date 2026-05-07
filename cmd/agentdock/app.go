package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/Ivantseng123/agentdock/app"
	"github.com/Ivantseng123/agentdock/app/bot"
	appconfig "github.com/Ivantseng123/agentdock/app/config"
	"github.com/Ivantseng123/agentdock/shared/tracing"

	"github.com/spf13/cobra"
	"go.opentelemetry.io/otel"
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
		prompted, err := appconfig.RunPreflight(appCfg)
		if err != nil {
			return fmt.Errorf("preflight: %w", err)
		}

		// OTel tracing setup — runs before app.Run so the global
		// TracerProvider is in place before any library code touches
		// otel.Tracer(...). Empty endpoint = silent skip per ADR-0001.
		tracingShutdown := setupTracing(cmd.Context(), "agentdock-app", appCfg.Tracing.OTLPEndpoint)
		defer tracingShutdown()

		identity := bot.Identity{}
		if v, ok := prompted["slack.bot_user_id"].(string); ok {
			identity.UserID = v
		}
		if v, ok := prompted["slack.bot_id"].(string); ok {
			identity.BotID = v
		}
		if v, ok := prompted["slack.bot_username"].(string); ok {
			identity.Username = v
		}

		handle, err := app.Run(appCfg, identity)
		if err != nil {
			return err
		}
		return handle.Wait()
	},
}

// setupTracing builds and registers the global TracerProvider. The returned
// closure should be deferred at the caller. Failures are logged but never
// propagated — tracing must never block the main process from running.
func setupTracing(ctx context.Context, serviceName, endpoint string) func() {
	source := "silent skip"
	switch {
	case os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "":
		source = "from env"
	case endpoint != "":
		source = "from yaml"
	}
	slog.Info("OTel tracing setup",
		"phase", "啟動",
		"service_name", serviceName,
		"endpoint", tracing.RedactEndpoint(endpoint),
		"source", source,
	)

	tp, shutdown := tracing.BuildTracerProvider(ctx, tracing.Config{
		OTLPEndpoint:   endpoint,
		ServiceName:    serviceName,
		ServiceVersion: version,
	}, slog.Default())
	otel.SetTracerProvider(tp)

	return func() {
		// Use a fresh background ctx so cmd.Context() cancellation doesn't
		// cut off the flush window. The Shutdown helper applies its own 5s
		// timeout internally.
		if err := shutdown(context.Background()); err != nil {
			slog.Warn("OTel tracing shutdown returned error",
				"phase", "失敗",
				"error", err,
			)
		}
	}
}

func init() {
	app.Version = version
	app.Commit = commit
	app.Date = date

	appCmd.Flags().StringVarP(&appConfigPath, "config", "c", "", "path to app config file (default ~/.config/agentdock/app.yaml)")
	appconfig.RegisterFlags(appCmd)
	rootCmd.AddCommand(appCmd)
}
