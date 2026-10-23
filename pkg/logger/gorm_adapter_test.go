package logger

import (
	"context"
	"errors"
	gormlogger "gorm.io/gorm/logger"
	"testing"
	"time"
)

// mockLogger is a simple Mock implementation for verifying logger calls.
type mockLogger struct {
	lastLevel  string
	lastMsg    string
	lastFields []Field
}

func (m *mockLogger) Debug(msg string, fields ...Field) {
	m.lastLevel = "debug"
	m.lastMsg = msg
	m.lastFields = fields
}
func (m *mockLogger) Info(msg string, fields ...Field) {
	m.lastLevel = "info"
	m.lastMsg = msg
	m.lastFields = fields
}
func (m *mockLogger) Warn(msg string, fields ...Field) {
	m.lastLevel = "warn"
	m.lastMsg = msg
	m.lastFields = fields
}
func (m *mockLogger) Error(msg string, fields ...Field) {
	m.lastLevel = "error"
	m.lastMsg = msg
	m.lastFields = fields
}
func (m *mockLogger) Fatal(msg string, fields ...Field) {
	m.lastLevel = "fatal"
	m.lastMsg = msg
	m.lastFields = fields
}
func (m *mockLogger) With(fields ...Field) Logger { return m }
func (m *mockLogger) Named(name string) Logger    { return m }
func (m *mockLogger) Sync() error                 { return nil }

func TestGormLogger_Trace(t *testing.T) {
	mock := &mockLogger{}
	gl := NewGormLogger(mock)
	gl.SlowThreshold = 100 * time.Millisecond

	ctx := context.Background()
	now := time.Now()

	t.Run("Normal Query", func(t *testing.T) {
		gl.Trace(ctx, now, func() (string, int64) {
			return "SELECT * FROM users", 1
		}, nil)

		if mock.lastLevel != "debug" {
			t.Errorf("expected level debug, got %s", mock.lastLevel)
		}
		if mock.lastMsg != "SQL query executed" {
			t.Errorf("expected msg 'SQL query executed', got %s", mock.lastMsg)
		}
	})

	t.Run("Slow Query", func(t *testing.T) {
		// Simulate execution time exceeding SlowThreshold
		begin := time.Now().Add(-200 * time.Millisecond)
		gl.Trace(ctx, begin, func() (string, int64) {
			return "SELECT * FROM large_table", 1000
		}, nil)

		if mock.lastLevel != "warn" {
			t.Errorf("expected level warn for slow query, got %s", mock.lastLevel)
		}
		if mock.lastMsg != "Slow SQL query detected" {
			t.Errorf("expected msg 'Slow SQL query detected', got %s", mock.lastMsg)
		}
	})

	t.Run("Error Query", func(t *testing.T) {
		err := errors.New("db connection lost")
		gl.Trace(ctx, now, func() (string, int64) {
			return "INSERT INTO users...", 0
		}, err)

		if mock.lastLevel != "error" {
			t.Errorf("expected level error, got %s", mock.lastLevel)
		}
		if mock.lastMsg != "SQL execution error" {
			t.Errorf("expected error msg, got %s", mock.lastMsg)
		}
	})
}

func TestGormLogger_Levels(t *testing.T) {
	mock := &mockLogger{}
	gl := NewGormLogger(mock)
	ctx := context.Background()

	gl.Info(ctx, "info message %s", "test")
	if mock.lastLevel != "info" || mock.lastMsg != "info message test" {
		t.Errorf("Info log failed")
	}

	gl.Warn(ctx, "warn message")
	if mock.lastLevel != "warn" {
		t.Errorf("Warn log failed")
	}

	gl.Error(ctx, "error message")
	if mock.lastLevel != "error" {
		t.Errorf("Error log failed")
	}
}

func TestGormLogger_LogMode(t *testing.T) {
	mock := &mockLogger{}
	gl := NewGormLogger(mock)

	newGl := gl.LogMode(gormlogger.Error)
	casted := newGl.(*GormLogger)

	if casted.LogLevel != gormlogger.Error {
		t.Errorf("expected log level Error, got %v", casted.LogLevel)
	}

	// Original logger should remain unchanged
	if gl.LogLevel != gormlogger.Info {
		t.Errorf("original logger level should be Info")
	}
}
