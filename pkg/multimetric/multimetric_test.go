package multimetric

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
)

func flat(v float64, n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = v
	}
	return out
}

func TestReduceStatistics(t *testing.T) {
	values := []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	for stat, want := range map[string]float64{StatAvg: 5.5, StatMax: 10, StatSum: 55, StatLast: 10, StatP50: 5.5, StatP90: 9.1, StatP99: 9.91} {
		got, ok := Reduce(stat, values)
		assert.True(t, ok, stat)
		assert.InDelta(t, want, got, 1e-9, stat)
	}
	_, ok := Reduce(StatAvg, nil)
	assert.False(t, ok)
	_, ok = Reduce("median-ish", values)
	assert.False(t, ok)
	assert.True(t, ValidStat(StatP95))
	assert.True(t, ValidStat("p97"), "any level is a statistic")
	assert.True(t, ValidCombine(CombinePrimary))
	assert.False(t, ValidCombine("mean"))
}

// the example of the requirement: CPU P99 1.2 of 5 cores, memory P99 8 of 10 GiB, one OOM
func TestTheRequirementExample(t *testing.T) {
	const gib = 1 << 30
	res := Compute(Config{}, []Input{
		{Source: Source{Key: "cpu", Name: "Pod CPU", Kind: "cpu", Resource: ResourceCPU, Weight: 2, Stat: StatP99, Threshold: 5}, Values: flat(1.2, 12)},
		{Source: Source{Key: "memory", Name: "Pod memory", Kind: "memory", Resource: ResourceMemory, Weight: 2, Stat: StatP99, Threshold: 10 * gib}, Values: flat(8*gib, 12)},
		{Source: Source{Key: "oom", Name: "OOM kills", Kind: "oom", Resource: ResourceSignal, Weight: 1, Stat: StatSum, Threshold: 1}, Values: []float64{0, 1, 0}},
	})
	cpu, mem := res.Rows[0], res.Rows[1]
	assert.InDelta(t, 0.24, cpu.Ratio, 1e-9)
	assert.InDelta(t, 0.8, mem.Ratio, 1e-9)
	assert.False(t, cpu.Triggered)
	assert.False(t, mem.Triggered)
	oom := res.Rows[2]
	assert.EqualValues(t, 1, oom.Demand)
	assert.True(t, oom.Triggered, "one OOM kill reaches the threshold of 1")
	assert.True(t, res.Triggered)
	assert.InDelta(t, 1.2, res.Resources[ResourceCPU].Value, 1e-9)
	assert.InDelta(t, 8*gib, res.Resources[ResourceMemory].Value, 1)
	assert.InDelta(t, (2*0.24+2*0.8+1*1.0)/5, res.Pressure, 1e-9)
	assert.InDelta(t, 1.0, res.MaxRatio, 1e-9)

	// the breach asks for capacity in proportion to its ratio, whatever the weights
	n, why := res.ReplicasFor(4)
	assert.Equal(t, 4, n)
	assert.Contains(t, why, "over its threshold")
}

func TestWithoutABreachTheWeightedPressureDecidesAndCanLowerTheCount(t *testing.T) {
	res := Compute(Config{}, []Input{
		{Source: Source{Key: "a", Name: "a", Resource: ResourceCPU, Weight: 1, Stat: StatMax, Threshold: 10}, Values: []float64{2}},
		{Source: Source{Key: "b", Name: "b", Resource: ResourceMemory, Weight: 3, Stat: StatMax, Threshold: 10}, Values: []float64{6}},
	})
	assert.InDelta(t, (1*0.2+3*0.6)/4, res.Pressure, 1e-9)
	assert.False(t, res.Triggered)
	n, _ := res.ReplicasFor(10)
	assert.Equal(t, 5, n, "ceil(10 x 0.5)")
	n, _ = res.ReplicasFor(1)
	assert.Equal(t, 1, n, "never below one")
	n, why := res.ReplicasFor(0)
	assert.Equal(t, 0, n)
	assert.Contains(t, why, "unknown")

	over := Compute(Config{}, []Input{
		{Source: Source{Key: "a", Resource: ResourceCPU, Weight: 1, Stat: StatMax, Threshold: 10}, Values: []float64{25}},
		{Source: Source{Key: "b", Resource: ResourceMemory, Weight: 9, Stat: StatMax, Threshold: 10}, Values: []float64{1}},
	})
	n, _ = over.ReplicasFor(4)
	assert.Equal(t, 10, n, "ceil(4 x 2.5): the heavy, idle source does not average the breach away")
}

func TestSourcesOfOneResourceCombine(t *testing.T) {
	in := []Input{
		{Source: Source{Key: "pod", Name: "Pod CPU", Resource: ResourceCPU, Priority: 1, Weight: 3, Stat: StatMax}, Values: []float64{2}},
		{Source: Source{Key: "gpu", Name: "GPU host CPU", Resource: ResourceCPU, Priority: 0, Weight: 1, Stat: StatMax}, Values: []float64{6}},
	}
	weighted := Compute(Config{}, in)
	assert.InDelta(t, 3.0, weighted.Resources[ResourceCPU].Value, 1e-9, "(3x2 + 1x6) / 4")
	assert.InDelta(t, 0.75, weighted.Rows[0].Share, 1e-9)

	byMax := Compute(Config{Combine: map[string]string{ResourceCPU: CombineMax}}, in)
	assert.EqualValues(t, 6, byMax.Resources[ResourceCPU].Value)
	bySum := Compute(Config{Combine: map[string]string{ResourceCPU: CombineSum}}, in)
	assert.EqualValues(t, 8, bySum.Resources[ResourceCPU].Value)
	primary := Compute(Config{Combine: map[string]string{ResourceCPU: CombinePrimary}}, in)
	assert.EqualValues(t, 6, primary.Resources[ResourceCPU].Value, "priority 0 wins")
	assert.Equal(t, []string{"gpu"}, primary.Resources[ResourceCPU].Sources)
	assert.Zero(t, primary.Rows[0].Share)
}

func TestAMissingSourceIsSkippedAndExplained(t *testing.T) {
	in := []Input{
		{Source: Source{Key: "pod", Name: "Pod CPU", Resource: ResourceCPU, Weight: 1, Stat: StatMax}, Missing: true, Reason: "forecast failed"},
		{Source: Source{Key: "gpu", Name: "GPU host CPU", Resource: ResourceCPU, Weight: 1, Stat: StatMax}, Values: []float64{4}},
	}
	res := Compute(Config{}, in)
	assert.EqualValues(t, 4, res.Resources[ResourceCPU].Value, "the weight is shared among the usable sources only")
	assert.False(t, res.Rows[0].Used)
	assert.Equal(t, "forecast failed", res.Rows[0].Note)
	assert.Contains(t, res.Notes[0], "Pod CPU")

	none := Compute(Config{}, in[:1])
	_, present := none.Resources[ResourceCPU]
	assert.False(t, present, "no usable source, no value")
	assert.Zero(t, none.Pressure)
	n, why := none.ReplicasFor(3)
	assert.Equal(t, 3, n)
	assert.Contains(t, why, "no source has a trigger")
}

func TestZeroWeightsCountEqually(t *testing.T) {
	res := Compute(Config{}, []Input{
		{Source: Source{Key: "a", Resource: ResourceCPU, Stat: StatMax, Threshold: 10}, Values: []float64{2}},
		{Source: Source{Key: "b", Resource: ResourceCPU, Stat: StatMax, Threshold: 10}, Values: []float64{4}},
	})
	assert.InDelta(t, 3.0, res.Resources[ResourceCPU].Value, 1e-9)
	assert.InDelta(t, 0.3, res.Pressure, 1e-9)
}

func TestAnyPercentileIsAStatisticAndNoneIsFixed(t *testing.T) {
	values := []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	for stat, want := range map[string]float64{"p1": 1.09, "p40": 4.6, "p41": 4.69, "p42": 4.78, "p50": 5.5, "p99.9": 9.991, "p99": 9.91} {
		assert.True(t, ValidStat(stat), stat)
		got, ok := Reduce(stat, values)
		assert.True(t, ok, stat)
		assert.InDelta(t, want, got, 1e-9, stat)
	}
	// the levels are ordered: a higher percentile is never lower
	prev := -1.0
	for n := 1; n <= 99; n++ {
		got, _ := Reduce("p"+strconv.Itoa(n), values)
		assert.GreaterOrEqual(t, got, prev, "p%d", n)
		prev = got
	}
	for _, bad := range []string{"", "p", "p0", "p100", "p-5", "p+5", "p1e1", "p 5", "pabc", "P95", "95", "p99.99999", "p0x10", "p__"} {
		assert.False(t, ValidStat(bad), "%q", bad)
		if bad == "" || bad == "p99.99999" {
			continue // "" is the average in Reduce, and p99.99999 is a level that is only too long to be stored (8 characters)
		}
		_, ok := Reduce(bad, values)
		assert.False(t, ok, "%q", bad)
	}
	level, ok := Level("p99.9")
	assert.True(t, ok)
	assert.InDelta(t, 0.999, level, 1e-12)
}
