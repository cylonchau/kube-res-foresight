package forecastsvc

import (
	"context"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cylonchau/kube-resource-fcst/pkg/model"
)

func TestCatalogDefinitionsAreComplete(t *testing.T) {
	f := newFixture(t)
	names := map[string]bool{}
	groups := map[string]bool{GroupUsage: true, GroupPrediction: true, GroupPerformance: true, GroupExecution: true}
	allowedLabels := map[string]bool{"metric": true, "algorithm": true, "quantile": true}
	seenGroups := map[string]bool{}
	for _, d := range Catalog() {
		assert.False(t, names[d.Name], "duplicate metric %s", d.Name)
		names[d.Name] = true
		assert.True(t, groups[d.Group], "%s: unknown group %q", d.Name, d.Group)
		seenGroups[d.Group] = true
		assert.NotEmpty(t, d.Help.ZH, d.Name)
		assert.NotEmpty(t, d.Help.EN, d.Name)
		assert.NotEmpty(t, d.Value.ZH, d.Name)
		assert.NotEmpty(t, d.Unit, d.Name)
		assert.Contains(t, []string{AggAvg, AggMax, AggLast}, d.Agg, d.Name)
		assert.True(t, f.db.Migrator().HasTable(d.Table), "%s reads %s, which does not exist", d.Name, d.Table)
		assert.NotNil(t, d.load, d.Name)
		for _, l := range d.Labels {
			assert.True(t, allowedLabels[l], "%s: label %q is not in the bounded label set", d.Name, l)
		}
		if d.Group != GroupUsage {
			assert.Positive(t, d.RetentionDays, "%s must state how long its rows are kept", d.Name)
		}
		if d.Export != nil {
			assert.True(t, strings.HasPrefix(d.Export.Name, "kube_resource_fcst_"), d.Name)
			assert.Positive(t, d.Export.Window, d.Name)
		}
	}
	for g := range groups {
		assert.True(t, seenGroups[g], "the catalog has no %s metric", g)
	}
	// the retention stated by the catalog is the one the pruning job applies
	pred, ok := LookupMetric("prediction_value")
	require.True(t, ok)
	assert.Equal(t, 30, pred.RetentionDays)
	evalDef, _ := LookupMetric("evaluation_wape")
	assert.Equal(t, 180, evalDef.RetentionDays)
	_, ok = LookupMetric("nope")
	assert.False(t, ok)
}

// seedForecastData fills every table the catalog reads for the fixture module.
func seedForecastData(t *testing.T, f *fixture) {
	t.Helper()
	ctx := context.Background()
	f.bind(t, "cpu", "naive", nil, 0)
	f.bind(t, "cpu", "seasonal_naive", map[string]any{"season_length_steps": 1440}, 1)
	f.bind(t, "rps", "naive", nil, 0)
	_, err := f.collect(ctx)
	require.NoError(t, err)
	_, err = f.predict(ctx)
	require.NoError(t, err)
	_, err = f.backtest(ctx, "cpu", 2)
	require.NoError(t, err)
	_, _, err = f.svc.Recommend(ctx, f.module.ID)
	require.NoError(t, err)
	for i, algo := range []string{"naive", "seasonal_naive"} {
		for k := 1; k <= 3; k++ {
			require.NoError(t, f.db.Create(&model.PredictionEvaluation{
				ModuleID: f.module.ID, Metric: "cpu", AlgorithmID: algo, ModelVersion: "v", PredictionTime: testNow.Add(-time.Duration(k*(i+2)) * time.Hour),
				QuantilePermille: 500, Points: 10, Expected: 10, MAE: float64(i+1) * 0.1, WAPE: 0.1 * float64(i+1) * float64(k), Bias: -0.01, PeakUnder: 0.05 * float64(i+1), Coverage: 0.9,
			}).Error)
		}
	}
	require.NoError(t, f.db.Create(&model.ScalingExecution{ModuleID: f.module.ID, Namespace: "prod", Scaler: "api", TargetKind: "Deployment", TargetName: "api",
		Current: 4, Ready: 3, Desired: 6, Action: "up", Result: model.ExecApplied}).Error)
}

type scopeOwner struct {
	id   uint
	name string
}

// owners are the objects a metric of the catalog belongs to: the module.
func owners(f *fixture, _ MetricDef) []scopeOwner {
	return []scopeOwner{{f.module.ID, f.module.Name}}
}

// The catalog, /metrics and the query API describe the same stored rows: every exported series equals the
// newest point of the same metric from the query API.
func TestCatalogMetricsAndQueryAgree(t *testing.T) {
	f := newFixture(t)
	seedForecastData(t, f)
	ctx := context.Background()

	reg := prometheus.NewRegistry()
	require.NoError(t, reg.Register(NewCollector(f.svc)))
	families, err := reg.Gather()
	require.NoError(t, err)
	exported := map[string]map[string]float64{} // metric family -> label set -> value
	for _, mf := range families {
		exported[mf.GetName()] = map[string]float64{}
		for _, m := range mf.GetMetric() {
			labels := map[string]string{}
			for _, l := range m.GetLabel() {
				labels[l.GetName()] = l.GetValue()
			}
			exported[mf.GetName()][labelKey(labels)] = m.GetGauge().GetValue()
		}
	}

	checked := 0
	for _, def := range Catalog() {
		if def.Export == nil {
			continue
		}
		for _, o := range owners(f, def) {
			latest, err := f.svc.latest(ctx, def, o.id, f.svc.now())
			require.NoError(t, err, def.Name)
			for _, p := range latest {
				labels := map[string]string{"module": o.name}
				for _, l := range def.Labels {
					if l != "quantile" {
						labels[l] = p.labels[l]
					}
				}
				for k, v := range def.Export.Fixed {
					labels[k] = v
				}
				got, ok := exported[def.Export.Name][labelKey(labels)]
				require.True(t, ok, "%s: /metrics has no series %v", def.Name, labels)
				assert.InDelta(t, p.value, got, 1e-9, "%s: /metrics differs from the stored row", def.Name)

				// the same point through the query API: a one second window around it holds that point alone
				qp := QueryParams{Name: def.Name, Metric: p.labels["metric"], Algorithm: p.labels["algorithm"],
					From: p.ts.Add(-5 * time.Second), To: p.ts.Add(5 * time.Second), Step: time.Second}
				qp.ModuleID = o.id
				res, err := f.svc.Query(ctx, qp)
				require.NoError(t, err, def.Name)
				var found bool
				for _, s := range res.Series {
					if labelKey(s.Labels) != labelKey(p.labels) {
						continue
					}
					last := s.Points[len(s.Points)-1]
					assert.InDelta(t, got, last.Value, 1e-9, "%s: the query differs from /metrics", def.Name)
					found = true
				}
				assert.True(t, found, "%s: the query has no series %v", def.Name, p.labels)
				checked++
			}
		}
	}
	assert.Greater(t, checked, 10, "the seeded data should exercise most exported metrics")

	// every metric the collector exposes is either a catalog export or one of the operational gauges
	operational := map[string]bool{
		"kube_resource_fcst_module_enabled": true, "kube_resource_fcst_module_bypass": true, "kube_resource_fcst_recommendation_source": true,
		"kube_resource_fcst_recommendation_age_seconds": true, "kube_resource_fcst_recommendation_valid": true,
		"kube_resource_fcst_series_latest_sample_age_seconds": true, "kube_resource_fcst_prediction_age_seconds": true, "kube_resource_fcst_forecast_metrics_error": true,
		"kube_resource_fcst_datasource_up": true, "kube_resource_fcst_datasource_latency_seconds": true,
		"kube_resource_fcst_algorithm_score": true, "kube_resource_fcst_algorithm_selected": true, "kube_resource_fcst_effective_max_replicas": true,
		"kube_resource_fcst_hard_max_replicas": true, "kube_resource_fcst_consecutive_failures": true,
	}
	declared := map[string]bool{}
	for _, def := range Catalog() {
		if def.Export != nil {
			declared[def.Export.Name] = true
		}
	}
	for name := range exported {
		assert.True(t, operational[name] || declared[name], "%s is exposed on /metrics but neither declared in the catalog nor an operational gauge", name)
	}
	for _, mf := range families {
		for _, m := range mf.GetMetric() {
			for _, l := range m.GetLabel() {
				assert.Contains(t, []string{"module", "instance", "mode", "resource", "source", "metric", "algorithm", "state", "datasource"}, l.GetName(), "no unbounded label on %s", mf.GetName())
			}
		}
	}
}

func TestQueryBucketsLongHistoryWithinThePointLimit(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	inst := f.bind(t, "cpu", "naive", nil, 0)
	series, err := f.svc.Samples.EnsureSeries(ctx, inst.ID, "cpu", f.cpuMap.ID, 60, "")
	require.NoError(t, err)
	const n = 12000 // more than ten thousand stored points
	rows := make([]model.MetricSample, n)
	start := testNow.Add(-n * time.Minute)
	for i := range rows {
		rows[i] = model.MetricSample{SeriesID: series.ID, TS: start.Add(time.Duration(i) * time.Minute).Unix(), Value: float64(i % 100)}
	}
	require.NoError(t, f.svc.Samples.Upsert(ctx, series.ID, rows))

	res, err := f.svc.Query(ctx, QueryParams{Name: "usage_cpu_cores", ModuleID: inst.ID, From: start, To: testNow, MaxPoints: 500})
	require.NoError(t, err)
	require.Len(t, res.Series, 1)
	assert.LessOrEqual(t, len(res.Series[0].Points), 500)
	assert.Greater(t, len(res.Series[0].Points), 100)
	assert.False(t, res.Truncated)
	assert.Equal(t, "cores", res.Unit)
	for _, p := range res.Series[0].Points {
		assert.EqualValues(t, 0, p.TS%int64(res.StepSeconds), "buckets are aligned to the step")
	}
	// averaging 100-value sawtooth buckets stays inside the data range
	for _, p := range res.Series[0].Points {
		assert.GreaterOrEqual(t, p.Value, 0.0)
		assert.LessOrEqual(t, p.Value, 99.0)
	}

	_, err = f.svc.Query(ctx, QueryParams{Name: "usage_cpu_cores", ModuleID: inst.ID, From: start, To: testNow, Step: time.Minute, MaxPoints: 500})
	assert.ErrorIs(t, err, ErrBadQuery, "a step that would return more than the limit is refused with the step that works")
	_, err = f.svc.Query(ctx, QueryParams{Name: "usage_cpu_cores", ModuleID: inst.ID, From: start, To: testNow, MaxPoints: MaxQueryPoints + 1})
	assert.ErrorIs(t, err, ErrBadQuery)
	_, err = f.svc.Query(ctx, QueryParams{Name: "nope", ModuleID: f.owner("cpu").ID})
	assert.ErrorIs(t, err, ErrBadQuery)
	_, err = f.svc.Query(ctx, QueryParams{Name: "prediction_value", ModuleID: f.owner("cpu").ID})
	assert.ErrorIs(t, err, ErrBadQuery, "prediction metrics need the metric kind for their unit")
}

func TestBucketizeCombinesPointsAsTheMetricDeclares(t *testing.T) {
	at := func(sec int64, v float64) rawPoint {
		return rawPoint{labels: map[string]string{"a": "x"}, ts: time.Unix(sec, 0), value: v}
	}
	points := []rawPoint{at(100, 1), at(110, 5), at(119, 3), at(130, 9)}
	byAgg := func(agg string) []Point { return bucketize(points, agg, 30)[0].Points }
	assert.Equal(t, []Point{{90, 3}, {120, 9}}, byAgg(AggAvg))
	assert.Equal(t, []Point{{90, 5}, {120, 9}}, byAgg(AggMax))
	assert.Equal(t, []Point{{90, 3}, {120, 9}}, byAgg(AggLast))
	assert.Empty(t, bucketize([]rawPoint{at(1, math.NaN())}, AggAvg, 30), "non-finite values are dropped")
}

// Comparing algorithms over the same window: every series lands on the same grid of target times.
func TestPredictionSeriesOfAlgorithmsShareTheTargetTimeGrid(t *testing.T) {
	f := newFixture(t)
	seedForecastData(t, f)
	ctx := context.Background()
	window := func(name string) *QueryResult {
		res, err := f.svc.Query(ctx, QueryParams{Name: "prediction_value", ModuleID: f.module.ID, Metric: "cpu", Algorithm: name, From: testNow.Add(-time.Hour), To: testNow.Add(time.Hour), Step: time.Minute})
		require.NoError(t, err)
		require.Len(t, res.Series, 1, name)
		assert.Equal(t, "cores", res.Unit)
		return res
	}
	naive, seasonal := window("naive"), window("seasonal_naive")
	grid := func(s Series) []int64 {
		out := make([]int64, len(s.Points))
		for i, p := range s.Points {
			out[i] = p.TS
		}
		return out
	}
	assert.Equal(t, grid(naive.Series[0]), grid(seasonal.Series[0]), "two algorithms forecasting the same source land on the same grid")
	assert.NotEmpty(t, naive.Series[0].Points)

	// the newest prediction of a target time wins when a later run overlaps an earlier one
	later := testNow.Add(time.Minute)
	*f.clock = later
	_, err := f.predict(ctx)
	require.NoError(t, err)
	again, err := f.svc.Query(ctx, QueryParams{Name: "prediction_value", ModuleID: f.module.ID, Metric: "cpu", Algorithm: "naive", From: testNow, To: testNow.Add(time.Hour), Step: time.Minute})
	require.NoError(t, err)
	require.Len(t, again.Series, 1)
	seen := map[int64]bool{}
	for _, p := range again.Series[0].Points {
		assert.False(t, seen[p.TS], "one value per target time")
		seen[p.TS] = true
	}
}

// An evaluation can be done from the query API alone: ranking algorithms by the mean of evaluation_wape gives
// the order the leaderboard shows.
func TestAlgorithmsCanBeRankedFromTheQueryAPIAlone(t *testing.T) {
	f := newFixture(t)
	seedForecastData(t, f)
	ctx := context.Background()
	mean := map[string]float64{}
	for range 1 {
		res, err := f.svc.Query(ctx, QueryParams{Name: "evaluation_wape", ModuleID: f.module.ID, Metric: "cpu", From: testNow.Add(-24 * time.Hour), To: testNow, Step: time.Minute, MaxPoints: 2000})
		require.NoError(t, err)
		for _, s := range res.Series {
			sum := 0.0
			for _, p := range s.Points {
				sum += p.Value
			}
			mean[s.Labels["algorithm"]] = sum / float64(len(s.Points))
		}
	}
	require.Len(t, mean, 2)
	best := "naive"
	if mean["seasonal_naive"] < mean["naive"] {
		best = "seasonal_naive"
	}
	board, err := f.svc.Leaderboard(ctx, f.module.ID, "cpu", nil, nil)
	require.NoError(t, err)
	require.NotEmpty(t, board)
	assert.Equal(t, best, board[0].AlgorithmID, fmt.Sprintf("query means %v", mean))
}

func TestTelemetryCountsThePipelineWorkWithBoundedLabels(t *testing.T) {
	f := newFixture(t)
	tel := NewTelemetry()
	f.svc.Telemetry = tel
	seedForecastData(t, f)
	_, err := f.evaluate(context.Background())
	require.NoError(t, err)

	reg := prometheus.NewRegistry()
	reg.MustRegister(tel.Collectors()...)
	families, err := reg.Gather()
	require.NoError(t, err)
	seen := map[string]bool{}
	for _, mf := range families {
		seen[mf.GetName()] = true
		for _, m := range mf.GetMetric() {
			for _, l := range m.GetLabel() {
				assert.Contains(t, []string{"metric", "algorithm", "result", "le"}, l.GetName(), "%s has an unbounded label", mf.GetName())
			}
		}
	}
	for _, want := range []string{"kube_resource_fcst_collect_runs_total", "kube_resource_fcst_collect_duration_seconds", "kube_resource_fcst_predict_runs_total",
		"kube_resource_fcst_predict_duration_seconds", "kube_resource_fcst_backtest_runs_total", "kube_resource_fcst_backtest_duration_seconds"} {
		assert.True(t, seen[want], "%s was not recorded", want)
	}
	// both algorithms of cpu and the rps one were predicted and one backtest pass ran for cpu
	var predicted, backtests float64
	for _, mf := range families {
		for _, m := range mf.GetMetric() {
			switch mf.GetName() {
			case "kube_resource_fcst_predict_runs_total":
				predicted += m.GetCounter().GetValue()
			case "kube_resource_fcst_backtest_runs_total":
				backtests += m.GetCounter().GetValue()
			}
		}
	}
	assert.EqualValues(t, 4, predicted)
	assert.EqualValues(t, 2, backtests)
}
