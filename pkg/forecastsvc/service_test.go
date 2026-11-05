package forecastsvc

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/collector"
	"github.com/cylonchau/kube-res-foresight/pkg/dao/rdb"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var dbSeq int32

var testNow = time.Date(2026, 3, 4, 10, 30, 0, 0, time.UTC)

// syntheticQuerier serves a deterministic daily-seasonal series per query text.
type syntheticQuerier struct {
	mu      sync.Mutex
	queries []string
	value   func(query string, ts time.Time) float64
	skip    func(ts time.Time) bool
}

func (q *syntheticQuerier) QueryRange(_ context.Context, query string, start, end time.Time, step time.Duration) ([]collector.Point, error) {
	q.mu.Lock()
	q.queries = append(q.queries, query)
	q.mu.Unlock()
	var out []collector.Point
	for ts := start; !ts.After(end); ts = ts.Add(step) {
		if q.skip != nil && q.skip(ts) {
			continue
		}
		out = append(out, collector.Point{Time: ts, Value: q.value(query, ts)})
	}
	return out, nil
}

func daily(base, amp float64, ts time.Time) float64 {
	minute := float64(ts.Hour()*60 + ts.Minute())
	return base + amp*math.Sin(2*math.Pi*minute/1440)
}

type fixture struct {
	svc     *Service
	db      *gorm.DB
	module  *model.ForecastModule
	querier *syntheticQuerier
	cpuMap  *model.MetricMapping
	rpsMap  *model.MetricMapping
	clock   *time.Time
	// maps are the metric mappings by kind, for the sources the tests add
	maps map[string]uint
	// instances are the algorithms the test has enabled, as they run for the module, by algorithm id
	instances map[string]*model.AlgorithmRun
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	clock := testNow
	// row timestamps follow the fixture clock, as they follow the wall clock in production
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s_%d?mode=memory&cache=shared", t.Name(), atomic.AddInt32(&dbSeq, 1))),
		&gorm.Config{Logger: logger.Default.LogMode(logger.Silent), NowFunc: func() time.Time { return clock }})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Datasource{}, &model.MetricMapping{}, &model.ForecastModule{PredictEnabled: true}, &model.AlgorithmConfig{},
		&model.ModulePeriod{}, &model.MetricSeries{}, &model.MetricSample{}, &model.Prediction{}, &model.BacktestRun{}, &model.Recommendation{}, &model.PredictionEvaluation{},
		&model.ScalingExecution{}, &model.MetricSource{}, &model.AlgorithmScore{}, &model.AdmissionDecision{}, &model.TrainingRun{}, &model.ModuleRunState{}, &model.ScaleOutcome{}))

	ds := &model.Datasource{Name: "prom", Type: "prometheus", BaseURL: "http://prom:9090", AuthType: "none", TimeoutSeconds: 30, MaxConcurrency: 2, Enabled: true, Status: "unknown"}
	require.NoError(t, db.Create(ds).Error)
	cpu := &model.MetricMapping{DatasourceID: ds.ID, Name: "cpu", Metric: "cpu", Level: "workload", Unit: "cores", StepSeconds: 60, Enabled: true,
		Query: `sum(rate(cpu{namespace="{{namespace}}",pod=~"{{workload}}-.*"}[{{range}}]))`}
	rps := &model.MetricMapping{DatasourceID: ds.ID, Name: "rps", Metric: "rps", Level: "workload", Unit: "rps", StepSeconds: 60, Enabled: true,
		Query: `sum(rate(rps{namespace="{{namespace}}",pod=~"{{workload}}-.*"}[{{range}}]))`}
	require.NoError(t, db.Create(cpu).Error)
	require.NoError(t, db.Create(rps).Error)
	mod := &model.ForecastModule{PredictEnabled: true, Name: "api", Enabled: true, Mode: model.ModeRecommend, Namespace: "prod", Workload: "api", TTLSeconds: 900,
		HeadroomRatio: 1.1, PodCapacityRPS: 100, MinReplicas: 2, MaxReplicas: 30, MinCPUMilli: 100, MaxCPUMilli: 8000,
		FallbackCPUMilli: 500, FallbackReplicas: 3, Timezone: "UTC", LookbackHours: 72, HorizonMinutes: 15, MaxFillSteps: 3}
	require.NoError(t, mod.Validate())
	require.NoError(t, db.Create(mod).Error)

	q := &syntheticQuerier{value: func(query string, ts time.Time) float64 {
		if query != "" && contains(query, "cpu{") {
			return daily(1, 0.5, ts)
		}
		return daily(300, 100, ts)
	}}
	svc := New(rdb.NewModuleDAO(db), rdb.NewMetricMappingDAO(db), rdb.NewDatasourceDAO(db), rdb.NewSampleDAO(db), rdb.NewPredictionDAO(db))
	svc.NewQuerier = func(*model.Datasource) (collector.RangeQuerier, error) { return q, nil }
	svc.Now = func() time.Time { return clock }
	// the tests choose which algorithms run: none is enabled until a test binds one
	require.NoError(t, svc.EnsureAlgorithms(context.Background()))
	require.NoError(t, db.Model(&model.AlgorithmConfig{}).Where("1 = 1").Update("enabled", false).Error)
	return &fixture{svc: svc, db: db, module: mod, querier: q, cpuMap: cpu, rpsMap: rps, clock: &clock,
		maps: map[string]uint{"cpu": cpu.ID, "rps": rps.ID}, instances: map[string]*model.AlgorithmRun{}}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// bind makes an algorithm take part in a source of the module: the algorithm is enabled with these parameters (an
// algorithm has one setting for the whole platform, so a call with nil parameters leaves them as they are), and the
// module gets the source when it has none. Every enabled algorithm forecasts every forecast source of the module.
func (f *fixture) bind(t *testing.T, metric, algorithm string, params map[string]any, priority int) *model.AlgorithmRun {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, f.svc.EnsureAlgorithms(ctx))
	cfg, err := f.svc.Algorithms.Get(ctx, algorithm)
	require.NoError(t, err)
	cfg.Enabled = true
	if params != nil {
		raw, err := json.Marshal(params)
		require.NoError(t, err)
		canonical, err := ValidateParams(algorithm, string(raw))
		require.NoError(t, err)
		cfg.Params = canonical
	}
	require.NoError(t, f.svc.Algorithms.Save(ctx, cfg))
	f.ensureSource(t, metric)
	return f.run(t, algorithm)
}

// run is the algorithm as it runs for the module now, with the module's sources.
func (f *fixture) run(t *testing.T, algorithm string) *model.AlgorithmRun {
	t.Helper()
	ctx := context.Background()
	mod, err := f.svc.Modules.Get(ctx, f.module.ID)
	require.NoError(t, err)
	cfg, err := f.svc.Algorithms.Get(ctx, algorithm)
	require.NoError(t, err)
	r := model.NewRun(mod, cfg)
	f.instances[algorithm] = r
	return r
}

// setParams changes the platform-wide parameters of an algorithm.
func (f *fixture) setParams(t *testing.T, algorithm string, params map[string]any) {
	t.Helper()
	ctx := context.Background()
	raw, err := json.Marshal(params)
	require.NoError(t, err)
	canonical, err := ValidateParams(algorithm, string(raw))
	require.NoError(t, err)
	cfg, err := f.svc.Algorithms.Get(ctx, algorithm)
	require.NoError(t, err)
	cfg.Params = canonical
	require.NoError(t, f.svc.Algorithms.Save(ctx, cfg))
}

// reload reads a run again with the module's current sources.
func (f *fixture) reload(t *testing.T, inst *model.AlgorithmRun) *model.AlgorithmRun {
	t.Helper()
	return f.run(t, inst.Name)
}

// ensureSource gives the module a source of the kind (named by its key), unless it has one.
func (f *fixture) ensureSource(t *testing.T, key string) {
	t.Helper()
	ctx := context.Background()
	mod, err := f.svc.Modules.Get(ctx, f.module.ID)
	require.NoError(t, err)
	if mod.Data().Source(key) != nil {
		return
	}
	mapping := f.maps[key]
	require.NotZero(t, mapping, "the fixture has no mapping for %s: add one with addAux", key)
	src := &model.MetricSource{ModuleID: mod.ID, Name: key, Kind: key, MappingID: mapping, Weight: 1, Stat: "max", Enabled: true, Priority: len(mod.Sources)}
	require.NoError(t, src.Validate())
	require.NoError(t, f.svc.Modules.SaveSource(ctx, src))
}

// addSourceTo is addSource for the algorithm the call used to name: the sources belong to the module.
func (f *fixture) addSourceTo(t *testing.T, _ *model.AlgorithmRun, key string) {
	f.ensureSource(t, key)
}

// collect, predict, backtest and evaluate run the data steps of the module.
func (f *fixture) collect(ctx context.Context) ([]CollectResult, error) {
	return f.svc.Collect(ctx, f.module.ID)
}

func (f *fixture) predict(ctx context.Context) ([]PredictOutcome, error) {
	return f.svc.Predict(ctx, f.module.ID)
}

func (f *fixture) backtest(ctx context.Context, metric string, cutoffs int) ([]*model.BacktestRun, error) {
	return f.svc.Backtest(ctx, f.module.ID, metric, cutoffs)
}

func (f *fixture) evaluate(ctx context.Context) ([]EvaluateResult, error) {
	return f.svc.Evaluate(ctx, f.module.ID)
}

func (f *fixture) sortedNames() []string {
	names := make([]string, 0, len(f.instances))
	for name := range f.instances {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// owner is the module seen as the owner of the source's history, or nil when the module has no such source.
func (f *fixture) owner(key string) *model.AlgorithmRun {
	mod, err := f.svc.Modules.Get(context.Background(), f.module.ID)
	if err != nil || mod.Data().Source(key) == nil {
		return nil
	}
	return mod.Data()
}

// disableSource turns a metric source of the module off.
func (f *fixture) disableSource(t *testing.T, key string) {
	t.Helper()
	require.NoError(t, f.db.Model(&model.MetricSource{}).Where("metric_source.key = ?", key).Update("enabled", false).Error)
}

func TestCollectBackfillsThenContinuesIdempotently(t *testing.T) {
	f := newFixture(t)
	f.bind(t, "cpu", "naive", nil, 0)
	f.bind(t, "rps", "naive", nil, 0)
	ctx := context.Background()
	results, err := f.collect(ctx)
	require.NoError(t, err)
	require.Len(t, results, 2)
	for _, r := range results {
		assert.Empty(t, r.Error, r.Metric)
		assert.Equal(t, 72*60+1+int(testNow.Minute()%1), r.Stored, "a full lookback window is stored on the first run")
		assert.Zero(t, r.Quality.MissingRatio)
	}
	for _, q := range f.querier.queries {
		assert.Contains(t, q, `namespace="prod"`, "template variables were rendered from the module")
		assert.NotContains(t, q, "{{")
	}

	*f.clock = testNow.Add(10 * time.Minute)
	results, err = f.collect(ctx)
	require.NoError(t, err)
	for _, r := range results {
		assert.LessOrEqual(t, r.Stored, 12, "only the new minutes are fetched, with one step of overlap")
	}
	var count int64
	require.NoError(t, f.db.Model(&model.MetricSample{}).Count(&count).Error)
	assert.LessOrEqual(t, count, int64(2*(72*60+1)+2), "old samples are pruned and re-collection adds no duplicates")
}

func TestCollectReportsGapsInsteadOfFillingThem(t *testing.T) {
	f := newFixture(t)
	f.bind(t, "cpu", "naive", nil, 0)
	f.bind(t, "rps", "naive", nil, 0)
	f.querier.skip = func(ts time.Time) bool { return ts.Hour() == 3 }
	results, err := f.collect(context.Background())
	require.NoError(t, err)
	assert.Greater(t, results[0].Quality.MissingRatio, 0.0)
	assert.Equal(t, 60, results[0].Quality.LongestGapSteps)

	h, err := f.svc.History(context.Background(), f.owner("cpu").ID, "cpu", testNow.Add(-24*time.Hour), testNow, 0)
	require.NoError(t, err)
	assert.Equal(t, 60, h.Quality.LongestGapSteps)
	assert.Less(t, len(h.Points), h.Quality.Expected, "missing steps stay missing in the response")
}

func TestCollectSkipsDisabledSourcesAndRejectsAModuleWithoutSources(t *testing.T) {
	f := newFixture(t)
	f.bind(t, "cpu", "naive", nil, 0)
	f.bind(t, "rps", "naive", nil, 0)
	f.disableSource(t, "rps")
	results, err := f.collect(context.Background())
	require.NoError(t, err)
	assert.Len(t, results, 1)

	f.disableSource(t, "cpu")
	_, err = f.collect(context.Background())
	assert.ErrorIs(t, err, ErrNotConfigured)
}

func TestCollectReportsADisabledDatasourceWithoutStoppingOtherMetrics(t *testing.T) {
	f := newFixture(t)
	f.bind(t, "cpu", "naive", nil, 0)
	f.bind(t, "rps", "naive", nil, 0)
	require.NoError(t, f.db.Model(&model.Datasource{}).Where("1 = 1").Update("enabled", false).Error)
	results, err := f.collect(context.Background())
	require.NoError(t, err)
	for _, r := range results {
		assert.Contains(t, r.Error, "disabled")
	}
}

func TestPredictThenBacktestThenRecommendEndToEnd(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.bind(t, "cpu", "seasonal_naive", map[string]any{"season_length_steps": 1440}, 0)
	f.bind(t, "cpu", "ewma", map[string]any{"alpha": 0.5}, 1)
	f.bind(t, "rps", "seasonal_naive", nil, 2)
	_, err := f.collect(ctx)
	require.NoError(t, err)

	outcomes, err := f.predict(ctx)
	require.NoError(t, err)
	require.Len(t, outcomes, 4, "every enabled algorithm forecasts every forecast source of the module")
	for _, o := range outcomes {
		assert.Equal(t, StatusOK, o.Status, "%s/%s: %s", o.Metric, o.AlgorithmID, o.Reason)
		assert.Equal(t, 15, o.Points)
	}
	rows, total, err := f.svc.Predictions.ListPredictions(ctx, rdb.PredictionFilter{ModuleID: f.module.ID, Metric: "cpu", AlgorithmID: "seasonal_naive"}, 1, 100)
	require.NoError(t, err)
	assert.EqualValues(t, 15, total)
	assert.Equal(t, testNow.Add(time.Minute), rows[0].TargetTime.UTC().Truncate(time.Minute).Add(0), "first target is one step after the newest sample")
	assert.Equal(t, "cores", rows[0].Unit)

	// Seasonal naive reproduces a noiseless daily series, so ewma cannot beat it and is not promoted.
	runs, err := f.backtest(ctx, "cpu", 4)
	require.NoError(t, err)
	byAlgo := map[string]*model.BacktestRun{}
	for _, r := range runs {
		byAlgo[r.AlgorithmID] = r
	}
	require.Contains(t, byAlgo, "seasonal_naive")
	require.Contains(t, byAlgo, "ewma")
	assert.True(t, byAlgo["seasonal_naive"].Promoted)
	assert.False(t, byAlgo["ewma"].Promoted)
	assert.Contains(t, byAlgo["ewma"].Reason, "does not beat baseline")

	f.perPodLevel(t) // the CPU values of this test are one Pod's: the request is the peak x headroom
	rec, detail, err := f.svc.Recommend(ctx, f.module.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, rec.Version)
	assert.Equal(t, "algorithm", rec.CPUSource)
	assert.Equal(t, "seasonal_naive", detail.CPU.Detail, "the unpromoted ewma is ignored although it ranks next")
	var considered []string
	for _, c := range detail.Considered {
		if c.AlgorithmID == "ewma" {
			considered = append(considered, c.Reason)
			assert.False(t, c.Eligible)
		}
	}
	require.NotEmpty(t, considered, "ewma forecasts every source of the module and is considered for each")
	for _, reason := range considered {
		assert.Contains(t, reason, "not eligible")
	}

	// the recommended cpu is the horizon peak of the seasonal forecast times the headroom
	var peak float64
	for _, r := range rows {
		peak = math.Max(peak, r.Value)
	}
	assert.EqualValues(t, RoundCPUMilli(int64(math.Ceil(peak*1.1*1000))), rec.CPUMilli, "the peak x headroom, rounded up to a readable step")
	assert.True(t, rec.CPUSet)
	assert.Equal(t, "algorithm", rec.ReplicasSource)
	assert.GreaterOrEqual(t, rec.Replicas, 2)
	assert.Equal(t, "original", rec.MemorySource, "memory has no mapping and no fallback, so the original value is kept")
	assert.Equal(t, testNow.Add(15*time.Minute), rec.ValidUntil.UTC())

	again, _, err := f.svc.Recommend(ctx, f.module.ID)
	require.NoError(t, err)
	assert.Equal(t, 2, again.Version, "every decision is a new immutable version")
}

func TestBypassKeepsOriginalValuesAndIsRecorded(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.bind(t, "cpu", "naive", nil, 0)
	_, err := f.collect(ctx)
	require.NoError(t, err)
	_, err = f.predict(ctx)
	require.NoError(t, err)
	require.NoError(t, f.svc.Modules.SetBypass(ctx, f.module.ID, true, "incident"))
	rec, detail, err := f.svc.Recommend(ctx, f.module.ID)
	require.NoError(t, err)
	assert.False(t, rec.CPUSet)
	assert.False(t, rec.ReplicasSet)
	assert.Equal(t, "bypass", rec.CPUSource)
	assert.True(t, detail.Bypass)
	assert.Equal(t, "incident", detail.BypassReason)
}

func TestFixedPeriodOverridesAndFallbackWhenModelsAreUnusable(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	// nothing collected or predicted: cpu falls back, replicas come from the active fixed period
	require.NoError(t, f.svc.Modules.SavePeriod(ctx, &model.ModulePeriod{
		ModuleID: f.module.ID, Name: "wed-morning", Enabled: true, Weekdays: 1 << int(time.Wednesday), StartMinute: 9 * 60, EndMinute: 12 * 60, Replicas: 9}))
	f.bind(t, "cpu", "naive", nil, 0)
	rec, detail, err := f.svc.Recommend(ctx, f.module.ID)
	require.NoError(t, err)
	assert.Equal(t, "fixed_period", rec.ReplicasSource)
	assert.Equal(t, 9, rec.Replicas)
	assert.Equal(t, "fallback", rec.CPUSource)
	assert.EqualValues(t, 500, rec.CPUMilli)
	assert.Equal(t, []string{"wed-morning"}, detail.ActivePeriods)
}

// An absolute window pins replicas, CPU and memory: inside it nothing an algorithm says changes them, outside it (or when
// the period is disabled) the decision is back to the algorithms and the module fallback. The detail lists every period
// with whether it applied, and the metric sources the figures come from.
func TestAbsoluteFixedPeriodPinsValuesInsideItsWindowOnly(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	start := testNow.Add(-time.Hour)
	end := testNow.Add(time.Hour)
	period := &model.ModulePeriod{ModuleID: f.module.ID, Name: "release", Enabled: true, AbsStart: &start, AbsEnd: &end,
		Replicas: 2, CPUMilli: 4000, MemoryBytes: 2 << 30}
	require.NoError(t, f.svc.Modules.SavePeriod(ctx, period))
	f.bind(t, "cpu", "naive", nil, 0)

	rec, detail, err := f.svc.Recommend(ctx, f.module.ID)
	require.NoError(t, err)
	assert.Equal(t, "fixed_period", rec.CPUSource)
	assert.Equal(t, "fixed_period", rec.ReplicasSource)
	assert.EqualValues(t, 4000, rec.CPUMilli)
	assert.Equal(t, 2, rec.Replicas)
	require.Len(t, detail.Periods, 1)
	assert.True(t, detail.Periods[0].Active)
	assert.False(t, detail.Periods[0].Weekly)
	assert.NotEmpty(t, detail.Sources, "the explanation names the metric sources")

	// after the window the module is back to its fallback
	*f.clock = end.Add(time.Minute)
	rec, detail, err = f.svc.Recommend(ctx, f.module.ID)
	require.NoError(t, err)
	assert.Equal(t, "fallback", rec.CPUSource)
	assert.EqualValues(t, 500, rec.CPUMilli)
	require.Len(t, detail.Periods, 1)
	assert.False(t, detail.Periods[0].Active)

	// a disabled period never applies, inside its window either
	*f.clock = testNow
	period.Enabled = false
	require.NoError(t, f.svc.Modules.SavePeriod(ctx, period))
	rec, detail, err = f.svc.Recommend(ctx, f.module.ID)
	require.NoError(t, err)
	assert.Equal(t, "fallback", rec.CPUSource)
	assert.Empty(t, detail.ActivePeriods)
	require.Len(t, detail.Periods, 1)
	assert.False(t, detail.Periods[0].Enabled)
}

func TestPredictSkipsStaleHistoryAndShortHistory(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.bind(t, "cpu", "seasonal_naive", map[string]any{"season_length_steps": 100000}, 0)
	f.bind(t, "cpu", "naive", nil, 1)
	_, err := f.collect(ctx)
	require.NoError(t, err)

	outcomes, err := f.predict(ctx)
	require.NoError(t, err)
	status := map[string]string{}
	for _, o := range outcomes {
		status[o.AlgorithmID] = o.Status
	}
	assert.Equal(t, StatusSkipped, status["seasonal_naive"], "a season longer than the history is skipped, never padded")
	assert.Equal(t, StatusOK, status["naive"])

	*f.clock = testNow.Add(2 * time.Hour) // the collector has not run for two hours
	outcomes, err = f.predict(ctx)
	require.NoError(t, err)
	for _, o := range outcomes {
		assert.Equal(t, StatusSkipped, o.Status)
		assert.Contains(t, o.Reason, "stale")
	}
}

func TestRecommendRefusesObserveModeAndStalePredictions(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.bind(t, "cpu", "naive", nil, 0)
	_, err := f.collect(ctx)
	require.NoError(t, err)
	_, err = f.predict(ctx)
	require.NoError(t, err)

	*f.clock = testNow.Add(time.Hour) // the prediction is older than the 15 minute ttl
	rec, detail, err := f.svc.Recommend(ctx, f.module.ID)
	require.NoError(t, err)
	assert.Equal(t, "fallback", rec.CPUSource)
	var reasons string
	for _, c := range detail.Considered {
		reasons += c.Reason
	}
	assert.Contains(t, reasons, "stale prediction")

	f.module.Mode = model.ModeObserve
	require.NoError(t, f.svc.Modules.Update(ctx, f.module))
	_, _, err = f.svc.Recommend(ctx, f.module.ID)
	assert.ErrorIs(t, err, ErrObserveOnly)
}

func TestChangedParametersInvalidateAPromotion(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.bind(t, "cpu", "naive", nil, 0)
	f.bind(t, "cpu", "ewma", map[string]any{"alpha": 0.9}, 1)
	// a series that trends upward: ewma lags, naive lags less, but the point is the promotion bookkeeping
	f.querier.value = func(q string, ts time.Time) float64 { return 1 + float64(ts.Unix()%86400)/86400 }
	_, err := f.collect(ctx)
	require.NoError(t, err)
	_, err = f.predict(ctx)
	require.NoError(t, err)
	_, err = f.backtest(ctx, "cpu", 3)
	require.NoError(t, err)

	// force a promotion record, then change the parameters: the record no longer applies
	require.NoError(t, f.db.Model(&model.BacktestRun{}).Where("algorithm_id = ?", "ewma").Update("promoted", true).Error)
	_, detail, err := f.svc.Recommend(ctx, f.module.ID)
	require.NoError(t, err)
	for _, c := range detail.Considered {
		if c.AlgorithmID == "ewma" {
			assert.True(t, c.Eligible)
		}
	}
	f.setParams(t, "ewma", map[string]any{"alpha": 0.1})
	_, detail, err = f.svc.Recommend(ctx, f.module.ID)
	require.NoError(t, err)
	for _, c := range detail.Considered {
		if c.AlgorithmID == "ewma" {
			assert.False(t, c.Eligible)
			assert.Contains(t, c.Reason, "changed since the promoting backtest")
		}
	}
}

func TestRunPipelineReportsPerStepErrors(t *testing.T) {
	f := newFixture(t)
	f.bind(t, "cpu", "naive", nil, 0)
	res, err := f.svc.RunPipeline(context.Background(), f.module.ID)
	require.NoError(t, err)
	assert.Empty(t, res.Errors)
	assert.NotNil(t, res.Recommend)
	assert.NotEmpty(t, res.Predict)

	f2 := newFixture(t)
	res, err = f2.svc.RunPipeline(context.Background(), f2.module.ID)
	require.NoError(t, err)
	assert.NotEmpty(t, res.Errors, "no source and no enabled algorithm: predict fails but the pipeline still reports and recommends")
}

func TestBacktestValidation(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	_, err := f.backtest(ctx, "memory", 3)
	assert.ErrorIs(t, err, ErrNotConfigured)
	_, err = f.backtest(ctx, "cpu", 3)
	assert.ErrorIs(t, err, ErrNotConfigured, "no source and no enabled algorithm")
	f.bind(t, "cpu", "naive", nil, 0)
	_, err = f.backtest(ctx, "cpu", 3)
	assert.ErrorIs(t, err, ErrNotConfigured, "no collected history")
	_, err = f.backtest(ctx, "cpu", 9999)
	assert.Error(t, err)
}

func TestHistoryOfALongWindowIsAveragedIntoBucketsButQualityUsesEveryStep(t *testing.T) {
	f := newFixture(t)
	inst := f.bind(t, "cpu", "naive", nil, 0)
	ctx := context.Background()
	f.querier.skip = func(ts time.Time) bool { return ts.Hour() == 3 }
	_, err := f.collect(ctx)
	require.NoError(t, err)

	// 72 hours at one minute is 4321 steps: with a 500 point budget they are averaged into buckets of 9 steps
	h, err := f.svc.History(ctx, inst.ID, "cpu", testNow.Add(-72*time.Hour), testNow, 500)
	require.NoError(t, err)
	assert.LessOrEqual(t, len(h.Points), 500)
	assert.Equal(t, 9*60, h.BucketSeconds)
	assert.Equal(t, 4321, h.Quality.Expected, "the quality report still covers every stored step")
	assert.Equal(t, 60, h.Quality.LongestGapSteps)
	for i := 1; i < len(h.Points); i++ {
		require.True(t, h.Points[i].Time.After(h.Points[i-1].Time))
	}
	// the three hours with no samples (one per day) produce no bucket points: a gap stays a gap
	var inGap int
	for _, p := range h.Points {
		if p.Time.Hour() == 3 && p.Time.Minute() >= 10 && p.Time.Minute() <= 50 {
			inGap++
		}
	}
	assert.Zero(t, inGap)

	full, err := f.svc.History(ctx, inst.ID, "cpu", testNow.Add(-6*time.Hour), testNow, 0)
	require.NoError(t, err)
	assert.Equal(t, 60, full.BucketSeconds, "a short window is returned at the stored resolution")

	_, err = f.svc.History(ctx, inst.ID, "cpu", testNow.Add(-400*24*time.Hour), testNow, 0)
	assert.Error(t, err, "the window itself is bounded")
}

func TestDownsampleAveragesWholeBuckets(t *testing.T) {
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var points []HistoryPoint
	for i := 0; i < 10; i++ {
		points = append(points, HistoryPoint{Time: from.Add(time.Duration(i) * time.Minute), Value: float64(i)})
	}
	out, bucket := downsample(points, from, time.Minute, 10, 5)
	assert.Equal(t, 2*time.Minute, bucket)
	require.Len(t, out, 5)
	assert.Equal(t, 0.5, out[0].Value)
	assert.Equal(t, 8.5, out[4].Value)
	same, bucket := downsample(points, from, time.Minute, 10, 10)
	assert.Equal(t, points, same)
	assert.Equal(t, time.Minute, bucket)
}

func TestOnlyBaselinesThatNeedNoParameterStartEnabled(t *testing.T) {
	assert.True(t, DefaultEnabled("naive"))
	assert.False(t, DefaultEnabled("seasonal_naive"), "it cannot run until the season length is set")
	assert.False(t, DefaultEnabled("gbdt"), "the heavy algorithms wait to be enabled")
	assert.False(t, DefaultEnabled("nope"))
}

func TestAnAlgorithmThatCannotRunDoesNotStopTheOthersFromBeingBacktested(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.bind(t, "cpu", "naive", nil, 0)
	f.bind(t, "cpu", "seasonal_naive", nil, 1) // enabled without its required season length
	_, err := f.collect(ctx)
	require.NoError(t, err)
	outcomes, err := f.predict(ctx)
	require.NoError(t, err)
	byAlgorithm := map[string]string{}
	for _, o := range outcomes {
		byAlgorithm[o.AlgorithmID] = o.Status
	}
	assert.Equal(t, StatusOK, byAlgorithm["naive"])
	assert.Equal(t, StatusFailed, byAlgorithm["seasonal_naive"], "the missing parameter is reported for that algorithm only")

	runs, err := f.backtest(ctx, "cpu", 3)
	require.NoError(t, err)
	require.Len(t, runs, 1)
	assert.Equal(t, "naive", runs[0].AlgorithmID)
}
