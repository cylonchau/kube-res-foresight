// pkg/job/tasks/registry.go
//
// This file is the only thing left in package tasks: every job.Task
// implementation now lives in its own subpackage (see the imports below),
// each following the same shape — type.go (JobType + wire contract),
// option.go (constructor knobs, where needed), impl.go (the job.Task
// itself). To add a new job type, write a new subpackage the same way and
// add one job.Register call for it here; nothing else (the Scheduler
// included) needs to change.
package tasks

import (
	"github.com/cylonchau/kubeforecast/pkg/dao/rdb"
	"github.com/cylonchau/kubeforecast/pkg/job"
	"github.com/cylonchau/kubeforecast/pkg/job/tasks/notifytask"
	"github.com/cylonchau/kubeforecast/pkg/model"
	"github.com/cylonchau/kubeforecast/pkg/notify"
	notifytelegram "github.com/cylonchau/kubeforecast/pkg/notify/telegram"
)

// RegisterAll injects DAO dependencies into tasks and registers them with the job.Registry
func RegisterAll(sysConfigDAO *rdb.SysConfigDAO) {
	telegramBindingDAO := rdb.NewTelegramBindingDAO(model.DB)

	// Notify channels: register each concrete channel once here. Detection
	// producer tasks never talk to a channel directly —
	// they call notify.Broadcast, which fans out to every registered
	// channel via this registry. Adding email/webhook/etc. later means
	// writing one more notify.Notifier implementation and registering it
	// here — nothing else changes.
	notify.Register(notifytelegram.WithClientAccount(
		notifytelegram.NewWithBots(sysConfigDAO, telegramBindingDAO, rdb.NewTelegramBotDAO(model.DB)),
		rdb.NewTelegramClientDAO(model.DB), rdb.NewTelegramAgentRequestDAO(model.DB)))

	job.Register(notifytask.New())
}
