package server

import (
	"context"
	"github.com/cylonchau/kube-resource-fcst/pkg/dao/rdb"
	"github.com/cylonchau/kube-resource-fcst/pkg/logger"
	"strconv"
	"time"
)

// operationLogRetentionCheckInterval is how often the loop wakes up to
// check whether it's time to purge — deliberately coarse (this is a
// housekeeping task, not something latency-sensitive) and independent of
// the configured retention window itself, which only affects the cutoff
// used once the loop does run.
const operationLogRetentionCheckInterval = time.Hour

const operationLogRetentionDaysKey = "sys.operation_log.retention_days"

const defaultOperationLogRetentionDays = 90

// startOperationLogRetentionLoop runs the "系统管理/操作日志" cleanup job
// inside the main server process (per the requirement: a job "server内就可以",
// no need for the separate job engine in pkg/job) — a plain ticker that
// reads sys.operation_log.retention_days from SysConfig on every tick (so an
// admin editing it in the system-config page takes effect on the next run,
// no restart needed) and purges anything older than that.
func startOperationLogRetentionLoop(ctx context.Context, configDAO *rdb.SysConfigDAO, opLogDAO *rdb.OperationLogDAO, loginLogDAO *rdb.LoginAuditLogDAO) {
	go func() {
		ticker := time.NewTicker(operationLogRetentionCheckInterval)
		defer ticker.Stop()
		// Run once shortly after startup too, instead of waiting a full
		// interval for the first pass.
		purgeOperationLogsOnce(ctx, configDAO, opLogDAO)
		purgeLoginLogsOnce(ctx, configDAO, loginLogDAO)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				purgeOperationLogsOnce(ctx, configDAO, opLogDAO)
				purgeLoginLogsOnce(ctx, configDAO, loginLogDAO)
			}
		}
	}()
}

func purgeOperationLogsOnce(ctx context.Context, configDAO *rdb.SysConfigDAO, opLogDAO *rdb.OperationLogDAO) {
	days := defaultOperationLogRetentionDays
	if cfg, err := configDAO.GetByKey(ctx, operationLogRetentionDaysKey); err == nil && cfg != nil {
		if parsed, err := strconv.Atoi(cfg.ConfigValue); err == nil && parsed > 0 {
			days = parsed
		}
	}
	cutoff := time.Now().AddDate(0, 0, -days)
	removed, err := opLogDAO.PurgeOlderThan(ctx, cutoff)
	if err != nil {
		logger.Error("operation log retention purge failed", logger.Err(err))
		return
	}
	if removed > 0 {
		logger.Info("operation log retention purge completed",
			logger.Int("removed", int(removed)),
			logger.Int("retention_days", days))
	}
}

const loginLogRetentionDaysKey = "sys.login_log.retention_days"

const defaultLoginLogRetentionDays = 90

// retentionDays reads a retention setting, falling back to def when it is missing or not a positive number.
func retentionDays(ctx context.Context, configDAO *rdb.SysConfigDAO, key string, def int) int {
	if cfg, err := configDAO.GetByKey(ctx, key); err == nil && cfg != nil {
		if parsed, err := strconv.Atoi(cfg.ConfigValue); err == nil && parsed > 0 {
			return parsed
		}
	}
	return def
}

// purgeLoginLogsOnce deletes login history older than sys.login_log.retention_days.
func purgeLoginLogsOnce(ctx context.Context, configDAO *rdb.SysConfigDAO, loginLogDAO *rdb.LoginAuditLogDAO) {
	days := retentionDays(ctx, configDAO, loginLogRetentionDaysKey, defaultLoginLogRetentionDays)
	removed, err := loginLogDAO.PurgeOlderThan(ctx, time.Now().AddDate(0, 0, -days))
	if err != nil {
		logger.Error("login log retention purge failed", logger.Err(err))
		return
	}
	if removed > 0 {
		logger.Info("login log retention purge completed", logger.Int("removed", int(removed)), logger.Int("retention_days", days))
	}
}
