// Package logging wires log/slog. Human-readable text by default; json when
// the tool runs unattended (cron on the VPS) and the output is shipped
// somewhere that parses it.
package logging

import (
	"io"
	"log/slog"
)

// New builds the process logger and installs it as slog's default so
// packages that reach for slog.Info directly still land in the right place.
func New(w io.Writer, level slog.Level, format string) *slog.Logger {
	opts := &slog.HandlerOptions{Level: level}

	var h slog.Handler
	if format == "json" {
		h = slog.NewJSONHandler(w, opts)
	} else {
		h = slog.NewTextHandler(w, opts)
	}

	l := slog.New(h)
	slog.SetDefault(l)
	return l
}
