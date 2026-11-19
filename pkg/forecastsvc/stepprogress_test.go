package forecastsvc

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTheFractionOfAPhaseIsMappedIntoItsShareOfTheWholeStep(t *testing.T) {
	var got []StepUpdate
	ctx := WithStepProgress(context.Background(), func(u StepUpdate) { got = append(got, u) })
	stepReport(ctx, "alone", 1, 4, nil)
	stepReport(withPhase(ctx, 0, 2), "first", 2, 4, nil)
	stepReport(withPhase(ctx, 1, 2), "second", 2, 4, nil)
	stepReport(withPhase(ctx, 1, 2), "second end", 4, 4, nil)
	stepReport(ctx, "nothing to do", 0, 0, nil)
	require.Len(t, got, 5)
	assert.InDelta(t, 0.25, got[0].Fraction, 1e-9)
	assert.InDelta(t, 0.25, got[1].Fraction, 1e-9, "half of the first of two phases")
	assert.InDelta(t, 0.75, got[2].Fraction, 1e-9, "half of the second of two phases")
	assert.InDelta(t, 1.0, got[3].Fraction, 1e-9)
	assert.InDelta(t, 1.0, got[4].Fraction, 1e-9, "a step with no parts is done")
	assert.Equal(t, "second", got[2].Stage)
	assert.Equal(t, 2, got[2].Done)
	assert.Equal(t, 4, got[2].Total)
	// without anyone listening it is a no-op
	stepReport(context.Background(), "x", 1, 2, nil)
}

func TestTheStepsOfAModuleReportHowFarTheyAreAndWhatTheyHaveProducedSoFar(t *testing.T) {
	f := newFixture(t)
	f.bind(t, "cpu", "seasonal_naive", map[string]any{"season_length_steps": 1440}, 0)
	f.bind(t, "cpu", "ewma", nil, 1)
	var got []StepUpdate
	var partials []any
	ctx := WithStepProgress(context.Background(), func(u StepUpdate) { got = append(got, u); partials = append(partials, u.Partial) })

	_, err := f.svc.Collect(ctx, f.module.ID)
	require.NoError(t, err)
	require.NotEmpty(t, got)
	assert.Equal(t, "collect: cpu", got[len(got)-1].Stage)
	assert.InDelta(t, 1.0, got[len(got)-1].Fraction, 1e-9)
	assert.Len(t, partials[len(partials)-1], 1, "the result of the metrics that are done")

	got, partials = nil, nil
	_, err = f.svc.Predict(ctx, f.module.ID)
	require.NoError(t, err)
	require.Len(t, got, 2, "one report for each algorithm that was run")
	assert.InDelta(t, 0.5, got[0].Fraction, 1e-9)
	assert.InDelta(t, 1.0, got[1].Fraction, 1e-9)
	assert.Len(t, partials[1], 2, "the outcomes that exist so far: the first report holds one, the last both")

	got = nil
	_, err = f.svc.Backtest(ctx, f.module.ID, "cpu", 3)
	require.NoError(t, err)
	require.NotEmpty(t, got)
	assert.Contains(t, got[0].Stage, "backtest: ")

	got = nil
	_, err = f.svc.Evaluate(ctx, f.module.ID)
	require.NoError(t, err)
	require.NotEmpty(t, got)
	assert.Contains(t, got[0].Stage, "evaluate: ")

	got = nil
	_, err = f.svc.RunData(ctx, f.module.ID, nil)
	require.NoError(t, err)
	var sawCollect, sawPredict bool
	for _, u := range got {
		if len(u.Stage) > 8 && u.Stage[:8] == "collect:" {
			sawCollect = true
			assert.LessOrEqual(t, u.Fraction, 0.5+1e-9, "collecting is the first of the two phases of a run")
		}
		if len(u.Stage) > 8 && u.Stage[:8] == "predict:" {
			sawPredict = true
			assert.GreaterOrEqual(t, u.Fraction, 0.5-1e-9, "predicting is the second")
		}
	}
	assert.True(t, sawCollect && sawPredict, "%+v", got)
}

func TestAPreviewRunInTheBackgroundIsHeldToThePipelineLimitNotToTheRoomUnderTheGateway(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	inRequest := f.svc.previewBudget(ctx)
	inBackground := f.svc.previewBudget(Background(ctx))
	assert.Less(t, inRequest, inBackground, "a request is kept inside the gateway timeout, a task is not")
}
