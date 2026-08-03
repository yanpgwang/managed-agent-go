// Package obs holds Mango's process-level observability wiring. It is purely
// operational: the Claude Managed Agents contract documents no logging,
// health, or telemetry surface, so nothing here may influence a wire payload.
//
// Logging is structured (log/slog) with a configurable handler so a developer
// gets readable text and a production deployment gets machine-parsable JSON.
// Log records must never carry credentials; call sites are responsible for
// passing already-sanitized values (see internal/model.APIError).
package obs

import (
	"fmt"
	"io"
	"log/slog"
	"strings"
)

// Handler formats supported by NewLogger.
const (
	// FormatText is the human-readable development handler.
	FormatText = "text"
	// FormatJSON is the machine-readable production handler.
	FormatJSON = "json"
)

// DefaultFormat is used when no format is configured.
const DefaultFormat = FormatText

// Options configures the process logger.
type Options struct {
	// Format selects the handler: FormatText or FormatJSON. Empty means
	// DefaultFormat.
	Format string
	// Level is the minimum record level. The zero value is slog.LevelInfo.
	Level slog.Level
	// Role labels the process ("serve" or "orchestrate") on every record so a
	// shared log sink can separate the API and worker roles.
	Role string
}

// ParseFormat validates a handler-format string. An empty value selects
// DefaultFormat.
func ParseFormat(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "":
		return DefaultFormat, nil
	case FormatText:
		return FormatText, nil
	case FormatJSON:
		return FormatJSON, nil
	default:
		return "", fmt.Errorf("unsupported log format %q (want %s or %s)",
			value, FormatText, FormatJSON)
	}
}

// ParseLevel validates a level string. An empty value selects slog.LevelInfo.
func ParseLevel(value string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "":
		return slog.LevelInfo, nil
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf(
			"unsupported log level %q (want debug, info, warn, or error)", value)
	}
}

// NewLogger builds a logger writing to w. An unknown format is a configuration
// error rather than a silent fallback, so a typo cannot quietly change the
// shape of production logs.
func NewLogger(w io.Writer, opts Options) (*slog.Logger, error) {
	format, err := ParseFormat(opts.Format)
	if err != nil {
		return nil, err
	}
	handlerOptions := &slog.HandlerOptions{Level: opts.Level}
	var handler slog.Handler
	switch format {
	case FormatJSON:
		handler = slog.NewJSONHandler(w, handlerOptions)
	default:
		handler = slog.NewTextHandler(w, handlerOptions)
	}
	logger := slog.New(handler)
	if role := strings.TrimSpace(opts.Role); role != "" {
		logger = logger.With(slog.String("role", role))
	}
	return logger, nil
}

// Configure builds the logger for opts and installs it as the slog default so
// packages that log through the package-level slog functions inherit it. It is
// called once during process startup, before any goroutine that logs is
// started.
func Configure(w io.Writer, opts Options) (*slog.Logger, error) {
	logger, err := NewLogger(w, opts)
	if err != nil {
		return nil, err
	}
	slog.SetDefault(logger)
	return logger, nil
}
