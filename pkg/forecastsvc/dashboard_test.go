package forecastsvc

import (
	"context"
	"testing"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// clearModules removes the module the fixture starts with, so a test counts only the modules it makes.
func (f *fixture) clearModules(t *testing.T) {
	t.Helper()
	require.NoError(t, f.db.Where("1 = 1").Delete(&model.ForecastModule{}).Error)
}

func (f *fixture) newModule(t *testing.T, name string, mutate func(*model.ForecastModule)) *model.ForecastModule {
	t.Helper()
	m := &model.ForecastModule{Name: name, Namespace: "prod", WorkloadKind: "Deployment", Workload: name, Enabled: true, Mode: model.ModeRecommend,
		PredictEnabled: true, PredictMode: model.PredictDynamic, TTLSeconds: 900, Timezone: "UTC", LookbackHours: 72, HorizonMinutes: 15, MaxFillSteps: 3, HeadroomRatio: 1}
	if mutate != nil {
		mutate(m)
	}
	require.NoError(t, f.db.Create(m).Error)
	return m
}

func (f *fixture) recommendation(t *testing.T, m *model.ForecastModule, ago time.Duration, mutate func(*model.Recommendation)) {
	t.Helper()
	at := testNow.Add(-ago)
	r := &model.Recommendation{ModuleID: m.ID, Version: 1, Mode: m.Mode, CreatedAt: at, ValidUntil: at.Add(15 * time.Minute), ReplicasSet: true, Replicas: 5, Algorithm: "naive"}
	if mutate != nil {
		mutate(r)
	}
	require.NoError(t, f.db.Create(r).Error)
}

// The home page counts the modules and lists those that need a look, worst first, with what makes each so. A module that is healthy, one
// that only observes and one that is disabled are not in the list.
func TestTheDashboardListsTheModulesThatNeedALookWorstFirst(t *testing.T) {
	f := newFixture(t)
	f.clearModules(t)
	ctx := context.Background()
	healthy := f.newModule(t, "healthy", nil)
	f.recommendation(t, healthy, 2*time.Minute, nil)

	failing := f.newModule(t, "failing", nil)
	f.recommendation(t, failing, 2*time.Minute, nil)
	require.NoError(t, f.db.Create(&model.ModuleRunState{ModuleID: failing.ID, ConsecutiveFailures: 4, LastError: "prometheus: connection refused", Selected: "naive"}).Error)

	hiccup := f.newModule(t, "hiccup", nil) // two failures in a row are not enough to flag
	f.recommendation(t, hiccup, 2*time.Minute, nil)
	require.NoError(t, f.db.Create(&model.ModuleRunState{ModuleID: hiccup.ID, ConsecutiveFailures: MinFailuresToFlag - 1}).Error)

	degraded := f.newModule(t, "degraded", nil)
	f.recommendation(t, degraded, 2*time.Minute, func(r *model.Recommendation) { r.Degraded = true })
	require.NoError(t, f.db.Create(&model.ModuleRunState{ModuleID: degraded.ID, DegradedFields: `{"replicas":"stale_forecast","cpu":"stale_forecast"}`}).Error)

	stale := f.newModule(t, "stale", nil)
	f.recommendation(t, stale, 2*time.Hour, nil)

	empty := f.newModule(t, "empty", nil)
	bypass := f.newModule(t, "bypassed", func(m *model.ForecastModule) { m.Bypass = true })
	f.recommendation(t, bypass, time.Minute, nil)

	observing := f.newModule(t, "observing", func(m *model.ForecastModule) { m.Mode = model.ModeObserve })
	_ = observing
	disabled := f.newModule(t, "disabled", func(m *model.ForecastModule) { m.Enabled = false; m.Bypass = true })
	_ = disabled
	fixed := f.newModule(t, "fixed", func(m *model.ForecastModule) { m.PredictEnabled = false }) // not asked to forecast: no recommendation is not a fault
	_ = fixed
	_ = empty

	d, err := f.svc.Dashboard(ctx, 24)
	require.NoError(t, err)
	assert.Equal(t, 10, d.Modules.Total)
	assert.Equal(t, 9, d.Modules.Enabled, "the disabled module is not counted")
	assert.Equal(t, 1, d.Modules.Observe)
	assert.Equal(t, 8, d.Modules.Recommend)
	assert.Equal(t, 8, d.Modules.Predicting, "the observing module forecasts too; only the module with prediction off and the disabled one are left out")
	assert.Equal(t, 1, d.Modules.Bypass, "a disabled module is not counted")

	var names []string
	flags := map[string][]string{}
	for _, row := range d.Attention {
		names = append(names, row.Name)
		flags[row.Name] = row.Flags
	}
	assert.Equal(t, []string{"failing", "degraded", "stale", "empty", "bypassed"}, names, "worst first: failing, degraded, stale, no recommendation, bypass")
	assert.Equal(t, 5, d.Modules.Attention)
	assert.Equal(t, []string{FlagFailing}, flags["failing"])
	assert.Equal(t, []string{FlagDegraded}, flags["degraded"])
	assert.Equal(t, []string{FlagStale}, flags["stale"])
	assert.Equal(t, []string{FlagNoRecommendation}, flags["empty"])
	assert.Equal(t, []string{FlagBypass}, flags["bypassed"])

	row := d.Attention[0]
	assert.Equal(t, "prometheus: connection refused", row.LastError)
	assert.Equal(t, 4, row.ConsecutiveFailures)
	assert.Equal(t, "naive", row.Algorithm)
	assert.Equal(t, 5, row.Replicas)
	assert.Equal(t, []string{"cpu", "replicas"}, d.Attention[1].DegradedFields)
}

// What the operator applied is counted by direction in buckets that are all present, and what it merely observed or held is not counted.
func TestTheDashboardCountsWhatTheOperatorAppliedInBucketsWithoutHoles(t *testing.T) {
	f := newFixture(t)
	f.clearModules(t)
	ctx := context.Background()
	m := f.newModule(t, "api", nil)
	exec := func(ago time.Duration, action, result string) {
		require.NoError(t, f.db.Create(&model.ScalingExecution{ModuleID: m.ID, Namespace: "prod", Scaler: "s", TargetKind: "Deployment", TargetName: "api",
			Action: action, Result: result, CreatedAt: testNow.Add(-ago)}).Error)
	}
	exec(30*time.Minute, "ScaleUp", model.ExecApplied)
	exec(40*time.Minute, "ScaleUp", model.ExecApplied)
	exec(5*time.Hour, "ScaleDown", model.ExecApplied)
	exec(2*time.Hour, "ScaleUp", model.ExecHeld)
	exec(3*time.Hour, "ScaleDown", model.ExecObserved)
	exec(100*time.Hour, "ScaleUp", model.ExecApplied) // outside the window
	require.NoError(t, f.db.Create(&model.TrainingRun{ModuleID: m.ID, Metric: "cpu", AlgorithmID: "naive", ModelVersion: "v", Status: StatusFailed, CreatedAt: testNow.Add(-time.Hour)}).Error)
	require.NoError(t, f.db.Create(&model.TrainingRun{ModuleID: m.ID, Metric: "cpu", AlgorithmID: "naive", ModelVersion: "v", Status: StatusOK, CreatedAt: testNow.Add(-time.Hour)}).Error)

	d, err := f.svc.Dashboard(ctx, 24)
	require.NoError(t, err)
	assert.Equal(t, 2, d.Totals.ScaleUps)
	assert.Equal(t, 1, d.Totals.ScaleDowns)
	assert.EqualValues(t, 1, d.Totals.FailedRuns)
	assert.Equal(t, 1, d.BucketHours)
	assert.Len(t, d.Activity, 25, "an hourly bucket for each of the 24 hours and the one in progress")
	for i := 1; i < len(d.Activity); i++ {
		assert.Equal(t, time.Hour, d.Activity[i].At.Sub(d.Activity[i-1].At), "no bucket is missing")
	}

	week, err := f.svc.Dashboard(ctx, 24*7)
	require.NoError(t, err)
	assert.Equal(t, 6, week.BucketHours)
	assert.Equal(t, 3, week.Totals.ScaleUps, "the longer window reaches the change of 100 hours ago")
	long, err := f.svc.Dashboard(ctx, 24*30)
	require.NoError(t, err)
	assert.Equal(t, 24, long.BucketHours)
}

// With nothing at all the page is empty, not an error.
func TestTheDashboardOfAnEmptyPlatformIsEmpty(t *testing.T) {
	f := newFixture(t)
	f.clearModules(t)
	d, err := f.svc.Dashboard(context.Background(), 0)
	require.NoError(t, err)
	assert.Equal(t, 24, d.Hours)
	assert.Zero(t, d.Modules.Total)
	assert.NotNil(t, d.Attention)
	assert.Empty(t, d.Attention)
}
