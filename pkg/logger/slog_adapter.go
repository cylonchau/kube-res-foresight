package logger

import (
	slogmulti "github.com/samber/slog-multi"
	"gopkg.in/natefinch/lumberjack.v2"
	"io"
	"log/slog"
	"os"
)

// slogLogger is an implementation of the Logger interface using Go's structured logging (slog).
type slogLogger struct {
	logger *slog.Logger
}

// newSlogLogger initializes a new slog-based logger. It configures handlers based on the provided pipeline settings.
func newSlogLogger(name string, config LoggerConfig) (Logger, error) {
	var handlers []slog.Handler

	level := parseSlogLevel(config.Level)

	for _, output := range config.Outputs {
		var writer io.Writer
		switch OutputType(output.Type) {
		case OutputStdout:
			writer = os.Stdout
		case OutputFile:
			maxBackups := output.File.MaxBackups
			if maxBackups <= 0 {
				maxBackups = 5 // Default to keeping 5 rotated log files.
			}
			writer = &lumberjack.Logger{
				Filename:   output.File.Filename,
				MaxSize:    ParseSizeMB(output.File.MaxSize),
				MaxBackups: maxBackups,
				MaxAge:     ParseAgeDays(output.File.MaxAge),
				Compress:   output.File.Compress,
			}
		case OutputLoki:
			// Placeholder for Loki integration; currently discards logs.
			url := output.Loki.URL
			if url == "" {
				url = "http://localhost:3100/loki/api/v1/push"
			}
			_ = url // Set default address even if unused for now.
			writer = io.Discard
		case OutputType("udp"):
			// Placeholder for UDP logging; currently discards logs.
			addr := output.UDP.Addr
			if addr == "" {
				addr = "localhost:514"
			}
			_ = addr // Set default syslog port even if unused for now.
			writer = io.Discard
		case OutputNull:
			writer = io.Discard
		default:
			// Unknown drivers are skipped. You can extend this for UDP, Loki, ELK, etc.
			continue
		}

		var handler slog.Handler
		opts := &slog.HandlerOptions{
			Level:     level,
			AddSource: config.EnableCaller,
		}

		if config.Format == FormatJSON {
			handler = slog.NewJSONHandler(writer, opts)
		} else {
			handler = slog.NewTextHandler(writer, opts)
		}
		handlers = append(handlers, handler)
	}

	if len(handlers) == 0 {
		return &slogLogger{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}, nil
	}

	// Use slog-multi to fan out logs to all configured handlers.
	multiHandler := slogmulti.Fanout(handlers...)

	logger := slog.New(multiHandler)
	if name != "" {
		logger = logger.With("component", name)
	}

	return &slogLogger{logger: logger}, nil
}

// parseSlogLevel maps our internal Level to slog's built-in Level types.
func parseSlogLevel(level Level) slog.Level {
	switch level {
	case LevelDebug:
		return slog.LevelDebug
	case LevelInfo:
		return slog.LevelInfo
	case LevelWarn:
		return slog.LevelWarn
	case LevelError:
		return slog.LevelError
	case LevelSilent:
		return slog.Level(100) // Use a very high value to effectively silence logs.
	default:
		return slog.LevelInfo
	}
}

// --- Logger Interface Implementation ---

func (s *slogLogger) Debug(msg string, fields ...Field) {
	s.logger.Debug(msg, toSlogArgs(fields)...)
}

func (s *slogLogger) Info(msg string, fields ...Field) {
	s.logger.Info(msg, toSlogArgs(fields)...)
}

func (s *slogLogger) Warn(msg string, fields ...Field) {
	s.logger.Warn(msg, toSlogArgs(fields)...)
}

func (s *slogLogger) Error(msg string, fields ...Field) {
	s.logger.Error(msg, toSlogArgs(fields)...)
}

func (s *slogLogger) Fatal(msg string, fields ...Field) {
	s.logger.Error("FATAL: "+msg, toSlogArgs(fields)...)
	os.Exit(1)
}

func (s *slogLogger) With(fields ...Field) Logger {
	return &slogLogger{logger: s.logger.With(toSlogArgs(fields)...)}
}

func (s *slogLogger) Named(name string) Logger {
	return &slogLogger{logger: s.logger.With("logger", name)}
}

func (s *slogLogger) Sync() error {
	return nil
}

// toSlogArgs converts our Field slice into a key-value pair slice expected by slog.
func toSlogArgs(fields []Field) []any {
	args := make([]any, 0, len(fields)*2)
	for _, f := range fields {
		args = append(args, f.Key, f.Value)
	}
	return args
}
