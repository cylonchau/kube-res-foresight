package forecastsvc

import (
	"context"
	"testing"

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
