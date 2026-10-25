package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/cylonchau/kube-resource-fcst/pkg/operator/api/v1alpha1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// fakePlatform serves the three endpoints the module sync uses.
type fakePlatform struct {
	mu       sync.Mutex
	modules  []PlatformModule
	replicas int32
	down     bool
	reports  []Report
	seenAuth string
}

func (p *fakePlatform) handler() http.Handler {
	mux := http.NewServeMux()
	reply := func(w http.ResponseWriter, data any) {
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 10000, "data": data})
	}
	guard := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			p.mu.Lock()
			defer p.mu.Unlock()
			p.seenAuth = r.Header.Get("Authorization")
			if p.down {
				http.Error(w, "down", http.StatusServiceUnavailable)
				return
			}
			h(w, r)
		}
	}
	mux.HandleFunc("/api/v1/operator/modules", guard(func(w http.ResponseWriter, _ *http.Request) {
		reply(w, map[string]any{"auto_allowed": true, "modules": p.modules})
	}))
	mux.HandleFunc("/api/v1/operator/decisions", guard(func(w http.ResponseWriter, r *http.Request) {
		until := now.Add(10 * time.Minute)
		reply(w, map[string]any{"available": true, "module": r.URL.Query().Get("module"), "version": 3, "replicas": p.replicas, "valid_until": until, "source": "algorithm"})
	}))
	mux.HandleFunc("/api/v1/operator/reports", guard(func(w http.ResponseWriter, r *http.Request) {
		var rep Report
		_ = json.NewDecoder(r.Body).Decode(&rep)
		p.reports = append(p.reports, rep)
		reply(w, nil)
	}))
	return mux
}

func moduleSync(t *testing.T, p *fakePlatform, objs ...client.Object) (*ModuleSync, client.Client) {
	t.Helper()
	srv := httptest.NewServer(p.handler())
	t.Cleanup(srv.Close)
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(objs...).Build()
	platform := &PlatformDecider{Reader: c, Endpoint: &PlatformEndpoint{URL: srv.URL, Credential: "ak:sk", Client: srv.Client()}}
	rec := &Reconciler{Client: c, Scheme: testScheme(t), Reporter: platform, NoCRD: true, Now: func() time.Time { return now },
		Deciders: map[v1alpha1.DecisionSource]Decider{v1alpha1.SourcePlatformAPI: platform}}
	return &ModuleSync{Reconciler: rec, Platform: platform}, c
}

func replicasOf(t *testing.T, c client.Client) int32 {
	t.Helper()
	var d appsv1.Deployment
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Namespace: "prod", Name: "api"}, &d))
	return *d.Spec.Replicas
}

func apiModule() PlatformModule {
	return PlatformModule{Name: "api", Namespace: "prod", WorkloadKind: "Deployment", Workload: "api", MinReplicas: 2, MaxReplicas: 10, ScaleUpMaxStep: 3}
}

func TestModuleSyncScalesAWorkloadFromTheModuleConfigWithoutAnyPredictiveScaler(t *testing.T) {
	p := &fakePlatform{modules: []PlatformModule{apiModule()}, replicas: 9}
	s, c := moduleSync(t, p, deployment(4, 4))

	s.Once(context.Background())
	assert.EqualValues(t, 7, replicasOf(t, c), "the module's scale-up step of 3 limits the move from 4 to 9")
	assert.Equal(t, "kube-resource-fcst-AK ak:sk", p.seenAuth)
	var scalers v1alpha1.PredictiveScalerList
	require.NoError(t, c.List(context.Background(), &scalers))
	assert.Empty(t, scalers.Items, "no object is created in the cluster")
	require.NotEmpty(t, p.reports)
	assert.Equal(t, "api", p.reports[len(p.reports)-1].Module, "the platform is told which module acted")

	// the platform changes the bounds: the next round obeys them, nothing has to be regenerated
	p.mu.Lock()
	p.modules[0].MaxReplicas, p.modules[0].ScaleUpMaxStep = 5, 0
	p.mu.Unlock()
	s.Once(context.Background())
	assert.EqualValues(t, 5, replicasOf(t, c))

	// a lower maximum scales down to it (the module is the only configuration)
	p.mu.Lock()
	p.modules[0].MaxReplicas = 3
	p.mu.Unlock()
	s.Once(context.Background())
	assert.EqualValues(t, 3, replicasOf(t, c))
}

func TestModuleSyncLeavesTheReplicasAloneWhenThePlatformCannotBeReadOrTheModuleIsGone(t *testing.T) {
	p := &fakePlatform{modules: []PlatformModule{apiModule()}, replicas: 9}
	s, c := moduleSync(t, p, deployment(4, 4))

	p.mu.Lock()
	p.down = true
	p.mu.Unlock()
	s.Once(context.Background())
	assert.EqualValues(t, 4, replicasOf(t, c), "an unreachable platform changes nothing")

	p.mu.Lock()
	p.down = false
	p.modules = nil // switched out of auto mode, disabled, in bypass or deleted
	p.mu.Unlock()
	s.Once(context.Background())
	assert.EqualValues(t, 4, replicasOf(t, c))
}

func TestModuleSyncRefusesAModuleWithoutAUsableMaximumAndYieldsToAPredictiveScalerObject(t *testing.T) {
	bad := apiModule()
	bad.MaxReplicas = 0
	_, err := bad.Scaler(nil)
	assert.Error(t, err, "a maximum of 0 would clamp the workload to zero replicas")
	bad = apiModule()
	bad.MinReplicas = 11
	_, err = bad.Scaler(nil)
	assert.Error(t, err)

	p := &fakePlatform{modules: []PlatformModule{apiModule()}, replicas: 9}
	s, c := moduleSync(t, p, deployment(4, 4), scaler(v1alpha1.ModeObserve))
	s.Reconciler.NoCRD = false
	s.Once(context.Background())
	assert.EqualValues(t, 4, replicasOf(t, c), "an explicit PredictiveScaler of the same workload takes precedence")
}

func TestModuleDrivenScalerCarriesTheModulePace(t *testing.T) {
	m := apiModule()
	m.FallbackReplicas, m.ScaleDownCooldownSeconds, m.ScaleDownMaxStep = 4, 120, 2
	last := now.Add(-time.Minute)
	ps, err := m.Scaler(&last)
	require.NoError(t, err)
	assert.True(t, IsModuleDriven(ps))
	assert.Equal(t, v1alpha1.ModeAuto, ps.Spec.Mode)
	require.NotNil(t, ps.Spec.FallbackReplicas)
	assert.EqualValues(t, 4, *ps.Spec.FallbackReplicas)
	assert.EqualValues(t, 3, ps.Spec.Behavior.ScaleUp.MaxStep)
	assert.Nil(t, ps.Spec.Behavior.ScaleUp.Cooldown)
	assert.Equal(t, 120*time.Second, ps.Spec.Behavior.ScaleDown.Cooldown.Duration)
	assert.Equal(t, "api", ps.Spec.Decision.PlatformAPI.Module)
	require.NotNil(t, ps.Status.LastScaleTime, "the cooldown clock survives between rounds")
}
