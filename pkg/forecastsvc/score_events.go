package forecastsvc

import (
	"math"
	"sort"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/forecast"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
)

// windowSpec is one stretch of history an algorithm is graded on. event is true (and rise says by how much) for a stretch picked because
// the load rose in it, false for one of the newest.
type windowSpec struct {
	start, end time.Time
	event      bool
	rise       float64
}

// referenceSource is the source whose load picks the event windows: the CPU one that is forecast, else the first that is.
func referenceSource(sources []*model.MetricSource) string {
	first := ""
	for _, src := range sources {
		if src.Basis != model.BasisForecast {
			continue
		}
		if src.Resource == model.ResourceCPU {
			return src.Key
		}
		if first == "" {
			first = src.Key
		}
	}
	return first
}

// eventWindows picks, from the history before the newest `skip` windows, the (at most) limit windows in which the load rose most: the
// windows whose peak is at least minRise above the peak of the window before them. A window that starts before notBefore is not offered:
// the algorithm has not enough history before it to forecast it. The windows come back in time order.
//
// Why: grading only the newest windows grades the calm of the last hour or two, where repeating the last value is right and every algorithm
// looks alike, so the simplest one tops the ranking and an algorithm that sees the daily peak coming never gets the credit. The windows in
// which the load rose are where forecasting matters.
func eventWindows(in forecast.Input, end time.Time, horizon time.Duration, skip, limit int, minRise float64, notBefore time.Time) []windowSpec {
	if in.Step <= 0 || horizon < in.Step || limit <= 0 {
		return nil
	}
	peak := func(from, to time.Time) (float64, bool) {
		i0, ok0 := gridIndex(in, from)
		i1, ok1 := gridIndex(in, to)
		if !ok0 || !ok1 || i1 <= i0 {
			return 0, false
		}
		top := math.Inf(-1)
		for _, v := range in.Values[i0+1 : i1+1] {
			if !math.IsNaN(v) {
				top = math.Max(top, v)
			}
		}
		return top, !math.IsInf(top, -1)
	}
	var found []windowSpec
	for j := skip; ; j++ {
		start := end.Add(-time.Duration(j+1) * horizon)
		if start.Before(notBefore) || start.Before(in.TimeAt(0)) {
			break
		}
		now, ok := peak(start, end.Add(-time.Duration(j)*horizon))
		if !ok {
			continue
		}
		before, ok := peak(start.Add(-horizon), start)
		if !ok || before <= 0 {
			continue
		}
		if rise := (now - before) / before; rise >= minRise {
			found = append(found, windowSpec{start: start, end: end.Add(-time.Duration(j) * horizon), event: true, rise: rise})
		}
	}
	sort.SliceStable(found, func(a, b int) bool { return found[a].rise > found[b].rise })
	if len(found) > limit {
		found = found[:limit]
	}
	sort.Slice(found, func(a, b int) bool { return found[a].start.Before(found[b].start) })
	return found
}
