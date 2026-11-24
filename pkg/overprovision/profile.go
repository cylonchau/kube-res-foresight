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
	// Recent is the use itself over the last 24 hours (see AddRecent): for each resource the use of all the Pods in small cells, and the use the normal count carries.
	Recent []RecentLoad `json:"recent"`
	// RecentStart is the first moment of the first of the 24 hours (unix seconds), RecentHours the hour of the day of each of them, and CellSeconds the length of a cell.
	RecentStart int64 `json:"recent_start"`
	RecentHours []int `json:"recent_hours"`
	CellSeconds int   `json:"cell_seconds"`
}

// RecentLoad is the use of all the Pods of one resource over the last 24 hours, in cells of CellSeconds (an hour holds 3600 / CellSeconds of them): the mean of the points of the cell
// as they were measured, or -1 where the cell has none. Capacity is what the normal count of replicas carries at the share the P95 is wanted to take (Normal x Requests x target):
// a cell above it is a high load, a cell under it the normal one.
type RecentLoad struct {
	Resource string    `json:"resource"`
	Capacity float64   `json:"capacity"`
	Values   []float64 `json:"values"`
}

// LoadInput is the use of all the Pods of one resource (the series as it counts, see Sustained), the Requests of one Pod and the step of the series.
type LoadInput struct {
	Resource string
	Total    []Point
	// Raw is the use of all the Pods as it was measured, for the cells of the last 24 hours (see AddRecent); Total is the series as it counts.
	Raw         []Point
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

// cellSecondsFor is the length of a cell of the last day: 5 minutes for a series of that step or finer, 10 for one up to that, else the step rounded up to whole 5 minutes.
func cellSecondsFor(step int) int {
	switch {
	case step <= 300:
		return 300
	case step <= 600:
		return 600
	}
	return (step + 299) / 300 * 300
}

// AddRecent adds the use itself over the 24 hours that end at end: the hours are whole hours of the day in loc (the last one is the hour end is in, and is cut at end), each cut into cells of
// 5 or 10 minutes, with the mean of the use of all the Pods in each, and the use the normal count of replicas carries. It is what shows which parts of the day are the normal load and
// which the high one, in CPU and in memory. A resource without a measured use is left out.
func (p *LoadProfile) AddRecent(ins []LoadInput, target float64, end time.Time, loc *time.Location) {
	if p == nil || len(ins) == 0 {
		return
	}
	if loc == nil {
		loc = time.UTC
	}
	step := 0
	for _, in := range ins {
		step = max(step, in.StepSeconds)
	}
	cell := cellSecondsFor(step)
	perHour := 3600 / cell
	if perHour < 1 {
		perHour = 1
	}
	e := end.In(loc)
	first := time.Date(e.Year(), e.Month(), e.Day(), e.Hour(), 0, 0, 0, loc).Add(-23 * time.Hour)
	p.RecentStart, p.CellSeconds = first.Unix(), 3600/perHour
	p.RecentHours = make([]int, 24)
	for h := range p.RecentHours {
		p.RecentHours[h] = first.Add(time.Duration(h) * time.Hour).In(loc).Hour()
	}
	p.Recent = nil
	for _, in := range ins {
		if !(in.Request > 0) || len(in.Raw) == 0 {
			continue
		}
		sum := make([]float64, 24*perHour)
		n := make([]int, 24*perHour)
		for _, pt := range in.Raw {
			if math.IsNaN(pt.V) || math.IsInf(pt.V, 0) || pt.V < 0 {
				continue
			}
			i := int((pt.TS - p.RecentStart) / int64(p.CellSeconds))
			if pt.TS < p.RecentStart || i >= len(sum) {
				continue
			}
			sum[i] += pt.V
			n[i]++
		}
		values := make([]float64, len(sum))
		for i := range values {
			values[i] = -1
			if n[i] > 0 {
				values[i] = sum[i] / float64(n[i])
			}
		}
		p.Recent = append(p.Recent, RecentLoad{Resource: in.Resource, Capacity: float64(p.Normal) * in.Request * target, Values: values})
	}
}
