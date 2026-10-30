package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"

	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

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

// Scaler builds the in-memory scaler of the module: the bounds and the pace of the module, and the module itself as the source of the
// decision. It is nil, with the reason, when the module cannot be scaled safely.
func (m PlatformModule) Scaler(lastScale *time.Time) (*Scaler, error) {
	switch {
	case m.Name == "" || m.Namespace == "" || m.Workload == "":
		return nil, fmt.Errorf("the module has no name, namespace or workload")
	case m.MaxReplicas < 1:
		// a maximum of 0 would clamp the workload to zero replicas
		return nil, fmt.Errorf("max_replicas is %d: the module has no usable maximum", m.MaxReplicas)
	case m.MinReplicas > m.MaxReplicas:
		return nil, fmt.Errorf("min_replicas %d is above max_replicas %d", m.MinReplicas, m.MaxReplicas)
	}
	s := &Scaler{
		Name: "module-" + m.Name, Namespace: m.Namespace, Module: m.Name,
		Target: TargetRef{Kind: m.WorkloadKind, Name: m.Workload},
		Spec: Spec{
			MinReplicas: m.MinReplicas, MaxReplicas: m.MaxReplicas,
			Behavior: Behavior{
				ScaleUp:   ScaleRule{MaxStep: m.ScaleUpMaxStep, Cooldown: time.Duration(m.ScaleUpCooldownSeconds) * time.Second},
				ScaleDown: ScaleRule{MaxStep: m.ScaleDownMaxStep, Cooldown: time.Duration(m.ScaleDownCooldownSeconds) * time.Second},
			},
		},
		LastScale: lastScale,
	}
	if m.FallbackReplicas > 0 {
		f := m.FallbackReplicas
		s.Spec.FallbackReplicas = &f
	}
	return s, nil
}

// ModuleSync scales the workloads of the platform's modules: every Interval it reads the modules that are in auto mode, builds the
// scaler of each in memory and applies the scaling rules. What is configured on the module (bounds, fallback, pace, mode, bypass) is the
// only configuration, and a change is in force at the next round with nothing to regenerate in the cluster.
type ModuleSync struct {
	Applier  *Applier
	Platform *PlatformDecider
	Interval time.Duration
	// Settings, when set, gives the interval in force at each round (the platform's setting, which may change while the operator runs);
	// Interval stands without it.
	Settings *SettingsStore
	// Cache keeps the list of modules the platform last gave, so a round that cannot reach the platform goes on with it for the TTL; the zero
	// value turns it off.
	Cache ModuleCache
	// Namespace restricts the sync to one namespace; empty = every namespace.
	Namespace string

	mu        sync.Mutex
	lastScale map[string]time.Time
}

// NeedLeaderElection keeps the sync on the elected replica only, like the reconciler.
func (s *ModuleSync) NeedLeaderElection() bool { return true }

// Start runs until ctx ends.
func (s *ModuleSync) Start(ctx context.Context) error {
	log := logf.FromContext(ctx).WithName("module-sync")
	log.Info("scaling the workloads of the platform's auto modules", "interval", s.interval().String())
	for {
		s.Once(ctx)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(s.interval()):
		}
	}
}

// interval is the time between two rounds: the setting of the platform when there is one, else Interval.
func (s *ModuleSync) interval() time.Duration {
	interval := s.Interval
	if s.Settings != nil {
		interval = s.Settings.SyncInterval()
	}
	if interval < minEvaluationInterval {
		interval = defaultEvaluationInterval
	}
	return interval
}

// platformModules lists the modules the platform wants scaled.
func (s *ModuleSync) platformModules(ctx context.Context) ([]PlatformModule, error) {
	data, err := s.Platform.do(ctx, http.MethodGet, "/api/v1/operator/modules", url.Values{}, nil)
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
		return nil, unreachable(fmt.Errorf("unreadable platform response: %w", err))
	}
	if env.Code != 10000 {
		return nil, unreachable(fmt.Errorf("platform error %d: %s", env.Code, env.Msg))
	}
	return env.Data.Modules, nil
}

// Once runs one round. When the platform cannot be reached the round goes on with the list of modules it last gave, for the cache TTL
// (each module then follows its remembered decision, see CachingDecider); past that, or with nothing remembered, nothing changes: the
// replicas stay where they are.
func (s *ModuleSync) Once(ctx context.Context) {
	log := logf.FromContext(ctx).WithName("module-sync")
	modules, err := s.platformModules(ctx)
	if err != nil {
		remembered, age, ok := s.Cache.Stand(err)
		if !ok {
			log.Info("could not read the modules from the platform: replicas are left as they are", "error", err.Error())
			return
		}
		log.Info("could not read the modules from the platform: going on with the list it last gave", "age", age.Round(time.Second).String(), "error", err.Error())
		modules = remembered
	} else {
		s.Cache.Remember(modules)
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
		scaler, err := m.Scaler(last)
		if err != nil {
			log.Info("skipping a module", "module", m.Name, "reason", err.Error())
			continue
		}
		s.Applier.Evaluate(ctx, scaler, "module/"+key)
		if t := scaler.LastScale; t != nil {
			s.mu.Lock()
			if s.lastScale == nil {
				s.lastScale = map[string]time.Time{}
			}
			s.lastScale[key] = *t
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
