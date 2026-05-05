package logging

import (
	"io"
	"log/slog"
	"os"
)

// BuildStderrHandler returns the stderr-bound slog.Handler for the given
// format. "json" routes through slog.NewJSONHandler so log aggregators can
// index every record; any other value (including the default "styled")
// returns the human-readable StyledTextHandler.
func BuildStderrHandler(format string, level slog.Level) slog.Handler {
	return buildStderrHandler(os.Stderr, format, level)
}

func buildStderrHandler(w io.Writer, format string, level slog.Level) slog.Handler {
	if format == "json" {
		return slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level})
	}
	return NewStyledTextHandler(w, &slog.HandlerOptions{Level: level})
}

// BaseAttrs returns the attribute set every record should carry when the
// process is running in JSON-stderr mode. In styled mode the function returns
// nil so the human-readable prefix layout stays uncluttered. Empty values are
// dropped from the result. pod_name comes from the K8s downward API
// (POD_NAME env var); when absent the attr is omitted.
func BaseAttrs(format, appName, version, commit string) []slog.Attr {
	if format != "json" {
		return nil
	}
	attrs := make([]slog.Attr, 0, 4)
	if appName != "" {
		attrs = append(attrs, slog.String(KeyAppName, appName))
	}
	if version != "" {
		attrs = append(attrs, slog.String(KeyAppVersion, version))
	}
	if commit != "" {
		attrs = append(attrs, slog.String(KeyAppCommit, commit))
	}
	if pod := os.Getenv("POD_NAME"); pod != "" {
		attrs = append(attrs, slog.String(KeyPodName, pod))
	}
	return attrs
}
