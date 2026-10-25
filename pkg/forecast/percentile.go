package forecast

import (
	"context"
	"fmt"
	"math"
	"sort"
)

// HistPercentile is the percentile recommender of the Kubernetes VPA and Google Autopilot: the forecast is a flat
// line at an exponentially decayed percentile of the history. A sample HalfLifeHours old counts half as much as
// the newest one, so the value follows rising load quickly and forgets a peak slowly. It does not model the
// future at all, which is why it is what an under-provisioning-averse memory recommendation is compared with.
type HistPercentile struct {
	Percentile    float64 // 1..100
	HalfLifeHours float64 // 0 = no decay
	SafetyMargin  float64 // the result is multiplied by 1+SafetyMargin
}

func (HistPercentile) ID() string      { return "hist_percentile" }
func (HistPercentile) MinSamples() int { return 4 }

func (m HistPercentile) Forecast(ctx context.Context, in Input, horizon int) ([]float64, error) {
	if m.Percentile <= 0 || m.Percentile > 100 {
		return nil, fmt.Errorf("percentile must be in (0, 100]")
	}
	if m.HalfLifeHours < 0 {
		return nil, fmt.Errorf("half_life_hours must not be negative")
	}
	if m.SafetyMargin < 0 {
		return nil, fmt.Errorf("safety_margin must not be negative")
	}
	if err := checkInput(m, in, horizon); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	n := len(in.Values)
	type weighted struct{ v, w float64 }
	pts := make([]weighted, n)
	halfLifeSteps := m.HalfLifeHours * 3600 / in.Step.Seconds()
	var total float64
	for i, v := range in.Values {
		age := float64(n - 1 - i)
		w := 1.0
		if halfLifeSteps > 0 {
			w = math.Pow(0.5, age/halfLifeSteps)
		}
		pts[i] = weighted{v, w}
		total += w
	}
	sort.Slice(pts, func(a, b int) bool { return pts[a].v < pts[b].v })
	// the smallest value whose cumulative weight reaches the percentile of the total weight
	target := total * m.Percentile / 100
	var cum float64
	level := pts[len(pts)-1].v
	for _, p := range pts {
		cum += p.w
		if cum >= target-1e-12 {
			level = p.v
			break
		}
	}
	level *= 1 + m.SafetyMargin
	out := make([]float64, horizon)
	for i := range out {
		out[i] = level
	}
	return out, finite(out)
}
