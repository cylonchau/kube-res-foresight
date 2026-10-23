package logger

import (
	"github.com/stretchr/testify/assert"
	"os"
	"testing"
)

func TestSlogLogger_MultiOutput(t *testing.T) {
	// Prepare test configuration
	config := Config{
		Loggers: map[string]LoggerConfig{
			LoggerNameBusiness: {
				Enabled: true,
				Level:   LevelInfo,
				Format:  FormatText,
				Outputs: []OutputConfig{
					{Type: string(OutputStdout)},
				},
			},
			LoggerNameSQL: {
				Enabled: true,
				Level:   LevelDebug,
				Format:  FormatJSON,
				Outputs: []OutputConfig{
					{Type: string(OutputNull)}, // SQL output to Null to simulate toggle
				},
			},
		},
	}

	// Initialize
	err := Initialize(config)
	assert.NoError(t, err)

	// Verify instance independence
	business := GetLogger(LoggerNameBusiness)
	sql := GetLogger(LoggerNameSQL)

	assert.NotNil(t, business)
	assert.NotNil(t, sql)

	business.Info("business log")
	sql.Debug("sql log", String("query", "select * from users"))
}

func TestSlogLogger_DefaultFallback(t *testing.T) {
	// Clear registry for testing
	mu.Lock()
	registry = make(map[string]Logger)
	mu.Unlock()

	// Getting business logger when uninitialized should trigger default initialization
	l := GetLogger(LoggerNameBusiness)
	assert.NotNil(t, l)

	// Getting unknown name should fallback to business logger
	unknown := GetLogger("unknown")
	assert.Equal(t, l, unknown)
}

func TestSlogLogger_FileRotation(t *testing.T) {
	tempFile := "test_rotate.log"
	defer os.Remove(tempFile)

	config := Config{
		Loggers: map[string]LoggerConfig{
			"file-test": {
				Enabled: true,
				Level:   LevelInfo,
				Format:  FormatJSON,
				Outputs: []OutputConfig{
					{
						Type: string(OutputFile),
						File: FileConfig{
							Filename:   tempFile,
							MaxSize:    "1M",
							MaxBackups: 3,
							MaxAge:     "7d",
						},
					},
				},
			},
		},
	}

	err := Initialize(config)
	assert.NoError(t, err)

	l := GetLogger("file-test")
	l.Info("test file output")

	// Check if file exists
	_, err = os.Stat(tempFile)
	assert.NoError(t, err)
}

func TestParseSizeMB(t *testing.T) {
	tests := []struct {
		input    string
		expected int
	}{
		{"100", 100},   // Pure number defaults to MB
		{"100M", 100},  // M
		{"100MB", 100}, // MB
		{"1G", 1024},   // G
		{"1GB", 1024},  // GB
		{"1024K", 1},   // K (clipped to minimum 1MB)
		{"512k", 1},    // k (clipped to minimum 1MB)
		{"", 100},      // Empty string defaults to 100MB
		{"invalid", 0}, // Invalid formats treated as parsing failure
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.expected, ParseSizeMB(tt.input))
		})
	}
}

func TestParseAgeDays(t *testing.T) {
	tests := []struct {
		input    string
		expected int
	}{
		{"7", 7},       // Pure number defaults to days
		{"7d", 7},      // d
		{"7day", 7},    // day
		{"1w", 7},      // w
		{"1week", 7},   // week
		{"1m", 30},     // m (month)
		{"1month", 30}, // month
		{"1y", 365},    // y
		{"1year", 365}, // year
		{"24h", 1},     // h
		{"12h", 1},     // h (clipped to minimum 1 day)
		{"", 7},        // Empty string defaults to 7 days
		{"invalid", 0}, // Invalid format
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.expected, ParseAgeDays(tt.input))
		})
	}
}
