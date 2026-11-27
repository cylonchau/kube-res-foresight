package forecastsvc

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cylonchau/kube-res-foresight/pkg/model"
)

// The three ways to set replicas never run together: the forecast decides while no strategy is on; the time period strategy sets them by its windows (a floor, a cap or a pinned count);
// the fixed count keeps exactly its replicas. Switching a strategy off gives the decision back to the forecast.
func TestTimeWindowsAlgorithmAndAFixedCountWorkTogether(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.predictedRPS(t)
	f.dynamic(t, func(m *model.ForecastModule) { m.MinReplicas, m.MaxReplicas = 1, 60 })

	base, _, err := f.svc.Recommend(ctx, f.module.ID)
	require.NoError(t, err)
	require.Equal(t, "algorithm", base.ReplicasSource)
	algo := base.Replicas
	require.Greater(t, algo, 0)

	// the fixture clock is Wednesday 10:30 UTC
	window := func(name, mode string, replicas int) {
		require.NoError(t, f.svc.Modules.SavePeriod(ctx, &model.ModulePeriod{ModuleID: f.module.ID, Name: name, Enabled: true,
			Weekdays: 127, StartMinute: 10 * 60, EndMinute: 11 * 60, Replicas: replicas, ReplicaMode: mode}))
	}
	clear := func() {
		require.NoError(t, f.db.Where("module_id = ?", f.module.ID).Delete(&model.ModulePeriod{}, &model.ModuleTrigger{}).Error)
	}

	// a window of a module whose periods strategy is off is not read, the forecast still decides
	window("floor", "min", algo+5)
	rec, detail, err := f.svc.Recommend(ctx, f.module.ID)
	require.NoError(t, err)
	assert.Equal(t, algo, rec.Replicas)
	assert.False(t, detail.ReplicaWindow)

	// the periods strategy is on: the forecast is off, the windows decide
	f.dynamic(t, func(m *model.ForecastModule) { m.PeriodsEnabled, m.PredictEnabled = true, false })
	rec, detail, err = f.svc.Recommend(ctx, f.module.ID)
	require.NoError(t, err)
	assert.Equal(t, algo+5, rec.Replicas, "the floor gives the count when nothing else decided one")
	assert.True(t, detail.ReplicaWindow)
	assert.Equal(t, "strategy:"+model.StrategyPeriods, detail.DecisionMode)

	clear()
	window("pin", "fixed", 33)
	rec, _, err = f.svc.Recommend(ctx, f.module.ID)
	require.NoError(t, err)
	assert.Equal(t, 33, rec.Replicas)
	assert.Equal(t, "fixed_period", rec.ReplicasSource)

	// the fixed count strategy keeps exactly its replicas; the periods strategy is off, so no window is read
	f.dynamic(t, func(m *model.ForecastModule) {
		m.PeriodsEnabled, m.FixedReplicas, m.FixedReplicasEnabled = false, 3, true
	})
	window("floor-night", "min", 8)
	rec, _, err = f.svc.Recommend(ctx, f.module.ID)
	require.NoError(t, err)
	assert.Equal(t, 3, rec.Replicas, "a fixed count keeps exactly its replicas; a window is not read")
	assert.Equal(t, "fixed_count", rec.ReplicasSource)

	// every strategy is off and the forecast on again: the count stays stored and takes no part, the forecast decides
	clear()
	f.dynamic(t, func(m *model.ForecastModule) {
		m.FixedReplicas, m.FixedReplicasEnabled, m.PredictEnabled = 3, false, true
	})
	rec, _, err = f.svc.Recommend(ctx, f.module.ID)
	require.NoError(t, err)
	assert.Equal(t, "algorithm", rec.ReplicasSource)
	assert.Equal(t, algo, rec.Replicas)
}
