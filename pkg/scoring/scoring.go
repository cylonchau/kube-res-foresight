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
// Under-provisioning counts twice as much as over-provisioning, an OOM kill in a window in which the algorithm under-provisioned
// memory lowers the memory score further (and only the memory score: an OOM kill says nothing about CPU), when the OOM weight is on,
// and a history with holes lowers it a little. Each resource has its own score from 0 to
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
	// OOMPenalty is the share of the memory score lost per OOM kill in a window in which memory was under-provisioned, capped by OOMCap.
	// 0 leaves the kills out of every score (the OOM weight of the module is off).
	OOMPenalty float64
	OOMCap     float64
}

// DefaultWeights are the resource weights; the OOM weight is the module's own and is off here.
func DefaultWeights() Weights {
	return Weights{Replicas: 1, CPU: 0.35, Memory: 0.45, GPU: 0.2, Under: 2, OOMCap: 0.6}
}

// WeightsWithOOM is DefaultWeights with the share of the memory score one OOM kill takes away (0 = the kills are not weighed).
func WeightsWithOOM(oomWeight float64) Weights {
	w := DefaultWeights()
	if oomWeight > 0 {
		w.OOMPenalty = oomWeight
	}
	return w
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
	OOMEvents    int     `json:"oom_events"`        // OOM kills in windows with memory under-provisioned (what is penalised)
	OOMPenalty   float64 `json:"oom_penalty"`       // share of the memory score lost to them
	Completeness float64 `json:"completeness"`      // share of windows that had all their data
	// Metrics is the grade of each resource by itself: what ranks the algorithms for CPU, for memory and for GPU.
	Metrics []MetricResult `json:"metrics,omitempty"`
	// Details are the windows the grade was computed from, with the forecast and the actual need of each resource: the basis of
	// every figure above, so a score can be checked by hand.
	Details []WindowDetail `json:"details,omitempty"`
	// Sources names the metric sources each resource was read from ("cpu": ["cpu_usage"]), filled by the caller that knows them.
	Sources map[string][]string `json:"sources,omitempty"`
	// UnderWeight is the factor a shortfall counted by in this grade.
	UnderWeight float64 `json:"under_weight,omitempty"`
}

// WindowDetail is one evaluated window as it entered the grade. A resource the module has no source for is absent.
type WindowDetail struct {
	Start     time.Time `json:"start"`
	End       time.Time `json:"end"`
	Predicted Figures   `json:"predicted"`
	Actual    Figures   `json:"actual"`
	// Error is the error each resource contributed: |forecast - actual|, times Under for a shortfall.
	Error    Figures `json:"error"`
	Under    bool    `json:"under_provisioned"`
	OOM      int     `json:"oom"`
	Complete bool    `json:"complete"`
}

// Figures holds a value per resource; a nil resource has no data in the window.
type Figures struct {
	Replicas int      `json:"replicas"`
	CPU      *float64 `json:"cpu,omitempty"`    // cores
	Memory   *float64 `json:"memory,omitempty"` // bytes
	GPU      *float64 `json:"gpu,omitempty"`    // cards
}

func figuresOf(o Outcome) Figures {
	f := Figures{Replicas: o.Replicas}
	if o.HasCPU {
		v := o.CPU
		f.CPU = &v
	}
	if o.HasMemory {
		v := o.Memory
		f.Memory = &v
	}
	if o.HasGPU {
		v := o.GPU
		f.GPU = &v
	}
	return f
}

// errorFigures is the error each resource contributed in a window, the way the accuracy counts it.
func errorFigures(p, a Outcome, under float64) Figures {
	f := Figures{}
	add := func(has bool, pred, act float64) *float64 {
		if !has {
			return nil
		}
		e := math.Abs(pred - act)
		if pred < act {
			e *= under
		}
		return &e
	}
	f.CPU = add(p.HasCPU && a.HasCPU, p.CPU, a.CPU)
	f.Memory = add(p.HasMemory && a.HasMemory, p.Memory, a.Memory)
	f.GPU = add(p.HasGPU && a.HasGPU, p.GPU, a.GPU)
	return f
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

// memoryShort reports whether the prediction left memory short: what an OOM kill answers for.
func memoryShort(p, a Outcome) bool {
	return p.HasMemory && a.HasMemory && p.Memory < a.Memory*0.98
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
	res.UnderWeight = wt.Under
	for _, w := range ws {
		under := underProvisioned(w.Predicted, w.Actual)
		oom := 0
		if memoryShort(w.Predicted, w.Actual) {
			oom = w.OOM // an OOM kill is a memory event: a window short of CPU or replicas does not own it
		}
		res.Details = append(res.Details, WindowDetail{Start: w.Start, End: w.End, Predicted: figuresOf(w.Predicted), Actual: figuresOf(w.Actual),
			Error: errorFigures(w.Predicted, w.Actual, wt.Under), Under: under, OOM: oom, Complete: w.Complete})
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
		if under {
			res.Under++
		}
		res.OOMEvents += oom
		if w.Complete {
			complete++
		}
	}
	res.Replicas = replicaSum / float64(len(ws))
	res.Completeness = float64(complete) / float64(len(ws))

	res.OOMPenalty = math.Min(wt.OOMCap, wt.OOMPenalty*float64(res.OOMEvents))
	var total, sum float64
	if haveCPU {
		res.CPU = cpu.accuracy()
		total += wt.CPU
		sum += wt.CPU * res.CPU
	}
	if haveMem {
		res.Memory = mem.accuracy()
		total += wt.Memory
		sum += wt.Memory * res.Memory * (1 - res.OOMPenalty) // the kills lower the memory part, not the whole
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
	holes := 0.7 + 0.3*res.Completeness
	res.Score = 100 * composite * holes
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
