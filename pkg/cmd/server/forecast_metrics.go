package server

import (
	"context"
	"sync"

	"github.com/cylonchau/kube-res-foresight/pkg/config"
	"github.com/cylonchau/kube-res-foresight/pkg/dao/rdb"
	"github.com/cylonchau/kube-res-foresight/pkg/forecastsvc"
	"github.com/cylonchau/kube-res-foresight/pkg/metrics"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
)

var registerForecastCollector sync.Once

// startForecastMetrics exports the forecasting state on the process registry and, when metrics.listen is
// configured, serves /metrics, /healthz and /readyz. The collector is registered once per process even if the
// platform is started again (all-in-one restarts, tests).
func startForecastMetrics(ctx context.Context) {
	svc := forecastsvc.New(rdb.NewModuleDAO(model.DB), rdb.NewMetricMappingDAO(model.DB), rdb.NewDatasourceDAO(model.DB),
		rdb.NewSampleDAO(model.DB), rdb.NewPredictionDAO(model.DB))
	registerForecastCollector.Do(func() {
		collector := forecastsvc.NewCollector(svc)
		collector.Jobs = rdb.NewJobDAO(model.DB)
		metrics.Registry.MustRegister(collector)
		metrics.Registry.MustRegister(forecastsvc.SharedTelemetry().Collectors()...)
	})
	metrics.LegacyNames = config.Get().Metrics.LegacyNames
	metrics.Serve(ctx, config.Get().Metrics.Listen, func() error { return svc.Ready(ctx) })
}
