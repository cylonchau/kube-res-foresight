package app

import (
	"context"

	"github.com/cylonchau/kubeforecast/pkg/dao/rdb"
	"github.com/cylonchau/kubeforecast/pkg/telegram"
	"github.com/cylonchau/kubeforecast/pkg/workflow"
	"gorm.io/gorm"
)

// NewWorkflowEngine wires the shared workflow persistence and internal delivery
// adapters for the management application, independently of CLI entrypoints.
func NewWorkflowEngine(ctx context.Context, db *gorm.DB) *workflow.Engine {
	engine := workflow.NewEngine(
		rdb.NewWorkflowStore(db),
		rdb.NewNotifyTemplateDAO(db),
		rdb.NewTelegramBindingDAO(db),
		nil,
	)
	engine.SetPendingReplyStore(rdb.NewWorkflowPendingReplyDAO(db))
	telegram.Bootstrap(ctx, db, engine)
	return engine
}
