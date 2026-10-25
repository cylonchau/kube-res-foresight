package decision

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestHeapPressureRaisesTheReplicasInProportion(t *testing.T) {
	cfg := baseSolver()
	cfg.CurrentReplicas = 6
	in := ReplicaInputs{RPS: []SeriesCandidate{rps("a", f64(0), 300)}} // throughput needs 3
	quiet := SolveReplicas(cfg, in)[0]
	assert.Equal(t, 6, quiet.Replicas, "no pressure: the throughput result, but a decline is held with too few steps")

	cfg.CurrentReplicas = 0
	assert.Equal(t, 3, SolveReplicas(cfg, in)[0].Replicas)

	cfg.CurrentReplicas = 6
	cfg.HeapThreshold, cfg.HeapPressure = 0.8, 0.9
	hot := SolveReplicas(cfg, in)[0]
	assert.Equal(t, 7, hot.Replicas, "ceil(max(3, current 6) x 0.9 / 0.8) = 7")
	assert.Equal(t, ConstraintJVMHeap, hot.Binding)

	cfg.CurrentReplicas, cfg.HeapPressure = 0, 0.7 // below the threshold: no constraint
	assert.Equal(t, 3, SolveReplicas(cfg, in)[0].Replicas)
	cfg.HeapPressure, cfg.HeapThreshold = 0.95, 0 // a zero threshold disables the rule
	assert.Equal(t, 3, SolveReplicas(cfg, in)[0].Replicas)
}

func TestOOMFloorAppliesToEverySourceExceptBypass(t *testing.T) {
	floor := int64(2 << 30)
	run := func(mutate func(*Inputs)) Recommendation {
		in := inputs()
		in.Policy.OOMFloorBytes, in.Policy.OOMFloorReason = floor, "oom 2 days ago"
		if mutate != nil {
			mutate(&in)
		}
		return Decide(in)
	}
	// an algorithm value below the floor is raised, one above it is left alone
	low := run(func(in *Inputs) { in.MemoryBytes = []Candidate{{Algorithm: "gbdt", Peak: float64(256 << 20)}} })
	assert.EqualValues(t, floor, low.MemoryBytes.Value)
	assert.True(t, low.MemoryBytes.Floored)
	assert.Contains(t, low.MemoryBytes.Reason, "OOM floor")
	assert.Equal(t, SourceAlgorithm, low.MemoryBytes.Source)
	big := run(func(in *Inputs) { in.MemoryBytes = []Candidate{{Algorithm: "gbdt", Peak: float64(4 << 30)}} })
	assert.False(t, big.MemoryBytes.Floored)

	// the module fallback and a fixed period are raised too
	fallback := run(func(in *Inputs) { in.MemoryBytes = nil })
	assert.Equal(t, SourceFallback, fallback.MemoryBytes.Source)
	assert.EqualValues(t, floor, fallback.MemoryBytes.Value, "the 512 MiB fallback is raised to the floor")
	period := run(func(in *Inputs) {
		in.Periods = []FixedPeriod{{Name: "all", Weekdays: 0x7f, StartMinute: 0, EndMinute: 1439, MemoryBytes: 128 << 20}}
	})
	assert.Equal(t, SourceFixedPeriod, period.MemoryBytes.Source)
	assert.EqualValues(t, floor, period.MemoryBytes.Value)

	// nothing else gives a value: the floor does, under its own source
	none := run(func(in *Inputs) { in.MemoryBytes = nil; in.Policy.Fallback.MemoryBytes = 0 })
	assert.Equal(t, SourceOOMFloor, none.MemoryBytes.Source)
	assert.True(t, none.MemoryBytes.Set)

	// bypass keeps the original value whatever happened
	bypass := run(func(in *Inputs) { in.Bypass = true })
	assert.False(t, bypass.MemoryBytes.Set)
	assert.Equal(t, SourceBypass, bypass.MemoryBytes.Source)

	// the module maximum has the last word, visibly
	capped := run(func(in *Inputs) { in.Policy.MaxMemoryBytes = 1 << 30 })
	assert.EqualValues(t, 1<<30, capped.MemoryBytes.Value)
	assert.True(t, capped.MemoryBytes.FloorCapped)

	// no OOM, no floor: the result is the one of before
	assert.False(t, Decide(inputs()).MemoryBytes.Floored)
}
