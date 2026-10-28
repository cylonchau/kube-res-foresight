package forecastsvc

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cylonchau/kube-resource-fcst/pkg/decision"
	"github.com/cylonchau/kube-resource-fcst/pkg/forecast"
	"github.com/cylonchau/kube-resource-fcst/pkg/model"
)

func (f *fixture) addEvaluations(t *testing.T, metric, algorithm string, peakUnder float64, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		require.NoError(t, f.db.Create(&model.PredictionEvaluation{
			ModuleID: f.module.ID, Metric: metric, AlgorithmID: algorithm, ModelVersion: "v", PredictionTime: testNow.Add(-time.Duration(i+1) * time.Hour),
			QuantilePermille: 500, Points: 10, Expected: 10, PeakUnder: peakUnder,
		}).Error)
	}
}

func (f *fixture) dynamic(t *testing.T, mutate func(*model.ForecastModule)) {
	t.Helper()
	f.module.ReplicaMode = model.ReplicaModeDynamic
	if mutate != nil {
		mutate(f.module)
	}
	require.NoError(t, f.module.Validate())
	require.NoError(t, f.db.Save(f.module).Error)
}

func peakOfRun(t *testing.T, f *fixture, metric, algorithm string) float64 {
	t.Helper()
	rows, err := f.svc.Predictions.LatestRun(context.Background(), f.module.ID, metric, algorithm)
	require.NoError(t, err)
	peak := math.Inf(-1)
	for _, r := range rows {
		if !r.TargetTime.Before(testNow) {
			peak = math.Max(peak, r.Value)
		}
	}
	return peak
}

func TestDynamicReplicasAreMoreConservativeForTheAlgorithmWithTheLargerMeasuredError(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.bind(t, "rps", "seasonal_naive", map[string]any{"season_length_steps": 1440}, 0)
	f.bind(t, "rps", "naive", nil, 1)
	_, err := f.collect(ctx)
	require.NoError(t, err)
	_, err = f.predict(ctx)
	require.NoError(t, err)
	f.addEvaluations(t, "rps", "seasonal_naive", 0.05, 6)
	f.addEvaluations(t, "rps", "naive", 0.50, 6)
	f.dynamic(t, nil)

	rec, detail, err := f.svc.Recommend(ctx, f.module.ID)
	require.NoError(t, err)
	require.NotNil(t, detail.Replica)
	require.Len(t, detail.Replica.Plans, 2)
	plans := map[string]decision.ReplicaPlan{}
	for _, p := range detail.Replica.Plans {
		plans[p.Algorithm] = p
	}
	accurate, noisy := plans["seasonal_naive"], plans["naive"]
	require.NotEmpty(t, accurate.Algorithm)
	require.NotEmpty(t, noisy.Algorithm)
	assert.Less(t, accurate.Replicas, noisy.Replicas, "the algorithm that missed peaks by more must be given more replicas")
	assert.Equal(t, decision.ConstraintThroughput, accurate.Binding)
	assert.Equal(t, decision.UncertaintyMeasured, accurate.Constraints[0].UncertaintySource)

	// the recommendation follows the plan of the algorithm in use (none is scored yet, so the order of the ids decides),
	// with the uncertainty derived from that algorithm's own evaluations
	chosen := plans[detail.Replicas.Detail]
	require.NotEmpty(t, chosen.Algorithm)
	assert.Equal(t, chosen.Replicas, rec.Replicas)
	assert.Equal(t, "algorithm", rec.ReplicasSource)
	u := 0.05 / 0.95
	want := int(math.Ceil(peakOfRun(t, f, "rps", "seasonal_naive") * (1 + u) / 100))
	assert.Equal(t, want, accurate.Replicas)
	assert.Equal(t, decision.CapacityManual, detail.Replica.Capacity.Source)
}

func TestDynamicReplicasWithoutEvaluationsFallBackToTheHeadroomAndSaySo(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.bind(t, "rps", "naive", nil, 0)
	_, err := f.collect(ctx)
	require.NoError(t, err)
	_, err = f.predict(ctx)
	require.NoError(t, err)
	f.dynamic(t, nil)

	rec, detail, err := f.svc.Recommend(ctx, f.module.ID)
	require.NoError(t, err)
	plan := detail.Replica.Plans[0]
	assert.Equal(t, decision.UncertaintyHeadroom, plan.Constraints[0].UncertaintySource)
	want := int(math.Ceil(peakOfRun(t, f, "rps", "naive") * f.module.HeadroomRatio / 100))
	assert.Equal(t, want, rec.Replicas, "this is exactly the fixed-mode formula")
}

func TestFixedModeIsUnchangedByEvaluations(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.bind(t, "rps", "naive", nil, 0)
	_, err := f.collect(ctx)
	require.NoError(t, err)
	_, err = f.predict(ctx)
	require.NoError(t, err)
	f.addEvaluations(t, "rps", "naive", 0.8, 10)

	rec, detail, err := f.svc.Recommend(ctx, f.module.ID)
	require.NoError(t, err)
	assert.Nil(t, detail.Replica, "fixed mode does not solve")
	assert.Equal(t, int(math.Ceil(peakOfRun(t, f, "rps", "naive")*f.module.HeadroomRatio/100)), rec.Replicas)
}

func TestDynamicReplicasUseCPUWhenRequestsAreSmall(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.bind(t, "rps", "naive", nil, 0)
	f.bind(t, "cpu", "naive", nil, 0)
	_, err := f.collect(ctx)
	require.NoError(t, err)
	_, err = f.predict(ctx)
	require.NoError(t, err)
	// the synthetic workload uses 0.5-1.5 cores; 100m requests at 50% target make cpu the binding constraint
	f.dynamic(t, func(m *model.ForecastModule) { m.PodCPURequestMilli, m.TargetUtilization = 100, 0.5 })

	rec, detail, err := f.svc.Recommend(ctx, f.module.ID)
	require.NoError(t, err)
	plan := detail.Replica.Plans[0]
	assert.Equal(t, decision.ConstraintCPU, plan.Binding)
	want := int(math.Ceil(peakOfRun(t, f, "cpu", "naive") * f.module.HeadroomRatio / (0.1 * 0.5)))
	assert.Equal(t, want, rec.Replicas, "ceil(cpu peak x headroom / (request x target))")
	assert.Greater(t, plan.Constraints[0].Needed, 0)
}

func TestDynamicReplicasEstimateCapacityFromHistoryOrExplainWhatIsMissing(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	repl := &model.MetricMapping{DatasourceID: f.cpuMap.DatasourceID, Name: "replicas", Metric: "replicas", Level: "workload", Unit: "count", StepSeconds: 60, Enabled: true,
		Query: `sum(kube_deployment_status_replicas_available{namespace="{{namespace}}",deployment="{{workload}}"})`}
	require.NoError(t, f.db.Create(repl).Error)
	f.querier.value = func(query string, ts time.Time) float64 {
		switch {
		case contains(query, "cpu{"):
			return daily(2.8, 0.4, ts) // 4 pods with a 1 core request: 60-80% busy
		case contains(query, "kube_deployment"):
			return 4
		}
		return daily(300, 100, ts)
	}
	f.bind(t, "rps", "naive", nil, 0)
	// the CPU history is measured for the capacity estimate; no CPU forecast is made, so there is no CPU constraint
	aux := f.auxInstance(t)
	cpuHistory := &model.MetricSource{ModuleID: aux.ID, Name: "cpu", Kind: "cpu", MappingID: f.cpuMap.ID, Weight: 1, Stat: "max", Basis: model.BasisHistory, WindowHours: 24, Enabled: true}
	require.NoError(t, cpuHistory.Validate())
	require.NoError(t, f.svc.Modules.SaveSource(context.Background(), cpuHistory))
	f.maps["replicas"] = repl.ID
	f.addSourceTo(t, f.auxInstance(t), "replicas")
	f.dynamic(t, func(m *model.ForecastModule) {
		m.CapacityMode, m.PodCPURequestMilli, m.TargetUtilization, m.PodCapacityRPS = model.CapacityModeEstimate, 1000, 0.7, 0
	})
	_, err := f.collect(ctx)
	require.NoError(t, err)
	_, err = f.predict(ctx)
	require.NoError(t, err)

	_, detail, err := f.svc.Recommend(ctx, f.module.ID)
	require.NoError(t, err)
	assert.Equal(t, decision.CapacityEstimated, detail.Replica.Capacity.Source, detail.Replica.Capacity.Note)
	assert.Greater(t, detail.Replica.Capacity.RPSPerPod, 0.0)
	require.NotEmpty(t, detail.Replica.Plans)

	// without enough busy moments the capacity is refused, with the reason stated
	f.querier.value = func(query string, ts time.Time) float64 {
		switch {
		case contains(query, "cpu{"):
			return 0.1
		case contains(query, "kube_deployment"):
			return 4
		}
		return 300
	}
	require.NoError(t, f.db.Exec("DELETE FROM metric_sample").Error)
	_, err = f.collect(ctx)
	require.NoError(t, err)
	_, err = f.predict(ctx)
	require.NoError(t, err)
	_, detail, err = f.svc.Recommend(ctx, f.module.ID)
	require.NoError(t, err)
	assert.Equal(t, decision.CapacityNone, detail.Replica.Capacity.Source)
	assert.Contains(t, detail.Replica.Capacity.Note, "near saturation")
	for _, p := range detail.Replica.Plans {
		for _, c := range p.Constraints {
			assert.NotEqual(t, decision.ConstraintThroughput, c.Name, "no capacity means no throughput constraint, so nothing is invented")
		}
	}
}

// A shortfall measured on a few jumps of the load (a stress test, a release) must not multiply the forecast by 10 for good: the
// factor is cut at 2 and the plan says that it was cut. Before the cap, an algorithm that had missed a peak by 95% five times in
// its last thirty runs was given (1 + 9) times its forecast, which put the replicas at the maximum whatever the load was.
func TestAMeasuredShortfallIsCutAtTheCapAndThePlanSaysSo(t *testing.T) {
	assert.InDelta(t, 1.0, shortfallToUncertainty(0.95), 1e-9, "a factor of 2 at most")
	assert.InDelta(t, 1.0, shortfallToUncertainty(0.5), 1e-9)
	assert.InDelta(t, 0.25, shortfallToUncertainty(0.2), 1e-9, "a shortfall below the cap is used as it is")

	f := newFixture(t)
	ctx := context.Background()
	f.bind(t, "rps", "naive", nil, 0)
	_, err := f.collect(ctx)
	require.NoError(t, err)
	_, err = f.predict(ctx)
	require.NoError(t, err)
	f.addEvaluations(t, "rps", "naive", 0.95, 6)
	f.dynamic(t, nil)

	_, detail, err := f.svc.Recommend(ctx, f.module.ID)
	require.NoError(t, err)
	require.NotNil(t, detail.Replica)
	require.Len(t, detail.Replica.Plans, 1)
	plan := detail.Replica.Plans[0]
	want := int(math.Ceil(peakOfRun(t, f, "rps", "naive") * 2 / 100))
	assert.Equal(t, want, plan.Replicas, "twice the peak over the capacity of a pod, not ten times")
	var said bool
	for _, n := range detail.Replica.Notes {
		said = said || (strings.Contains(n, "naive") && strings.Contains(n, "limit"))
	}
	assert.True(t, said, "the plan says the shortfall was cut: %v", detail.Replica.Notes)
}

// The cap is on the solver, not on one algorithm: every algorithm of the catalogue, with the worst shortfall that can be measured
// for it, is given at most twice its forecast, for every constraint, whoever made the measurement.
func TestNoAlgorithmIsScaledByMoreThanTwoWhateverItsMeasuredShortfall(t *testing.T) {
	huge := 9.0 // what a shortfall of 0.9 used to be turned into
	for _, id := range forecast.IDs() {
		cfg := decision.SolverConfig{CapacityRPS: 100, CapacitySource: decision.CapacityManual, Headroom: 1.1,
			PodCPUCores: 0.1, PodMemoryBytes: 1 << 20, TargetUtilization: 0.9}
		candidate := func(v float64) decision.SeriesCandidate {
			return decision.SeriesCandidate{Algorithm: id, Values: []float64{v}, Uncertainty: &huge, UncertaintySource: decision.UncertaintyMeasured}
		}
		plans := decision.SolveReplicas(cfg, decision.ReplicaInputs{
			RPS: []decision.SeriesCandidate{candidate(450)}, CPU: []decision.SeriesCandidate{candidate(0.7)}, Memory: []decision.SeriesCandidate{candidate(5 << 20)},
		})
		require.Len(t, plans, 1, id)
		for _, c := range plans[0].Constraints {
			assert.LessOrEqual(t, c.Uncertainty, decision.MaxUncertainty, "%s %s", id, c.Name)
			assert.LessOrEqual(t, float64(c.Needed), math.Ceil(c.PeakDemand*2/c.UnitCapacity)+1e-9, "%s %s: at most twice the forecast over a pod", id, c.Name)
		}
	}
	// the load of the report: 700m on pods of 100m at 90% with a P90 shortfall of 0.95 is 9 replicas, not the maximum
	plans := decision.SolveReplicas(decision.SolverConfig{PodCPUCores: 0.1, TargetUtilization: 0.9, Headroom: 1.1}, decision.ReplicaInputs{
		CPU: []decision.SeriesCandidate{{Algorithm: "seasonal_naive", Values: []float64{0.3973}, Uncertainty: &huge, UncertaintySource: decision.UncertaintyMeasured}},
	})
	require.Len(t, plans, 1)
	assert.Equal(t, 9, plans[0].Replicas)
}
