package forecastsvc

import (
	"context"
	"testing"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/decision"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// aheadFixture is a dynamic module on a request-rate source with a ready-replicas series: naive forecasts the last value, a Pod
// carries 100 rps, and 4 replicas are in force.
func aheadFixture(t *testing.T) *fixture {
	t.Helper()
	f := newFixture(t)
	repl := &model.MetricMapping{DatasourceID: f.cpuMap.DatasourceID, Name: "replicas", Metric: "replicas", Level: "workload", Unit: "count", StepSeconds: 60, Enabled: true,
		Query: `sum(kube_deployment_status_replicas_available{namespace="{{namespace}}",deployment="{{workload}}"})`}
	require.NoError(t, f.db.Create(repl).Error)
	f.maps["replicas"] = repl.ID
	f.querier.value = func(query string, ts time.Time) float64 {
		if contains(query, "kube_deployment") {
			return 4
		}
		return 300
	}
	f.bind(t, "rps", "naive", nil, 0)
	f.ensureSource(t, "replicas")
	f.dynamic(t, func(m *model.ForecastModule) { m.HeadroomRatio = 1 })
	require.NoError(t, f.svc.Predictions.SaveExecution(context.Background(), &model.ScalingExecution{ModuleID: f.module.ID, Namespace: "prod", Scaler: "api",
		TargetKind: "Deployment", TargetName: "api", Current: 4, Ready: 4, Desired: 4, Action: "None", Result: "observed"}))
	return f
}

func (f *fixture) fullCycle(t *testing.T) (*model.Recommendation, *RecommendDetail) {
	t.Helper()
	ctx := context.Background()
	_, err := f.collect(ctx)
	require.NoError(t, err)
	_, err = f.predict(ctx)
	require.NoError(t, err)
	res, err := f.svc.RunModule(ctx, f.module.ID, map[string]string{}, nil)
	require.NoError(t, err)
	require.NotNil(t, res.Recommend)
	rec, detail, err := f.svc.Recommend(ctx, f.module.ID)
	require.NoError(t, err)
	return rec, detail
}

func TestTheReplicaDecisionLooksAheadWithALeadAndWindows(t *testing.T) {
	f := aheadFixture(t)
	_, detail := f.fullCycle(t)
	require.NotNil(t, detail.Ahead)
	a := detail.Ahead
	assert.Equal(t, decision.LeadConfigured, a.Lead.ReadySource, "no scale-up was measured yet")
	assert.Equal(t, 180.0, a.Lead.Seconds, "60s Pod + 90s decision wait, in whole steps")
	var leadWindows, horizonWindows int
	for _, w := range a.Windows {
		if w.Lead {
			leadWindows++
			assert.Equal(t, 3, w.Steps)
		}
		if w.Horizon {
			horizonWindows++
			assert.Equal(t, 15, w.Steps)
		}
		require.NotEmpty(t, w.Needs)
	}
	assert.Equal(t, 1, leadWindows)
	assert.Equal(t, 1, horizonWindows)
	assert.Len(t, a.Windows, 4, "the lead and 5, 10, 15 minutes")
	assert.Equal(t, 3, a.Up.Replicas, "300 rps over 100 per Pod")
	assert.Equal(t, int64(a.Settlement.Replicas), detail.Replicas.Value)
}

func TestTheLoadMeasuredNowRaisesTheReplicasAtOnceWhenTheForecastIsBehind(t *testing.T) {
	f := aheadFixture(t)
	ctx := context.Background()
	f.fullCycle(t)
	// the load jumps in the last two minutes; the naive forecast of the same cycle would follow the last value, so it is stored before the jump
	owner := f.owner("rps")
	series, err := f.svc.Samples.Series(ctx, owner.ID, "rps")
	require.NoError(t, err)
	last, ok, err := f.svc.Samples.Latest(ctx, series.ID)
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, f.svc.Samples.Upsert(ctx, series.ID, []model.MetricSample{{SeriesID: series.ID, TS: last.Unix(), Value: 1500}, {SeriesID: series.ID, TS: last.Add(-time.Minute).Unix(), Value: 1500}}))

	rec, detail := f.cycleRecommendOnly(t)
	require.NotNil(t, detail.Ahead)
	assert.True(t, detail.Ahead.Live.OK)
	assert.Equal(t, 15, detail.Ahead.Live.Need)
	assert.True(t, detail.Ahead.Settlement.Emergency, detail.Ahead.Settlement.Reason)
	assert.Equal(t, 15, rec.Replicas)
	assert.True(t, rec.Urgent)

	// the same scale-up asked for again at the next poll is one record, not two
	f.cycleRecommendOnly(t)
	list, total, err := f.svc.OutcomesOf(ctx, f.module.ID, "", 1, 10)
	require.NoError(t, err)
	require.EqualValues(t, 1, total)
	assert.Equal(t, 4, list[0].FromReplicas)
	assert.Equal(t, 15, list[0].ToReplicas)
	assert.True(t, list[0].Emergency)
	assert.Equal(t, decision.VerdictLate, list[0].Verdict, "the load is already there and the replicas are not Ready yet")
	assert.False(t, list[0].Final)
}

// cycleRecommendOnly decides again on the stored forecasts and samples, as the scheduled pipeline does after the data steps.
func (f *fixture) cycleRecommendOnly(t *testing.T) (*model.Recommendation, *RecommendDetail) {
	t.Helper()
	rec, detail, err := f.svc.recommend(context.Background(), f.module.ID, map[string]string{}, true)
	require.NoError(t, err)
	return rec, detail
}

func TestAnUpwardDecisionIsRecordedOnceAndJudgedLaterAndTheLeadLearnsFromIt(t *testing.T) {
	f := aheadFixture(t)
	ctx := context.Background()
	t0 := testNow
	// the load needs more than 4 replicas from t0+5m; the replicas are Ready at t0+2m
	f.querier.value = func(query string, ts time.Time) float64 {
		if contains(query, "kube_deployment") {
			if ts.Before(t0.Add(2 * time.Minute)) {
				return 4
			}
			return 12
		}
		if ts.Before(t0.Add(5 * time.Minute)) {
			return 300
		}
		return 1200
	}
	f.fullCycle(t)
	// a forecast-driven scale-up decided at t0
	require.NoError(t, f.svc.Predictions.SaveOutcome(ctx, &model.ScaleOutcome{ModuleID: f.module.ID, Version: 1, DecidedAt: t0, FromReplicas: 4, ToReplicas: 12, LeadSeconds: 180,
		LeadSource: decision.LeadConfigured, Verdict: decision.VerdictPending}))

	*f.clock = t0.Add(12 * time.Minute)
	_, err := f.collect(ctx)
	require.NoError(t, err)
	mod, err := f.svc.Modules.Get(ctx, f.module.ID)
	require.NoError(t, err)
	bs, err := f.svc.boundSet(ctx, mod)
	require.NoError(t, err)
	require.NoError(t, f.svc.evaluateOutcomes(ctx, mod, bs, decision.SolverConfig{CapacityRPS: 100}, *f.clock))

	list, _, err := f.svc.OutcomesOf(ctx, f.module.ID, "", 1, 10)
	require.NoError(t, err)
	var judged *model.ScaleOutcome
	for _, o := range list {
		if o.DecidedAt.Equal(t0) {
			judged = o
		}
	}
	require.NotNil(t, judged)
	assert.Equal(t, decision.VerdictTimely, judged.Verdict, judged.Note)
	assert.True(t, judged.Final)
	assert.InDelta(t, 120, judged.ReadySeconds, 1)
	assert.InDelta(t, 180, judged.SlackSeconds, 1)
	assert.Equal(t, 3, judged.ActualNeed, "within the lead after the decision the load still needed 3; the rise came after it")
}

func TestALateScaleUpMakesTheNextLeadLonger(t *testing.T) {
	f := aheadFixture(t)
	ctx := context.Background()
	f.fullCycle(t)
	before, err := f.svc.Predictions.RecentOutcomes(ctx, f.module.ID, 5, false)
	require.NoError(t, err)
	require.Empty(t, before)
	_, detail := f.cycleRecommendOnly(t)
	first := detail.Ahead.Lead.Seconds

	// three finished scale-ups that were Ready 5 minutes after the decision and 3 minutes after the load needed them
	for i := 0; i < 3; i++ {
		decided := testNow.Add(-time.Duration(i+1) * time.Hour)
		ready := decided.Add(5 * time.Minute)
		needed := decided.Add(2 * time.Minute)
		require.NoError(t, f.svc.Predictions.SaveOutcome(ctx, &model.ScaleOutcome{ModuleID: f.module.ID, Version: i + 1, DecidedAt: decided, FromReplicas: 4, ToReplicas: 8,
			LeadSeconds: 180, Verdict: decision.VerdictLate, Final: true, ReadyAt: &ready, NeededAt: &needed, ReadySeconds: 300, LateSeconds: 180}))
	}
	_, detail = f.cycleRecommendOnly(t)
	assert.Equal(t, decision.LeadMeasured, detail.Ahead.Lead.ReadySource)
	assert.Greater(t, detail.Ahead.Lead.Seconds, first, "the measured Ready time and the lateness are in the lead now")
	assert.Equal(t, 3, detail.Ahead.Outcomes.Late)
	assert.GreaterOrEqual(t, detail.Ahead.Lead.MarginSeconds, 180.0)
}
