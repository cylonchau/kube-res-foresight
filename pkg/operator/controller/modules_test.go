package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

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
	// validUntil is when the decisions the platform gives stop being valid; zero means ten minutes after the test's fixed time
	validUntil time.Time
	reports    []Report
	seenAuth   string
	// rejectModule is a module whose reports the platform refuses for good (404), as it would after the module was deleted.
	rejectModule string
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
		if !p.validUntil.IsZero() {
			until = p.validUntil
		}
		reply(w, map[string]any{"available": true, "module": r.URL.Query().Get("module"), "version": 3, "replicas": p.replicas, "valid_until": until, "source": "algorithm"})
	}))
	mux.HandleFunc("/api/v1/operator/reports", guard(func(w http.ResponseWriter, r *http.Request) {
		var rep Report
		_ = json.NewDecoder(r.Body).Decode(&rep)
		if p.rejectModule != "" && rep.Module == p.rejectModule {
			http.Error(w, "module not found", http.StatusNotFound)
			return
		}
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
	platform := &PlatformDecider{Endpoint: &PlatformEndpoint{URL: srv.URL, Credential: "ak:sk", Client: srv.Client()}}
	applier := &Applier{Client: c, Reporter: platform, Now: func() time.Time { return now }, Decider: platform}
	return &ModuleSync{Applier: applier, Platform: platform}, c
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

func TestModuleSyncScalesAWorkloadFromTheModuleConfigAndCreatesNothingInTheCluster(t *testing.T) {
	p := &fakePlatform{modules: []PlatformModule{apiModule()}, replicas: 9}
	s, c := moduleSync(t, p, deployment(4, 4))

	s.Once(context.Background())
	assert.EqualValues(t, 7, replicasOf(t, c), "the module's scale-up step of 3 limits the move from 4 to 9")
	assert.Equal(t, "kube-resource-fcst-AK ak:sk", p.seenAuth)
	var deployments appsv1.DeploymentList
	require.NoError(t, c.List(context.Background(), &deployments))
	assert.Len(t, deployments.Items, 1, "the only object in the cluster is the workload, whose replicas were written")
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

func TestAModuleWithoutAUsableMaximumIsRefused(t *testing.T) {
	bad := apiModule()
	bad.MaxReplicas = 0
	_, err := bad.Scaler(nil)
	assert.Error(t, err, "a maximum of 0 would clamp the workload to zero replicas")
	bad = apiModule()
	bad.MinReplicas = 11
	_, err = bad.Scaler(nil)
	assert.Error(t, err)
}

func TestTheScalerOfAModuleCarriesTheModulePace(t *testing.T) {
	m := apiModule()
	m.FallbackReplicas, m.ScaleDownCooldownSeconds, m.ScaleDownMaxStep = 4, 120, 2
	last := now.Add(-time.Minute)
	sc, err := m.Scaler(&last)
	require.NoError(t, err)
	assert.Equal(t, "api", sc.Module)
	assert.Equal(t, "module-api", sc.Name)
	assert.Equal(t, TargetRef{Kind: "Deployment", Name: "api"}, sc.Target)
	require.NotNil(t, sc.Spec.FallbackReplicas)
	assert.EqualValues(t, 4, *sc.Spec.FallbackReplicas)
	assert.EqualValues(t, 3, sc.Spec.Behavior.ScaleUp.MaxStep)
	assert.Zero(t, sc.Spec.Behavior.ScaleUp.Cooldown)
	assert.EqualValues(t, 2, sc.Spec.Behavior.ScaleDown.MaxStep)
	assert.Equal(t, 120*time.Second, sc.Spec.Behavior.ScaleDown.Cooldown)
	require.NotNil(t, sc.LastScale, "the cooldown clock survives between rounds")
	assert.True(t, sc.LastScale.Equal(last))
}
