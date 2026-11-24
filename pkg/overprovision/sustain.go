package overprovision

import "math"

// Sustained is the use as it is held for a stretch of time: every point is the mean of the use over the window of minutes that ends at it. A peak that lasts for less than the
// window is spread over it (a few minutes at 4 cores in an hour of 2 cores is about 2.2), and a use that lasts for the whole window stays what it is (an hour at 4 cores is
// 4). The percentiles of this series say what the use was for as long as a Pod has to carry it, and are what a Requests is sized for; the percentiles of the raw points say
// how high a moment went, and are what the Limits is sized for. A window the step is too coarse for (fewer than two points) leaves the points as they are.
func Sustained(points []Point, windowMinutes int) []Point {
	if windowMinutes <= 0 || len(points) < 2 {
		return points
	}
	window := int64(windowMinutes) * 60
	// the step of the series: the median gap of the first points
	var gaps []int64
	for i := 1; i < len(points) && i < 50; i++ {
		if g := points[i].TS - points[i-1].TS; g > 0 {
			gaps = append(gaps, g)
		}
	}
	if len(gaps) == 0 {
		return points
	}
	for i := 1; i < len(gaps); i++ { // a few gaps: insertion sort is enough
		for j := i; j > 0 && gaps[j] < gaps[j-1]; j-- {
			gaps[j], gaps[j-1] = gaps[j-1], gaps[j]
		}
	}
	if window < 2*gaps[len(gaps)/2] {
		return points
	}
	out := make([]Point, 0, len(points))
	lo, sum, n := 0, 0.0, 0
	for hi, p := range points {
		if math.IsNaN(p.V) || math.IsInf(p.V, 0) {
			continue
		}
		sum += p.V
		n++
		for points[lo].TS <= p.TS-window {
			if !math.IsNaN(points[lo].V) && !math.IsInf(points[lo].V, 0) {
				sum -= points[lo].V
				n--
			}
			lo++
		}
		_ = hi
		if n > 0 {
			out = append(out, Point{TS: p.TS, V: sum / float64(n)})
		}
	}
	return out
}

// Exceed says how long the use was above a level: the share of the points above it, how many separate times, and the longest stretch in minutes. It is what a Requests that
// is more than the use at a percentile still has to answer for: the use is above it 0.8% of the time, in 3 stretches, the longest 25 minutes.
type Exceed struct {
	Level          float64 `json:"level"`
	Share          float64 `json:"share"`
	Events         int     `json:"events"`
	LongestMinutes int     `json:"longest_minutes"`
}

// ExceedOf measures how long the points were above a level (0 when the level is not positive or there are no points).
func ExceedOf(points []Point, level float64, stepSeconds int) *Exceed {
	if !(level > 0) || len(points) == 0 {
		return nil
	}
	if stepSeconds <= 0 {
		stepSeconds = 60
	}
	out := &Exceed{Level: level}
	run, longest, above := 0, 0, 0
	for _, p := range points {
		if p.V > level {
			above++
			if run == 0 {
				out.Events++
			}
			run++
			if run > longest {
				longest = run
			}
		} else {
			run = 0
		}
	}
	out.Share = float64(above) / float64(len(points))
	out.LongestMinutes = int(math.Round(float64(longest*stepSeconds) / 60))
	return out
}
