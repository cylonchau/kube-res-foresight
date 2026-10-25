package forecasttask

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/cylonchau/kube-resource-fcst/pkg/collector"
	"github.com/cylonchau/kube-resource-fcst/pkg/dao/rdb"
	"github.com/cylonchau/kube-resource-fcst/pkg/forecastsvc"
	"github.com/cylonchau/kube-resource-fcst/pkg/model"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type constQuerier struct{}

func (constQuerier) QueryRange(_ context.Context, _ string, start, end time.Time, step time.Duration) ([]collector.Point, error) {
	var out []collector.Point
	for ts := start; !ts.After(end); ts = ts.Add(step) {
		out = append(out, collector.Point{Time: ts, Value: 1})
	}
	return out, nil
}

type env struct {
	modules *rdb.ModuleDAO
	svc     *forecastsvc.Service
	db      *gorm.DB
	mapping *model.MetricMapping
}

func setup(t *testing.T) *env {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Datasource{}, &model.MetricMapping{}, &model.ForecastModule{}, &model.AlgorithmConfig{},
		&model.ModulePeriod{}, &model.MetricSeries{}, &model.MetricSample{}, &model.Prediction{}, &model.BacktestRun{}, &model.Recommendation{}, &model.PredictionEvaluation{},
		&model.MetricSource{}, &model.AlgorithmScore{}, &model.AdmissionDecision{}, &model.TrainingRun{}, &model.ModuleRunState{}, &model.ScalingExecution{}))
	modules := rdb.NewModuleDAO(db)
	svc := forecastsvc.New(modules, rdb.NewMetricMappingDAO(db), rdb.NewDatasourceDAO(db), rdb.NewSampleDAO(db), rdb.NewPredictionDAO(db))
	svc.NewQuerier = func(*model.Datasource) (collector.RangeQuerier, error) { return constQuerier{}, nil }
	ds := &model.Datasource{Name: "p", Type: "prometheus", BaseURL: "http://p:9090", AuthType: "none", TimeoutSeconds: 30, MaxConcurrency: 1, Enabled: true, Status: "unknown"}
	require.NoError(t, db.Create(ds).Error)
	mapping := &model.MetricMapping{DatasourceID: ds.ID, Name: "cpu", Metric: "cpu", Level: "namespace", Unit: "cores", StepSeconds: 60, Enabled: true,
		Query: `sum(rate(c{namespace="{{namespace}}"}[{{range}}]))`}
	require.NoError(t, db.Create(mapping).Error)
	require.NoError(t, svc.EnsureAlgorithms(context.Background()))
	return &env{modules: modules, svc: svc, db: db, mapping: mapping}
}

// module makes an enabled module, with a cpu source when asked.
func (e *env) module(t *testing.T, name, mode string, withSource bool) *model.ForecastModule {
	t.Helper()
	m := &model.ForecastModule{Name: name, Enabled: true, Mode: mode, Namespace: "prod", Workload: name, FallbackCPUMilli: 500, LookbackHours: 2}
	require.NoError(t, m.Validate())
	require.NoError(t, e.modules.Create(context.Background(), m))
	if withSource {
		src := &model.MetricSource{ModuleID: m.ID, Name: "cpu", Kind: "cpu", MappingID: e.mapping.ID, Weight: 1, Stat: "p95", Enabled: true}
		require.NoError(t, src.Validate())
		require.NoError(t, e.modules.SaveSource(context.Background(), src))
	}
	return m
}

func TestRunJobAppliesEveryEnabledAlgorithmToEveryModuleWithoutBindings(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	observed := e.module(t, "observed", model.ModeObserve, true)
	live := e.module(t, "live", model.ModeRecommend, true)
	bare := e.module(t, "bare", model.ModeRecommend, false) // no source: a misconfigured module must not fail the job
	off := e.module(t, "off", model.ModeRecommend, true)
	require.NoError(t, e.db.Model(off).Update("enabled", false).Error)
	// the baselines are enabled by default; ewma is turned on once and reaches every module
	require.NoError(t, e.db.Model(&model.AlgorithmConfig{}).Where("algorithm_id = ?", "ewma").Update("enabled", true).Error)

	job := &Run{Modules: e.modules, Service: e.svc}
	assert.Equal(t, RunJobType, job.Type())
	require.NoError(t, job.Execute(ctx, &model.Job{}))

	algorithmsOf := func(module uint) map[string]bool {
		var ids []string
		require.NoError(t, e.db.Model(&model.Prediction{}).Where("module_id = ?", module).Distinct().Pluck("algorithm_id", &ids).Error)
		out := map[string]bool{}
		for _, id := range ids {
			out[id] = true
		}
		return out
	}
	for _, m := range []*model.ForecastModule{observed, live} {
		got := algorithmsOf(m.ID)
		assert.True(t, got["naive"] && got["ewma"], "module %s was never bound to the algorithms: %v", m.Name, got)
	}
	assert.Empty(t, algorithmsOf(bare.ID))
	assert.Empty(t, algorithmsOf(off.ID), "a disabled module is untouched")

	var recs []model.Recommendation
	require.NoError(t, e.db.Find(&recs).Error)
	moduleIDs := map[uint]bool{}
	for _, r := range recs {
		moduleIDs[r.ModuleID] = true
	}
	assert.True(t, moduleIDs[live.ID], "a module outside observe mode got a recommendation")
	assert.False(t, moduleIDs[observed.ID], "observe mode produces none")
	assert.False(t, moduleIDs[off.ID])
}

func TestBacktestJobSkipsModulesWithoutHistory(t *testing.T) {
	e := setup(t)
	e.module(t, "fresh", model.ModeRecommend, true)

	job := &Backtest{Modules: e.modules, Service: e.svc}
	assert.Equal(t, BacktestJobType, job.Type())
	require.NoError(t, job.Execute(context.Background(), &model.Job{}), "no history yet is not a failure")
	var runs int64
	require.NoError(t, e.db.Model(&model.BacktestRun{}).Count(&runs).Error)
	assert.Zero(t, runs)
}

func TestScoreJobSkipsModulesWithNothingToScore(t *testing.T) {
	e := setup(t)
	e.module(t, "empty", model.ModeRecommend, false)
	job := &Score{Modules: e.modules, Service: e.svc}
	assert.Equal(t, ScoreJobType, job.Type())
	require.NoError(t, job.Execute(context.Background(), &model.Job{}), "nothing to score is not a failure")
}

func TestRunJobHonoursTheRunIntervalOfAModule(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	hourly := e.module(t, "hourly", model.ModeObserve, true)
	every := e.module(t, "every", model.ModeObserve, true)
	require.NoError(t, e.db.Model(hourly).Update("run_interval_minutes", 60).Error)

	job := &Run{Modules: e.modules, Service: e.svc}
	runsOf := func(m *model.ForecastModule) int64 {
		var n int64
		require.NoError(t, e.db.Model(&model.TrainingRun{}).Where("module_id = ?", m.ID).Distinct("prediction_time").Count(&n).Error)
		return n
	}
	require.NoError(t, job.Execute(ctx, &model.Job{}))
	assert.EqualValues(t, 1, runsOf(hourly), "the first run is always due")
	assert.EqualValues(t, 1, runsOf(every))

	require.NoError(t, job.Execute(ctx, &model.Job{}))
	assert.EqualValues(t, 1, runsOf(hourly), "an hourly module is not run again a moment later")
	assert.EqualValues(t, 2, runsOf(every), "a module without an interval runs at every cycle")

	// an hour later it is due again
	require.NoError(t, e.db.Model(&model.TrainingRun{}).Where("module_id = ?", hourly.ID).Update("prediction_time", time.Now().UTC().Add(-61*time.Minute)).Error)
	require.NoError(t, job.Execute(ctx, &model.Job{}))
	assert.EqualValues(t, 2, runsOf(hourly))
}
