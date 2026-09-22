package middleware

import (
	"log/slog"
	"os"
	"strings"
)

// SetupLogger creates and configures a logger with the specified level and format.
// Returns a configured slog.Logger instance.
func SetupLogger(level, format string) *slog.Logger {
	// Parse level string to slog.Level
	var logLevel slog.Level
	switch strings.ToLower(level) {
	case "debug":
		logLevel = slog.LevelDebug
	case "info":
		logLevel = slog.LevelInfo
	case "warn":
		logLevel = slog.LevelWarn
	case "error":
		logLevel = slog.LevelError
	default:
		logLevel = slog.LevelInfo // default to info
	}

	// Create handler based on format
	var stdout slog.Handler
	opts := &slog.HandlerOptions{
		Level:     logLevel,
		AddSource: false,
	}
	if strings.ToLower(format) == "json" {
		stdout = slog.NewJSONHandler(os.Stdout, opts)
	} else {
		stdout = slog.NewTextHandler(os.Stdout, opts)
	}

	return slog.New(newTeeHandler(stdout, NewRingHandler(DefaultLogRing(), logLevel)))
}

// ComponentLogger creates a logger with a component attribute.
// Returns a logger that includes the component name in all log messages.
func ComponentLogger(name string) *slog.Logger {
	return slog.Default().With("component", name)
}
