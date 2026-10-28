package forecast

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"
)

// SeasonalProfile is the percentile recommender of the Kubernetes VPA and Google Autopilot with a clock added: instead of one
// flat percentile of the whole history, the history is put into buckets of the time of the week (the day of the week and the
// time of day, in steps of BucketMinutes), and the forecast of a moment is a decayed percentile of the values that were seen
// at the same moment of the week before. A load that peaks at nine every morning is forecast as that peak at nine, and a
// quiet Sunday night as a quiet one.
//
// Compared with the algorithms it sits between: hist_percentile is a flat line, it cannot know a peak is coming; seasonal_naive
// copies the one value of one cycle ago, so one odd day (an incident, a batch that ran late) is repeated as if it were the
// pattern. Here every day that was seen at that time takes part, an odd one is one vote among many, and the percentile (90 or
// 95) is what keeps the line above the usual peak, which is what a resource recommendation wants.
//
// A bucket that has fewer than MinPerBucket values (a young history, a weekday seen once) is not trusted: the forecast falls
// back to the time of day over all days, and when that is thin too, to the percentile of the whole history, so it always
// answers, and with a short history it is the hist_percentile it replaces. Times are UTC plus UTCOffsetMinutes, so "nine
// o'clock" can be the local nine. Only values before the forecast origin are used.
type SeasonalProfile struct {
	Percentile       float64 // 1..100
	BucketMinutes    int     // a divisor of 1440
	ByWeekday        bool    // false: the time of day only
	HalfLifeDays     float64 // 0 = no decay
	MinPerBucket     int
	UTCOffsetMinutes int
	SafetyMargin     float64
}

func (SeasonalProfile) ID() string { return "seasonal_profile" }

// MinSamples is small on purpose: with little history the model falls back to the percentile of everything it has.
func (SeasonalProfile) MinSamples() int { return 4 }

func (m SeasonalProfile) validate() error {
	if m.Percentile <= 0 || m.Percentile > 100 {
		return fmt.Errorf("percentile must be in (0, 100]")
	}
	if m.BucketMinutes < 1 || m.BucketMinutes > 720 || 1440%m.BucketMinutes != 0 {
		return fmt.Errorf("bucket_minutes must be a divisor of 1440 (a day) between 1 and 720")
	}
	if m.HalfLifeDays < 0 {
		return fmt.Errorf("half_life_days must not be negative")
	}
	if m.MinPerBucket < 1 {
		return fmt.Errorf("min_samples_per_bucket must be at least 1")
	}
	if m.UTCOffsetMinutes < -720 || m.UTCOffsetMinutes > 840 {
		return fmt.Errorf("utc_offset_minutes must be between -720 and 840")
	}
	if m.SafetyMargin < 0 {
		return fmt.Errorf("safety_margin must not be negative")
	}
	return nil
}

// weighted is a value with the weight it counts for.
type weighted struct{ v, w float64 }

// weightedPercentile is the smallest value whose cumulative weight reaches percent of the total weight. It sorts pts.
func weightedPercentile(pts []weighted, percent float64) float64 {
	sort.Slice(pts, func(a, b int) bool { return pts[a].v < pts[b].v })
	var total float64
	for _, p := range pts {
		total += p.w
	}
	target := total * percent / 100
	var cum float64
	for _, p := range pts {
		cum += p.w
		if cum >= target-1e-12 {
			return p.v
		}
	}
	return pts[len(pts)-1].v
}

func (m SeasonalProfile) Forecast(ctx context.Context, in Input, horizon int) ([]float64, error) {
	if err := m.validate(); err != nil {
		return nil, err
	}
	if err := checkInput(m, in, horizon); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	n := len(in.Values)
	perDay := 1440 / m.BucketMinutes
	offset := time.Duration(m.UTCOffsetMinutes) * time.Minute
	bucketsOf := func(t time.Time) (daily, weekly int) {
		local := t.UTC().Add(offset)
		b := (local.Hour()*60 + local.Minute()) / m.BucketMinutes
		return b, int(local.Weekday())*perDay + b
	}

	halfLifeSteps := m.HalfLifeDays * 86400 / in.Step.Seconds()
	daily := make([][]weighted, perDay)
	var weekly [][]weighted
	if m.ByWeekday {
		weekly = make([][]weighted, 7*perDay)
	}
	all := make([]weighted, n)
	for i, v := range in.Values {
		w := 1.0
		if halfLifeSteps > 0 {
			w = math.Pow(0.5, float64(n-1-i)/halfLifeSteps)
		}
		p := weighted{v, w}
		all[i] = p
		d, wk := bucketsOf(in.TimeAt(i))
		daily[d] = append(daily[d], p)
		if m.ByWeekday {
			weekly[wk] = append(weekly[wk], p)
		}
	}

	// the percentile of a bucket is read once, however many steps fall into it
	cache := map[[2]int]float64{}
	read := func(kind, key int, pts []weighted) float64 {
		if v, ok := cache[[2]int{kind, key}]; ok {
			return v
		}
		v := weightedPercentile(pts, m.Percentile)
		cache[[2]int{kind, key}] = v
		return v
	}
	var global float64
	haveGlobal := false

	out := make([]float64, horizon)
	for h := 1; h <= horizon; h++ {
		d, wk := bucketsOf(in.End.Add(time.Duration(h) * in.Step))
		var v float64
		switch {
		case m.ByWeekday && len(weekly[wk]) >= m.MinPerBucket:
			v = read(0, wk, weekly[wk])
		case len(daily[d]) >= m.MinPerBucket:
			v = read(1, d, daily[d])
		default:
			if !haveGlobal {
				global, haveGlobal = weightedPercentile(all, m.Percentile), true
			}
			v = global
		}
		out[h-1] = v * (1 + m.SafetyMargin)
	}
	return out, finite(out)
}
