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

// StderrBaseAttrs returns the attribute set the stderr handler should carry
// when the process runs in JSON-stderr mode. In styled mode the function
// returns nil so the human-readable prefix layout stays uncluttered.
func StderrBaseAttrs(format, appName, version, commit string) []slog.Attr {
	if format != "json" {
		return nil
	}
	return buildBaseAttrs(appName, version, commit)
}

// FileBaseAttrs returns the attribute set the file handler should carry. The
// file handler is always JSON regardless of stderr_format, and post-mortem
// analysis needs build identity on every record — so this returns the full
// set unconditionally. Empty values are dropped; pod_name is added when the
// POD_NAME env var (K8s downward API) is set.
func FileBaseAttrs(appName, version, commit string) []slog.Attr {
	return buildBaseAttrs(appName, version, commit)
}

func buildBaseAttrs(appName, version, commit string) []slog.Attr {
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
