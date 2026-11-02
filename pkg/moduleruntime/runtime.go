// Package moduleruntime contains the small lifecycle primitive shared by
// standalone kube-res-foresight modules that do not yet have provider workers.
package moduleruntime

import (
	"context"
	"github.com/cylonchau/kube-res-foresight/pkg/logger"
)

// Wait marks a module as running and keeps it alive until the process context
// is cancelled. Collector and scanner will replace this wait with registered
// workers in their own packages; the process lifecycle and CLI contract stay
// stable while that functionality is added.
func Wait(ctx context.Context, name string) error {
	logger.Info("kube-res-foresight module lifecycle ready",
		logger.String("module", name),
		logger.Int("registered_workers", 0))
	<-ctx.Done()
	logger.Info("kube-res-foresight module stopped", logger.String("module", name))
	return nil
}
