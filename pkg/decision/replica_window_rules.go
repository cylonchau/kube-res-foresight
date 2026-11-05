package decision

import "fmt"

// How the replicas of a period act. A "fixed" period gives the replica count and nothing else decides it (the original behavior of a fixed
// period); a "min" period is a floor and a "max" period a cap: the algorithm still decides, the period only keeps the count at or above, or
// at or below, its number while it is active. "20:00-03:00 at least 10" lets the forecast go to 14 and never below 10.
const (
	ReplicaWindowFixed = "fixed"
	ReplicaWindowMin   = "min"
	ReplicaWindowMax   = "max"
)

// ValidReplicaWindow reports whether mode is one of the three (the empty string is "fixed").
func ValidReplicaWindow(mode string) bool {
	switch mode {
	case "", ReplicaWindowFixed, ReplicaWindowMin, ReplicaWindowMax:
		return true
	}
	return false
}

// overridesReplicas is true when the period pins the replica count outright.
func (p FixedPeriod) overridesReplicas() bool {
	return p.Replicas > 0 && (p.ReplicaMode == "" || p.ReplicaMode == ReplicaWindowFixed)
}

// ActivePeriods are the periods that apply at in.Now, highest priority first.
func ActivePeriods(in Inputs) []FixedPeriod { return activePeriods(in) }

// ApplyReplicaWindows keeps the replica count of f within the floors and caps of the active "min" and "max" periods, then within the bounds of the
// module (lo and hi, 0 = none: a bound of the module is the last word). With several floors the largest counts, with several caps the smallest;
// when a floor is above a cap the floor wins, because fewer replicas than someone asked to keep is the riskier mistake. A count that was
// fixed by a period or by bypass is not touched, and a value that did not exist (nothing was decided) is only given by a floor.
func ApplyReplicaWindows(f Field, active []FixedPeriod, lo, hi int64) Field {
	if f.Source == SourceBypass || f.Source == SourceFixedPeriod {
		return f
	}
	var floor, ceil int64
	var floorBy, ceilBy string
	capLost := false
	for _, p := range active {
		switch p.ReplicaMode {
		case ReplicaWindowMin:
			if int64(p.Replicas) > floor {
				floor, floorBy = int64(p.Replicas), p.Name
			}
		case ReplicaWindowMax:
			if p.Replicas > 0 && (ceil == 0 || int64(p.Replicas) < ceil) {
				ceil, ceilBy = int64(p.Replicas), p.Name
			}
		}
	}
	if floor == 0 && ceil == 0 {
		return f
	}
	if floor > 0 && ceil > 0 && ceil < floor {
		ceil, ceilBy, capLost = 0, "", true
	}
	v, set, was := f.Value, f.Set, f.Value
	var notes []string
	if capLost {
		notes = append(notes, fmt.Sprintf("a cap window is below the floor window %q: the floor wins", floorBy))
	}
	if floor > 0 && (!set || v < floor) {
		v, set = floor, true
		notes = append(notes, fmt.Sprintf("raised to %d by the window %q (at least)", floor, floorBy))
	}
	if ceil > 0 && set && v > ceil {
		v = ceil
		notes = append(notes, fmt.Sprintf("capped at %d by the window %q (at most)", ceil, ceilBy))
	}
	if hi > 0 && v > hi {
		v = hi
		notes = append(notes, fmt.Sprintf("capped at the maximum %d of the module", hi))
	}
	if set && lo > 0 && v < lo {
		v = lo
		notes = append(notes, fmt.Sprintf("raised to the minimum %d of the module", lo))
	}
	if len(notes) == 0 {
		return f
	}
	if !f.Set {
		f.Source, f.Detail, f.Reason = SourceFixedPeriod, floorBy, ""
	}
	if f.Reason != "" {
		f.Reason += "; "
	}
	f.Set, f.Value = set, v
	f.Reason += joinReasons(notes)
	f.Clamped = f.Clamped || v != was
	return f
}

// fixedCount is the replica count of a module that keeps a fixed number, within the bounds of the module.
func fixedCount(n int, lo, hi int64) Field {
	v := int64(n)
	f := Field{Set: true, Source: SourceFixedCount, Reason: fmt.Sprintf("the module keeps a fixed %d replicas", n)}
	if lo > 0 && v < lo {
		v, f.Clamped = lo, true
		f.Reason += fmt.Sprintf("; raised to the minimum %d", lo)
	}
	if hi > 0 && v > hi {
		v, f.Clamped = hi, true
		f.Reason += fmt.Sprintf("; capped at the maximum %d", hi)
	}
	f.Value = v
	return f
}
