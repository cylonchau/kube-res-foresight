package notify

import (
	"context"
	"github.com/cylonchau/kube-res-foresight/pkg/dao/rdb"
	"github.com/cylonchau/kube-res-foresight/pkg/logger"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/cylonchau/kube-res-foresight/pkg/moduleruntime"
	"sync"
	"time"
)

var (
	defaultEngine   *Engine
	defaultEngineMu sync.RWMutex
)

// GetEngine returns the active Notification Engine singleton.
func GetEngine() *Engine {
	defaultEngineMu.RLock()
	defer defaultEngineMu.RUnlock()
	return defaultEngine
}

// SetEngine sets the active Notification Engine singleton.
func SetEngine(e *Engine) {
	defaultEngineMu.Lock()
	defer defaultEngineMu.Unlock()
	defaultEngine = e
}

// Run starts the standalone notification module lifecycle and escalation monitor.
func Run(ctx context.Context) error {

	if model.DB != nil {
		dao := rdb.NewNotificationDAO(model.DB)
		engine := NewEngine(dao)
		SetEngine(engine)

		// Start escalation watcher in background
		go func() {
			ticker := time.NewTicker(time.Minute)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					if count, err := engine.CheckAndEscalate(ctx, 15*time.Minute); err == nil && count > 0 {
						logger.Warn("Escalated unacknowledged alerts", logger.Int("count", count))
					}
				}
			}
		}()
	}

	return moduleruntime.Wait(ctx, "notify")
}
