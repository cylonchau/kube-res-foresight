package forecast

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"
)

// KNN is analogue forecasting: the latest Window values are compared with every earlier window of the history, and
// the forecast is the average of what followed the K most similar ones. With Normalize "relative" each window is
// divided by its own mean first, so a pattern seen at a lower load still matches, and the result is scaled back to
// the level of the current window. Only values before the forecast origin are compared, so nothing leaks.
//
// The K windows are K different events. Windows that start one step apart look almost the same, so without a rule the K
// nearest would be one event counted K times (the best match and its shifted copies). An exclusion zone keeps the chosen
// windows at least Window steps apart; AllowOverlap turns it off (the behaviour before version 2). SameTimeOfDay further
// keeps only windows that end at the time of day of the current one (within TimeToleranceMinutes, UTC, around midnight
// too), for a load whose pattern belongs to the hour of the day.
type KNN struct {
	Window    int
	K         int
	Normalize string // "relative" or "none"

	AllowOverlap         bool
	SameTimeOfDay        bool
	TimeToleranceMinutes int
}

func (KNN) ID() string { return "knn" }
func (m KNN) MinSamples() int {
	min := m.Window + m.K + 1
	if !m.AllowOverlap && m.K*m.Window+1 > min {
		min = m.K*m.Window + 1 // K windows that do not overlap
	}
	return min
}
func (m KNN) relative() bool { return m.Normalize != "none" }
func (m KNN) validate() error {
	if m.Window < 2 {
		return fmt.Errorf("window must be at least 2")
	}
	if m.K < 1 {
		return fmt.Errorf("k must be at least 1")
	}
	if m.Normalize != "" && m.Normalize != "relative" && m.Normalize != "none" {
		return fmt.Errorf("normalize must be relative or none")
	}
	if m.TimeToleranceMinutes < 0 || m.TimeToleranceMinutes > 720 {
		return fmt.Errorf("time_tolerance_minutes must be between 0 and 720")
	}
	return nil
}

// candidate is one earlier window: the index of its last value and its distance to the current window.
type candidate struct {
	end  int
	dist float64
}

// pick takes the K nearest candidates (sorted by distance) of which no two end less than the exclusion zone apart.
func (m KNN) pick(sorted []candidate) []candidate {
	if m.AllowOverlap {
		if len(sorted) > m.K {
			sorted = sorted[:m.K]
		}
		return sorted
	}
	var out []candidate
	for _, c := range sorted {
		clear := true
		for _, o := range out {
			gap := c.end - o.end
			if gap < 0 {
				gap = -gap
			}
			if gap < m.Window {
				clear = false
				break
			}
		}
		if clear {
			out = append(out, c)
			if len(out) == m.K {
				break
			}
		}
	}
	return out
}

// minutesOfDay is the UTC time of day of t in minutes.
func minutesOfDay(t time.Time) int {
	u := t.UTC()
	return u.Hour()*60 + u.Minute()
}

// sameTimeOfDay is whether two times of day are within tolerance minutes of each other, around midnight too.
func sameTimeOfDay(a, b time.Time, tolerance int) bool {
	d := minutesOfDay(a) - minutesOfDay(b)
	if d < 0 {
		d = -d
	}
	if d > 720 {
		d = 1440 - d
	}
	return d <= tolerance
}

func windowMean(v []float64) float64 {
	var s float64
	for _, x := range v {
		s += x
	}
	return s / float64(len(v))
}

// scale is the number a window is divided by before windows are compared: its own mean with relative normalisation.
func (m KNN) scale(mean float64) float64 {
	if !m.relative() || math.Abs(mean) < 1e-12 {
		return 1
	}
	return mean
}

// analogues are the K earlier windows the forecast is made from, nearest first: every window with the horizon values after it
// (and, with SameTimeOfDay, ending at the time of day of the current one) is compared with the current window, and the chosen
// ones are kept apart by the exclusion zone.
func (m KNN) analogues(ctx context.Context, in Input, horizon int) ([]candidate, error) {
	n := len(in.Values)
	query := in.Values[n-m.Window:]
	qScale := m.scale(windowMean(query))
	var cands []candidate
	queryTime := in.TimeAt(n - 1)
	// a window ending at e is usable when the horizon values after it exist
	for e := m.Window - 1; e+horizon <= n-1; e++ {
		if e%2048 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		if m.SameTimeOfDay && !sameTimeOfDay(in.TimeAt(e), queryTime, m.TimeToleranceMinutes) {
			continue
		}
		win := in.Values[e-m.Window+1 : e+1]
		wScale := m.scale(windowMean(win))
		var d float64
		for i := range win {
			diff := win[i]/wScale - query[i]/qScale
			d += diff * diff
		}
		cands = append(cands, candidate{e, d})
	}
	sort.SliceStable(cands, func(a, b int) bool { return cands[a].dist < cands[b].dist })
	cands = m.pick(cands)
	if len(cands) < m.K {
		return nil, fmt.Errorf("%w: knn needs %d separate earlier windows followed by %d values, found %d", ErrInsufficientHistory, m.K, horizon, len(cands))
	}
	return cands, nil
}

func (m KNN) Forecast(ctx context.Context, in Input, horizon int) ([]float64, error) {
	if err := m.validate(); err != nil {
		return nil, err
	}
	if err := checkInput(m, in, horizon); err != nil {
		return nil, err
	}
	n := len(in.Values)
	qScale := m.scale(windowMean(in.Values[n-m.Window:]))
	cands, err := m.analogues(ctx, in, horizon)
	if err != nil {
		return nil, err
	}

	out := make([]float64, horizon)
	var wsum float64
	for _, c := range cands {
		w := 1 / (math.Sqrt(c.dist) + 1e-9) // closer analogues count more
		wScale := m.scale(windowMean(in.Values[c.end-m.Window+1 : c.end+1]))
		for h := 0; h < horizon; h++ {
			out[h] += w * in.Values[c.end+1+h] / wScale
		}
		wsum += w
	}
	for h := range out {
		out[h] = out[h] / wsum * qScale
	}
	return out, finite(out)
}

// MinSamplesFor adds the horizon: the K nearest windows must each be followed by horizon values, so there have to be K
// windows ending at least horizon steps before the last one.
func (m KNN) MinSamplesFor(horizon int) int {
	min := m.Window + m.K + horizon - 1
	if !m.AllowOverlap && m.K*m.Window+horizon > min {
		min = m.K*m.Window + horizon // K windows that do not overlap, each followed by the horizon
	}
	return min
}
