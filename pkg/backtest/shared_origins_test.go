package backtest

import (
	"context"
	"testing"

	"github.com/cylonchau/kube-resource-fcst/pkg/forecast"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// level predicts one fixed value; needs says how many steps of history it asks for.
type level struct {
	value float64
	needs int
}

func (level) ID() string        { return "level" }
func (l level) MinSamples() int { return l.needs }
func (l level) Forecast(_ context.Context, _ forecast.Input, h int) ([]float64, error) {
	out := make([]float64, h)
	for i := range out {
		out[i] = l.value
	}
	return out, nil
}

func TestOriginsAreSpreadOverTheHistoryAndSharedByModelsWithTheSameMinimum(t *testing.T) {
	cfg := Config{HorizonSteps: 10, Cutoffs: 5}
	packed := Origins(500, cfg, 50)
	assert.Equal(t, []int{490, 480, 470, 460, 450}, packed, "without Spread the origins are packed into the latest steps")

	cfg.Spread = true
	spread := Origins(500, cfg, 50)
	require.Len(t, spread, 5)
	assert.Equal(t, 490, spread[0], "the newest origin still leaves a full horizon to score")
	assert.GreaterOrEqual(t, spread[len(spread)-1], 50)
	assert.Less(t, spread[len(spread)-1], 200, "the oldest origin reaches back over the history, not only the last hours")
	for i := 1; i < len(spread); i++ {
		assert.GreaterOrEqual(t, spread[i-1]-spread[i], 10, "origins are at least one horizon apart, so the windows do not overlap")
	}
	assert.Equal(t, spread, Origins(500, cfg, 50), "the same inputs give the same origins")

	// a stride that is an exact multiple of the season would score the same hour of the day every time
	cfg.SeasonSteps = (490 - 50) / 4
	seasonal := Origins(500, cfg, 50)
	assert.NotEqual(t, 0, (seasonal[0]-seasonal[1])%cfg.SeasonSteps)

	// a model that needs more history gets a subset of the windows, never different ones
	longer := Origins(500, Config{HorizonSteps: 10, Cutoffs: 5}, 475)
	assert.Equal(t, []int{490, 480}, longer)
	assert.Nil(t, Origins(500, cfg, 495))
}

func TestExplicitOriginsScoreExactlyThoseWindows(t *testing.T) {
	s := series(200, func(i int) float64 { return 100 })
	origins := Origins(200, Config{HorizonSteps: 10, Cutoffs: 4, Spread: true}, 60)
	res, err := Run(context.Background(), s, level{value: 100, needs: 5}, Config{HorizonSteps: 10, Cutoffs: 4, Origins: origins})
	require.NoError(t, err)
	assert.Equal(t, len(origins), res.Cutoffs)
	assert.Equal(t, len(origins)*10, res.Points)

	res, err = Run(context.Background(), s, level{value: 100, needs: 80}, Config{HorizonSteps: 10, Cutoffs: 4, Origins: origins})
	require.NoError(t, err)
	assert.Contains(t, res.Skipped, "insufficient history", "a model that needs more than a shared origin offers is skipped, not padded")
}

// Underestimating starves the workload, overestimating only wastes capacity: with the same absolute error the forecast that
// runs low scores worse on RiskWAPE while WAPE cannot tell them apart.
func TestRiskWAPEPenalisesRunningLow(t *testing.T) {
	s := series(200, func(i int) float64 { return 100 })
	cfg := Config{HorizonSteps: 10, Cutoffs: 3}
	low, err := Run(context.Background(), s, level{value: 90, needs: 5}, cfg)
	require.NoError(t, err)
	high, err := Run(context.Background(), s, level{value: 110, needs: 5}, cfg)
	require.NoError(t, err)

	assert.InDelta(t, 0.1, low.WAPE, 1e-9)
	assert.InDelta(t, low.WAPE, high.WAPE, 1e-9, "WAPE is blind to the direction")
	assert.InDelta(t, 0.3, low.RiskWAPE, 1e-9, "an underestimate costs DefaultUnderWeight (3) times as much")
	assert.InDelta(t, 0.1, high.RiskWAPE, 1e-9)
	assert.Equal(t, DefaultUnderWeight, low.UnderWeight)
	assert.InDelta(t, -10, low.Bias, 1e-9)
	assert.InDelta(t, 10, high.Bias, 1e-9)
}

// RiskWAPE can be rebuilt from WAPE, MAE and bias, which is all the stored real-outcome evaluations hold.
func TestRiskWAPEIsRebuiltFromWAPEMAEAndBias(t *testing.T) {
	s := series(300, func(i int) float64 { return 100 + float64(i%7)*10 })
	for _, value := range []float64{70, 100, 130} {
		res, err := Run(context.Background(), s, level{value: value, needs: 5}, Config{HorizonSteps: 14, Cutoffs: 6, Spread: true})
		require.NoError(t, err)
		assert.InDelta(t, res.RiskWAPE, RiskWAPE(res.WAPE, res.MAE, res.Bias, DefaultUnderWeight), 1e-9, "forecast level %v", value)
	}
	assert.Equal(t, 0.4, RiskWAPE(0.4, 0, 0, 3), "no error, no direction: WAPE is returned")
	assert.InDelta(t, 0.2*(1+2*0.5), RiskWAPE(0.2, 10, 0, 3), 1e-9, "a balanced error is half underestimate")
}

func TestPromotionRefusesAWinnerThatRunsLowAndIgnoresResultsWithoutRisk(t *testing.T) {
	baseline := Result{Metrics: Metrics{Points: 100, Cutoffs: 10, WAPE: 0.20, RiskWAPE: 0.30, UnderWeight: 3}}
	// better WAPE, but the improvement comes from running low
	low := Result{Metrics: Metrics{Points: 100, Cutoffs: 10, WAPE: 0.15, RiskWAPE: 0.40, UnderWeight: 3}}
	v := Evaluate(baseline, low, Policy{MinPoints: 10})
	assert.False(t, v.Promoted)
	assert.Contains(t, v.Reason, "underestimate-weighted")

	ok := Result{Metrics: Metrics{Points: 100, Cutoffs: 10, WAPE: 0.15, RiskWAPE: 0.25, UnderWeight: 3}}
	assert.True(t, Evaluate(baseline, ok, Policy{MinPoints: 10}).Promoted)
	assert.True(t, Evaluate(baseline, low, Policy{MinPoints: 10, PeakTolerance: 0.5}).Promoted, "the tolerance of the policy applies to RiskWAPE too")

	// results stored before RiskWAPE existed carry no weight and are judged as before
	legacy := Result{Metrics: Metrics{Points: 100, Cutoffs: 10, WAPE: 0.15}}
	legacyBase := Result{Metrics: Metrics{Points: 100, Cutoffs: 10, WAPE: 0.20}}
	assert.True(t, Evaluate(legacyBase, legacy, Policy{MinPoints: 10}).Promoted)
}

func TestPromotionNeedsTheSameWindows(t *testing.T) {
	baseline := Result{Metrics: Metrics{Points: 100, Cutoffs: 5, WAPE: 0.20, RiskWAPE: 0.30, UnderWeight: 3}}
	candidate := Result{Metrics: Metrics{Points: 200, Cutoffs: 10, WAPE: 0.10, RiskWAPE: 0.15, UnderWeight: 3}}
	v := Evaluate(baseline, candidate, Policy{MinPoints: 10})
	assert.False(t, v.Promoted)
	assert.Contains(t, v.Reason, "not comparable")
}

// A model whose requirement grows with the horizon (KNN needs windows each followed by a whole horizon) must not be given a cutoff
// where it cannot run: spreading the origins over the history would otherwise make it skip the whole backtest.
func TestSpreadOriginsRespectAHorizonDependentMinimum(t *testing.T) {
	s := series(180, func(i int) float64 { return 50 + float64(i%12) })
	knn := forecast.KNN{Window: 6, K: 5, Normalize: "relative", AllowOverlap: true}
	res, err := Run(context.Background(), s, knn, Config{HorizonSteps: 10, Cutoffs: 10, Spread: true, SeasonSteps: 1440})
	require.NoError(t, err)
	assert.Empty(t, res.Skipped)
	assert.Equal(t, 10, res.Cutoffs)
	assert.Equal(t, knn.Window+knn.K+10-1, forecast.MinSamplesFor(knn, 10))

	// with the exclusion zone the K windows must not overlap: K x window + horizon, and the origins still fit
	separate := forecast.KNN{Window: 6, K: 5, Normalize: "relative"}
	res, err = Run(context.Background(), s, separate, Config{HorizonSteps: 10, Cutoffs: 10, Spread: true, SeasonSteps: 1440})
	require.NoError(t, err)
	assert.Empty(t, res.Skipped)
	assert.Equal(t, 10, res.Cutoffs)
	assert.Equal(t, separate.K*separate.Window+10, forecast.MinSamplesFor(separate, 10))
}

// A P90 line runs high on purpose, so it is not judged on WAPE: it has to cover about 90% of the actuals and beat the baseline's
// pinball loss at the same level.
func TestAQuantileForecastIsPromotedOnCoverageAndPinballNotOnWAPE(t *testing.T) {
	metric := func(alpha, wape, pinball, coverage, peak float64) Result {
		return Result{Metrics: Metrics{Points: 100, Cutoffs: 10, Alpha: alpha, WAPE: wape, RiskWAPE: wape, UnderWeight: 3, Pinball: pinball, Coverage: coverage, PeakUnder: peak}}
	}
	baseline := metric(0.9, 0.05, 6.0, 0.45, 0.05) // a point forecast scored at the 90% level: it is under the actual about half the time
	line := metric(0.9, 0.20, 2.0, 0.88, 0.0)      // far worse WAPE, calibrated, much lower pinball loss
	v := Evaluate(baseline, line, Policy{MinPoints: 10})
	assert.True(t, v.Promoted, v.Reason)
	assert.Contains(t, v.Reason, "covers")

	uncalibrated := metric(0.9, 0.20, 2.0, 0.60, 0.0)
	v = Evaluate(baseline, uncalibrated, Policy{MinPoints: 10})
	assert.False(t, v.Promoted)
	assert.Contains(t, v.Reason, "covers only")

	noBetter := metric(0.9, 0.20, 6.5, 0.9, 0.0)
	v = Evaluate(baseline, noBetter, Policy{MinPoints: 10})
	assert.False(t, v.Promoted)
	assert.Contains(t, v.Reason, "pinball")

	// the median forecast keeps the WAPE rule
	middle := metric(0.5, 0.20, 6.0, 0.5, 0.0)
	baseMiddle := metric(0.5, 0.05, 6.0, 0.5, 0.0)
	v = Evaluate(baseMiddle, middle, Policy{MinPoints: 10})
	assert.False(t, v.Promoted)
	assert.Contains(t, v.Reason, "does not beat baseline")
}
