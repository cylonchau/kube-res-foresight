package forecastsvc

import (
	"context"
	"testing"
	"time"

	"github.com/cylonchau/kube-resource-fcst/pkg/decision"
	"github.com/cylonchau/kube-resource-fcst/pkg/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The delay report is made of the scale-ups of the window: those before it are not counted, the judged ones give the rates, and the
// newest recommendation gives how far the forecasts reach beside the lead.
func TestTheDelayReportJudgesTheScaleUpsOfTheWindow(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	add := func(ago time.Duration, verdict string, late float64, emergency bool) {
		o := &model.ScaleOutcome{ModuleID: f.module.ID, Version: 1, DecidedAt: testNow.Add(-ago), FromReplicas: 2, ToReplicas: 4, Emergency: emergency,
			Verdict: verdict, Final: verdict != decision.VerdictPending, LateSeconds: late}
		require.NoError(t, f.db.Create(o).Error)
	}
	for i := 0; i < 10; i++ {
		add(time.Duration(i+1)*time.Hour/2, decision.VerdictTimely, 0, false)
	}
	add(5*time.Hour, decision.VerdictLate, 45, false)
	add(6*time.Hour, decision.VerdictUnneeded, 0, false)
	add(90*time.Hour, decision.VerdictLate, 900, true) // outside the window

	// the scale-ups alone do not make a pass: the forecast of the algorithm in use has to have been evaluated too
	first, err := f.svc.DelayReport(ctx, f.module.ID, 24)
	require.NoError(t, err)
	assert.Equal(t, decision.IndicatorInsufficient, first.Verdict, "good scale-ups, but no evaluated forecast: not enough to say 达标")
	require.NoError(t, f.db.Create(&model.ModuleRunState{ModuleID: f.module.ID, Selected: "naive"}).Error)
	for i := 0; i < 5; i++ {
		require.NoError(t, f.db.Create(&model.PredictionEvaluation{ModuleID: f.module.ID, Metric: "cpu", AlgorithmID: "naive", ModelVersion: "v",
			PredictionTime: testNow.Add(-time.Duration(i+1) * time.Hour), Points: 10, Expected: 10, PeakUnder: 0.05}).Error)
	}

	rep, err := f.svc.DelayReport(ctx, f.module.ID, 24)
	require.NoError(t, err)
	assert.Equal(t, 12, rep.Scaled, "the scale-up of four days ago is not in the window")
	assert.Equal(t, 12, rep.Judged)
	got := map[string]decision.Indicator{}
	for _, i := range rep.Indicators {
		got[i.Key] = i
	}
	assert.Equal(t, decision.IndicatorPass, got["timely_rate"].Verdict)
	assert.InDelta(t, 10.0/11, got["timely_rate"].Value, 1e-9)
	assert.Equal(t, decision.IndicatorPass, got["late_p90_seconds"].Verdict)
	assert.InDelta(t, 45, got["late_p90_seconds"].Value, 1e-9)
	assert.Equal(t, decision.IndicatorPass, got["emergency_share"].Verdict)
	assert.Equal(t, decision.IndicatorInsufficient, got["reach_over_lead"].Verdict, "nothing has been decided yet, so there is no reach to read")
	assert.Equal(t, decision.IndicatorPass, got["decision_wait_seconds"].Verdict)
	assert.Equal(t, decision.IndicatorPass, rep.Verdict)

	// a longer look brings the late one in
	long, err := f.svc.DelayReport(ctx, f.module.ID, 120)
	require.NoError(t, err)
	assert.Equal(t, 13, long.Scaled)
	byKeyLong := map[string]decision.Indicator{}
	for _, i := range long.Indicators {
		byKeyLong[i.Key] = i
	}
	assert.Equal(t, decision.IndicatorFail, byKeyLong["late_p90_seconds"].Verdict, "900 seconds late")
	assert.Equal(t, decision.IndicatorFail, long.Verdict)

	_, err = f.svc.DelayReport(ctx, 9999, 24)
	assert.Error(t, err)
}
