package decision

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func f64(v float64) *float64 { return &v }

func rps(algo string, u *float64, values ...float64) SeriesCandidate {
	return SeriesCandidate{Algorithm: algo, Values: values, Uncertainty: u, UncertaintySource: UncertaintyMeasured}
}

func baseSolver() SolverConfig {
	return SolverConfig{CapacityRPS: 100, CapacitySource: CapacityManual, Headroom: 1.2}
}

func TestSolveReplicas(t *testing.T) {
	tests := []struct {
		name        string
		cfg         func(*SolverConfig)
		in          ReplicaInputs
		replicas    int
		binding     string
		constraints int
	}{
		{
			name:        "throughput alone decides",
			in:          ReplicaInputs{RPS: []SeriesCandidate{rps("a", f64(0.1), 100, 450, 300)}},
			replicas:    5, // ceil(450 * 1.1 / 100) = ceil(4.95)
			binding:     ConstraintThroughput,
			constraints: 1,
		},
		{
			name: "cpu dominates when requests are small",
			cfg:  func(c *SolverConfig) { c.PodCPUCores, c.TargetUtilization = 0.5, 0.8 },
			in: ReplicaInputs{
				RPS: []SeriesCandidate{rps("a", f64(0), 200)}, // 2 by throughput
				CPU: []SeriesCandidate{rps("a", f64(0), 3.9)}, // ceil(3.9 / 0.4) = 10
			},
			replicas: 10, binding: ConstraintCPU, constraints: 2,
		},
		{
			name: "memory dominates",
			cfg:  func(c *SolverConfig) { c.PodMemoryBytes, c.TargetUtilization = 1<<30, 0.5 },
			in: ReplicaInputs{
				RPS:    []SeriesCandidate{rps("a", f64(0), 100)},
				Memory: []SeriesCandidate{rps("a", f64(0), 5<<30)}, // ceil(5 / 0.5) = 10
			},
			replicas: 10, binding: ConstraintMemory, constraints: 2,
		},
		{
			name:        "no measurement falls back to the module headroom",
			in:          ReplicaInputs{RPS: []SeriesCandidate{rps("a", nil, 450)}},
			replicas:    6, // ceil(450 * 1.2 / 100): the legacy formula
			binding:     ConstraintThroughput,
			constraints: 1,
		},
		{
			name:        "all-zero traffic needs one replica",
			in:          ReplicaInputs{RPS: []SeriesCandidate{rps("a", f64(0.3), 0, 0, 0)}},
			replicas:    1,
			binding:     ConstraintThroughput,
			constraints: 1,
		},
		{
			name:        "the peak at the end of the horizon counts",
			in:          ReplicaInputs{RPS: []SeriesCandidate{rps("a", f64(0), 10, 10, 10, 10, 990)}},
			replicas:    10,
			binding:     ConstraintThroughput,
			constraints: 1,
		},
		{
			name:        "bounds raise a small result",
			cfg:         func(c *SolverConfig) { c.Bounds = Bounds{MinReplicas: 3, MaxReplicas: 8} },
			in:          ReplicaInputs{RPS: []SeriesCandidate{rps("a", f64(0), 50)}},
			replicas:    3,
			binding:     ConstraintBounds,
			constraints: 1,
		},
		{
			name:        "bounds cap a large result",
			cfg:         func(c *SolverConfig) { c.Bounds = Bounds{MinReplicas: 3, MaxReplicas: 8} },
			in:          ReplicaInputs{RPS: []SeriesCandidate{rps("a", f64(0), 5000)}},
			replicas:    8,
			binding:     ConstraintBounds,
			constraints: 1,
		},
		{
			name:        "a decline on a short horizon is held",
			cfg:         func(c *SolverConfig) { c.CurrentReplicas = 10 },
			in:          ReplicaInputs{RPS: []SeriesCandidate{rps("a", f64(0), 200, 200)}},
			replicas:    10,
			binding:     ConstraintHold,
			constraints: 1,
		},
		{
			name:        "a confirmed decline is followed",
			cfg:         func(c *SolverConfig) { c.CurrentReplicas = 10 },
			in:          ReplicaInputs{RPS: []SeriesCandidate{rps("a", f64(0), 200, 200, 200, 200)}},
			replicas:    2,
			binding:     ConstraintThroughput,
			constraints: 1,
		},
		{
			name: "a scale-up is never held",
			cfg:  func(c *SolverConfig) { c.CurrentReplicas = 2 },
			in:   ReplicaInputs{RPS: []SeriesCandidate{rps("a", f64(0), 900)}},
			// one step only, still goes up
			replicas: 9, binding: ConstraintThroughput, constraints: 1,
		},
		{
			name:        "invalid values are skipped, not guessed",
			in:          ReplicaInputs{RPS: []SeriesCandidate{rps("a", f64(0), 100, math.NaN())}},
			replicas:    0,
			constraints: 0,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := baseSolver()
			if tc.cfg != nil {
				tc.cfg(&cfg)
			}
			plans := SolveReplicas(cfg, tc.in)
			if tc.constraints == 0 {
				assert.Empty(t, plans)
				return
			}
			require.Len(t, plans, 1)
			assert.Equal(t, tc.replicas, plans[0].Replicas, plans[0].Reason)
			assert.Equal(t, tc.binding, plans[0].Binding)
			assert.Len(t, plans[0].Constraints, tc.constraints)
		})
	}
}

func TestHigherMeasuredErrorIsMoreConservative(t *testing.T) {
	// the same forecast from two algorithms whose recent peak shortfall differs
	in := ReplicaInputs{RPS: []SeriesCandidate{
		rps("accurate", f64(0.05), 400, 500),
		rps("noisy", f64(0.60), 400, 500),
	}}
	plans := SolveReplicas(baseSolver(), in)
	require.Len(t, plans, 2)
	assert.Equal(t, "accurate", plans[0].Algorithm, "binding priority order is kept")
	assert.Less(t, plans[0].Replicas, plans[1].Replicas)
	assert.Equal(t, 6, plans[0].Replicas) // ceil(500 * 1.05 / 100)
	assert.Equal(t, 8, plans[1].Replicas) // ceil(500 * 1.60 / 100)
}

func TestMissingMetricBorrowsTheFirstEligibleSeries(t *testing.T) {
	cfg := baseSolver()
	cfg.PodCPUCores, cfg.TargetUtilization = 1, 1
	in := ReplicaInputs{
		RPS: []SeriesCandidate{rps("a", f64(0), 100), rps("b", f64(0), 100)},
		CPU: []SeriesCandidate{rps("a", f64(0), 4)},
	}
	plans := SolveReplicas(cfg, in)
	require.Len(t, plans, 2)
	b := plans[1]
	assert.Equal(t, "b", b.Algorithm)
	var borrowed bool
	for _, c := range b.Constraints {
		if c.Name == ConstraintCPU {
			borrowed = c.Borrowed
			assert.Equal(t, "a", c.Algorithm)
		}
	}
	assert.True(t, borrowed)
	assert.Equal(t, 4, b.Replicas)
}

func TestMissingCapacityProducesNoThroughputConstraint(t *testing.T) {
	cfg := SolverConfig{Headroom: 1.2, CapacitySource: CapacityNone}
	plans := SolveReplicas(cfg, ReplicaInputs{RPS: []SeriesCandidate{rps("a", nil, 500)}})
	assert.Empty(t, plans)
}

func TestSolverMatchesTheLegacyFormula(t *testing.T) {
	// the replicas field of Decide with a fixed capacity: ceil(peak * headroom / capacity)
	in := inputs()
	legacy := Decide(in).Replicas.Value
	cfg := SolverConfig{CapacityRPS: in.Policy.PodCapacityRPS, CapacitySource: CapacityManual, Headroom: in.Policy.HeadroomRatio}
	plans := SolveReplicas(cfg, ReplicaInputs{RPS: []SeriesCandidate{{Algorithm: "gbdt", Values: []float64{10, in.RPS[0].Peak, 20}}}})
	require.Len(t, plans, 1)
	assert.EqualValues(t, legacy, plans[0].Replicas)
}

func busySamples(n int, perPodRPS float64) []Sample {
	out := make([]Sample, n)
	start := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	for i := range out {
		out[i] = Sample{TS: start.Add(time.Duration(i) * time.Minute), RPS: perPodRPS * 4, Replicas: 4, CPUCores: 4 * 0.8} // 80% of a 1-core request
	}
	return out
}

func TestEstimateCapacity(t *testing.T) {
	// 100 rps per pod at 80% CPU saturation, scaled to the 70% target = 87.5 rps per pod
	est, err := EstimateCapacity(busySamples(40, 100), 1, 0.7)
	require.NoError(t, err)
	assert.InDelta(t, 87.5, est.RPSPerPod, 0.001)
	assert.Equal(t, 40, est.Used)
}

func TestEstimateCapacityRefusesWithTooFewBusySamples(t *testing.T) {
	_, err := EstimateCapacity(busySamples(10, 100), 1, 0.7)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "10 of 10")

	idle := busySamples(60, 100)
	for i := range idle {
		idle[i].CPUCores = 0.2 // 5% saturation: not informative
	}
	_, err = EstimateCapacity(idle, 1, 0.7)
	require.Error(t, err)

	_, err = EstimateCapacity(busySamples(60, 100), 0, 0.7)
	require.Error(t, err, "the CPU request is required")
}

func TestPercentile(t *testing.T) {
	v := []float64{5, 1, 3, 2, 4}
	assert.Equal(t, 3.0, Percentile(v, 0.5))
	assert.InDelta(t, 4.6, Percentile(v, 0.9), 1e-9)
	assert.Equal(t, []float64{5, 1, 3, 2, 4}, v, "the input is not reordered")
	assert.True(t, math.IsNaN(Percentile(nil, 0.5)))
}
