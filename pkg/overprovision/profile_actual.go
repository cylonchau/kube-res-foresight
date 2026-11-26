package overprovision

import (
	"math"
	"sort"
	"time"
)

// The replicas the load profile counts are what the use asks for (Need): an estimate, the use of the hour over what one Pod carries. What the workload really had is a different
// figure and is never worked out of that one: it is what the replicas metric of the module measured, hour of the day by hour of the day over the same period (the median of the
// samples of that hour on every day, with the fewest and the most seen), and the replicas of each of the last 24 hours. Where the metric has no sample the hour has no figure
// (0): nothing is filled in from the estimate.

// AddActual puts the measured replicas into the profile. reps are the samples of the replicas metric over the period (ready replicas, as measured), end the end of the last 24
// hours (the same as AddRecent was given), source the name of the metric the samples are of. A profile that is nil, or a metric with no usable sample, is left as it is.
func (p *LoadProfile) AddActual(reps []Point, end time.Time, loc *time.Location, source string) {
	if p == nil || len(reps) == 0 {
		return
	}
	if loc == nil {
		loc = time.UTC
	}
	buckets := make([][]float64, 24)
	var cut, cutEnd int64
	if p.RecentStart > 0 {
		cut, cutEnd = p.RecentStart, p.RecentStart+24*3600
	}
	recent := make([][]float64, 24)
	samples := 0
	for _, pt := range reps {
		if math.IsNaN(pt.V) || math.IsInf(pt.V, 0) || pt.V < 1 {
			continue
		}
		h := time.Unix(pt.TS, 0).In(loc).Hour()
		buckets[h] = append(buckets[h], pt.V)
		samples++
		if cut > 0 && pt.TS >= cut && pt.TS < cutEnd {
			recent[(pt.TS-cut)/3600] = append(recent[(pt.TS-cut)/3600], pt.V)
		}
	}
	if samples == 0 {
		return
	}
	measured := make([]int, 0, 24)
	for h := range p.Hours {
		vals := buckets[p.Hours[h].Hour]
		if len(vals) == 0 {
			continue
		}
		sort.Float64s(vals)
		med := int(math.Round(median(vals)))
		p.Hours[h].Actual = med
		p.Hours[h].ActualMin = int(math.Round(vals[0]))
		p.Hours[h].ActualMax = int(math.Round(vals[len(vals)-1]))
		p.Hours[h].ActualSamples = len(vals)
		measured = append(measured, med)
	}
	if len(measured) == 0 {
		return
	}
	sort.Ints(measured)
	p.ActualHours, p.ActualSamples, p.ActualSource = len(measured), samples, source
	p.ActualNormal, p.ActualPeak = measured[len(measured)/2], measured[len(measured)-1]
	if cut > 0 {
		p.RecentReplicas = make([]int, 24)
		for i, vals := range recent {
			if len(vals) == 0 {
				continue
			}
			sort.Float64s(vals)
			p.RecentReplicas[i] = int(math.Round(median(vals)))
		}
	}
}

func median(sorted []float64) float64 {
	n := len(sorted)
	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2
}
