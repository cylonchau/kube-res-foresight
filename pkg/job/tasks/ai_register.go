package tasks

import (
	"github.com/cylonchau/kube-resource-fcst/pkg/assistant"
	"github.com/cylonchau/kube-resource-fcst/pkg/dao/rdb"
	"github.com/cylonchau/kube-resource-fcst/pkg/forecastsvc"
	"github.com/cylonchau/kube-resource-fcst/pkg/job"
	"github.com/cylonchau/kube-resource-fcst/pkg/job/tasks/aitask"
	"github.com/cylonchau/kube-resource-fcst/pkg/model"
	"github.com/cylonchau/kube-resource-fcst/pkg/notify"
	"github.com/cylonchau/kube-resource-fcst/pkg/notify/email"
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
		Connections: rdb.NewAssistantConnectionDAO(model.DB), SkillStore: rdb.NewAssistantSkillDAO(model.DB), Tasks: rdb.NewAssistantTaskDAO(model.DB),
		Database: model.DB.Dialector.Name()})
	queue := &aitask.Queue{Jobs: rdb.NewJobDAO(model.DB)}
	job.Register(&aitask.Schedule{Service: service, Queue: queue})
	job.Register(&aitask.Run{Service: service})
}
