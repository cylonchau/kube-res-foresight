package forecast

import (
	"context"
	"math"
	"time"
)

// ShortTerm is the quick correction put around an algorithm that remembers long: a seasonal profile, a percentile of weeks of history, a
// decomposition. Such an algorithm is right about the shape of an ordinary day and blind to what has just changed: a burst that starts
// now moves a percentile of a month by nothing, and the forecast stays at the quiet level while the load is already several times it.
//
// The correction compares what the wrapped algorithm would have forecast for the newest few minutes (a real forecast, made from the
// history before them, so it cannot see them) with what the load did. The gap is how far the long memory is off right now:
//
//   - A gap smaller than the Trigger share of the recent level, or than twice the noise of the quiet stretch before, is noise and changes
//     nothing; the long-period forecast stands.
//   - A rise the forecast did not see is added to the forecast for the steps ahead, fading with a half-life of FadeMinutes (a burst fades
//     into the shape of the day, it does not persist forever), and, while the gap is still growing (a ramp), extended along its slope for
//     as many steps as were measured. It is never taken above 1.5 times the highest recent value.
//   - A fall is followed only DownShare of the way (0 by default): the forecast is allowed to be slow to give capacity back, and an algorithm
//     that forecasts high on purpose (a percentile, an envelope) is not pulled down to the mean.
//
// Only values before the forecast origin are used, so a backtest through ShortTerm stays free of leakage. The correction needs history
// for the wrapped algorithm to forecast the recent steps; without it the forecast is the wrapped algorithm's own.
type ShortTerm struct {
	Inner Model
	Cfg   ShortTermConfig
}

// ShortTermConfig sets the correction. A zero RecentMinutes turns it off.
type ShortTermConfig struct {
	// RecentMinutes is the newest stretch that is compared with the forecast of the wrapped algorithm (at least three steps).
	RecentMinutes float64
	// Trigger is the share of the recent level the gap has to exceed before it is corrected.
	Trigger float64
	// FadeMinutes is the half-life of the correction along the horizon.
	FadeMinutes float64
	// DownShare is the share of a downward gap that is followed, 0 (never) to 1.
	DownShare float64
}

// DefaultShortTerm is the correction the platform starts with.
func DefaultShortTerm() ShortTermConfig {
	return ShortTermConfig{RecentMinutes: 5, Trigger: 0.15, FadeMinutes: 30, DownShare: 0}
}

// Enabled reports whether the correction does anything.
func (c ShortTermConfig) Enabled() bool { return c.RecentMinutes > 0 }

// longMemory are the algorithms that remember long and so get the correction. The ones that read the newest window (ewma, naive, theta,
// knn, the regression models) follow a change on their own.
var longMemory = map[string]bool{
	"seasonal_naive": true, "hist_percentile": true, "seasonal_profile": true, "mstl": true, "holt_winters": true, "peak_envelope": true,
}

// LongMemory reports whether the algorithm (or the built-in one a variant of it is derived from) remembers long.
func LongMemory(def Definition) bool {
	return longMemory[def.ID] || (def.Base != "" && longMemory[def.Base])
}

// ShortTermReport says what the correction found and did in one forecast.
type ShortTermReport struct {
	// Applied is true when the forecast was changed.
	Applied bool `json:"applied"`
	// Gap is how far the load was from what the wrapped algorithm forecast for the newest steps, now; Slope how fast that gap grows per step.
	Gap   float64 `json:"gap"`
	Slope float64 `json:"slope"`
	// Threshold is the gap that starts the correction.
	Threshold float64 `json:"threshold"`
	Reason    string  `json:"reason"`
}

func (m ShortTerm) ID() string      { return m.Inner.ID() }
func (m ShortTerm) MinSamples() int { return m.Inner.MinSamples() }

// MinSamplesFor is the wrapped algorithm's.
func (m ShortTerm) MinSamplesFor(horizon int) int { return MinSamplesFor(m.Inner, horizon) }

// QuantileLevel is the wrapped algorithm's, when it forecasts a quantile.
func (m ShortTerm) QuantileLevel() (float64, bool) {
	if q, ok := m.Inner.(Quantiled); ok {
		return q.QuantileLevel()
	}
	return 0, false
}

// Forecast implements Model.
func (m ShortTerm) Forecast(ctx context.Context, in Input, horizon int) ([]float64, error) {
	out, _, err := m.ForecastReport(ctx, in, horizon)
	return out, err
}

// ForecastReport is Forecast with what the correction did.
func (m ShortTerm) ForecastReport(ctx context.Context, in Input, horizon int) ([]float64, ShortTermReport, error) {
	base, err := m.Inner.Forecast(ctx, in, horizon)
	if err != nil || !m.Cfg.Enabled() {
		return base, ShortTermReport{Reason: "off"}, err
	}
	out, rep := m.correct(ctx, in, base)
	if err := finite(out); err != nil {
		return base, ShortTermReport{Reason: "the correction was not finite; the forecast is the algorithm's own"}, nil
	}
	return out, rep, nil
}

func (m ShortTerm) correct(ctx context.Context, in Input, base []float64) ([]float64, ShortTermReport) {
	out := append([]float64(nil), base...)
	n := len(in.Values)
	step := in.Step
	if step <= 0 || n == 0 {
		return out, ShortTermReport{Reason: "no step"}
	}
	k := int(math.Ceil(m.Cfg.RecentMinutes * float64(time.Minute) / float64(step)))
	if k < 3 {
		k = 3
	}
	if n-k < MinSamplesFor(m.Inner, k) {
		return out, ShortTermReport{Reason: "not enough history to forecast the recent steps"}
	}
	back, err := m.Inner.Forecast(ctx, cut(in, n-k), k)
	if err != nil || len(back) != k {
		return out, ShortTermReport{Reason: "the recent steps could not be forecast"}
	}
	recent := in.Values[n-k:]
	resid := make([]float64, k)
	var level, recentMax float64
	for j := range recent {
		resid[j] = recent[j] - back[j]
		level += math.Abs(recent[j])
		recentMax = math.Max(recentMax, recent[j])
	}
	level /= float64(k)

	lastN := 2
	if lastN > k {
		lastN = k
	}
	var gap float64
	for _, r := range resid[k-lastN:] {
		gap += r
	}
	gap /= float64(lastN)
	slope := (resid[k-1] - resid[0]) / float64(k-1)

	// the noise of the quiet stretch before the recent steps: a gap inside it is not a change
	var noise float64
	if lo := n - 4*k; lo >= 0 {
		noise = stddev(in.Values[lo : n-k])
	} else if n-k >= 3 {
		noise = stddev(in.Values[:n-k])
	}
	threshold := math.Max(m.Cfg.Trigger*level, 2*noise)
	rep := ShortTermReport{Gap: gap, Slope: slope, Threshold: threshold}
	if math.Abs(gap) <= threshold || threshold < 0 {
		rep.Reason = "the load is where the long-period forecast has it"
		return out, rep
	}

	extrapolate := 0.0
	g := gap
	if g > 0 && slope > 0 {
		extrapolate = slope
	}
	if g < 0 {
		share := m.Cfg.DownShare
		if share < 0 || share > 1 {
			share = 0
		}
		g *= share
	}
	half := m.Cfg.FadeMinutes * float64(time.Minute) / float64(step)
	if half <= 0 {
		half = 1
	}
	nonNegative := true
	for _, v := range in.Values {
		if v < 0 {
			nonNegative = false
			break
		}
	}
	for h := range out {
		fade := math.Pow(0.5, float64(h)/half)
		corr := (g + extrapolate*math.Min(float64(h+1), float64(k))) * fade
		v := base[h] + corr
		if gap > 0 && recentMax > 0 {
			v = math.Min(v, math.Max(base[h], 1.5*recentMax))
		}
		if nonNegative && v < 0 {
			v = 0
		}
		out[h] = v
	}
	rep.Applied = true
	if gap > 0 {
		rep.Reason = "the load rose above what the long-period forecast had for it: the rise is added and fades"
	} else {
		rep.Reason = "the load fell below the long-period forecast: followed part of the way"
	}
	return out, rep
}

func stddev(v []float64) float64 {
	if len(v) < 2 {
		return 0
	}
	var mean float64
	for _, x := range v {
		mean += x
	}
	mean /= float64(len(v))
	var ss float64
	for _, x := range v {
		ss += (x - mean) * (x - mean)
	}
	return math.Sqrt(ss / float64(len(v)))
}
