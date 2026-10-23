package logger

import (
	"fmt"
	"strings"
	"sync"
)

// Level defines the log severity levels.
type Level string

const (
	LevelDebug  Level = "debug"
	LevelInfo   Level = "info"
	LevelWarn   Level = "warn"
	LevelError  Level = "error"
	LevelSilent Level = "silent"
)

// Format defines the supported log output formats.
type Format string

const (
	FormatText Format = "text"
	FormatJSON Format = "json"
)

// OutputType defines the supported log destinations.
type OutputType string

const (
	OutputStdout OutputType = "stdout"
	OutputFile   OutputType = "file"
	OutputNull   OutputType = "null"
	OutputLoki   OutputType = "loki"
)

// FileConfig defines the configuration for file-based log rotation.
type FileConfig struct {
	Filename   string `json:"filename" yaml:"filename"`
	MaxSize    string `json:"max_size" yaml:"max_size"` // Maximum size before rotation (e.g., "100M", "1G")
	MaxBackups int    `json:"max_backups" yaml:"max_backups"`
	MaxAge     string `json:"max_age" yaml:"max_age"` // Maximum retention period (e.g., "7d")
	Compress   bool   `json:"compress" yaml:"compress"`
}

// OutputConfig represents a single log output destination.
type OutputConfig struct {
	Type string     `json:"type" yaml:"type"`
	File FileConfig `json:"file" yaml:"file"`
	Loki LokiConfig `json:"loki" yaml:"loki"`
	UDP  UDPConfig  `json:"udp" yaml:"udp"`
}

// LokiConfig defines settings for pushing logs to Grafana Loki.
type LokiConfig struct {
	URL      string `json:"url" yaml:"url"`
	TenantID string `json:"tenant_id" yaml:"tenant_id"`
}

// UDPConfig defines settings for remote logging via UDP (e.g., Syslog).
type UDPConfig struct {
	Addr string `json:"addr" yaml:"addr"`
}

// LoggerConfig defines the configuration for a specific logger pipeline.
type LoggerConfig struct {
	Enabled      bool           `json:"enabled" yaml:"enabled"`
	Level        Level          `json:"level" yaml:"level"`
	Format       Format         `json:"format" yaml:"format"`
	Outputs      []OutputConfig `json:"outputs" yaml:"outputs"`
	EnableCaller bool           `json:"enable_caller" yaml:"enable_caller"`
}

// Config represents the complete global logging configuration.
type Config struct {
	Loggers map[string]LoggerConfig `json:"loggers" yaml:"loggers"`
}

// Field represents a key-value pair for structured logging.
type Field struct {
	Key   string
	Value interface{}
}

// Logger is the main interface for logging operations.
type Logger interface {
	Debug(msg string, fields ...Field)
	Info(msg string, fields ...Field)
	Warn(msg string, fields ...Field)
	Error(msg string, fields ...Field)
	Fatal(msg string, fields ...Field)

	With(fields ...Field) Logger
	Named(name string) Logger
	Sync() error
}

var (
	registry = make(map[string]Logger)
	mu       sync.RWMutex
)

const (
	LoggerNameBusiness = "business"
	LoggerNameSQL      = "sql"
)

// Initialize sets up all configured logger instances.
func Initialize(config Config) error {
	mu.Lock()

	for name, lConfig := range config.Loggers {
		if !lConfig.Enabled {
			continue
		}
		logger, err := newSlogLogger(name, lConfig)
		if err != nil {
			mu.Unlock()
			return fmt.Errorf("failed to init logger [%s]: %w", name, err)
		}
		registry[name] = logger
	}
	mu.Unlock()

	return nil
}

// GetLogger retrieves a logger instance by name.
func GetLogger(name string) Logger {
	mu.RLock()
	logger, ok := registry[name]
	mu.RUnlock()

	if ok {
		return logger
	}

	mu.Lock()
	defer mu.Unlock()

	// Double check to prevent race conditions.
	if l, ok := registry[name]; ok {
		return l
	}

	// Handle default fallbacks while avoiding recursive locks.
	targetName := name
	if name != LoggerNameBusiness && name != LoggerNameSQL {
		targetName = LoggerNameBusiness
		// If Business logger is already initialized, return it.
		if l, ok := registry[LoggerNameBusiness]; ok {
			return l
		}
	}

	var l Logger
	switch targetName {
	case LoggerNameBusiness:
		// Default Business Logger: Text format + Stdout
		l, _ = newSlogLogger(LoggerNameBusiness, LoggerConfig{
			Level:   LevelInfo,
			Format:  FormatText,
			Outputs: []OutputConfig{{Type: string(OutputStdout)}},
		})
	case LoggerNameSQL:
		// Default SQL Logger: JSON format + Discard (Null)
		l, _ = newSlogLogger(LoggerNameSQL, LoggerConfig{
			Level:   LevelDebug,
			Format:  FormatJSON,
			Outputs: []OutputConfig{{Type: string(OutputNull)}},
		})
	}

	registry[targetName] = l
	return l
}

// Default returns the default business logger.
func Default() Logger {
	return GetLogger(LoggerNameBusiness)
}

// --- Global Convenience Functions ---

func Debug(msg string, fields ...Field) { Default().Debug(msg, fields...) }
func Info(msg string, fields ...Field)  { Default().Info(msg, fields...) }
func Warn(msg string, fields ...Field)  { Default().Warn(msg, fields...) }
func Error(msg string, fields ...Field) { Default().Error(msg, fields...) }
func Fatal(msg string, fields ...Field) { Default().Fatal(msg, fields...) }

func With(fields ...Field) Logger { return Default().With(fields...) }
func Named(name string) Logger    { return Default().Named(name) }
func Sync() error                 { return Default().Sync() }

// --- Helper Functions ---

func String(key, value string) Field          { return Field{Key: key, Value: value} }
func Int(key string, value int) Field         { return Field{Key: key, Value: value} }
func Int64(key string, value int64) Field     { return Field{Key: key, Value: value} }
func Bool(key string, value bool) Field       { return Field{Key: key, Value: value} }
func Err(err error) Field                     { return Field{Key: "error", Value: err} }
func Any(key string, value interface{}) Field { return Field{Key: key, Value: value} }

// ParseSizeMB converts a human-readable size string (e.g., "1G", "10M") to megabytes.
func ParseSizeMB(s string) int {
	if s == "" {
		return 100 // Default to 100MB
	}
	var res int
	var unit string
	_, _ = fmt.Sscanf(s, "%d%s", &res, &unit)

	switch strings.ToLower(unit) {
	case "g", "gb":
		return res * 1024
	case "m", "mb":
		return res
	case "k", "kb":
		if res > 0 && res < 1024 {
			return 1 // Minimum 1MB
		}
		return res / 1024
	default:
		return res
	}
}

// ParseAgeDays converts a human-readable duration string (e.g., "7d", "1m") to days.
func ParseAgeDays(s string) int {
	if s == "" {
		return 7 // Default to 7 days
	}
	var res int
	var unit string
	_, _ = fmt.Sscanf(s, "%d%s", &res, &unit)

	switch strings.ToLower(unit) {
	case "y", "year":
		return res * 365
	case "m", "month":
		return res * 30
	case "w", "week":
		return res * 7
	case "d", "day":
		return res
	case "h", "hour":
		if res > 0 && res < 24 {
			return 1 // Minimum 1 day
		}
		return res / 24
	default:
		return res
	}
}
