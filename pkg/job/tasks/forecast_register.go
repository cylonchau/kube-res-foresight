package tasks

import (
	"github.com/cylonchau/kube-resource-fcst/pkg/dao/rdb"
	"github.com/cylonchau/kube-resource-fcst/pkg/forecastsvc"
	"github.com/cylonchau/kube-resource-fcst/pkg/job"
	"github.com/cylonchau/kube-resource-fcst/pkg/job/tasks/forecasttask"
	"github.com/cylonchau/kube-resource-fcst/pkg/job/tasks/ldaptask"
	"github.com/cylonchau/kube-resource-fcst/pkg/ldapauth"
	"github.com/cylonchau/kube-resource-fcst/pkg/model"
)

// registerForecast registers the forecasting jobs: one for the regular collect/predict/recommend pass and one,
// scheduled far less often, for the costlier backtests.
func registerForecast() {
	modules := rdb.NewModuleDAO(model.DB)
	service := forecastsvc.New(modules, rdb.NewMetricMappingDAO(model.DB), rdb.NewDatasourceDAO(model.DB),
		rdb.NewSampleDAO(model.DB), rdb.NewPredictionDAO(model.DB))
	job.Register(&forecasttask.Run{Modules: modules, Service: service})
	job.Register(&forecasttask.Backtest{Modules: modules, Service: service})
	job.Register(&forecasttask.Score{Modules: modules, Service: service})
	job.Register(&forecasttask.Evaluate{Modules: modules, Service: service})
}

// registerLDAP registers the directory sync job.
func registerLDAP() {
	service := &ldapauth.Service{DB: model.DB, Dir: ldapauth.Client{}, RevokeSessions: rdb.NewRefreshTokenSessionDAO(model.DB).RevokeUser}
	job.Register(&ldaptask.Sync{Service: service})
}
