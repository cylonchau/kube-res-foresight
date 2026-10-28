// Package scoring grades a forecasting algorithm on replayed history. For each evaluation window the algorithm's
// recommendation (made from the data before the window) is compared with what the measured usage of the window
// actually required, for CPU, memory and GPU separately.
//
// The accuracy is that of the resource, not of the replica count: the resource the whole workload needs is forecast, and
// the replica count follows from it (the resource divided by what one Pod holds), so a replica count is never a figure
// of its own. With 3 CPU cores actually needed and 4 forecast, the CPU accuracy is 1 - |4-3|/3 (a shortfall counts
// Under times as much). The replica count of a window is kept as a derived figure (how many windows it matched) and is
// scored only for a module with no CPU, memory or GPU source at all.
//
// Under-provisioning counts twice as much as over-provisioning, an OOM kill in a window the algorithm under-provisioned
// lowers the memory score further, and a history with holes lowers it a little. Each resource has its own score from 0 to
// 100 (Result.Metrics) and the result is one number from 0 to 100 combining them, plus the figures behind it.
package scoring

import (
	"math"
	"time"
)

// Outcome is what one side of the comparison recommends or required for a window.
type Outcome struct {
	Replicas                  int
	CPU                       float64 // cores
	Memory                    float64 // bytes
	GPU                       float64 // cards
	HasCPU, HasMemory, HasGPU bool
}

// Window is one evaluated stretch of time.
type Window struct {
	Start, End time.Time
	Predicted  Outcome // from the algorithm, using only data before Start
	Actual     Outcome // what the measured usage of the window required
	// OOM is the number of OOM kills seen in the window.
	OOM int
	// Complete is false when the window lacked data and the comparison is partial.
	Complete bool
}

// Weights set how much each part counts. A part with no data in any window is left out and the rest renormalised.
type Weights struct {
	// Replicas counts only when there is no resource part at all.
	Replicas float64
	CPU      float64
	Memory   float64
	GPU      float64
	// Under is the factor by which an under-provision error counts over an over-provision one.
	Under float64
	// OOMPenalty is the share of the score lost per OOM in an under-provisioned window, capped by OOMCap.
	OOMPenalty float64
	OOMCap     float64
}

// DefaultWeights are used until a module sets its own.
func DefaultWeights() Weights {
	return Weights{Replicas: 1, CPU: 0.35, Memory: 0.45, GPU: 0.2, Under: 2, OOMPenalty: 0.2, OOMCap: 0.6}
}

// Result is the grade of an algorithm over a set of windows.
type Result struct {
	Score   float64 `json:"score"` // 0..100
	Windows int     `json:"windows"`
	// Accuracies are 0..1; a negative value means the part had no data.
	Replicas     float64 `json:"replica_accuracy"`
	CPU          float64 `json:"cpu_accuracy"`
	Memory       float64 `json:"memory_accuracy"`
	GPU          float64 `json:"gpu_accuracy"`
	ReplicaExact int     `json:"replica_exact"`     // windows where the replica count matched exactly
	Under        int     `json:"under_provisioned"` // windows where something was under-provisioned
	OOMEvents    int     `json:"oom_events"`        // OOM kills in under-provisioned windows (what is penalised)
	OOMPenalty   float64 `json:"oom_penalty"`       // share of the score lost to them
	Completeness float64 `json:"completeness"`      // share of windows that had all their data
	// Metrics is the grade of each resource by itself: what ranks the algorithms for CPU, for memory and for GPU.
	Metrics []MetricResult `json:"metrics,omitempty"`
}

// MetricResult is the grade of one resource: its accuracy (0..1) and its score (0..100).
type MetricResult struct {
	Metric   string  `json:"metric"` // cpu, memory or gpu
	Accuracy float64 `json:"accuracy"`
	Score    float64 `json:"score"`
}

func replicaAccuracy(pred, actual int, under float64) float64 {
	if pred == actual {
		return 1
	}
	den := math.Max(float64(actual), 1)
	e := math.Abs(float64(pred-actual)) / den
	if pred < actual {
		e *= under
	}
	return math.Max(0, 1-e)
}

type resourceAcc struct{ abs, actual float64 }

func (a *resourceAcc) add(pred, actual, under float64) {
	e := math.Abs(pred - actual)
	if pred < actual {
		e *= under
	}
	a.abs += e
	a.actual += math.Abs(actual)
}

func (a resourceAcc) accuracy() float64 {
	if a.actual <= 0 {
		if a.abs == 0 {
			return 1
		}
		return 0
	}
	return math.Max(0, 1-a.abs/a.actual)
}

// underProvisioned reports whether the prediction would have left the workload short in the window.
func underProvisioned(p, a Outcome) bool {
	if p.Replicas < a.Replicas {
		return true
	}
	// memory short by more than 2%% is what kills a container
	if p.HasMemory && a.HasMemory && p.Memory < a.Memory*0.98 {
		return true
	}
	if p.HasCPU && a.HasCPU && p.CPU < a.CPU*0.9 {
		return true
	}
	if p.HasGPU && a.HasGPU && p.GPU < a.GPU {
		return true
	}
	return false
}

// Score grades the windows. No windows scores zero.
func Score(ws []Window, wt Weights) Result {
	res := Result{Windows: len(ws), Replicas: -1, CPU: -1, Memory: -1, GPU: -1}
	if len(ws) == 0 {
		return res
	}
	if wt.Under < 1 {
		wt.Under = 1
	}
	var replicaSum float64
	var cpu, mem, gpu resourceAcc
	var haveCPU, haveMem, haveGPU bool
	var complete int
	for _, w := range ws {
		replicaSum += replicaAccuracy(w.Predicted.Replicas, w.Actual.Replicas, wt.Under)
		if w.Predicted.Replicas == w.Actual.Replicas {
			res.ReplicaExact++
		}
		if w.Predicted.HasCPU && w.Actual.HasCPU {
			haveCPU = true
			cpu.add(w.Predicted.CPU, w.Actual.CPU, wt.Under)
		}
		if w.Predicted.HasMemory && w.Actual.HasMemory {
			haveMem = true
			mem.add(w.Predicted.Memory, w.Actual.Memory, wt.Under)
		}
		if w.Predicted.HasGPU && w.Actual.HasGPU {
			haveGPU = true
			gpu.add(w.Predicted.GPU, w.Actual.GPU, wt.Under)
		}
		if underProvisioned(w.Predicted, w.Actual) {
			res.Under++
			res.OOMEvents += w.OOM
		}
		if w.Complete {
			complete++
		}
	}
	res.Replicas = replicaSum / float64(len(ws))
	res.Completeness = float64(complete) / float64(len(ws))

	var total, sum float64
	if haveCPU {
		res.CPU = cpu.accuracy()
		total += wt.CPU
		sum += wt.CPU * res.CPU
	}
	if haveMem {
		res.Memory = mem.accuracy()
		total += wt.Memory
		sum += wt.Memory * res.Memory
	}
	if haveGPU {
		res.GPU = gpu.accuracy()
		total += wt.GPU
		sum += wt.GPU * res.GPU
	}
	if total == 0 {
		// no resource to grade: the replica count is the only thing there is to grade
		total, sum = wt.Replicas, wt.Replicas*res.Replicas
	}
	composite := 0.0
	if total > 0 {
		composite = sum / total
	}
	res.OOMPenalty = math.Min(wt.OOMCap, wt.OOMPenalty*float64(res.OOMEvents))
	holes := 0.7 + 0.3*res.Completeness
	res.Score = 100 * composite * (1 - res.OOMPenalty) * holes
	if haveCPU {
		res.Metrics = append(res.Metrics, MetricResult{Metric: "cpu", Accuracy: res.CPU, Score: 100 * res.CPU * holes})
	}
	if haveMem {
		// an OOM kill is a memory event: only the memory score pays for it
		res.Metrics = append(res.Metrics, MetricResult{Metric: "memory", Accuracy: res.Memory, Score: 100 * res.Memory * (1 - res.OOMPenalty) * holes})
	}
	if haveGPU {
		res.Metrics = append(res.Metrics, MetricResult{Metric: "gpu", Accuracy: res.GPU, Score: 100 * res.GPU * holes})
	}
	return res
}
