package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Mode selects whether the operator only reports what it would do or also scales.
// +kubebuilder:validation:Enum=Observe;Auto
type Mode string

const (
	// ModeObserve evaluates decisions and reports status without touching the target.
	ModeObserve Mode = "Observe"
	// ModeAuto writes the target's replicas through the scale subresource.
	ModeAuto Mode = "Auto"
)

// DecisionSource selects where the desired replicas come from.
// +kubebuilder:validation:Enum=Static;PlatformAPI;Prometheus
type DecisionSource string

const (
	// SourceStatic uses spec.decision.static.replicas.
	SourceStatic DecisionSource = "Static"
	// SourcePlatformAPI asks the kube-resource-fcst platform for the replicas of a module.
	SourcePlatformAPI DecisionSource = "PlatformAPI"
	// SourcePrometheus reads one existing Prometheus series as the desired replicas. The Adapter source
	// (external metrics API) is deliberately not offered: this operator replaces HPA/KEDA and must not depend on one.
	SourcePrometheus DecisionSource = "Prometheus"
)

// TargetRef identifies the workload whose replicas are controlled.
type TargetRef struct {
	// +kubebuilder:validation:Enum=Deployment;StatefulSet
	Kind string `json:"kind"`
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
	// APIVersion defaults to apps/v1.
	// +optional
	APIVersion string `json:"apiVersion,omitempty"`
}

// StaticDecision is a fixed replica count.
type StaticDecision struct {
	// +kubebuilder:validation:Minimum=0
	Replicas int32 `json:"replicas"`
}

// SecretKeyRef points at a key of a Secret in the same namespace as the PredictiveScaler.
type SecretKeyRef struct {
	Name string `json:"name"`
	// +kubebuilder:default=token
	Key string `json:"key,omitempty"`
}

// PlatformAPIDecision reads the replicas of a platform module.
type PlatformAPIDecision struct {
	// URL is the base URL of the kube-resource-fcst platform, for example https://kube-resource-fcst.example.com.
	URL string `json:"url"`
	// Module is the name of the platform module that decides this workload.
	// +kubebuilder:validation:MinLength=1
	Module string `json:"module"`
	// AccessKeySecretRef holds the developer credential as "<access key>:<access secret>".
	AccessKeySecretRef SecretKeyRef `json:"accessKeySecretRef"`
	// CABundleSecretRef optionally holds a PEM CA bundle used to verify the platform certificate.
	// +optional
	CABundleSecretRef *SecretKeyRef `json:"caBundleSecretRef,omitempty"`
	// TimeoutSeconds bounds one request to the platform.
	// +kubebuilder:default=5
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=60
	TimeoutSeconds int32 `json:"timeoutSeconds,omitempty"`
}

// PrometheusDecision reads the desired replicas from a Prometheus instant query. The query must return exactly one
// series; the value is rounded up to a whole number of replicas.
type PrometheusDecision struct {
	// URL is the base URL of the Prometheus-compatible API, for example http://prometheus.monitoring:9090.
	URL string `json:"url"`
	// Query is an instant query returning a single series whose value is the desired replica count.
	// +kubebuilder:validation:MinLength=1
	Query string `json:"query"`
	// BearerTokenSecretRef optionally holds a bearer token sent to Prometheus.
	// +optional
	BearerTokenSecretRef *SecretKeyRef `json:"bearerTokenSecretRef,omitempty"`
	// CABundleSecretRef optionally holds a PEM CA bundle used to verify the Prometheus certificate.
	// +optional
	CABundleSecretRef *SecretKeyRef `json:"caBundleSecretRef,omitempty"`
	// TimeoutSeconds bounds one request.
	// +kubebuilder:default=5
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=60
	TimeoutSeconds int32 `json:"timeoutSeconds,omitempty"`
	// MaxStalenessSeconds is how old the sample may be before the decision counts as expired; it is also how long
	// the decision stays valid after the sample time.
	// +kubebuilder:default=120
	// +kubebuilder:validation:Minimum=5
	MaxStalenessSeconds int32 `json:"maxStalenessSeconds,omitempty"`
}

// Decision selects and configures the source of the desired replicas.
type Decision struct {
	Source DecisionSource `json:"source"`
	// +optional
	Static *StaticDecision `json:"static,omitempty"`
	// +optional
	PlatformAPI *PlatformAPIDecision `json:"platformAPI,omitempty"`
	// +optional
	Prometheus *PrometheusDecision `json:"prometheus,omitempty"`
}

// ScaleRule limits how fast replicas change in one direction.
type ScaleRule struct {
	// MaxStep is the most replicas added or removed by one scaling action; 0 means no limit.
	// +kubebuilder:validation:Minimum=0
	MaxStep int32 `json:"maxStep,omitempty"`
	// Cooldown is the minimum time since the last scaling action before this direction may act again.
	// +optional
	Cooldown *metav1.Duration `json:"cooldown,omitempty"`
}

// Behavior holds the per-direction rules.
type Behavior struct {
	// +optional
	ScaleUp ScaleRule `json:"scaleUp,omitempty"`
	// +optional
	ScaleDown ScaleRule `json:"scaleDown,omitempty"`
}

// PredictiveScalerSpec is the desired behavior. It replaces an HPA or KEDA ScaledObject for the target.
type PredictiveScalerSpec struct {
	ScaleTargetRef TargetRef `json:"scaleTargetRef"`
	// +kubebuilder:default=Observe
	Mode Mode `json:"mode,omitempty"`
	// +kubebuilder:validation:Minimum=0
	MinReplicas int32 `json:"minReplicas"`
	// +kubebuilder:validation:Minimum=1
	MaxReplicas int32 `json:"maxReplicas"`
	// FallbackReplicas is used when the decision is unavailable or expired; without it the operator
	// freezes the current replicas. A fallback below the current replicas never scales down.
	// +optional
	// +kubebuilder:validation:Minimum=0
	FallbackReplicas *int32 `json:"fallbackReplicas,omitempty"`
	// EvaluationInterval is how often the decision is read and applied.
	// +optional
	EvaluationInterval *metav1.Duration `json:"evaluationInterval,omitempty"`
	// +optional
	Behavior Behavior `json:"behavior,omitempty"`
	Decision Decision `json:"decision"`
}

// DecisionStatus records the last decision used.
type DecisionStatus struct {
	Replicas   int32        `json:"replicas"`
	Source     string       `json:"source"`
	Version    int64        `json:"version,omitempty"`
	ValidUntil *metav1.Time `json:"validUntil,omitempty"`
	Reason     string       `json:"reason,omitempty"`
	// Algorithm and Score are the algorithm instance behind a platform decision and its score.
	Algorithm string `json:"algorithm,omitempty"`
	Score     string `json:"score,omitempty"`
}

// PredictiveScalerStatus is the observed state.
type PredictiveScalerStatus struct {
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	CurrentReplicas    int32 `json:"currentReplicas"`
	ReadyReplicas      int32 `json:"readyReplicas"`
	// DesiredReplicas is the bounded value the operator wants after safety checks.
	DesiredReplicas int32 `json:"desiredReplicas"`
	// +optional
	LastDecision *DecisionStatus `json:"lastDecision,omitempty"`
	// +optional
	LastScaleTime *metav1.Time `json:"lastScaleTime,omitempty"`
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// Condition types.
const (
	// ConditionReady is true when the last reconcile read the target and a valid decision.
	ConditionReady = "Ready"
	// ConditionConflict is true when another autoscaler also controls the target; the operator then does nothing.
	ConditionConflict = "Conflict"
	// ConditionDecisionValid is true while the decision is fresh and within its validity.
	ConditionDecisionValid = "DecisionValid"
)

// PredictiveScaler controls the replicas of one workload from predictions.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=ps,categories=autoscaling
// +kubebuilder:printcolumn:name="Mode",type=string,JSONPath=".spec.mode"
// +kubebuilder:printcolumn:name="Current",type=integer,JSONPath=".status.currentReplicas"
// +kubebuilder:printcolumn:name="Desired",type=integer,JSONPath=".status.desiredReplicas"
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=".status.conditions[?(@.type==\"Ready\")].status"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"
type PredictiveScaler struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PredictiveScalerSpec   `json:"spec,omitempty"`
	Status PredictiveScalerStatus `json:"status,omitempty"`
}

// PredictiveScalerList is a list of PredictiveScaler.
// +kubebuilder:object:root=true
type PredictiveScalerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PredictiveScaler `json:"items"`
}

func init() {
	SchemeBuilder.Register(&PredictiveScaler{}, &PredictiveScalerList{})
}
