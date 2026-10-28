package forecast

import (
	"context"
	"fmt"
	"math"
)

// RegimeReset makes any model forget what came before the last level shift of the load. A release that halves the memory of a
// service, a scale-out that moves the traffic of each pod, a switch of the traffic to another region: the history before it is
// the history of another workload, and a model that averages it in (holt_winters, theta and mstl all do) stays wrong for as long
// as the old level is in its window. With the reset on, the model is given only the history since the last shift.
//
// A shift is found offline in the history it is handed (nothing after the forecast origin is involved). For every candidate
// point the mean of the steps after it is compared with what the steps before it predict, which includes the trend of those
// steps, so a steady ramp is not mistaken for a shift. The difference is measured in units of the noise of the series, estimated
// from the step-to-step changes (robust to the shift itself, a step is one outlier among them), and a shift is accepted when it
// is at least Threshold of those units and at least MinShift of the level, so a clean series with a tiny jump and a noisy one
// with a big jump are both judged fairly. The search is repeated on what follows the shift found, so the last one wins.
//
// A shift needs regimeMinSegment steps on each side to be believed: one that happened a few steps ago cannot be told from a
// spike yet, and nothing is reset for it. When the regime since the shift is shorter than the model needs (a seasonal model needs
// two cycles), the model gets the newest MinSamples steps instead, which still reach back over the shift: a reset never leaves a
// model without the history it asks for.
type RegimeReset struct {
	Base      Model
	Threshold float64 // in units of the noise
	MinShift  float64 // of the level, e.g. 0.2 = 20%
}

// regimeMinSegment is how many steps a regime needs on each side of a shift for the shift to be believed.
const regimeMinSegment = 6

// regimeWindow is how many steps before and after a candidate are compared.
const regimeWindow = 12

func (m RegimeReset) ID() string { return m.Base.ID() }

func (m RegimeReset) MinSamples() int { return m.Base.MinSamples() }

func (m RegimeReset) MinSamplesFor(horizon int) int { return MinSamplesFor(m.Base, horizon) }

// QuantileLevel passes the quantile of the base through: a reset does not change what the forecast is a forecast of.
func (m RegimeReset) QuantileLevel() (float64, bool) {
	if q, ok := m.Base.(Quantiled); ok {
		return q.QuantileLevel()
	}
	return 0, false
}

func (m RegimeReset) validate() error {
	if m.Base == nil {
		return fmt.Errorf("a regime reset needs a base model")
	}
	if m.Threshold <= 0 {
		return fmt.Errorf("change_threshold must be positive")
	}
	if m.MinShift < 0 {
		return fmt.Errorf("change_min_shift must not be negative")
	}
	return nil
}

// shiftNoise is the noise of the series per step: the robust standard deviation of the step-to-step changes divided by the root
// of two (the change of two independent noises has twice the variance).
func shiftNoise(y []float64) float64 {
	d := make([]float64, 0, len(y)-1)
	for i := 1; i < len(y); i++ {
		d = append(d, y[i]-y[i-1])
	}
	med := medianOf(d)
	dev := make([]float64, len(d))
	for i, v := range d {
		dev[i] = math.Abs(v - med)
	}
	return 1.4826 * medianOf(dev) / math.Sqrt2
}

// strongestShift is the index in seg where the load changed level the most, and whether that is a shift (see RegimeReset).
func strongestShift(seg []float64, threshold, minShift float64) (int, bool) {
	n := len(seg)
	if n < 2*regimeMinSegment {
		return 0, false
	}
	// prefix sums of y, i*y and y*y: the means, the slopes and the squared errors of any window in O(1)
	p1 := make([]float64, n+1)
	p2 := make([]float64, n+1)
	p3 := make([]float64, n+1)
	var level float64
	for i, v := range seg {
		p1[i+1] = p1[i] + v
		p2[i+1] = p2[i] + float64(i)*v
		p3[i+1] = p3[i] + v*v
		level += math.Abs(v)
	}
	level /= float64(n)
	sigma := shiftNoise(seg)
	if floor := 1e-6 * (1 + level); sigma < floor {
		sigma = floor
	}

	best, bestK := 0.0, -1
	for k := regimeMinSegment; k <= n-regimeMinSegment; k++ {
		l := regimeWindow
		if k < l {
			l = k
		}
		if n-k < l {
			l = n - k
		}
		fl := float64(l)
		left := (p1[k] - p1[k-l]) / fl
		right := (p1[k+l] - p1[k]) / fl
		// the slope of the left window predicts where the mean of the right one would be if the trend simply went on
		sxx := fl * (fl*fl - 1) / 12
		slope := 0.0
		if sxx > 0 {
			sxy := (p2[k] - p2[k-l]) - float64(k-l)*(p1[k]-p1[k-l]) - (fl-1)/2*(p1[k]-p1[k-l])
			slope = sxy / sxx
		}
		excess := right - (left + slope*fl)
		stat := math.Abs(excess) / (sigma * math.Sqrt(14/fl))
		scale := math.Max(math.Abs(left), math.Abs(right))
		if scale < 1e-12 || math.Abs(excess)/scale < minShift {
			continue
		}
		if stat > best {
			best, bestK = stat, k
		}
	}
	if bestK < 0 || best < threshold {
		return 0, false
	}

	// the window blurs where the shift is: place it where two flat pieces fit the steps around it best (a piece may be shorter than
	// a regime has to be, a shift near the end is placed first and believed after)
	a, b := bestK-regimeWindow, bestK+regimeWindow
	if a < 0 {
		a = 0
	}
	if b > n {
		b = n
	}
	sse := func(from, to int) float64 {
		cnt := float64(to - from)
		s := p1[to] - p1[from]
		return (p3[to] - p3[from]) - s*s/cnt
	}
	refined, least := bestK, math.Inf(1)
	for k := a + 2; k <= b-2; k++ {
		if cost := sse(a, k) + sse(k, b); cost < least {
			least, refined = cost, k
		}
	}
	if refined < regimeMinSegment || n-refined < regimeMinSegment {
		return 0, false // too close to an end to be told from a spike
	}
	return refined, true
}

// lastShift is the index at which the last regime of y starts, 0 when there is no shift.
func lastShift(y []float64, threshold, minShift float64) int {
	start := 0
	for pass := 0; pass < 10; pass++ {
		k, ok := strongestShift(y[start:], threshold, minShift)
		if !ok {
			break
		}
		start += k
	}
	return start
}

func (m RegimeReset) Forecast(ctx context.Context, in Input, horizon int) ([]float64, error) {
	if err := m.validate(); err != nil {
		return nil, err
	}
	n := len(in.Values)
	start := lastShift(in.Values, m.Threshold, m.MinShift)
	if start > 0 {
		// never give the model less than it asks for
		if need := MinSamplesFor(m.Base, horizon); n-start < need {
			start = n - need
		}
	}
	if start <= 0 {
		return m.Base.Forecast(ctx, in, horizon)
	}
	trimmed := Input{Values: in.Values[start:], Step: in.Step, End: in.End}
	if len(in.Covariates) > 0 {
		trimmed.Covariates = make(map[string][]float64, len(in.Covariates))
		for name, series := range in.Covariates {
			trimmed.Covariates[name] = series[start:]
		}
	}
	return m.Base.Forecast(ctx, trimmed, horizon)
}

// withRegime wraps a builder so that reset_on_change makes the model forget what came before the last level shift. Off (the
// default) the model is returned unchanged.
func withRegime(build Builder) Builder {
	return func(p map[string]any) (Model, error) {
		m, err := build(p)
		if err != nil {
			return nil, err
		}
		if pBool(p, "reset_on_change") {
			return RegimeReset{Base: m, Threshold: pFloat(p, "change_threshold"), MinShift: pFloat(p, "change_min_shift")}, nil
		}
		return m, nil
	}
}

// regimeParams are added to every algorithm that is not a baseline. They stay out of the stored parameters while they have their
// defaults, so nothing that exists changes version.
func regimeParams() []ParamSpec {
	return []ParamSpec{
		{OmitWhenDefault: true, Name: "reset_on_change", Type: "bool", Default: false,
			Description: "水平突变后重置：检测历史里最后一次水平突变（发布、扩缩容、流量切换），只用突变之后的历史预测。关闭时用全部历史（现状）。突变之后至少要有 6 步才会被确认；突变后的历史不够算法所需时，取算法所需的最新一段。"},
		{OmitWhenDefault: true, Name: "change_threshold", Type: "float", Default: 8.0, Min: num(3), Max: num(50),
			Description: "突变阈值：突变幅度（扣除前一段趋势后）超过噪声的多少倍才算，越大越不容易误报，噪声由相邻步变化的稳健标准差估计。仅 reset_on_change=true 时使用。"},
		{OmitWhenDefault: true, Name: "change_min_shift", Type: "float", Default: 0.2, Min: num(0), Max: num(5),
			Description: "最小突变幅度：突变至少是水平的这个比例（0.2 = 20%），很干净的序列上的微小跳变不算。仅 reset_on_change=true 时使用。"},
	}
}
