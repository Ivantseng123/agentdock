package logging

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
)

// StyledTextHandler is a slog.Handler that renders log lines in the format:
//
//	HH:MM:SS LEVEL [Component][Phase] message key=value ...
//
// The "component" attr (set via WithAttrs) and "phase" attr (set per-record)
// are consumed into the prefix and do NOT appear in the key=value list.
// If either is missing the corresponding bracket is omitted entirely.
type StyledTextHandler struct {
	mu        *sync.Mutex
	w         io.Writer
	opts      slog.HandlerOptions
	component string   // extracted from WithAttrs
	group     string   // current group prefix (dot-separated)
	preAttrs  []string // pre-formatted "key=value" strings from WithAttrs (excl. component)
}

// NewStyledTextHandler creates a StyledTextHandler writing to w with the given options.
func NewStyledTextHandler(w io.Writer, opts *slog.HandlerOptions) *StyledTextHandler {
	if opts == nil {
		opts = &slog.HandlerOptions{}
	}
	return &StyledTextHandler{
		mu:   &sync.Mutex{},
		w:    w,
		opts: *opts,
	}
}

// Enabled reports whether the handler handles records at the given level.
func (h *StyledTextHandler) Enabled(_ context.Context, level slog.Level) bool {
	minLevel := slog.LevelInfo
	if h.opts.Level != nil {
		minLevel = h.opts.Level.Level()
	}
	return level >= minLevel
}

// Handle formats and writes the log record.
func (h *StyledTextHandler) Handle(_ context.Context, r slog.Record) error {
	// Extract phase from this record's attrs; collect remaining attrs.
	var phase string
	var recAttrs []string
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == "phase" {
			phase = a.Value.String()
			return true
		}
		recAttrs = append(recAttrs, h.formatAttr(h.group, a))
		return true
	})

	// Build the prefix brackets.
	var prefix string
	switch {
	case h.component != "" && phase != "":
		prefix = fmt.Sprintf("[%s][%s] ", h.component, phase)
	case h.component != "":
		prefix = fmt.Sprintf("[%s] ", h.component)
	case phase != "":
		prefix = fmt.Sprintf("[%s] ", phase)
	}

	// Level padded to 5 chars.
	levelStr := padLevel(r.Level)

	// Time in HH:MM:SS.
	timeStr := r.Time.Format("15:04:05")

	// Assemble all key=value pairs: pre-attrs then record attrs.
	allAttrs := make([]string, 0, len(h.preAttrs)+len(recAttrs))
	allAttrs = append(allAttrs, h.preAttrs...)
	allAttrs = append(allAttrs, recAttrs...)

	var buf bytes.Buffer
	buf.WriteString(timeStr)
	buf.WriteByte(' ')
	buf.WriteString(levelStr)
	buf.WriteByte(' ')
	buf.WriteString(prefix)
	buf.WriteString(r.Message)
	if len(allAttrs) > 0 {
		buf.WriteByte(' ')
		buf.WriteString(strings.Join(allAttrs, " "))
	}
	buf.WriteByte('\n')

	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := h.w.Write(buf.Bytes())
	return err
}

// WithAttrs returns a new handler with the given attributes pre-applied.
// "component" is extracted and stored separately; all other attrs are pre-formatted.
func (h *StyledTextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	clone := h.clone()
	for _, a := range attrs {
		// "component" is always a special key: captured into the prefix regardless of group.
		if a.Key == "component" {
			clone.component = a.Value.String()
			continue
		}
		clone.preAttrs = append(clone.preAttrs, h.formatAttr(h.group, a))
	}
	return clone
}

// WithGroup returns a new handler with the given group name prepended to all attribute keys.
func (h *StyledTextHandler) WithGroup(name string) slog.Handler {
	clone := h.clone()
	if clone.group == "" {
		clone.group = name
	} else {
		clone.group = clone.group + "." + name
	}
	return clone
}

// clone returns a shallow copy of the handler.
func (h *StyledTextHandler) clone() *StyledTextHandler {
	preAttrs := make([]string, len(h.preAttrs))
	copy(preAttrs, h.preAttrs)
	return &StyledTextHandler{
		mu:        h.mu,
		w:         h.w,
		opts:      h.opts,
		component: h.component,
		group:     h.group,
		preAttrs:  preAttrs,
	}
}

// formatAttr renders a slog.Attr as "key=value", prefixing with groupPrefix if set.
func (h *StyledTextHandler) formatAttr(groupPrefix string, a slog.Attr) string {
	key := a.Key
	if groupPrefix != "" {
		key = groupPrefix + "." + key
	}
	val := a.Value.Resolve()
	switch val.Kind() {
	case slog.KindString:
		return fmt.Sprintf("%s=%s", key, val.String())
	default:
		return fmt.Sprintf("%s=%v", key, val.Any())
	}
}

// padLevel returns the level string padded/truncated to exactly 5 characters.
func padLevel(l slog.Level) string {
	s := l.String() // "DEBUG", "INFO", "WARN", "ERROR"
	if len(s) >= 5 {
		return s[:5]
	}
	return s + strings.Repeat(" ", 5-len(s))
}
