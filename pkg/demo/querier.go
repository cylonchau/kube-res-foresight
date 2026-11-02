// Package demo serves clearly artificial metrics so the whole forecasting pipeline can be tried without any
// Prometheus. The generator recognises the series names of the built-in presets, so the demo mappings are the
// real presets applied to a datasource of type "demo". The numbers are synthetic and must never be mistaken
// for measurements: the datasource, its mappings and the module are all named and flagged as demo.
package demo

import (
	"context"
	"hash/fnv"
	"math"
	"strings"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/collector"
)

// Shape of the artificial workload. One pod safely serves PodCapacityRPS when it uses TargetUtil of its PodCPUCores request; the
// traffic follows a daily and a weekly cycle with a little deterministic noise.
const (
	PodCapacityRPS = 100.0
	PodCPUCores    = 1.0
	TargetUtil     = 0.7
	baseRPS        = 260.0
	dailyAmplitude = 120.0
	weeklyDip      = 0.25 // weekend traffic is this share lower
	noiseShare     = 0.03
	memoryBase     = 6.0 * (1 << 30)
	memoryPerRPS   = 3.0 * (1 << 20)
)

// Signal names the quantity a query asks for.
type Signal string

const (
	SignalRPS      Signal = "rps"
	SignalCPU      Signal = "cpu"
	SignalMemory   Signal = "memory"
	SignalReplicas Signal = "replicas"
	SignalLatency  Signal = "latency"
	SignalOOM      Signal = "oom"

	SignalOOMWorkingSet Signal = "oom_working_set"
	SignalJVMHeapUsed   Signal = "jvm_heap_used"
	SignalJVMHeapMax    Signal = "jvm_heap_max"
	SignalJVMNonHeap    Signal = "jvm_nonheap_used"
	SignalJVMGCPause    Signal = "jvm_gc_pause"
	SignalJVMThreads    Signal = "jvm_threads"
	SignalK8sCPUReq     Signal = "k8s_cpu_requests"
	SignalK8sCPULimit   Signal = "k8s_cpu_limits"
	SignalK8sMemReq     Signal = "k8s_mem_requests"
	SignalK8sMemLimit   Signal = "k8s_mem_limits"
	SignalK8sThrottle   Signal = "k8s_cpu_throttle"

	SignalGPU       Signal = "gpu"
	SignalGPUMemory Signal = "gpu_memory"
	SignalNetRx     Signal = "network_rx"
	SignalNetTx     Signal = "network_tx"
)

// signals maps the series a preset reads to the quantity it stands for. Order matters only for readability:
// the first matching name wins and no two preset series share a name.
var signals = []struct {
	series string
	signal Signal
}{
	// the more specific queries come first: the working-set maximum reads the same series as the memory preset
	{"max_over_time(container_memory_working_set_bytes", SignalOOMWorkingSet},
	{`jvm_memory_used_bytes{area="heap"`, SignalJVMHeapUsed},
	{`jvm_memory_max_bytes{area="heap"`, SignalJVMHeapMax},
	{`jvm_memory_used_bytes{area="nonheap"`, SignalJVMNonHeap},
	{"jvm_gc_pause_seconds_sum", SignalJVMGCPause},
	{"jvm_threads_live_threads", SignalJVMThreads},
	{`kube_pod_container_resource_requests{resource="cpu"`, SignalK8sCPUReq},
	{`kube_pod_container_resource_limits{resource="cpu"`, SignalK8sCPULimit},
	{`kube_pod_container_resource_requests{resource="memory"`, SignalK8sMemReq},
	{`kube_pod_container_resource_limits{resource="memory"`, SignalK8sMemLimit},
	{"container_cpu_cfs_throttled_seconds_total", SignalK8sThrottle},
	{"DCGM_FI_DEV_GPU_UTIL", SignalGPU},
	{"DCGM_FI_DEV_FB_USED", SignalGPUMemory},
	{"container_network_receive_bytes_total", SignalNetRx},
	{"container_network_transmit_bytes_total", SignalNetTx},
	{"nginx_ingress_controller_request_duration_seconds_bucket", SignalLatency},
	{"istio_request_duration_milliseconds_bucket", SignalLatency},
	{"nginx_ingress_controller_requests", SignalRPS},
	{"istio_requests_total", SignalRPS},
	{"container_cpu_usage_seconds_total", SignalCPU},
	{"container_memory_working_set_bytes", SignalMemory},
	{"kube_deployment_status_replicas_available", SignalReplicas},
	{"kube_pod_status_ready", SignalReplicas},
	{"container_oom_events_total", SignalOOM},
	{"kube_pod_container_status_last_terminated_reason", SignalOOM},
}

// SignalOf returns the quantity a PromQL query reads, or false for a query the generator does not know.
func SignalOf(query string) (Signal, bool) {
	for _, s := range signals {
		if strings.Contains(query, s.series) {
			return s.signal, true
		}
	}
	return "", false
}

// Querier implements collector.RangeQuerier over the artificial workload.
type Querier struct{}

// QueryRange returns one series for the whole [start, end] range at the step, aligned to the step grid.
func (Querier) QueryRange(ctx context.Context, query string, start, end time.Time, step time.Duration) ([]collector.Point, error) {
	signal, ok := SignalOf(query)
	if !ok {
		return nil, errUnknownSeries
	}
	if step <= 0 {
		return nil, errBadStep
	}
	seed := seedOf(query)
	var out []collector.Point
	for ts := start; !ts.After(end); ts = ts.Add(step) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		out = append(out, collector.Point{Time: ts, Value: Value(signal, ts, seed)})
	}
	return out, nil
}

type demoError string

func (e demoError) Error() string { return string(e) }

const (
	errUnknownSeries demoError = "the demo datasource only serves the series of the built-in presets"
	errBadStep       demoError = "step must be positive"
)

// seedOf makes the noise differ per query text without being random.
func seedOf(query string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(query))
	return h.Sum32()
}

func noise(ts time.Time, seed uint32) float64 {
	// a small deterministic hash of the minute and the seed mapped to [-1, 1]
	x := uint64(ts.Unix()/60)*2654435761 + uint64(seed)*40503
	x ^= x >> 13
	x *= 0x5bd1e995
	x ^= x >> 15
	return float64(x%2000)/1000 - 1
}

// RPS is the artificial request rate at a time: a daily cycle peaking in the afternoon, a quieter weekend and
// a small noise.
func RPS(ts time.Time, seed uint32) float64 {
	t := ts.UTC()
	minute := float64(t.Hour()*60 + t.Minute())
	v := baseRPS + dailyAmplitude*math.Sin(2*math.Pi*(minute-360)/1440)
	if wd := t.Weekday(); wd == time.Saturday || wd == time.Sunday {
		v *= 1 - weeklyDip
	}
	v *= 1 + noiseShare*noise(ts, seed)
	return math.Max(v, 1)
}

// Replicas is the artificial ready pod count: enough pods that each serves at most its safe capacity.
func Replicas(rps float64) float64 { return math.Max(1, math.Ceil(rps/PodCapacityRPS)) }

// Value returns the artificial value of a signal at a time.
func Value(signal Signal, ts time.Time, seed uint32) float64 {
	rps := RPS(ts, 0) // the load itself is shared by every signal so they stay consistent
	switch signal {
	case SignalRPS:
		return RPS(ts, seed)
	case SignalCPU:
		// pods run at their target utilization when serving their capacity
		return rps / PodCapacityRPS * PodCPUCores * TargetUtil
	case SignalMemory:
		return memoryBase + memoryPerRPS*rps
	case SignalReplicas:
		return Replicas(rps)
	case SignalLatency:
		perPod := rps / Replicas(rps) / PodCapacityRPS
		return 0.04 + 0.08*perPod*perPod
	case SignalOOM:
		return 0
	}
	// JVM and Kubernetes configuration signals follow the same load: heap pressure rises with the traffic share.
	pods := Replicas(rps)
	heapMax := 2.0 * (1 << 30) * pods
	pressure := math.Min(0.95, 0.4+0.5*rps/(baseRPS+dailyAmplitude))
	switch signal {
	case SignalOOMWorkingSet:
		return (memoryBase + memoryPerRPS*rps) * 1.05
	case SignalJVMHeapMax:
		return heapMax
	case SignalJVMHeapUsed:
		return heapMax * pressure
	case SignalJVMNonHeap:
		return 256.0 * (1 << 20) * pods
	case SignalJVMGCPause:
		return 0.02 + 0.1*pressure*pressure
	case SignalJVMThreads:
		return 50*pods + 0.1*rps
	case SignalK8sCPUReq:
		return pods * PodCPUCores
	case SignalK8sCPULimit:
		return pods * PodCPUCores * 2
	case SignalK8sMemReq:
		return pods * 8.0 * (1 << 30)
	case SignalK8sMemLimit:
		return pods * 10.0 * (1 << 30)
	case SignalK8sThrottle:
		return math.Max(0, rps/PodCapacityRPS/pods-0.9) * 5
	case SignalGPU:
		return rps / PodCapacityRPS * 0.4 // a fraction of a card per pod's worth of traffic
	case SignalGPUMemory:
		return pods * 6.0 * (1 << 30) * (0.5 + 0.4*rps/(baseRPS+dailyAmplitude))
	case SignalNetRx:
		return rps * 12 * 1024
	case SignalNetTx:
		return rps * 48 * 1024
	}
	return 0
}
