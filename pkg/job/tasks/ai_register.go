package tasks

import (
	"context"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/assistant"
	"github.com/cylonchau/kube-res-foresight/pkg/dao/rdb"
	"github.com/cylonchau/kube-res-foresight/pkg/forecastsvc"
	"github.com/cylonchau/kube-res-foresight/pkg/job"
	"github.com/cylonchau/kube-res-foresight/pkg/job/tasks/aitask"
	"github.com/cylonchau/kube-res-foresight/pkg/job/tasks/asyncrun"
	"github.com/cylonchau/kube-res-foresight/pkg/job/tasks/steprun"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/cylonchau/kube-res-foresight/pkg/notify"
	"github.com/cylonchau/kube-res-foresight/pkg/notify/email"
	"github.com/cylonchau/kube-res-foresight/pkg/timing"
)

// registerAI registers the two jobs of the AI scheduled tasks and the e-mail channel their reports are sent by (the process that runs jobs
// is the one that sends; the channel is registered here, as the notification command registers its own).
func registerAI(config *rdb.SysConfigDAO) {
	if model.DB == nil || config == nil {
		return
	}
	if _, ok := notify.Get(email.Channel); !ok {
		notify.Register(email.New(config))
	}
	modules, predictions := rdb.NewModuleDAO(model.DB), rdb.NewPredictionDAO(model.DB)
	forecast := forecastsvc.New(modules, rdb.NewMetricMappingDAO(model.DB), rdb.NewDatasourceDAO(model.DB), rdb.NewSampleDAO(model.DB), predictions)
	service := assistant.NewService(&assistant.Service{DB: model.DB, Forecast: forecast, Modules: modules, Predictions: predictions, Config: config,
		Connections: rdb.NewAssistantConnectionDAO(model.DB), SkillStore: rdb.NewAssistantSkillDAO(model.DB), Tasks: rdb.NewAssistantTaskDAO(model.DB), Conversations: rdb.NewAssistantConversationDAO(model.DB), Audits: rdb.NewAssistantAuditDAO(model.DB), AsyncTasks: rdb.NewAsyncTaskDAO(model.DB),
		Database: model.DB.Dialector.Name()})
	queue := &aitask.Queue{Jobs: rdb.NewJobDAO(model.DB)}
	job.Register(&aitask.Schedule{Service: service, Queue: queue})
	job.Register(&aitask.Run{Service: service})
	asyncTasks := rdb.NewAsyncTaskDAO(model.DB)
	retention := func(ctx context.Context) time.Duration {
		raw, _ := config.GetValueOrDefault(ctx, timing.AsyncRetentionHours.Key, "")
		return time.Duration(timing.AsyncRetentionHours.Parse(raw)) * time.Hour
	}
	job.Register(&asyncrun.Runner{Tasks: asyncTasks, Retention: retention, Handlers: map[string]asyncrun.Handler{aitask.ChatKind: aitask.ChatHandler(service),
		steprun.Kind: steprun.Handler(forecast), steprun.PreviewKind: steprun.PreviewHandler(forecast)}})
	asyncSweeper = &asyncrun.Sweeper{Tasks: asyncTasks, Queue: &asyncrun.Queue{Tasks: asyncTasks, Jobs: rdb.NewJobDAO(model.DB), Retention: retention}}
}

// asyncSweeper looks for the tasks whose worker is gone; it runs for as long as the process that runs the jobs does.
var asyncSweeper *asyncrun.Sweeper

// StartBackground starts what runs beside the job engine for as long as ctx lives: the sweep of the async tasks whose worker was lost.
func StartBackground(ctx context.Context) {
	if asyncSweeper != nil {
		go asyncSweeper.Run(ctx)
	}
}
