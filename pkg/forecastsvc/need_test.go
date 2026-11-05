package forecastsvc

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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
