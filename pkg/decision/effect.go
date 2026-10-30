package decision

import (
	"sort"
	"time"
)

// This file holds the replica-side figures of the effect report: how much capacity the platform held beyond what the load needed, and
// how often it changed its mind. Like the rest of the package it is pure: the facts come in, the figures go out.

// HeldReplicas is a replica count the platform asked for, and for how long that request stood (until the next one replaced it).
type HeldReplicas struct {
	From, To time.Time
	Replicas int
}

// Waste is what a window of requested replicas cost against what the measured load needed.
type Waste struct {
	// Judged is how many requests had a measured load to be compared with; the others are left out, not counted as right.
	Judged int `json:"judged"`
	// Over and Under count the requests that asked for more or fewer replicas than the load needed at its peak while the request stood.
	Over  int `json:"over"`
	Under int `json:"under"`
	// OverReplicas is the mean surplus of the requests that were over (replicas), UnderReplicas the mean shortfall of those that were under.
	OverReplicas  float64 `json:"over_replicas"`
	UnderReplicas float64 `json:"under_replicas"`
	// SurplusReplicaHours is the surplus held, replica by replica, for as long as each request stood.
	SurplusReplicaHours float64 `json:"surplus_replica_hours"`
}

// WasteOf compares every request with the largest replica count the load needed while it stood. A request is over when it asked for more
// than that peak need and under when it asked for fewer; a request with no measured need in its span is not judged.
func WasteOf(held []HeldReplicas, needs []NeedPoint) Waste {
	sorted := append([]NeedPoint(nil), needs...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].At.Before(sorted[j].At) })
	var w Waste
	var overSum, underSum float64
	for _, h := range held {
		if h.Replicas <= 0 || !h.To.After(h.From) {
			continue
		}
		peak, seen := 0, false
		i := sort.Search(len(sorted), func(i int) bool { return !sorted[i].At.Before(h.From) })
		for ; i < len(sorted) && sorted[i].At.Before(h.To); i++ {
			seen = true
			if sorted[i].Need > peak {
				peak = sorted[i].Need
			}
		}
		if !seen {
			continue
		}
		w.Judged++
		switch {
		case h.Replicas > peak:
			w.Over++
			overSum += float64(h.Replicas - peak)
			w.SurplusReplicaHours += float64(h.Replicas-peak) * h.To.Sub(h.From).Hours()
		case h.Replicas < peak:
			w.Under++
			underSum += float64(peak - h.Replicas)
		}
	}
	if w.Over > 0 {
		w.OverReplicas = overSum / float64(w.Over)
	}
	if w.Under > 0 {
		w.UnderReplicas = underSum / float64(w.Under)
	}
	return w
}

// ScaleEvent is one change of the replica count the operator made.
type ScaleEvent struct {
	At time.Time
	Up bool
}

// Churn counts the changes and the reversals among them.
type Churn struct {
	Ups   int `json:"ups"`
	Downs int `json:"downs"`
	// Reversals are changes that undid the one before within the window: a scale-down soon after a scale-up, or the other way round.
	Reversals int `json:"reversals"`
}

// ChurnOf counts the scale events, oldest first after sorting, and how many of them reversed the previous one within the window.
func ChurnOf(events []ScaleEvent, within time.Duration) Churn {
	sorted := append([]ScaleEvent(nil), events...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].At.Before(sorted[j].At) })
	var c Churn
	for i, e := range sorted {
		if e.Up {
			c.Ups++
		} else {
			c.Downs++
		}
		if i > 0 && sorted[i-1].Up != e.Up && e.At.Sub(sorted[i-1].At) <= within {
			c.Reversals++
		}
	}
	return c
}

// ReplicaSample is the Ready replicas measured at a moment.
type ReplicaSample struct {
	At       time.Time
	Replicas int
}

// Capacity is what the replicas that really ran amounted to against what the measured load needed, over the moments both were measured.
type Capacity struct {
	// Points is how many moments were compared and CoveredHours how long they stand for, together.
	Points       int     `json:"points"`
	CoveredHours float64 `json:"covered_hours"`
	// The Pod-hours that ran (Ready replicas), that the load needed (the replicas it asked for at the target utilization, no safety
	// margin), the surplus (ran above the need) and the shortfall (ran below it).
	ActualPodHours    float64 `json:"actual_pod_hours"`
	NeededPodHours    float64 `json:"needed_pod_hours"`
	SurplusPodHours   float64 `json:"surplus_pod_hours"`
	ShortfallPodHours float64 `json:"shortfall_pod_hours"`
	// ShortMinutes is how long fewer replicas were Ready than the load needed; Attainment is the share of the covered time that was not.
	ShortMinutes float64 `json:"short_minutes"`
	Attainment   float64 `json:"attainment"`
}

// CapacityOf compares the Ready replicas with the replicas the load needed, moment by moment. A moment stands for the time until the next
// one, but never for more than twice the usual gap (a hole in the data is not counted as measured time). A replica sample counts for a
// need point only if it is the newest one at or before it and no older than twice the usual gap; a need point with none is left out.
func CapacityOf(replicas []ReplicaSample, needs []NeedPoint) Capacity {
	needs = append([]NeedPoint(nil), needs...)
	sort.Slice(needs, func(i, j int) bool { return needs[i].At.Before(needs[j].At) })
	replicas = append([]ReplicaSample(nil), replicas...)
	sort.Slice(replicas, func(i, j int) bool { return replicas[i].At.Before(replicas[j].At) })
	var out Capacity
	if len(needs) == 0 || len(replicas) == 0 {
		return out
	}
	gap := usualGap(needs)
	var covered, short float64
	for i, n := range needs {
		k := sort.Search(len(replicas), func(k int) bool { return replicas[k].At.After(n.At) }) - 1
		if k < 0 || n.At.Sub(replicas[k].At) > 2*gap {
			continue
		}
		dt := gap
		if i+1 < len(needs) {
			if next := needs[i+1].At.Sub(n.At); next < 2*gap {
				dt = next
			} else {
				dt = 2 * gap
			}
		}
		hours, ready := dt.Hours(), float64(replicas[k].Replicas)
		need := float64(n.Need)
		out.Points++
		covered += dt.Seconds()
		out.ActualPodHours += ready * hours
		out.NeededPodHours += need * hours
		switch {
		case ready > need:
			out.SurplusPodHours += (ready - need) * hours
		case ready < need:
			out.ShortfallPodHours += (need - ready) * hours
			short += dt.Seconds()
		}
	}
	out.CoveredHours = covered / 3600
	out.ShortMinutes = short / 60
	if covered > 0 {
		out.Attainment = 1 - short/covered
	}
	return out
}

// usualGap is the median gap between consecutive points, the step of the series; a minute when there is only one point.
func usualGap(needs []NeedPoint) time.Duration {
	if len(needs) < 2 {
		return time.Minute
	}
	gaps := make([]time.Duration, 0, len(needs)-1)
	for i := 1; i < len(needs); i++ {
		if g := needs[i].At.Sub(needs[i-1].At); g > 0 {
			gaps = append(gaps, g)
		}
	}
	if len(gaps) == 0 {
		return time.Minute
	}
	sort.Slice(gaps, func(i, j int) bool { return gaps[i] < gaps[j] })
	return gaps[(len(gaps)-1)/2] // the lower median: one long hole must not become the usual gap
}
