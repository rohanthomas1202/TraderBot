package logging

import (
	"log/slog"
	"os"
)

// SetupLogger creates the standard JSON structured logger for a service.
// It sets the global default and returns the logger for local use.
func SetupLogger(service string) *slog.Logger {
	handler := slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})
	logger := slog.New(handler).With("service", service)
	slog.SetDefault(logger)
	return logger
}
