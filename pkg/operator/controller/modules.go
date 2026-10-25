package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/cylonchau/kubeforecast/pkg/operator/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// AnnotationModuleDriven marks a PredictiveScaler that was built in memory from a platform module and never stored in
// the cluster.
const AnnotationModuleDriven = "autoscaling.kubeforecast.io/module-driven"

// IsModuleDriven reports whether the scaler comes from a platform module rather than from a PredictiveScaler object.
func IsModuleDriven(ps *v1alpha1.PredictiveScaler) bool {
	return ps != nil && ps.Annotations[AnnotationModuleDriven] == "true"
}

// PlatformModule is one workload the platform wants the operator to scale (GET /operator/modules).
type PlatformModule struct {
	Name                     string `json:"name"`
	Namespace                string `json:"namespace"`
	WorkloadKind             string `json:"workload_kind"`
	Workload                 string `json:"workload"`
	MinReplicas              int32  `json:"min_replicas"`
	MaxReplicas              int32  `json:"max_replicas"`
	HardMaxReplicas          int32  `json:"hard_max_replicas"`
	FallbackReplicas         int32  `json:"fallback_replicas"`
	ScaleUpMaxStep           int32  `json:"scale_up_max_step"`
	ScaleDownMaxStep         int32  `json:"scale_down_max_step"`
	ScaleUpCooldownSeconds   int32  `json:"scale_up_cooldown_seconds"`
	ScaleDownCooldownSeconds int32  `json:"scale_down_cooldown_seconds"`
}

// Scaler builds the in-memory PredictiveScaler of the module: Auto mode, the bounds and the pace of the module, and the
// platform as the source of the decision. It is nil, with the reason, when the module cannot be scaled safely.
func (m PlatformModule) Scaler(lastScale *time.Time) (*v1alpha1.PredictiveScaler, error) {
	switch {
	case m.Name == "" || m.Namespace == "" || m.Workload == "":
		return nil, fmt.Errorf("the module has no name, namespace or workload")
	case m.MaxReplicas < 1:
		// a maximum of 0 would clamp the workload to zero replicas
		return nil, fmt.Errorf("max_replicas is %d: the module has no usable maximum", m.MaxReplicas)
	case m.MinReplicas > m.MaxReplicas:
		return nil, fmt.Errorf("min_replicas %d is above max_replicas %d", m.MinReplicas, m.MaxReplicas)
	}
	ps := &v1alpha1.PredictiveScaler{
		ObjectMeta: metav1.ObjectMeta{Name: "module-" + m.Name, Namespace: m.Namespace, Annotations: map[string]string{AnnotationModuleDriven: "true"}},
		Spec: v1alpha1.PredictiveScalerSpec{
			ScaleTargetRef: v1alpha1.TargetRef{Kind: m.WorkloadKind, Name: m.Workload},
			Mode:           v1alpha1.ModeAuto,
			MinReplicas:    m.MinReplicas,
			MaxReplicas:    m.MaxReplicas,
			Behavior: v1alpha1.Behavior{
				ScaleUp:   rule(m.ScaleUpMaxStep, m.ScaleUpCooldownSeconds),
				ScaleDown: rule(m.ScaleDownMaxStep, m.ScaleDownCooldownSeconds),
			},
			Decision: v1alpha1.Decision{Source: v1alpha1.SourcePlatformAPI, PlatformAPI: &v1alpha1.PlatformAPIDecision{Module: m.Name}},
		},
	}
	if m.FallbackReplicas > 0 {
		f := m.FallbackReplicas
		ps.Spec.FallbackReplicas = &f
	}
	if lastScale != nil {
		t := metav1.NewTime(*lastScale)
		ps.Status.LastScaleTime = &t
	}
	return ps, nil
}

func rule(step, cooldownSeconds int32) v1alpha1.ScaleRule {
	r := v1alpha1.ScaleRule{MaxStep: step}
	if cooldownSeconds > 0 {
		r.Cooldown = &metav1.Duration{Duration: time.Duration(cooldownSeconds) * time.Second}
	}
	return r
}

// ModuleSync scales the workloads of the platform's modules without a PredictiveScaler object: every Interval it reads
// the modules that are in auto mode, builds the scaler of each in memory and runs the same scaling rules as the
// reconciler of the CRD. What is configured on the module (bounds, fallback, pace, mode, bypass) is therefore the only
// configuration, and a change is in force at the next round with nothing to regenerate in the cluster.
type ModuleSync struct {
	Reconciler *Reconciler
	Platform   *PlatformDecider
	Interval   time.Duration
	// Namespace restricts the sync to one namespace; empty = every namespace.
	Namespace string

	mu        sync.Mutex
	lastScale map[string]time.Time
}

// NeedLeaderElection keeps the sync on the elected replica only, like the reconciler.
func (s *ModuleSync) NeedLeaderElection() bool { return true }

// Start runs until ctx ends.
func (s *ModuleSync) Start(ctx context.Context) error {
	interval := s.Interval
	if interval < minEvaluationInterval {
		interval = defaultEvaluationInterval
	}
	log := logf.FromContext(ctx).WithName("module-sync")
	log.Info("scaling the workloads of the platform's auto modules", "interval", interval.String())
	for {
		s.Once(ctx)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(interval):
		}
	}
}

// platformModules lists the modules the platform wants scaled.
func (s *ModuleSync) platformModules(ctx context.Context) ([]PlatformModule, error) {
	probe := &v1alpha1.PredictiveScaler{
		ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{AnnotationModuleDriven: "true"}},
		Spec:       v1alpha1.PredictiveScalerSpec{Decision: v1alpha1.Decision{PlatformAPI: &v1alpha1.PlatformAPIDecision{}}},
	}
	data, err := s.Platform.do(ctx, probe, http.MethodGet, "/api/v1/operator/modules", url.Values{}, nil)
	if err != nil {
		return nil, err
	}
	var env struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			AutoAllowed bool             `json:"auto_allowed"`
			Modules     []PlatformModule `json:"modules"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, fmt.Errorf("unreadable platform response: %w", err)
	}
	if env.Code != 10000 {
		return nil, fmt.Errorf("platform error %d: %s", env.Code, env.Msg)
	}
	return env.Data.Modules, nil
}

// Once runs one round. An unreachable platform changes nothing: the replicas stay where they are.
func (s *ModuleSync) Once(ctx context.Context) {
	log := logf.FromContext(ctx).WithName("module-sync")
	modules, err := s.platformModules(ctx)
	if err != nil {
		log.Info("could not read the modules from the platform: replicas are left as they are", "error", err.Error())
		return
	}
	seen := make(map[string]bool, len(modules))
	for _, m := range modules {
		if s.Namespace != "" && m.Namespace != s.Namespace {
			continue
		}
		key := m.Namespace + "/" + m.Name
		seen[key] = true
		s.mu.Lock()
		var last *time.Time
		if t, ok := s.lastScale[key]; ok {
			last = &t
		}
		s.mu.Unlock()
		ps, err := m.Scaler(last)
		if err != nil {
			log.Info("skipping a module", "module", m.Name, "reason", err.Error())
			continue
		}
		if _, err := s.Reconciler.evaluate(ctx, ps, "module/"+key); err != nil {
			log.Info("could not evaluate a module", "module", m.Name, "error", err.Error())
			continue
		}
		if t := ps.Status.LastScaleTime; t != nil {
			s.mu.Lock()
			if s.lastScale == nil {
				s.lastScale = map[string]time.Time{}
			}
			s.lastScale[key] = t.Time
			s.mu.Unlock()
		}
	}
	s.mu.Lock()
	for key := range s.lastScale {
		if !seen[key] {
			delete(s.lastScale, key)
		}
	}
	s.mu.Unlock()
}
