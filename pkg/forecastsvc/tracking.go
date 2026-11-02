package forecastsvc

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/model"
)

const (
	// trackingWindowSteps is how far back the forecasts are compared with what happened.
	trackingWindowSteps = 10
	// trackingMinPoints is how many compared moments an algorithm needs before it can be called lagging: fewer is noise.
	trackingMinPoints = 3
	// trackingMaxWAPE is how far off the recent forecasts may be, as a share of the actual load, before the algorithm is lagging.
	// A forecast that is off by half of what happened, over the last ten steps, is not tracking the load.
	trackingMaxWAPE = 0.5
)

// Tracking is how well the newest forecasts of an algorithm followed the load that came: over the last few steps, the forecast the
// platform had just before each step against the value measured at it. The score of an algorithm replays old windows and is
// refreshed every few hours; this is what it did over the last minutes, which is when a load that has just changed (a ramp, a
// drop, a release) shows an algorithm that copies the past (seasonal_naive) or averages it (ewma) to be behind.
type Tracking struct {
	Points int `json:"points"`
	// WAPE is the sum of the absolute misses over the sum of the actual values; Bias the signed miss over the same sum (positive:
	// the forecast was above what happened, negative: below).
	WAPE float64 `json:"wape"`
	Bias float64 `json:"bias"`
}

// Lagging is true when there are enough compared moments and the forecasts missed by more than trackingMaxWAPE.
func (t Tracking) Lagging() bool { return t.Points >= trackingMinPoints && t.WAPE > trackingMaxWAPE }

// track compares the forecasts of an algorithm on a source with the values measured after them. ok is false when there is nothing
// to compare (no forecast of the last steps has met its actual value yet).
func (s *Service) track(ctx context.Context, inst *model.AlgorithmRun, key string, now time.Time) (Tracking, bool, error) {
	series, err := s.Samples.Series(ctx, inst.ID, key)
	if err != nil {
		return Tracking{}, false, nil
	}
	step := time.Duration(series.StepSeconds) * time.Second
	from := now.Add(-trackingWindowSteps * step)
	samples, err := s.Samples.Range(ctx, series.ID, from, now)
	if err != nil || len(samples) == 0 {
		return Tracking{}, false, err
	}
	forecasts, err := s.Predictions.ForecastsForTargets(ctx, inst.ID, key, inst.Key(), from, now)
	if err != nil {
		return Tracking{}, false, err
	}
	predicted := make(map[int64]float64, len(forecasts))
	for _, f := range forecasts {
		predicted[f.TargetTime.Unix()] = f.Value
	}
	var miss, signed, actual float64
	var points int
	for _, sm := range samples {
		p, ok := predicted[sm.TS]
		if !ok {
			continue
		}
		miss += math.Abs(p - sm.Value)
		signed += p - sm.Value
		actual += math.Abs(sm.Value)
		points++
	}
	if points == 0 || actual < 1e-9 {
		return Tracking{}, false, nil
	}
	return Tracking{Points: points, WAPE: miss / actual, Bias: signed / actual}, true, nil
}

// trackInstance is the worst tracking over the forecast sources of an instance that have a forecast in use: an algorithm that
// follows the CPU but not the memory is not following the workload.
func (s *Service) trackInstance(ctx context.Context, st *instanceState, now time.Time) error {
	for _, key := range st.inst.SourceKeys() {
		if f, ok := st.forecast[key]; !ok || !f.usable() {
			continue
		}
		t, ok, err := s.track(ctx, st.inst, key, now)
		if err != nil {
			return err
		}
		if ok && (!st.tracked || t.WAPE > st.tracking.WAPE) {
			st.tracking, st.tracked = t, true
		}
	}
	return nil
}

// dropLagging takes out of the running the algorithms whose recent forecasts are far off, as long as another one is not: the
// algorithm in use is not kept because its replay score is high. When every algorithm lags there is nothing better to switch to,
// and the order is left as the scores give it. It returns how many were taken out.
func dropLagging(states []*instanceState) int {
	var following int
	for _, st := range states {
		if st.eligible && !st.tracking.Lagging() {
			following++
		}
	}
	if following == 0 {
		return 0
	}
	dropped := 0
	for _, st := range states {
		if !st.eligible || !st.tracking.Lagging() {
			continue
		}
		st.eligible, st.lagging = false, true
		direction := "above"
		if st.tracking.Bias < 0 {
			direction = "below"
		}
		st.reason = fmt.Sprintf("lagging: its forecasts of the last %d steps missed the actual values by %.0f%% (limit %.0f%%), %s them",
			st.tracking.Points, st.tracking.WAPE*100, trackingMaxWAPE*100, direction)
		dropped++
	}
	return dropped
}
