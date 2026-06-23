// Package logger configures structured logging.
package logger

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
)

const (
	colorReset  = "\033[0m"
	colorRed    = "\033[31m"
	colorYellow = "\033[33m"
	colorBlue   = "\033[34m"
	colorCyan   = "\033[36m"
	colorGray   = "\033[90m"
)

var stderrMutex sync.Mutex

func New(level, format string, colorEnabled bool) *slog.Logger {
	var logLevel slog.Level
	switch strings.ToLower(level) {
	case "debug":
		logLevel = slog.LevelDebug
	case "warn":
		logLevel = slog.LevelWarn
	case "error":
		logLevel = slog.LevelError
	default:
		logLevel = slog.LevelInfo
	}

	var handler slog.Handler
	switch strings.ToLower(format) {
	case "json":
		handler = slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel})
	case "text":
		handler = slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel})
	default:
		handler = &prettyHandler{minLevel: logLevel, color: colorEnabled}
	}

	newLogger := slog.New(handler)
	slog.SetDefault(newLogger)
	return newLogger
}

type prettyHandler struct {
	minLevel   slog.Level
	attributes []slog.Attr
	color      bool
}

func (handler *prettyHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= handler.minLevel
}

func (handler *prettyHandler) Handle(_ context.Context, record slog.Record) error {
	var buffer bytes.Buffer

	levelText, levelColor := levelLabel(record.Level)
	bracketedLevel := fmt.Sprintf("[%s]", levelText)

	if handler.color {
		fmt.Fprintf(&buffer, "%s%s%s %s%-7s%s  %s",
			colorGray, record.Time.Format("2006-01-02 15:04:05"), colorReset,
			levelColor, bracketedLevel, colorReset,
			record.Message,
		)
	} else {
		fmt.Fprintf(&buffer, "%s %-7s  %s",
			record.Time.Format("2006-01-02 15:04:05"),
			bracketedLevel,
			record.Message,
		)
	}

	writeAttr := func(attribute slog.Attr) {
		attribute.Value = attribute.Value.Resolve()
		if attribute.Equal(slog.Attr{}) {
			return
		}
		if handler.color {
			fmt.Fprintf(&buffer, "  %s%s%s=%v", colorBlue, attribute.Key, colorReset, attribute.Value)
		} else {
			fmt.Fprintf(&buffer, "  %s=%v", attribute.Key, attribute.Value)
		}
	}

	for _, attribute := range handler.attributes {
		writeAttr(attribute)
	}
	record.Attrs(func(attribute slog.Attr) bool {
		writeAttr(attribute)
		return true
	})

	buffer.WriteByte('\n')

	stderrMutex.Lock()
	defer stderrMutex.Unlock()
	_, err := os.Stderr.Write(buffer.Bytes())
	return err
}

func (handler *prettyHandler) WithAttrs(attributes []slog.Attr) slog.Handler {
	combined := make([]slog.Attr, len(handler.attributes)+len(attributes))
	copy(combined, handler.attributes)
	copy(combined[len(handler.attributes):], attributes)
	return &prettyHandler{minLevel: handler.minLevel, attributes: combined, color: handler.color}
}

// WithGroup is required by slog.Handler but groups are not used in this codebase.
func (handler *prettyHandler) WithGroup(_ string) slog.Handler {
	return handler
}

func levelLabel(level slog.Level) (text, color string) {
	switch {
	case level >= slog.LevelError:
		return "ERROR", colorRed
	case level >= slog.LevelWarn:
		return "WARN", colorYellow
	case level >= slog.LevelInfo:
		return "INFO", colorCyan
	default:
		return "DEBUG", colorGray
	}
}
