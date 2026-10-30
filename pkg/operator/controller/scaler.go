package controller

import "time"

// TargetRef identifies the workload whose replicas are controlled.
type TargetRef struct {
	// Kind is Deployment or StatefulSet (apps/v1).
	Kind string
	Name string
}

// ScaleRule limits how fast replicas change in one direction.
type ScaleRule struct {
	// MaxStep is the most replicas added or removed by one scaling action; 0 means no limit.
	MaxStep int32
	// Cooldown is the minimum time since the last scaling action before this direction may act again; 0 means none.
	Cooldown time.Duration
}

// Behavior holds the per-direction rules.
type Behavior struct {
	ScaleUp   ScaleRule
	ScaleDown ScaleRule
}

// Spec is what the scaling rules need to know about a workload: its bounds, the replicas to fall back to, and the pace.
type Spec struct {
	MinReplicas int32
	MaxReplicas int32
	// FallbackReplicas is used when the decision is unavailable or expired; without it the operator freezes the current replicas. A
	// fallback below the current replicas never scales down.
	FallbackReplicas *int32
	Behavior         Behavior
}

// Scaler is one workload the operator scales. It is built from a module of the platform at every round and lives only in memory: the
// platform module is the only configuration, nothing is stored in the cluster for it, and the replicas of the target are the only thing
// the operator writes.
type Scaler struct {
	// Name is the name of the scaler ("module-<module>"), Namespace the namespace of the workload.
	Name      string
	Namespace string
	// Module is the platform module that decides the replicas.
	Module string
	Target TargetRef
	Spec   Spec
	// LastScale is when the operator last scaled the target, nil when it has not since it started.
	LastScale *time.Time
}
