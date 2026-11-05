package forecastsvc

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cylonchau/kube-res-foresight/pkg/decision"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
)

func (f *fixture) predictedRPS(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	f.bind(t, "rps", "naive", nil, 0)
	_, err := f.collect(ctx)
	require.NoError(t, err)
	_, err = f.predict(ctx)
	require.NoError(t, err)
}

// The recommendation says what the forecast needs before the bounds of the module, so a minimum above the need can be seen and lowered, and
// a maximum below it raised, instead of the recommendation being the bound whatever the forecast says.
func TestTheNeedIsReadBeforeTheMinimumAndTheMaximumOfTheModule(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.predictedRPS(t)

	f.dynamic(t, func(m *model.ForecastModule) { m.MinReplicas, m.MaxReplicas = 25, 30 })
	rec, detail, err := f.svc.Recommend(ctx, f.module.ID)
	require.NoError(t, err)
	require.NotNil(t, detail.Need)
	need := detail.Need.Replicas
	assert.Equal(t, 25, rec.Replicas, "the minimum of the module holds the recommendation")
	assert.EqualValues(t, 25, need.Recommended)
	assert.Greater(t, need.Needed, int64(0))
	assert.Less(t, need.Needed, int64(25), "what the forecast needs is below the minimum")
	assert.Equal(t, "min", need.Bound)
	assert.Equal(t, AdviceLowerMin, need.Advice)
	assert.EqualValues(t, 25, need.Min)

	f.dynamic(t, func(m *model.ForecastModule) { m.MinReplicas, m.MaxReplicas = 1, 2 })
	_, detail, err = f.svc.Recommend(ctx, f.module.ID)
	require.NoError(t, err)
	need = detail.Need.Replicas
	assert.Greater(t, need.Needed, int64(2), "the forecast needs more than the maximum allows")
	assert.Equal(t, "max", need.Bound)
	assert.Equal(t, AdviceRaiseMax, need.Advice)

	f.dynamic(t, func(m *model.ForecastModule) { m.MinReplicas, m.MaxReplicas = 1, 30 })
	_, detail, err = f.svc.Recommend(ctx, f.module.ID)
	require.NoError(t, err)
	need = detail.Need.Replicas
	assert.Empty(t, need.Bound)
	assert.Empty(t, need.Advice)
	assert.Equal(t, need.Needed, need.Recommended, "within the bounds the need is the recommendation")
}

// The need can be read before a module is allowed to act: a module in observe mode makes no recommendation, but its need is answered, and
// nothing is stored.
func TestTheNeedOfAModuleInObserveModeIsAnsweredAndNothingIsStored(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.predictedRPS(t)
	f.dynamic(t, func(m *model.ForecastModule) { m.Mode = model.ModeObserve })

	_, _, err := f.svc.Recommend(ctx, f.module.ID)
	require.ErrorIs(t, err, ErrObserveOnly)

	var before, after int64
	require.NoError(t, f.db.Model(&model.Recommendation{}).Count(&before).Error)
	row, detail, err := f.svc.Estimate(ctx, f.module.ID)
	require.NoError(t, err)
	require.NotNil(t, detail.Need)
	assert.Equal(t, "forecast", detail.Need.Basis)
	assert.Greater(t, detail.Need.Replicas.Needed, int64(0))
	assert.Zero(t, row.ID, "the estimate is not stored")
	require.NoError(t, f.db.Model(&model.Recommendation{}).Count(&after).Error)
	assert.Equal(t, before, after)
}

// The need is told in three parts that are not one number: the load measured now, the load forecast, and the margin put on the forecast. A
// forecast that is deliberately high (a percentile) says so, because it is a level that is enough most of the time, not what is expected.
func TestTheNeedIsToldAsTheLoadNowTheForecastAndTheMargin(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.bind(t, "rps", "naive", nil, 0)
	_, err := f.collect(ctx)
	require.NoError(t, err)
	_, err = f.predict(ctx)
	require.NoError(t, err)
	f.dynamic(t, func(m *model.ForecastModule) { m.MinReplicas, m.MaxReplicas = 1, 30 })

	_, detail, err := f.svc.Recommend(ctx, f.module.ID)
	require.NoError(t, err)
	parts := detail.Need.Parts
	require.NotNil(t, parts)
	assert.Equal(t, "rps", parts.Forecast.Resource)
	assert.Equal(t, "rps", parts.Forecast.Unit)
	assert.Greater(t, parts.Forecast.Value, 0.0)
	assert.Greater(t, parts.PerPod, 0.0)
	assert.GreaterOrEqual(t, parts.Margin.Factor, 1.0)
	assert.EqualValues(t, parts.Forecast.Replicas+parts.Margin.Replicas, detail.Need.Replicas.Needed, "the forecast alone plus the margin is the need")
	assert.Empty(t, parts.Forecast.Conservatism, "naive forecasts the last value: it is the expected load")
	if parts.Current != nil {
		assert.Greater(t, parts.Current.Replicas, 0)
	}
}

func TestAPercentileForecastIsSaidToBeConservative(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.bind(t, "rps", "hist_percentile", nil, 0)
	_, err := f.collect(ctx)
	require.NoError(t, err)
	f.setPredict(t, true, model.PredictFixed, "hist_percentile") // not a baseline: only the administrator's choice decides without a promoting backtest
	_, err = f.predict(ctx)
	require.NoError(t, err)
	f.dynamic(t, func(m *model.ForecastModule) { m.MinReplicas, m.MaxReplicas = 1, 30 })
	_, detail, err := f.svc.Recommend(ctx, f.module.ID)
	require.NoError(t, err)
	require.NotNil(t, detail.Need.Parts)
	assert.Regexp(t, `^P\d`, detail.Need.Parts.Forecast.Conservatism)
}

// perPodLevel makes every metric of the fixture return the value of one container instead of the workload total.
func (f *fixture) perPodLevel(t *testing.T) {
	t.Helper()
	require.NoError(t, f.db.Model(&model.MetricMapping{}).Where("1 = 1").Update("level", "container").Error)
}

// A metric that returns the workload total (all the Pods added up) gives the CPU of one Pod only when it is spread over the Pods: the total
// read as one Pod's value was cut to the largest a Pod may have, and the need showed "53 cores per Pod" for a workload of 53 cores in all.
func TestAWorkloadTotalIsSpreadOverThePodsForTheirRequests(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.bind(t, "cpu", "naive", nil, 0)
	f.bind(t, "rps", "naive", nil, 1)
	f.querier.value = func(query string, _ time.Time) float64 {
		if strings.Contains(query, "cpu{") {
			return 24 // 24 cores in all
		}
		return 400
	}
	_, err := f.collect(ctx)
	require.NoError(t, err)
	_, err = f.predict(ctx)
	require.NoError(t, err)
	f.dynamic(t, func(m *model.ForecastModule) {
		m.MinReplicas, m.MaxReplicas, m.MaxCPUMilli, m.PodCPURequestMilli, m.TargetUtilization, m.HeadroomRatio = 1, 40, 8000, 2000, 1, 1
	})

	rec, detail, err := f.svc.Recommend(ctx, f.module.ID)
	require.NoError(t, err)
	require.Greater(t, rec.Replicas, 1)
	// the workload needs about 24 cores in all: a Pod's share of it, not the 24 (which the 8 core maximum would cut to 8)
	perPod := float64(rec.CPUMilli) / 1000
	assert.InDelta(t, 24/float64(rec.Replicas), perPod, 24/float64(rec.Replicas)*0.35+0.3, "the total over the replicas, rounded up to a readable step")
	assert.Less(t, perPod, 8.0, "not the total cut to the largest a Pod may have")
	require.NotNil(t, detail.Need)
	assert.Less(t, detail.Need.CPU.Needed, int64(8000), "the need of one Pod is a share of the total as well")
	assert.Empty(t, detail.Need.CPU.Bound, "so no maximum is what holds it")
}

// The load measured now is shown for the resource the forecast part is about: a memory figure beside a CPU forecast compares nothing.
func TestTheLoadNowIsOfTheSameResourceAsTheForecast(t *testing.T) {
	plan := &decision.ReplicaPlan{Constraints: []decision.ConstraintResult{
		{Name: decision.ConstraintCPU, Metric: "cpu", Needed: 20, PeakDemand: 39, UnitCapacity: 2.8, Uncertainty: 0.4, UncertaintySource: decision.UncertaintyMeasured},
		{Name: decision.ConstraintMemory, Metric: "memory", Needed: 12, PeakDemand: 5e10, UnitCapacity: 4.5e9},
	}}
	cpuAt := time.Now()
	live := &LiveDetail{OK: true, Need: 12, Resource: "memory", Value: 5e10, Resources: []LiveResource{
		{Resource: "memory", Value: 5e10, Need: 12},
		{Resource: "cpu", Value: 25, Need: 9, At: &cpuAt},
	}}

	parts := partsOf(plan, live, "seasonal_profile", "")
	require.NotNil(t, parts)
	assert.Equal(t, "cpu", parts.Forecast.Resource)
	require.NotNil(t, parts.Current)
	assert.Equal(t, "cpu", parts.Current.Resource, "the load now is the CPU one, like the forecast")
	assert.Equal(t, "cores", parts.Current.Unit)
	assert.Equal(t, 9, parts.Current.Replicas)
	assert.Empty(t, parts.Current.Note)

	// with no measurement of that resource the one that is there is shown, and it says it is another resource
	live.Resources = live.Resources[:1]
	parts = partsOf(plan, live, "seasonal_profile", "")
	require.NotNil(t, parts.Current)
	assert.Equal(t, "memory", parts.Current.Resource)
	assert.Equal(t, "other_resource", parts.Current.Note)
}

// A Pod spec that was kept (the forecast is within the change ratio, or a decrease waits out the hold) is told, so a recommendation far above
// what the forecast needs is not read as a wrong value.
func TestAKeptPodSpecIsToldInTheNeed(t *testing.T) {
	spec := &ResourceSpecDetail{HoldHours: 24, ChangePercent: 20, Reasons: map[string]string{
		"cpu":    "kept: a decrease of 61.0% waits out the 24h0m0s hold",
		"memory": "kept: the forecast moved 5.0%, within 20%",
	}}
	held, limit := heldOf(spec, "cpu")
	assert.Equal(t, "hold", held)
	assert.Equal(t, 24, limit)
	held, limit = heldOf(spec, "memory")
	assert.Equal(t, "ratio", held)
	assert.Equal(t, 20, limit)
	held, _ = heldOf(&ResourceSpecDetail{Reasons: map[string]string{"cpu": "taken: the forecast moved 90.0%, more than 20%"}}, "cpu")
	assert.Empty(t, held)
	held, _ = heldOf(nil, "cpu")
	assert.Empty(t, held)
}

// With a target utilization the Pod is sized for the load level the replica count assumed: its request is its share of the load divided by the
// utilization. Without that the Pods came out smaller than the ones the replicas were counted for (a total of 46 cores shown as "2.89 cores per
// Pod" beside "4 cores now", while the replicas were counted for 4 core Pods at 70%), which reads as "scale in" next to a scale out.
func TestAPodIsSizedForTheTargetUtilizationOfTheReplicaCount(t *testing.T) {
	in := decision.Inputs{}
	in.Policy.HeadroomRatio = 1.2
	in.CPUCores = []decision.Candidate{{Algorithm: "a", Peak: 40}}
	in.MemoryBytes = []decision.Candidate{{Algorithm: "a", Peak: 8e10}}

	plain := perPodInputs(in, true, true, 16, 0)
	assert.InDelta(t, 40.0/16, plain.CPUCores[0].Peak, 1e-9)
	assert.Equal(t, 1.2, plain.Policy.HeadroomRatio, "no target utilization: the headroom ratio stays")

	sized := perPodInputs(in, true, false, 16, 0.7)
	assert.InDelta(t, 40.0/(16*0.7), sized.CPUCores[0].Peak, 1e-9, "40 cores over 16 Pods that are meant to run at 70%")
	assert.Equal(t, 1.0, sized.Policy.HeadroomRatio, "the utilization replaces the headroom ratio: one margin, not two")
	assert.Equal(t, in.MemoryBytes[0].Peak, sized.MemoryBytes[0].Peak, "a resource that is not a workload total is left alone")
	assert.Equal(t, 40.0, in.CPUCores[0].Peak, "the input given is not changed")

	assert.Equal(t, 1.2, perPodInputs(in, true, true, 16, 1).Policy.HeadroomRatio, "a utilization of 1 sizes nothing")
}
