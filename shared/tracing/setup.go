package tracing

import (
	"context"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// Config holds the runtime knobs for tracing setup.
//
// OTLPEndpoint == "" produces a real SDK with no exporter (silent skip per
// ADR-0001). Non-empty endpoint enables the OTLP gRPC exporter; transport
// errors are logged warn and suppressed.
type Config struct {
	OTLPEndpoint   string
	ServiceName    string
	ServiceVersion string
}

// shutdownTimeout caps every Shutdown() call so a hung collector cannot
// stall process exit. Caller-supplied ctx with a shorter deadline still
// wins (context.WithTimeout takes the earlier of the two).
const shutdownTimeout = 5 * time.Second

// BuildTracerProvider creates a TracerProvider plus a shutdown closure.
// The provider is always usable: an empty endpoint yields an SDK with no
// exporter so SpanContext still propagates through ctx and log handlers
// can pull trace_id, but no OTLP traffic is emitted. Any error wiring the
// exporter is logged warn and the provider is returned exporter-less
// rather than failing — see ADR-0001.
//
// Caller must defer the returned shutdown at process exit. logger may be
// nil (warnings will be silently dropped).
func BuildTracerProvider(ctx context.Context, cfg Config, logger *slog.Logger) (*sdktrace.TracerProvider, func(context.Context) error) {
	res := resource.NewWithAttributes(
		semconv.SchemaURL,
		semconv.ServiceName(cfg.ServiceName),
		semconv.ServiceVersion(cfg.ServiceVersion),
	)

	opts := []sdktrace.TracerProviderOption{
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
		sdktrace.WithResource(res),
	}

	if cfg.OTLPEndpoint != "" {
		exp, err := otlptracegrpc.New(ctx,
			otlptracegrpc.WithEndpoint(cfg.OTLPEndpoint),
			otlptracegrpc.WithInsecure(),
		)
		if err != nil {
			if logger != nil {
				logger.Warn("OTLP exporter init failed; continuing without exporter",
					"phase", "失敗",
					"endpoint", cfg.OTLPEndpoint,
					"error", err)
			}
		} else {
			// Pin BatchSpanProcessor sizing explicitly. Values match the SDK
			// defaults today (queue 2048, batch 512, 5s tick) so behavior is
			// unchanged, but they're now grep-able and easy to tune if the
			// "buffer 滿 → span loss" risk in the spec ever materializes.
			opts = append(opts, sdktrace.WithBatcher(exp,
				sdktrace.WithMaxQueueSize(2048),
				sdktrace.WithMaxExportBatchSize(512),
				sdktrace.WithBatchTimeout(5*time.Second),
			))
		}
	}

	tp := sdktrace.NewTracerProvider(opts...)

	shutdown := func(ctx context.Context) error {
		ctx, cancel := context.WithTimeout(ctx, shutdownTimeout)
		defer cancel()
		return tp.Shutdown(ctx)
	}

	return tp, shutdown
}
