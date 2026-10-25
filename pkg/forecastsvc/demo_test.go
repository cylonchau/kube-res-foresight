package forecastsvc

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/cylonchau/kubeforecast/pkg/dao/rdb"
	"github.com/cylonchau/kubeforecast/pkg/decision"
	"github.com/cylonchau/kubeforecast/pkg/model"
)

func demoService(t *testing.T) (*Service, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s_%d?mode=memory&cache=shared", t.Name(), atomic.AddInt32(&dbSeq, 1))), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent), NowFunc: func() time.Time { return testNow }})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Datasource{}, &model.MetricMapping{}, &model.ForecastModule{}, &model.AlgorithmConfig{},
		&model.ModulePeriod{}, &model.MetricSeries{}, &model.MetricSample{}, &model.Prediction{}, &model.BacktestRun{}, &model.Recommendation{},
		&model.PredictionEvaluation{}, &model.ScalingExecution{}, &model.MetricSource{}, &model.AlgorithmScore{}, &model.AdmissionDecision{}, &model.TrainingRun{}, &model.ModuleRunState{}))
	svc := New(rdb.NewModuleDAO(db), rdb.NewMetricMappingDAO(db), rdb.NewDatasourceDAO(db), rdb.NewSampleDAO(db), rdb.NewPredictionDAO(db))
	svc.Now = func() time.Time { return testNow }
	return svc, db
}

func count(t *testing.T, db *gorm.DB, table any) int64 {
	t.Helper()
	var n int64
	require.NoError(t, db.Model(table).Count(&n).Error)
	return n
}

func TestDemoRunsTheWholePipelineWithoutAnyConfiguration(t *testing.T) {
	svc, db := demoService(t)
	ctx := context.Background()

	res, err := svc.CreateDemo(ctx)
	require.NoError(t, err)
	assert.True(t, res.Created)
	assert.Equal(t, 12, res.Warmup)
	assert.Empty(t, res.Pipeline.Errors, "every step of the first run works")
	require.NotNil(t, res.Pipeline.Recommend)
	assert.Greater(t, res.Backtests, 0)
	assert.Greater(t, res.Evaluated, 0, "the replayed cycles are scored against the actuals")

	// history, predictions and evaluations are all there for the pages
	assert.Greater(t, count(t, db, &model.MetricSample{}), int64(1000))
	assert.Greater(t, count(t, db, &model.Prediction{}), int64(0))
	assert.Greater(t, count(t, db, &model.PredictionEvaluation{}), int64(0))

	// the datasource is flagged as demo data and the mappings keep the preset they came from
	ds, err := svc.Datasources.ByName(ctx, DemoDatasourceName)
	require.NoError(t, err)
	require.NotNil(t, ds)
	assert.Equal(t, model.DatasourceTypeDemo, ds.Type)
	mappings, err := svc.Mappings.ListByDatasource(ctx, ds.ID)
	require.NoError(t, err)
	assert.Len(t, mappings, 11, "the forecast metrics and the auxiliary JVM, OOM and Kubernetes ones")
	for _, m := range mappings {
		assert.NotEmpty(t, m.PresetID)
	}

	// the dynamic solver estimated the pod capacity from the synthetic history and used it
	_, detail, err := svc.Recommend(ctx, res.ModuleID)
	require.NoError(t, err)
	require.NotNil(t, detail.Replica)
	assert.Equal(t, decision.CapacityEstimated, detail.Replica.Capacity.Source, detail.Replica.Capacity.Note)
	assert.InDelta(t, 100, detail.Replica.Capacity.RPSPerPod, 5, "the artificial pods serve about 100 rps each")
	assert.NotEmpty(t, detail.Replica.Plans)

	// the auxiliary metrics were collected and the covariate algorithm ran on the input set
	enabled, err := svc.Algorithms.Enabled(ctx)
	require.NoError(t, err)
	var ids []string
	for _, c := range enabled {
		ids = append(ids, c.AlgorithmID)
	}
	assert.Subset(t, ids, []string{"seasonal_naive", "ewma", "holt_winters", "ridge_ar"}, "the demo turns its algorithms on; they apply to its module without any binding")
	require.NotNil(t, detail.Heap)
	assert.Greater(t, detail.Heap.Ratio, 0.3, "the synthetic heap follows the load")
	var ridge *PredictOutcome
	for i := range res.Pipeline.Predict {
		if res.Pipeline.Predict[i].AlgorithmID == "ridge_ar" && res.Pipeline.Predict[i].Metric == "cpu" {
			ridge = &res.Pipeline.Predict[i]
		}
	}
	require.NotNil(t, ridge)
	assert.Equal(t, StatusOK, ridge.Status, ridge.Reason)
	assert.Subset(t, ridge.Covariates, []string{"jvm_heap_used", "rps"}, "the other forecast sources of the module are its covariates")
}

func TestDemoIsIdempotentAndRemovesOnlyItself(t *testing.T) {
	svc, db := demoService(t)
	ctx := context.Background()

	other := &model.Datasource{Name: "real", Type: "prometheus", BaseURL: "http://prom:9090", AuthType: "none", TimeoutSeconds: 30, MaxConcurrency: 1, Enabled: true, Status: "unknown"}
	require.NoError(t, db.Create(other).Error)

	first, err := svc.CreateDemo(ctx)
	require.NoError(t, err)
	second, err := svc.CreateDemo(ctx)
	require.NoError(t, err)
	assert.False(t, second.Created)
	assert.Equal(t, first.ModuleID, second.ModuleID)
	assert.EqualValues(t, 1, count(t, db, &model.ForecastModule{}))
	assert.EqualValues(t, 11, count(t, db, &model.MetricSource{}), "sources are not duplicated")

	require.NoError(t, svc.DeleteDemo(ctx))
	assert.Zero(t, count(t, db, &model.ForecastModule{}))
	assert.Zero(t, count(t, db, &model.MetricSource{}))
	assert.Zero(t, count(t, db, &model.MetricMapping{}))
	assert.Zero(t, count(t, db, &model.Prediction{}))
	assert.Zero(t, count(t, db, &model.MetricSample{}))
	assert.EqualValues(t, 1, count(t, db, &model.Datasource{}), "the real datasource is untouched")
	require.NoError(t, svc.DeleteDemo(ctx), "deleting twice is harmless")
}
