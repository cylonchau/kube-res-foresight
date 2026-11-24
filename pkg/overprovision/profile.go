package overprovision

import (
	"math"
	"sort"
	"time"
)

// The load profile answers a question the percentiles cannot: the replicas one percentile asks for are the replicas needed for the busiest part of the period, and a workload
// that is quiet most of the day and busy for a few hours is then given the busy part's replicas all day. The profile takes the use of all the Pods apart for each hour of the
// day (over every day of the period), works out the replicas each hour asks for with the share of the Requests the P95 is wanted to take, and says which hours are the
// busy ones: the normal count (the replicas the typical hour asks for), the peak count (the busiest hour), the hours above the normal count, how much of the time the use
// is above what the normal count carries, and how many replica-hours a count that follows the hours would save over one that stays at the peak.

// HourNeed is the replicas one hour of the day asks for (the hour is in the module's time zone), and what each resource asks.
type HourNeed struct {
	Hour int `json:"hour"`
	// Need is the larger of what the resources ask for; CPU and Memory are what each asks for alone (0 = the resource was not measured).
	Need   int `json:"need"`
	CPU    int `json:"cpu"`
	Memory int `json:"memory"`
}

// LoadProfile is the replicas the hours of the day ask for.
type LoadProfile struct {
	// Days is how many days the period holds, Target the share of the Requests the P95 of an hour is wanted to take, Percentile the percentile an hour is judged by (95).
	Days       int     `json:"days"`
	Target     float64 `json:"target"`
	Percentile float64 `json:"percentile"`
	// Normal is the replicas the typical hour asks for (the median of the 24 hours), Peak the replicas the busiest hour asks for, Steady the replicas that cover every
	// hour (they are the Peak).
	Normal int `json:"normal"`
	Peak   int `json:"peak"`
	// Hours are the 24 hours; PeakHours are the hours of the day that ask for more than the Normal count.
	Hours     []HourNeed `json:"hours"`
	PeakHours []int      `json:"peak_hours"`
	// Above says for how much of the period the use of all the Pods is above what the Normal count carries (for the resource where that is longest): the share of the time, the
	// stretches and the longest one, in minutes. Resource is that resource.
	Above    *Exceed `json:"above"`
	Resource string  `json:"resource"`
	// Saved is the share of the replica-hours that a count following the hours would not run over one that stays at the Peak all day (0 when the hours ask for the same).
	Saved float64 `json:"saved"`
}

// LoadInput is the use of all the Pods of one resource (the series as it counts, see Sustained), the Requests of one Pod and the step of the series.
type LoadInput struct {
	Resource    string
	Total       []Point
	Request     float64
	StepSeconds int
}

// BuildLoadProfile works out the replicas each hour of the day asks for. target is the share of the Requests the percentile of an hour is wanted to take; loc the time zone
// the hours are in. It returns nil when no resource has what it needs, or the period is shorter than two days (an hour of the day is then one or two samples, not a habit).
func BuildLoadProfile(ins []LoadInput, target, percentile float64, loc *time.Location) *LoadProfile {
	if loc == nil {
		loc = time.UTC
	}
	if !(target > 0) || !(percentile > 0) || percentile > 100 {
		return nil
	}
	need := make([]HourNeed, 24)
	for h := range need {
		need[h].Hour = h
	}
	days := map[string]bool{}
	used := 0
	for _, in := range ins {
		if !(in.Request > 0) || len(in.Total) == 0 {
			continue
		}
		buckets := make([][]float64, 24)
		for _, p := range in.Total {
			if math.IsNaN(p.V) || math.IsInf(p.V, 0) || p.V < 0 {
				continue
			}
			t := time.Unix(p.TS, 0).In(loc)
			buckets[t.Hour()] = append(buckets[t.Hour()], p.V)
			days[t.Format("2006-01-02")] = true
		}
		covered := 0
		for h, vals := range buckets {
			if len(vals) < 2 {
				continue
			}
			sort.Float64s(vals)
			n := max(int(math.Ceil(rank(vals, percentile)/(in.Request*target)-1e-9)), 1)
			if in.Resource == Memory {
				need[h].Memory = n
			} else {
				need[h].CPU = n
			}
			covered++
		}
		if covered == 24 {
			used++
		}
	}
	if used == 0 || len(days) < 2 {
		return nil
	}
	sizes := make([]int, 24)
	for h := range need {
		need[h].Need = max(need[h].CPU, need[h].Memory)
		sizes[h] = need[h].Need
	}
	sort.Ints(sizes)
	prof := &LoadProfile{Days: len(days), Target: target, Percentile: percentile, Hours: need, PeakHours: []int{}}
	prof.Normal, prof.Peak = sizes[12], sizes[23]
	sum := 0
	for _, h := range need {
		sum += h.Need
		if h.Need > prof.Normal {
			prof.PeakHours = append(prof.PeakHours, h.Hour)
		}
	}
	if prof.Peak > 0 {
		prof.Saved = 1 - float64(sum)/float64(24*prof.Peak)
	}
	for _, in := range ins {
		if !(in.Request > 0) || len(in.Total) == 0 {
			continue
		}
		if ex := ExceedOf(in.Total, float64(prof.Normal)*in.Request*target, in.StepSeconds); ex != nil && (prof.Above == nil || ex.Share > prof.Above.Share) {
			prof.Above, prof.Resource = ex, in.Resource
		}
	}
	return prof
}
