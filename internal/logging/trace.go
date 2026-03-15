package logging

import (
	"context"
	"log/slog"
)

type contextKey string

const traceIDKey contextKey = "trace_id"

// WithTraceID stores a trace_id in the context.
func WithTraceID(ctx context.Context, traceID string) context.Context {
	return context.WithValue(ctx, traceIDKey, traceID)
}

// TraceID extracts trace_id from context, returns "" if absent.
func TraceID(ctx context.Context) string {
	if v, ok := ctx.Value(traceIDKey).(string); ok {
		return v
	}
	return ""
}

// LoggerWithTrace returns a logger with trace_id attached if present in ctx.
func LoggerWithTrace(ctx context.Context, logger *slog.Logger) *slog.Logger {
	if tid := TraceID(ctx); tid != "" {
		return logger.With("trace_id", tid)
	}
	return logger
}
