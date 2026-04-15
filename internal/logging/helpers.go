package logging

import "log/slog"

// ComponentLogger returns a logger tagged with the given component name.
// The component attribute is intercepted by StyledTextHandler to render
// as a [Component] prefix on stderr.
func ComponentLogger(base *slog.Logger, component string) *slog.Logger {
	return base.With("component", component)
}
