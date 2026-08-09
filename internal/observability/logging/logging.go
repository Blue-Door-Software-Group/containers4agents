package logging

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/Blue-Door-Software-Group/containers4agents/internal/api/middleware"
)

func levelFromString(level string) slog.Level {
	var slogLevel slog.Level
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		slogLevel = slog.LevelDebug
	case "warn":
		slogLevel = slog.LevelWarn
	case "error":
		slogLevel = slog.LevelError
	default:
		slogLevel = slog.LevelInfo
	}
	return slogLevel
}

func New(level string) *slog.Logger {
	return NewWithWriter(level, os.Stdout)
}

func NewWithWriter(level string, writer io.Writer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(writer, &slog.HandlerOptions{
		Level: levelFromString(level),
	}))
}

func WithRequestID(ctx context.Context, logger *slog.Logger) *slog.Logger {
	if logger == nil {
		logger = slog.Default()
	}
	requestID := middleware.GetRequestID(ctx)
	if requestID == "" {
		return logger
	}
	return logger.With("request_id", requestID)
}

func ErrorCategory(err error) string {
	switch {
	case err == nil:
		return "none"
	case errors.Is(err, context.Canceled):
		return "context_canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	default:
		return "internal_error"
	}
}

// ErrorSummary returns a sanitized, low-cardinality error summary safe for logs.
// It intentionally avoids including raw error strings that might contain user payloads.
func ErrorSummary(err error) string {
	switch {
	case err == nil:
		return "none"
	case errors.Is(err, context.Canceled):
		return "context_canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	default:
		return "redacted"
	}
}
