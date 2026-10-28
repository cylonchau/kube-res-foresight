package scoring

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

var noon = time.Date(2026, 10, 1, 11, 0, 0, 0, time.UTC)

func window(pred, actual Outcome, oom int) Window {
	return Window{Start: noon, End: noon.Add(time.Hour), Predicted: pred, Actual: actual, OOM: oom, Complete: true}
}

// 2026-10-01 11:00-12:00: 4 predicted, 4 needed is 100%; 3 predicted, 4 needed with an OOM scores lower
func TestTheReplicaExample(t *testing.T) {
	exact := Score([]Window{window(Outcome{Replicas: 4}, Outcome{Replicas: 4}, 0)}, DefaultWeights())
	assert.EqualValues(t, 1, exact.Replicas)
	assert.EqualValues(t, 100, exact.Score)
	assert.Equal(t, 1, exact.ReplicaExact)

	short := Score([]Window{window(Outcome{Replicas: 3}, Outcome{Replicas: 4}, 0)}, DefaultWeights())
	assert.InDelta(t, 0.5, short.Replicas, 1e-9, "a quarter short counts twice: 1 - 0.25 x 2")
	assert.Equal(t, 1, short.Under)

	withOOM := Score([]Window{window(Outcome{Replicas: 3}, Outcome{Replicas: 4}, 1)}, DefaultWeights())
	assert.Equal(t, 1, withOOM.OOMEvents)
	assert.InDelta(t, 0.2, withOOM.OOMPenalty, 1e-9)
	assert.Less(t, withOOM.Score, short.Score)
	assert.InDelta(t, short.Score*0.8, withOOM.Score, 1e-9)
}

func TestOverProvisioningCostsLessThanUnderProvisioning(t *testing.T) {
	over := Score([]Window{window(Outcome{Replicas: 5}, Outcome{Replicas: 4}, 0)}, DefaultWeights())
	under := Score([]Window{window(Outcome{Replicas: 3}, Outcome{Replicas: 4}, 0)}, DefaultWeights())
	assert.Greater(t, over.Score, under.Score)
	assert.InDelta(t, 0.75, over.Replicas, 1e-9)
}

func TestResourcesAreScoredSeparately(t *testing.T) {
	p := Outcome{Replicas: 4, CPU: 2, Memory: 8, GPU: 1, HasCPU: true, HasMemory: true, HasGPU: true}
	a := Outcome{Replicas: 4, CPU: 2, Memory: 10, GPU: 1, HasCPU: true, HasMemory: true, HasGPU: true}
	res := Score([]Window{window(p, a, 0)}, DefaultWeights())
	assert.EqualValues(t, 1, res.CPU)
	assert.EqualValues(t, 1, res.GPU)
	assert.InDelta(t, 0.6, res.Memory, 1e-9, "20%% short counts twice")
	// the replica count was right and still the score is below 100 because the memory was short
	assert.Less(t, res.Score, 100.0)
	assert.Equal(t, 1, res.Under, "memory short by more than 2%% is under-provisioning")

	// parts with no data are left out and the rest renormalised
	only := Score([]Window{window(Outcome{Replicas: 4}, Outcome{Replicas: 4}, 0)}, DefaultWeights())
	assert.EqualValues(t, -1, only.CPU)
	assert.EqualValues(t, 100, only.Score)
}

func TestIncompleteDataLowersTheScoreAndTheOOMPenaltyIsCapped(t *testing.T) {
	w := window(Outcome{Replicas: 4}, Outcome{Replicas: 4}, 0)
	w.Complete = false
	res := Score([]Window{w}, DefaultWeights())
	assert.InDelta(t, 70, res.Score, 1e-9)
	assert.Zero(t, res.Completeness)

	many := Score([]Window{window(Outcome{Replicas: 1}, Outcome{Replicas: 4}, 9)}, DefaultWeights())
	assert.InDelta(t, 0.6, many.OOMPenalty, 1e-9, "capped")
}

func TestNoWindowsScoresZeroAndTheScoreIsAnAverageOverWindows(t *testing.T) {
	assert.Zero(t, Score(nil, DefaultWeights()).Score)
	two := Score([]Window{
		window(Outcome{Replicas: 4}, Outcome{Replicas: 4}, 0),
		window(Outcome{Replicas: 2}, Outcome{Replicas: 4}, 0),
	}, DefaultWeights())
	assert.InDelta(t, 0.5, two.Replicas, 1e-9, "(1 + 0) / 2")
	assert.Equal(t, 1, two.ReplicaExact)
	assert.Equal(t, 2, two.Windows)
}

// A Pod holds 1 core and 2 GiB; the workload needs 3 cores and 6 GiB (3 Pods). Forecasting 4 cores and 8 GiB (4 Pods) is
// graded on the resources, not on the replica count, which follows from them.
func TestTheAccuracyIsThatOfTheResourcesNotOfTheReplicaCount(t *testing.T) {
	gib := float64(1 << 30)
	pred := Outcome{Replicas: 4, CPU: 4, Memory: 8 * gib, HasCPU: true, HasMemory: true}
	actual := Outcome{Replicas: 3, CPU: 3, Memory: 6 * gib, HasCPU: true, HasMemory: true}
	res := Score([]Window{window(pred, actual, 0)}, DefaultWeights())
	assert.InDelta(t, 1-1.0/3, res.CPU, 1e-9)
	assert.InDelta(t, 1-2.0/6, res.Memory, 1e-9)
	// the same resources with a wildly different replica count score the same: the replica count is no part of the grade
	pred.Replicas = 40
	other := Score([]Window{window(pred, actual, 0)}, DefaultWeights())
	assert.InDelta(t, res.Score, other.Score, 1e-9)

	// each resource has its own score, and only the memory score pays for an OOM kill
	withOOM := Score([]Window{window(Outcome{CPU: 2, Memory: 4 * gib, HasCPU: true, HasMemory: true}, actual, 2)}, DefaultWeights())
	byMetric := map[string]MetricResult{}
	for _, m := range withOOM.Metrics {
		byMetric[m.Metric] = m
	}
	assert.Len(t, byMetric, 2)
	assert.InDelta(t, 100*(1-2*(1.0/3)), byMetric["cpu"].Score, 1e-9, "a quarter... a third short counts twice")
	assert.Less(t, byMetric["memory"].Score, 100*byMetric["memory"].Accuracy, "the memory score is lowered by the OOM penalty")
	assert.InDelta(t, 100*byMetric["cpu"].Accuracy, byMetric["cpu"].Score, 1e-9, "the CPU score is not")
}
