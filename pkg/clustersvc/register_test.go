package clustersvc

import (
	"context"
	"testing"

	"github.com/cylonchau/kube-res-foresight/pkg/clusteragent"
	"github.com/cylonchau/kube-res-foresight/pkg/dao/rdb"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/cylonchau/kube-res-foresight/pkg/modulecopy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (f *fixture) withModules(t *testing.T) {
	t.Helper()
	require.NoError(t, f.db.AutoMigrate(&model.ForecastModule{PredictEnabled: true}, &model.MetricSource{}, &model.ModulePeriod{}, &model.MetricMapping{}))
	f.svc.Modules = rdb.NewModuleDAO(f.db)
	f.svc.Copier = &modulecopy.Copier{Modules: f.svc.Modules, Mappings: rdb.NewMetricMappingDAO(f.db)}
}

func web(ns, name string) clusteragent.Candidate {
	r := int32(3)
	return clusteragent.Candidate{Namespace: ns, Kind: "Deployment", Name: name, Container: "app", Replicas: &r, Requests: &clusteragent.Res{CPUMilli: 500, MemoryBytes: 1 << 30}}
}

// the switch of the cluster is off until somebody turns it on: a report makes nothing
func TestNothingIsRegisteredWhileTheSwitchIsOff(t *testing.T) {
	f := newFixture(t)
	f.withModules(t)
	res, err := f.svc.Register(context.Background(), f.def, clusteragent.RegistrationBatch{Workloads: []clusteragent.Candidate{web("shop", "api")}})
	require.NoError(t, err)
	assert.False(t, res.Enabled)
	assert.Empty(t, res.Registered)
	var n int64
	require.NoError(t, f.db.Model(&model.ForecastModule{}).Count(&n).Error)
	assert.Zero(t, n)
}

// a registered module is switched off, observing, and holds what the operator read; the maximum of its replicas is not guessed
func TestARegisteredModuleIsSwitchedOffAndHasNoGuessedBounds(t *testing.T) {
	f := newFixture(t)
	f.withModules(t)
	f.def.AutoRegister = true
	res, err := f.svc.Register(context.Background(), f.def, clusteragent.RegistrationBatch{Workloads: []clusteragent.Candidate{web("shop", "api")}})
	require.NoError(t, err)
	require.Equal(t, []string{"shop-api"}, res.Registered)
	m, err := f.svc.Modules.ByName(context.Background(), "shop-api")
	require.NoError(t, err)
	assert.False(t, m.Enabled, "switched off")
	assert.Equal(t, model.ModeObserve, m.Mode)
	assert.False(t, m.PredictEnabled)
	assert.True(t, m.AutoRegistered)
	assert.Equal(t, "", m.Cluster, "the default cluster names none")
	assert.Equal(t, "app", m.Container)
	assert.Equal(t, int64(500), m.PodCPURequestMilli, "read from the Pod template")
	assert.Zero(t, m.MaxReplicas, "not guessed")
	assert.Zero(t, m.MinReplicas)
	assert.False(t, m.Bypass)
}

// reporting again changes nothing: the workload is one module, and an edited one is left as it is
func TestTheSameWorkloadIsNeverRegisteredTwiceNorChanged(t *testing.T) {
	f := newFixture(t)
	f.withModules(t)
	f.def.AutoRegister = true
	ctx := context.Background()
	b := clusteragent.RegistrationBatch{Workloads: []clusteragent.Candidate{web("shop", "api")}}
	_, err := f.svc.Register(ctx, f.def, b)
	require.NoError(t, err)
	m, _ := f.svc.Modules.ByName(ctx, "shop-api")
	m.MaxReplicas, m.Description = 9, "mine"
	require.NoError(t, f.svc.Modules.Update(ctx, m))
	res, err := f.svc.Register(ctx, f.def, b)
	require.NoError(t, err)
	assert.Empty(t, res.Registered)
	assert.Equal(t, 1, res.Existing)
	again, _ := f.svc.Modules.ByName(ctx, "shop-api")
	assert.Equal(t, 9, again.MaxReplicas)
	assert.Equal(t, "mine", again.Description)
}

// a module made by hand for the workload is left alone as well
func TestAWorkloadThatIsAlreadyAModuleByHandIsLeftAlone(t *testing.T) {
	f := newFixture(t)
	f.withModules(t)
	f.def.AutoRegister = true
	ctx := context.Background()
	require.NoError(t, f.svc.Modules.Create(ctx, &model.ForecastModule{Name: "hand", Enabled: true, Mode: model.ModeAuto, Namespace: "shop", WorkloadKind: "Deployment", Workload: "api", MaxReplicas: 5}))
	res, err := f.svc.Register(ctx, f.def, clusteragent.RegistrationBatch{Workloads: []clusteragent.Candidate{web("shop", "api")}})
	require.NoError(t, err)
	assert.Equal(t, 1, res.Existing)
	hand, _ := f.svc.Modules.ByName(ctx, "hand")
	assert.True(t, hand.Enabled)
	assert.False(t, hand.AutoRegistered)
	assert.Empty(t, hand.MissingToEnable(), "a module made by hand is not held to the list")
}

// a name that is taken gets the kind, then a number, never an error
func TestATakenNameGetsAnotherOne(t *testing.T) {
	f := newFixture(t)
	f.withModules(t)
	f.def.AutoRegister = true
	ctx := context.Background()
	require.NoError(t, f.svc.Modules.Create(ctx, &model.ForecastModule{Name: "shop-api", Namespace: "other", WorkloadKind: "Deployment", Workload: "x"}))
	res, err := f.svc.Register(ctx, f.def, clusteragent.RegistrationBatch{Workloads: []clusteragent.Candidate{web("shop", "api")}})
	require.NoError(t, err)
	assert.Equal(t, []string{"shop-api-deployment"}, res.Registered)
}

// the annotation copy-from takes the parameters of a module that is set up: the new one has them, and is still switched off
func TestCopyFromGivesTheParametersAndStaysSwitchedOff(t *testing.T) {
	f := newFixture(t)
	f.withModules(t)
	f.def.AutoRegister = true
	ctx := context.Background()
	require.NoError(t, f.db.Create(&model.MetricMapping{DatasourceID: 1, Name: "cpu", Metric: "cpu", Level: "workload", Query: `sum(rate(c{namespace="{{namespace}}",pod=~"{{workload}}-.*"}[{{range}}]))`, Unit: "cores", Enabled: true}).Error)
	ref := &model.ForecastModule{Name: "ref", Enabled: true, Mode: model.ModeObserve, Namespace: "ref", WorkloadKind: "Deployment", Workload: "ref", MinReplicas: 2, MaxReplicas: 12, ScaleUpMaxStep: 3, LookbackHours: 100}
	require.NoError(t, f.svc.Modules.Create(ctx, ref))
	require.NoError(t, f.svc.Modules.SaveSource(ctx, &model.MetricSource{ModuleID: ref.ID, Name: "cpu", Kind: "cpu", Resource: "cpu", MappingID: 1, Weight: 1, Stat: "p95", Basis: "forecast", Enabled: true, Scale: 1, Precision: 6, Aggregation: "avg", Missing: "interpolate"}))
	c := web("shop", "api")
	c.CopyFrom = "ref"
	res, err := f.svc.Register(ctx, f.def, clusteragent.RegistrationBatch{Workloads: []clusteragent.Candidate{c}})
	require.NoError(t, err)
	require.Equal(t, []string{"shop-api"}, res.Registered)
	m, err := f.svc.Modules.ByName(ctx, "shop-api")
	require.NoError(t, err)
	assert.Equal(t, 12, m.MaxReplicas)
	assert.Equal(t, 2, m.MinReplicas)
	assert.Equal(t, 3, m.ScaleUpMaxStep)
	assert.Equal(t, 100, m.LookbackHours)
	assert.False(t, m.Enabled, "copying does not switch it on")
	assert.True(t, m.AutoRegistered)
	assert.Equal(t, "shop", m.Namespace, "who it is is not copied")
	assert.Equal(t, int64(500), m.PodCPURequestMilli, "what the operator read is not copied over")
	full, _ := f.svc.Modules.Get(ctx, m.ID)
	require.Len(t, full.Sources, 1)
	assert.Empty(t, full.MissingToEnable(), "with the bounds and a CPU source it may be switched on")
}

// a source whose query names a workload would read the other module's data: it is not copied, and the copy says so
func TestASourceWrittenForOneWorkloadIsNotCopied(t *testing.T) {
	f := newFixture(t)
	f.withModules(t)
	ctx := context.Background()
	require.NoError(t, f.db.Create(&model.MetricMapping{DatasourceID: 1, Name: "fixed", Metric: "cpu", Level: "workload", Query: `sum(rate(c{pod=~"ref-.*"}[5m]))`, Unit: "cores", Enabled: true}).Error)
	ref := &model.ForecastModule{Name: "ref", Namespace: "ref", WorkloadKind: "Deployment", Workload: "ref"}
	require.NoError(t, f.svc.Modules.Create(ctx, ref))
	require.NoError(t, f.svc.Modules.SaveSource(ctx, &model.MetricSource{ModuleID: ref.ID, Name: "cpu", Kind: "cpu", Resource: "cpu", MappingID: 1, Weight: 1, Stat: "p95", Basis: "forecast", Enabled: true, Scale: 1, Precision: 6, Aggregation: "avg", Missing: "interpolate"}))
	to := &model.ForecastModule{Name: "to", Namespace: "to", WorkloadKind: "Deployment", Workload: "to"}
	require.NoError(t, f.svc.Modules.Create(ctx, to))
	res, err := f.svc.Copier.Copy(ctx, ref.ID, to.ID, []string{modulecopy.GroupSources})
	require.NoError(t, err)
	require.Len(t, res.Skipped, 1)
	assert.Contains(t, res.Skipped[0].Reason, "names a workload")
	full, _ := f.svc.Modules.Get(ctx, to.ID)
	assert.Empty(t, full.Sources)
}

// a module that is switched on is not changed under its own feet
func TestCopyRefusesAModuleThatIsSwitchedOn(t *testing.T) {
	f := newFixture(t)
	f.withModules(t)
	ctx := context.Background()
	a := &model.ForecastModule{Name: "a", Namespace: "a", WorkloadKind: "Deployment", Workload: "a"}
	b := &model.ForecastModule{Name: "b", Enabled: true, Namespace: "b", WorkloadKind: "Deployment", Workload: "b"}
	require.NoError(t, f.svc.Modules.Create(ctx, a))
	require.NoError(t, f.svc.Modules.Create(ctx, b))
	_, err := f.svc.Copier.Copy(ctx, a.ID, b.ID, nil)
	require.ErrorIs(t, err, modulecopy.ErrTargetEnabled)
	_, err = f.svc.Copier.Copy(ctx, a.ID, a.ID, nil)
	require.ErrorIs(t, err, modulecopy.ErrSame)
	_, err = f.svc.Copier.Copy(ctx, a.ID, 0, []string{"nonsense"})
	require.Error(t, err)
}

// a bad report is refused whole
func TestAReportWithABadKindIsRefused(t *testing.T) {
	f := newFixture(t)
	f.withModules(t)
	f.def.AutoRegister = true
	_, err := f.svc.Register(context.Background(), f.def, clusteragent.RegistrationBatch{Workloads: []clusteragent.Candidate{{Namespace: "a", Kind: "Pod", Name: "b"}}})
	require.ErrorIs(t, err, clusteragent.ErrInvalid)
}

// the switch is stored by an update and reaches the operator in its settings; a new cluster has it off
func TestTheSwitchIsStoredAndReachesTheOperator(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	c, err := f.svc.Create(ctx, Input{Name: "east", Enabled: true})
	require.NoError(t, err)
	assert.False(t, c.AutoRegister, "off until somebody says so")
	assert.False(t, f.svc.Settings(ctx, c).AutoRegister)
	c, err = f.svc.Update(ctx, c.ID, Input{Name: "east", Enabled: true, AutoRegister: true})
	require.NoError(t, err)
	again, err := f.svc.DAO.Get(ctx, c.ID)
	require.NoError(t, err)
	assert.True(t, again.AutoRegister, "stored")
	assert.True(t, f.svc.Settings(ctx, again).AutoRegister)
	c, err = f.svc.Update(ctx, c.ID, Input{Name: "east", Enabled: true})
	require.NoError(t, err)
	again, _ = f.svc.DAO.Get(ctx, c.ID)
	assert.False(t, again.AutoRegister, "and turned off again")
}
