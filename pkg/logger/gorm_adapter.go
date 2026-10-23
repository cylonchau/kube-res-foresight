package logger

import (
	"context"
	"errors"
	"fmt"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
	"time"
)

// GormLogger is an adapter that bridges GORM's logging interface with our custom Logger.
type GormLogger struct {
	logger                    Logger
	LogLevel                  gormlogger.LogLevel
	SlowThreshold             time.Duration
	IgnoreRecordNotFoundError bool
}

// NewGormLogger creates a GormLogger instance with default settings for production use.
func NewGormLogger(logger Logger) *GormLogger {
	return &GormLogger{
		logger:                    logger,
		LogLevel:                  gormlogger.Info,
		SlowThreshold:             200 * time.Millisecond,
		IgnoreRecordNotFoundError: true,
	}
}

// LogMode sets the log level for GORM operations.
func (l *GormLogger) LogMode(level gormlogger.LogLevel) gormlogger.Interface {
	newLogger := *l
	newLogger.LogLevel = level
	return &newLogger
}

// Info logs messages at the Info level.
func (l *GormLogger) Info(ctx context.Context, msg string, data ...interface{}) {
	if l.LogLevel >= gormlogger.Info {
		l.logger.Info(fmt.Sprintf(msg, data...))
	}
}

// Warn logs messages at the Warn level.
func (l *GormLogger) Warn(ctx context.Context, msg string, data ...interface{}) {
	if l.LogLevel >= gormlogger.Warn {
		l.logger.Warn(fmt.Sprintf(msg, data...))
	}
}

// Error logs messages at the Error level.
func (l *GormLogger) Error(ctx context.Context, msg string, data ...interface{}) {
	if l.LogLevel >= gormlogger.Error {
		l.logger.Error(fmt.Sprintf(msg, data...))
	}
}

// Trace logs SQL statements, execution times, and any errors encountered.
func (l *GormLogger) Trace(ctx context.Context, begin time.Time, fc func() (sql string, rowsAffected int64), err error) {
	if l.LogLevel <= gormlogger.Silent {
		return
	}

	elapsed := time.Since(begin)
	sql, rows := fc()

	switch {
	case err != nil && l.LogLevel >= gormlogger.Error && (!errors.Is(err, gorm.ErrRecordNotFound) || !l.IgnoreRecordNotFoundError):
		l.logger.Error("SQL execution error",
			String("sql", sql),
			Int64("rows", rows),
			String("elapsed", elapsed.String()),
			Err(err),
		)
	case elapsed > l.SlowThreshold && l.SlowThreshold != 0 && l.LogLevel >= gormlogger.Warn:
		l.logger.Warn("Slow SQL query detected",
			String("sql", sql),
			Int64("rows", rows),
			String("elapsed", elapsed.String()),
			String("threshold", l.SlowThreshold.String()),
		)
	case l.LogLevel >= gormlogger.Info:
		l.logger.Debug("SQL query executed",
			String("sql", sql),
			Int64("rows", rows),
			String("elapsed", elapsed.String()),
		)
	}
}
