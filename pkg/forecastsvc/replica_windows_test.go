package forecastsvc

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cylonchau/kube-res-foresight/pkg/model"
)

// A window that is a floor or a cap keeps the algorithm's replica count within it, a window that is fixed pins it, and a module that keeps a
// fixed count ignores the algorithm: the three ways to set replicas work together.
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
	window("floor", "min", algo+5)
	rec, detail, err := f.svc.Recommend(ctx, f.module.ID)
	require.NoError(t, err)
	assert.Equal(t, algo+5, rec.Replicas, "the floor is above what the algorithm asks")
	assert.Equal(t, "algorithm", rec.ReplicasSource, "the algorithm still decided")
	assert.True(t, detail.ReplicaWindow)
	assert.Contains(t, detail.Replicas.Reason, "floor")

	require.NoError(t, f.db.Where("module_id = ?", f.module.ID).Delete(&model.ModulePeriod{}).Error)
	window("floor-low", "min", 1)
	rec, detail, err = f.svc.Recommend(ctx, f.module.ID)
	require.NoError(t, err)
	assert.Equal(t, algo, rec.Replicas, "a floor below the algorithm changes nothing")
	assert.False(t, detail.ReplicaWindow)

	require.NoError(t, f.db.Where("module_id = ?", f.module.ID).Delete(&model.ModulePeriod{}).Error)
	window("cap", "max", 1)
	rec, _, err = f.svc.Recommend(ctx, f.module.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, rec.Replicas)

	require.NoError(t, f.db.Where("module_id = ?", f.module.ID).Delete(&model.ModulePeriod{}).Error)
	window("pin", "fixed", 33)
	rec, _, err = f.svc.Recommend(ctx, f.module.ID)
	require.NoError(t, err)
	assert.Equal(t, 33, rec.Replicas)
	assert.Equal(t, "fixed_period", rec.ReplicasSource)

	// the module keeps a fixed count: no algorithm decides, and a floor window still raises it
	require.NoError(t, f.db.Where("module_id = ?", f.module.ID).Delete(&model.ModulePeriod{}).Error)
	f.dynamic(t, func(m *model.ForecastModule) { m.FixedReplicas = 3 })
	rec, _, err = f.svc.Recommend(ctx, f.module.ID)
	require.NoError(t, err)
	assert.Equal(t, 3, rec.Replicas)
	assert.Equal(t, "fixed_count", rec.ReplicasSource)
	window("floor-night", "min", 8)
	rec, _, err = f.svc.Recommend(ctx, f.module.ID)
	require.NoError(t, err)
	assert.Equal(t, 8, rec.Replicas, "3 normally, at least 8 in the window")

}
