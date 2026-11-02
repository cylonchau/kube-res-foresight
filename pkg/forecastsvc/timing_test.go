package forecastsvc

import (
	"context"
	"testing"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/cylonchau/kube-res-foresight/pkg/timing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (f *fixture) setParameter(t *testing.T, key, value string) {
	t.Helper()
	require.NoError(t, f.db.AutoMigrate(&model.SysConfig{}))
	require.NoError(t, f.db.Unscoped().Where("config_key = ?", key).Delete(&model.SysConfig{}).Error)
	require.NoError(t, f.svc.Config.Create(context.Background(), &model.SysConfig{ConfigKey: key, ConfigValue: value}))
}

// A time limit is what the system parameter says, kept in the range of the parameter; nothing usable stored is the default.
func TestATimeLimitIsReadFromTheSystemParameter(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	assert.Equal(t, 300*time.Second, f.svc.Timing(ctx, timing.RefreshBudget), "nothing stored: the default")

	f.setParameter(t, timing.RefreshBudget.Key, "45")
	assert.Equal(t, 45*time.Second, f.svc.Timing(ctx, timing.RefreshBudget))
	f.setParameter(t, timing.RefreshBudget.Key, "999999")
	assert.Equal(t, time.Duration(timing.RefreshBudget.Max)*time.Second, f.svc.Timing(ctx, timing.RefreshBudget), "above the range: the highest allowed")
	f.setParameter(t, timing.RefreshBudget.Key, "soon")
	assert.Equal(t, 300*time.Second, f.svc.Timing(ctx, timing.RefreshBudget), "not a number: the default")

	f.svc.Config = nil
	assert.Equal(t, 60*time.Second, f.svc.Timing(ctx, timing.PreviewBudget), "without a database of parameters: the default")
}

// The budget of a forecast is the algorithm's own, else the system parameter.
func TestTheBudgetOfAForecastIsTheAlgorithmsOwnElseTheSystemParameter(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	assert.Equal(t, 120*time.Second, f.svc.budgetFor(ctx, &model.AlgorithmRun{}))
	f.setParameter(t, timing.TrainBudget.Key, "30")
	assert.Equal(t, 30*time.Second, f.svc.budgetFor(ctx, &model.AlgorithmRun{}))
	assert.Equal(t, 7*time.Second, f.svc.budgetFor(ctx, &model.AlgorithmRun{TrainBudgetSeconds: 7}), "the algorithm's own budget wins")
}

// The wait of a request of the operator is the parameter but always shorter than the time the operator waits itself.
func TestTheRefreshWaitIsAlwaysShorterThanTheOperatorsRequestTimeout(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	assert.Equal(t, 8*time.Second, f.svc.RefreshWait(ctx))

	f.setParameter(t, timing.DecisionRefreshWait.Key, "30")
	assert.Equal(t, 9*time.Second, f.svc.RefreshWait(ctx), "30 s asked, the operator waits 10: one second less")

	f.setParameter(t, timing.OperatorRequestTimeout.Key, "60")
	assert.Equal(t, 30*time.Second, f.svc.RefreshWait(ctx), "the operator was told to wait longer: the wait is as asked")

	f.setParameter(t, timing.OperatorRequestTimeout.Key, "1")
	assert.Equal(t, time.Second, f.svc.RefreshWait(ctx), "never less than a second")
}

// A percentile is what the system parameter says, as a level: 41 is P41 (0.41), none is fixed; nothing usable is the default P90.
func TestAPercentileIsReadFromTheSystemParameter(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	assert.InDelta(t, 0.9, f.svc.Percentile(ctx, timing.LeadPercentile), 1e-12, "nothing stored: P90")
	for _, p := range []timing.Param{timing.LeadPercentile, timing.JudgePercentile, timing.UncertaintyPercentile} {
		for stored, want := range map[string]float64{"40": 0.40, "41": 0.41, "42": 0.42, "99": 0.99, "1": 0.01, "0": 0.01, "250": 0.99, "p95": 0.9, "": 0.9} {
			f.setParameter(t, p.Key, stored)
			assert.InDelta(t, want, f.svc.Percentile(ctx, p), 1e-12, "%s=%q", p.Key, stored)
		}
	}
	f.svc.Config = nil
	assert.InDelta(t, 0.9, f.svc.Percentile(ctx, timing.JudgePercentile), 1e-12)
}

// The lead of a module follows the percentile that was set.
func TestTheLeadFollowsThePercentileOfTheParameter(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	assert.InDelta(t, 0.9, f.svc.aheadSettings(ctx).LeadPct, 1e-12)
	f.setParameter(t, timing.LeadPercentile.Key, "41")
	assert.InDelta(t, 0.41, f.svc.aheadSettings(ctx).LeadPct, 1e-12)
}
