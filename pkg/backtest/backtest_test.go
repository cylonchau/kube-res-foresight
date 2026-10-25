package backtest

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/cylonchau/kube-resource-fcst/pkg/forecast"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var t0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func series(n int, f func(i int) float64) forecast.Input {
	v := make([]float64, n)
	for i := range v {
		v[i] = f(i)
	}
	return forecast.Input{Values: v, Step: time.Minute, End: t0.Add(time.Duration(n-1) * time.Minute)}
}

// spy records everything it was allowed to see.
type spy struct {
	maxSeen   float64
	lastEnd   []time.Time
	histories []int
}

func (s *spy) ID() string      { return "spy" }
func (s *spy) MinSamples() int { return 3 }
func (s *spy) Forecast(_ context.Context, in forecast.Input, h int) ([]float64, error) {
	for _, v := range in.Values {
		s.maxSeen = math.Max(s.maxSeen, v)
	}
	s.lastEnd = append(s.lastEnd, in.End)
	s.histories = append(s.histories, len(in.Values))
	out := make([]float64, h)
	for i := range out {
		out[i] = in.Values[len(in.Values)-1]
	}
	return out, nil
}

func TestRunNeverShowsTheModelAnythingAfterTheCutoff(t *testing.T) {
	// The last 6 values are a huge sentinel: with a horizon of 6 they are only ever test data of the last cutoff.
	s := series(60, func(i int) float64 {
		if i >= 54 {
			return 1e9
		}
		return float64(i % 5)
	})
	m := &spy{}
	res, err := Run(context.Background(), s, m, Config{HorizonSteps: 6, Cutoffs: 4})
	require.NoError(t, err)
	assert.Empty(t, res.Skipped)
	assert.Less(t, m.maxSeen, 1e9, "the sentinel test data leaked into a training window")
	assert.Equal(t, 4, res.Cutoffs)
	for i, end := range m.lastEnd {
		assert.Equal(t, s.TimeAt(m.histories[i]-1), end, "training end matches its own history length")
	}
	assert.Equal(t, 54, m.histories[0], "the newest cutoff trains on everything before the test window")
}

func TestRunMetricsOnAKnownSeries(t *testing.T) {
	// Values 1..10, naive forecast from cutoff 6 (history 1..6 -> predicts 6) against actual 7..10.
	s := series(10, func(i int) float64 { return float64(i + 1) })
	res, err := Run(context.Background(), s, forecast.Naive{}, Config{HorizonSteps: 4, Cutoffs: 1})
	require.NoError(t, err)
	// errors: 1,2,3,4 ; actual sum 7+8+9+10=34
	assert.Equal(t, 4, res.Points)
	assert.InDelta(t, 2.5, res.MAE, 1e-9)
	assert.InDelta(t, math.Sqrt((1+4+9+16)/4.0), res.RMSE, 1e-9)
	assert.InDelta(t, 10.0/34.0, res.WAPE, 1e-9)
	assert.InDelta(t, (10.0-6.0)/10.0, res.PeakUnder, 1e-9)
	assert.InDelta(t, 0.0, res.Coverage, 1e-9, "every actual is above the flat forecast")
	assert.InDelta(t, 0.5*2.5, res.Pinball, 1e-9)
}

func TestRunSkipsInsteadOfPaddingShortHistory(t *testing.T) {
	s := series(20, func(i int) float64 { return float64(i) })
	res, err := Run(context.Background(), s, forecast.SeasonalNaive{Season: 30}, Config{HorizonSteps: 5})
	require.NoError(t, err)
	assert.Contains(t, res.Skipped, "insufficient history")
	assert.Zero(t, res.Points)
}

func TestRunIgnoresTestWindowsWithGaps(t *testing.T) {
	s := series(40, func(i int) float64 { return 10 })
	s.Values[37] = math.NaN()
	res, err := Run(context.Background(), s, forecast.Naive{}, Config{HorizonSteps: 5, Cutoffs: 3, Stride: 5})
	require.NoError(t, err)
	assert.Equal(t, 2, res.Cutoffs, "the newest window holds a gap and is dropped")
}

func TestRunConfigValidation(t *testing.T) {
	_, err := Run(context.Background(), series(10, func(int) float64 { return 1 }), forecast.Naive{}, Config{})
	assert.Error(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = Run(ctx, series(30, func(int) float64 { return 1 }), forecast.Naive{}, Config{HorizonSteps: 3})
	assert.ErrorIs(t, err, context.Canceled)
}

func TestEvaluatePromotionRules(t *testing.T) {
	base := Result{Metrics: Metrics{Points: 100, WAPE: 0.20, PeakUnder: 0.10}}
	better := Result{Metrics: Metrics{Points: 100, WAPE: 0.10, PeakUnder: 0.05}}
	assert.True(t, Evaluate(base, better, Policy{}).Promoted)

	assert.False(t, Evaluate(base, base, Policy{}).Promoted, "a tie is not an improvement")
	assert.False(t, Evaluate(base, better, Policy{MinImprovement: 0.6}).Promoted)

	worsePeak := Result{Metrics: Metrics{Points: 100, WAPE: 0.10, PeakUnder: 0.30}}
	v := Evaluate(base, worsePeak, Policy{})
	assert.False(t, v.Promoted)
	assert.Contains(t, v.Reason, "peak underestimation")
	assert.True(t, Evaluate(base, worsePeak, Policy{PeakTolerance: 2}).Promoted)

	assert.False(t, Evaluate(base, better, Policy{MinPoints: 500}).Promoted)
	assert.False(t, Evaluate(base, Result{Skipped: "no data"}, Policy{}).Promoted)
	assert.False(t, Evaluate(Result{Skipped: "no data"}, better, Policy{}).Promoted)
}

func TestSeasonalModelBeatsNaiveOnASeasonalSeries(t *testing.T) {
	const season = 24
	s := series(season*10, func(i int) float64 { return 100 + 40*math.Sin(2*math.Pi*float64(i%season)/season) })
	cfg := Config{HorizonSteps: season, Cutoffs: 4}
	naive, err := Run(context.Background(), s, forecast.Naive{}, cfg)
	require.NoError(t, err)
	seasonal, err := Run(context.Background(), s, forecast.SeasonalNaive{Season: season}, cfg)
	require.NoError(t, err)
	assert.Less(t, seasonal.WAPE, naive.WAPE)
	assert.True(t, Evaluate(naive, seasonal, Policy{MinPoints: 24}).Promoted)
}
