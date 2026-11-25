package clusteragent

import (
	"fmt"
	"strings"
	"time"
)

// The state changes of the objects of a cluster (P12 E7, E8): what the operator saw an object go from and to, with the reasons the object itself gives for it and the
// configuration it had at that moment. The objects are Node, Pod, workload (the replicas of a Deployment or a StatefulSet) and HPA (the HorizontalPodAutoscaler of the
// Kubernetes API, not the scaling of this platform). The operator only reads: a change is what a watch showed. What it does not know is left empty, never filled in.
// The usage at the time of the change is not here: the platform asks Prometheus for it (before, at and after the change), as the operator holds no usage history.

// The kinds of object a change is about.
const (
	KindNode     = "Node"
	KindPod      = "Pod"
	KindWorkload = "Workload"
	KindHPA      = "HPA"
)

// Limits of a batch of changes.
const (
	MaxBatchChanges = 500
	MaxSnapshotPods = 100
)

// The sources of a reason the object itself gives (the events are the platform's, see ClusterEvent).
const (
	SourceNodeCondition = "node_condition"
	SourceTaint         = "taint"
	SourcePodStatus     = "pod_status"
	SourceHPACondition  = "hpa_condition"
	SourceManagedFields = "managed_fields"
)

// Reason is one thing the object says about why it changed: a condition's reason and message, a taint, how a container ended, who last wrote the replicas.
type Reason struct {
	Source    string `json:"source"`
	Reason    string `json:"reason,omitempty"`
	Message   string `json:"message,omitempty"`
	Component string `json:"component,omitempty"`
}

// Res is CPU in millicores and memory in bytes; 0 is "not set".
type Res struct {
	CPUMilli    int64 `json:"cpu_milli"`
	MemoryBytes int64 `json:"memory_bytes"`
}

// PodRef is a Pod on a node at the time of the change, with what it asked for.
type PodRef struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Phase     string `json:"phase,omitempty"`
	Workload  string `json:"workload,omitempty"`
	Requests  Res    `json:"requests"`
	Limits    Res    `json:"limits"`
}

// ContainerState is how one container of a Pod was when it changed.
type ContainerState struct {
	Name     string `json:"name"`
	Restarts int32  `json:"restarts"`
	// State is running, waiting or terminated; Reason and Message are the waiting or terminated reason; ExitCode is meaningful when terminated.
	State    string `json:"state,omitempty"`
	Reason   string `json:"reason,omitempty"`
	Message  string `json:"message,omitempty"`
	ExitCode int32  `json:"exit_code,omitempty"`
	// Last* is how the container ended the time before (the last terminated state): OOMKilled and the exit code are only here.
	LastReason   string `json:"last_reason,omitempty"`
	LastExitCode int32  `json:"last_exit_code,omitempty"`
}

// Snapshot is the configuration of the object when it changed: read from the operator's cache at that moment, so a deleted object still has its last one.
type Snapshot struct {
	// Node: where it is and what it can hold, the taints it carried, and the Pods on it (at most MaxSnapshotPods of PodCount).
	Labels       map[string]string `json:"labels,omitempty"`
	Taints       []string          `json:"taints,omitempty"`
	Capacity     *Res              `json:"capacity,omitempty"`
	Allocatable  *Res              `json:"allocatable,omitempty"`
	CapacityPods int64             `json:"capacity_pods,omitempty"`
	PodCount     int               `json:"pod_count,omitempty"`
	Pods         []PodRef          `json:"pods,omitempty"`
	// Pod (and the template of a workload): what its containers ask for, who owns it, which node it is on.
	Requests   *Res             `json:"requests,omitempty"`
	Limits     *Res             `json:"limits,omitempty"`
	Owner      string           `json:"owner,omitempty"`
	Containers []ContainerState `json:"containers,omitempty"`
	// Workload and HPA: the replicas, and for an HPA its bounds and what it targets.
	Desired   *int32 `json:"desired,omitempty"`
	Ready     *int32 `json:"ready,omitempty"`
	Available *int32 `json:"available,omitempty"`
	Min       *int32 `json:"min,omitempty"`
	Max       *int32 `json:"max,omitempty"`
	Current   *int32 `json:"current,omitempty"`
	Target    string `json:"target,omitempty"`
}

// Change is one change of one object.
type Change struct {
	// RecordID is the same for the same change however often it is sent (a retry, a restart): the platform keeps one row for it.
	RecordID  string `json:"record_id"`
	Kind      string `json:"kind"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
	ObjectUID string `json:"object_uid,omitempty"`
	// Type is what changed: ready, pressure, unschedulable, taint, created, deleted (every kind); phase, restart, waiting, scheduled, node, status, terminating (Pod); replicas
	// (workload); bounds, desired, condition (HPA).
	Type string `json:"type"`
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
	// OccurredAt is the time the object gives for it (the transition time of a condition) or else when the operator saw it; ObservedAt is when the operator saw it.
	OccurredAt time.Time `json:"occurred_at"`
	ObservedAt time.Time `json:"observed_at"`
	// Node is the node a Pod was on; Workload the workload that owns a Pod or that an HPA targets: they are how changes of different objects are told to be about the same
	// place.
	Node     string    `json:"node,omitempty"`
	Workload string    `json:"workload,omitempty"`
	Reasons  []Reason  `json:"reasons,omitempty"`
	Snapshot *Snapshot `json:"snapshot,omitempty"`
}

// ChangeBatch is one report of changes.
type ChangeBatch struct {
	Operator string   `json:"operator,omitempty"`
	Changes  []Change `json:"changes"`
}

// Validate refuses a batch that is too big, or a change that cannot be told apart or placed in time.
func (b ChangeBatch) Validate() error {
	if len(b.Changes) > MaxBatchChanges {
		return fmt.Errorf("%w: %d changes in a batch (at most %d)", ErrInvalid, len(b.Changes), MaxBatchChanges)
	}
	for i, c := range b.Changes {
		switch c.Kind {
		case KindNode, KindPod, KindWorkload, KindHPA:
		default:
			return fmt.Errorf("%w: change %d has kind %q", ErrInvalid, i, c.Kind)
		}
		if strings.TrimSpace(c.RecordID) == "" || strings.TrimSpace(c.Name) == "" || strings.TrimSpace(c.Type) == "" {
			return fmt.Errorf("%w: change %d needs a record id, a name and a type", ErrInvalid, i)
		}
		if c.OccurredAt.IsZero() && c.ObservedAt.IsZero() {
			return fmt.Errorf("%w: change %s has no time", ErrInvalid, c.RecordID)
		}
	}
	return nil
}
