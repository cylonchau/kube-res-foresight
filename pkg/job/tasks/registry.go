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
	"github.com/cylonchau/kube-res-foresight/pkg/dao/rdb"
	"github.com/cylonchau/kube-res-foresight/pkg/job"
	"github.com/cylonchau/kube-res-foresight/pkg/job/tasks/notifytask"
)

// RegisterAll registers every job type with the job.Registry. Notification channels (webhook, voice) are
// registered by the notify runtime, not here; the notify task only looks a channel up by name.
func RegisterAll(config *rdb.SysConfigDAO) {
	job.Register(notifytask.New())
	registerForecast()
	registerLDAP()
	registerAI(config)
}
