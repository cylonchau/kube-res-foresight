package forecastsvc

import (
	"context"
	"testing"
	"time"

	"github.com/cylonchau/kube-resource-fcst/pkg/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (f *fixture) addRecommendations(t *testing.T, n int, every time.Duration, mutate func(i int, r *model.Recommendation)) {
	t.Helper()
	for i := 0; i < n; i++ {
		at := testNow.Add(-time.Duration(n-i) * every)
		r := &model.Recommendation{ModuleID: f.module.ID, Version: 1000 + i, Mode: model.ModeRecommend, CreatedAt: at, ValidUntil: at.Add(15 * time.Minute),
			ReplicasSet: true, Replicas: 10, Algorithm: "naive"}
		if mutate != nil {
			mutate(i, r)
		}
		require.NoError(t, f.db.Create(r).Error)
	}
}

// A module with no history reports every section as not enough instead of a zero that reads like a good result.
func TestTheEffectReportSaysNotEnoughWhereThereIsNoData(t *testing.T) {
	f := newFixture(t)
	rep, err := f.svc.EffectReport(context.Background(), f.module.ID, 24)
	require.NoError(t, err)
	assert.Equal(t, 24, rep.Hours)
	assert.Empty(t, rep.Accuracy)
	assert.False(t, rep.Waste.Enough)
	assert.NotEmpty(t, rep.Waste.Note)
	assert.False(t, rep.Stability.Enough)
	assert.False(t, rep.Adoption.Enough)
	assert.Nil(t, rep.Tuning, "no model service in use")
	require.NotNil(t, rep.Delay)

	_, err = f.svc.EffectReport(context.Background(), 9999, 24)
	assert.Error(t, err)
}

// Accuracy is the evaluated runs of the algorithm in use, weighted by the steps each had an actual for, per metric, inside the window.
func TestTheEffectReportWeighsTheAccuracyOfTheAlgorithmInUse(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	require.NoError(t, f.db.Create(&model.ModuleRunState{ModuleID: f.module.ID, Selected: "naive"}).Error)
	add := func(metric, algorithm string, ago time.Duration, points int, wape, under float64) {
		require.NoError(t, f.db.Create(&model.PredictionEvaluation{ModuleID: f.module.ID, Metric: metric, AlgorithmID: algorithm, ModelVersion: "v",
			PredictionTime: testNow.Add(-ago), Points: points, Expected: points, WAPE: wape, PeakUnder: under, Bias: 0.1, Coverage: 0.9}).Error)
	}
	for i := 0; i < 5; i++ {
		add("cpu", "naive", time.Duration(i+1)*time.Hour, 10, 0.2, 0.1)
	}
	add("cpu", "naive", 2*time.Hour+time.Minute, 30, 0.4, 0.3) // three times the weight of a run of 10
	add("cpu", "ewma", time.Hour, 10, 0.9, 0.9)                // not the algorithm in use
	add("cpu", "naive", 100*time.Hour, 10, 0.9, 0.9)           // outside the window
	for i := 0; i < 2; i++ {
		add("memory", "naive", time.Duration(i+1)*time.Hour, 10, 0.1, 0)
	}

	rep, err := f.svc.EffectReport(ctx, f.module.ID, 24)
	require.NoError(t, err)
	assert.Equal(t, "naive", rep.Algorithm)
	require.Len(t, rep.Accuracy, 2)
	cpu, mem := rep.Accuracy[0], rep.Accuracy[1]
	assert.Equal(t, "cpu", cpu.Metric)
	assert.Equal(t, 6, cpu.Runs)
	assert.Equal(t, 80, cpu.Points)
	assert.True(t, cpu.Enough)
	assert.InDelta(t, (5*10*0.2+30*0.4)/80, cpu.WAPE, 1e-9)
	assert.InDelta(t, (5*10*0.1+30*0.3)/80, cpu.PeakUnder, 1e-9)
	assert.Equal(t, "memory", mem.Metric)
	assert.False(t, mem.Enough, "two runs are not enough to read")
	assert.Zero(t, mem.WAPE)
}

// Stability counts what the operator applied and the recommendations that fell back, by the reason; adoption who decided them.
func TestTheEffectReportCountsStabilityAndAdoption(t *testing.T) {
	f := newFixture(t)
	f.addRecommendations(t, 8, 30*time.Minute, func(i int, r *model.Recommendation) {
		if i%2 == 1 {
			r.Algorithm = "ewma"
		}
		if i < 3 {
			r.Degraded, r.DegradeReasons = true, `{"replicas":"stale_forecast","cpu":"stale_forecast"}`
		}
	})
	exec := func(ago time.Duration, action, result string) {
		require.NoError(t, f.db.Create(&model.ScalingExecution{ModuleID: f.module.ID, Namespace: "prod", Scaler: "s", TargetKind: "Deployment", TargetName: "api",
			Action: action, Result: result, CreatedAt: testNow.Add(-ago)}).Error)
	}
	exec(5*time.Hour, "ScaleUp", model.ExecApplied)
	exec(5*time.Hour-10*time.Minute, "ScaleDown", model.ExecApplied) // reverses the one before
	exec(3*time.Hour, "ScaleUp", model.ExecApplied)
	exec(2*time.Hour, "ScaleDown", model.ExecHeld) // held: nothing changed
	exec(60*time.Hour, "ScaleUp", model.ExecApplied)

	rep, err := f.svc.EffectReport(context.Background(), f.module.ID, 24)
	require.NoError(t, err)
	assert.Equal(t, 8, rep.Stability.Recommendations)
	assert.Equal(t, 3, rep.Stability.Degraded)
	assert.Equal(t, map[string]int{"stale_forecast": 3}, rep.Stability.Reasons, "one count per recommendation, however many fields fell back")
	assert.Equal(t, 2, rep.Stability.Ups)
	assert.Equal(t, 1, rep.Stability.Downs)
	assert.Equal(t, 1, rep.Stability.Reversals)
	assert.True(t, rep.Stability.Enough)

	assert.True(t, rep.Adoption.Enough)
	assert.Equal(t, 8, rep.Adoption.Total)
	require.Len(t, rep.Adoption.Rows, 2)
	assert.Equal(t, AdoptionRow{Algorithm: "ewma", Count: 4, Share: 0.5}, rep.Adoption.Rows[0])
}

// Waste is the requested replicas against what the measured load needed: the module asks for 40 replicas where about 3 to 5 carry the load.
func TestTheEffectReportMeasuresTheReplicasAskedForAgainstTheLoad(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.bind(t, "rps", "naive", nil, 0)
	_, err := f.collect(ctx)
	require.NoError(t, err)
	f.addRecommendations(t, 12, time.Hour, func(i int, r *model.Recommendation) { r.Replicas = 40 })

	rep, err := f.svc.EffectReport(ctx, f.module.ID, 24)
	require.NoError(t, err)
	require.True(t, rep.Waste.Enough, rep.Waste.Note)
	assert.Equal(t, rep.Waste.Judged, rep.Waste.Over, "40 replicas is above any peak the load reached")
	assert.Zero(t, rep.Waste.Under)
	assert.InDelta(t, 1.0, rep.Waste.OverShare, 1e-9)
	assert.Greater(t, rep.Waste.OverReplicas, 30.0)
	assert.Greater(t, rep.Waste.SurplusReplicaHours, 0.0)

	again, err := f.svc.EffectReport(ctx, f.module.ID, 24)
	require.NoError(t, err)
	assert.Equal(t, rep.Waste, again.Waste, "the same window gives the same figures")
}

// The model service's fine-tune records are read for the age of the copy and the newest failure.
func TestTheEffectReportShowsTheFineTunedCopyOfAModelService(t *testing.T) {
	f := newFixture(t)
	train := func(ago time.Duration, status, reason string) {
		require.NoError(t, f.db.Create(&model.TrainingRun{ModuleID: f.module.ID, Metric: "cpu", AlgorithmID: "model_service", ModelVersion: "v",
			Status: status, Reason: reason, CreatedAt: testNow.Add(-ago)}).Error)
	}
	train(8*time.Hour, StatusOK, "fine-tuned (full, 30 steps, learning rate 1e-05, batch 16, context 256) on 900 points in 40 s")
	train(5*time.Hour, StatusFailed, "fine-tune failed: out of memory")
	train(2*time.Hour, StatusOK, "fine-tuned (full, 30 steps, learning rate 1e-05, batch 16, context 256) on 950 points in 41 s")
	train(time.Hour, StatusOK, "") // a forecast run, not a fine-tune

	rep, err := f.svc.EffectReport(context.Background(), f.module.ID, 24)
	require.NoError(t, err)
	require.NotNil(t, rep.Tuning)
	assert.Equal(t, 2, rep.Tuning.Tuned)
	assert.Equal(t, 1, rep.Tuning.Failed)
	assert.InDelta(t, 2, rep.Tuning.AgeHours, 1e-9)
	assert.Contains(t, rep.Tuning.LastFailure, "out of memory")
}
