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
