package inventorysvc

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/dao/rdb"
	"github.com/cylonchau/kube-res-foresight/pkg/inventory"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/cylonchau/kube-res-foresight/pkg/timing"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var seq int32

var t0 = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

type fixture struct {
	svc   *Service
	db    *gorm.DB
	clock *time.Time
	web   *model.ForecastModule
	api   *model.ForecastModule
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:inv%d?mode=memory&cache=shared", atomic.AddInt32(&seq, 1))), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.ForecastModule{}, &model.SysConfig{}, &model.InventoryReport{}, &model.ClusterNodeSnapshot{}, &model.WorkloadConfig{},
		&model.WorkloadConfigChange{}, &model.WorkloadSnapshot{}, &model.MetricSource{}))
	clock := t0
	mk := func(name string, enabled bool) *model.ForecastModule {
		m := &model.ForecastModule{Name: name, Namespace: "prod", Workload: name, WorkloadKind: "Deployment", Enabled: enabled, Mode: model.ModeObserve, Timezone: "UTC",
			LookbackHours: 72, HorizonMinutes: 15, MaxReplicas: 10, MinReplicas: 1, HeadroomRatio: 1.1, PodCapacityRPS: 10, TTLSeconds: 900, MaxFillSteps: 3}
		require.NoError(t, m.Validate())
		require.NoError(t, db.Create(m).Error)
		return m
	}
	f := &fixture{db: db, clock: &clock, web: mk("web", true), api: mk("api", true)}
	mk("off", false)
	f.svc = &Service{DAO: rdb.NewInventoryDAO(db), Modules: rdb.NewModuleDAO(db), Config: rdb.NewSysConfigDAO(db), Now: func() time.Time { return clock }}
	return f
}

func workload(module string, cpu int64, desired int32) inventory.Workload {
	return inventory.Workload{Module: module, Namespace: "prod", Kind: "Deployment", Name: module, Desired: desired, Ready: desired, Available: desired, Updated: desired,
		Containers: []inventory.Container{{Name: "app", Requests: inventory.Resources{CPUMilli: cpu, MemoryBytes: 512 << 20}, Limits: inventory.Resources{CPUMilli: cpu * 2, MemoryBytes: 1 << 30}}},
		Pods:       []inventory.Pod{{Name: module + "-1", Node: "n1", Phase: "Running", Ready: true, Restarts: 1}, {Name: module + "-0", Node: "n2", Phase: "Running", Ready: true}}}
}

func nodes(ready bool) []inventory.Node {
	return []inventory.Node{
		{Name: "n1", Ready: true, Provider: "aws", Region: "us-east-1", Zone: "us-east-1a", InstanceType: "m5.xlarge", KubeletVersion: "v1.30.2",
			Capacity: inventory.Capacity{CPUMilli: 4000, MemoryBytes: 16 << 30, Pods: 110}, Allocatable: inventory.Capacity{CPUMilli: 3900, MemoryBytes: 15 << 30, Pods: 110},
			Pods: 2, Requests: inventory.Resources{CPUMilli: 1000, MemoryBytes: 1 << 30}},
		{Name: "n2", Ready: ready, Unschedulable: !ready, Pressure: []string{"MemoryPressure"}, Provider: "aws", Region: "us-east-1", Zone: "us-east-1b", InstanceType: "m5.xlarge",
			Allocatable: inventory.Capacity{CPUMilli: 3900, MemoryBytes: 15 << 30, Pods: 110}},
	}
}

func (f *fixture) report(t *testing.T, id string, at time.Time, n []inventory.Node, w ...inventory.Workload) *IngestResult {
	t.Helper()
	res, err := f.svc.Ingest(context.Background(), inventory.Snapshot{RecordID: id, Operator: "op-1", ObservedAt: at, Nodes: n, Workloads: w})
	require.NoError(t, err)
	return res
}

func TestAReportIsStoredOnceHoweverOftenItIsSent(t *testing.T) {
	f := newFixture(t)
	first := f.report(t, "r1", t0, nodes(true), workload("web", 500, 3))
	assert.False(t, first.Duplicate)
	assert.Equal(t, 2, first.Nodes)
	assert.Equal(t, 1, first.Workloads)
	again := f.report(t, "r1", t0, nodes(true), workload("web", 500, 3))
	assert.True(t, again.Duplicate, "the same record id is a repeat: the answer was lost, or the operator started again")
	var n int64
	require.NoError(t, f.db.Model(&model.ClusterNodeSnapshot{}).Count(&n).Error)
	assert.EqualValues(t, 2, n)
	require.NoError(t, f.db.Model(&model.WorkloadSnapshot{}).Count(&n).Error)
	assert.EqualValues(t, 1, n)
}

func TestTheNodesOfTheNewestReportAreTheClusterWithTheirSumsAndWhereTheyRun(t *testing.T) {
	f := newFixture(t)
	require.NoError(t, f.db.Create(&model.SysConfig{ConfigKey: KeyClusterName, ConfigValue: "prod-east", ConfigName: "x"}).Error)
	view, err := f.svc.Nodes(context.Background())
	require.NoError(t, err)
	assert.False(t, view.Reported, "nothing has been reported yet")
	assert.Equal(t, "prod-east", view.Cluster)
	assert.Empty(t, view.Nodes)

	f.report(t, "r1", t0.Add(-time.Hour), nodes(true))
	*f.clock = t0.Add(time.Hour)
	f.report(t, "r2", *f.clock, nodes(false))
	view, err = f.svc.Nodes(context.Background())
	require.NoError(t, err)
	assert.True(t, view.Reported)
	assert.False(t, view.Stale)
	require.Len(t, view.Nodes, 2, "only the newest report is the cluster")
	assert.Equal(t, 2, view.Totals.Nodes)
	assert.Equal(t, 1, view.Totals.Ready, "n2 is not ready in the newest report")
	assert.Equal(t, 1, view.Totals.Unschedulable)
	assert.EqualValues(t, 7800, view.Totals.AllocCPUMilli)
	assert.EqualValues(t, 2, view.Totals.ModulePods)
	assert.EqualValues(t, 1000, view.Totals.ReqCPUMilli)
	assert.Equal(t, map[string]int{"aws": 2}, view.Providers)
	assert.Equal(t, map[string]int{"us-east-1a": 1, "us-east-1b": 1}, view.Zones)
	assert.Equal(t, []string{"MemoryPressure"}, view.Nodes[1].PressureList)
	assert.Equal(t, "prod-east", view.Nodes[0].Cluster)

	*f.clock = f.clock.Add(3 * time.Hour)
	view, _ = f.svc.Nodes(context.Background())
	assert.True(t, view.Stale, "the operator has not reported for more than two hours")
}

func TestTheNumberOfReadyNodesIsTheTrendOfTheReports(t *testing.T) {
	f := newFixture(t)
	f.report(t, "r1", t0.Add(-3*time.Hour), nodes(true))
	f.report(t, "r2", t0.Add(-2*time.Hour), nodes(false))
	f.report(t, "r3", t0.Add(-time.Hour), nodes(true))
	*f.clock = t0
	trend, err := f.svc.NodeTrend(context.Background(), 2)
	require.NoError(t, err)
	require.Len(t, trend, 2, "the reports of the last two hours")
	assert.Equal(t, 1, trend[0].Ready)
	assert.Equal(t, 2, trend[0].Total)
	assert.Equal(t, 2, trend[1].Ready)
	trend, _ = f.svc.NodeTrend(context.Background(), 24)
	assert.Len(t, trend, 3)
	assert.True(t, trend[0].ObservedAt.Before(trend[2].ObservedAt), "oldest first")
}

func TestAWorkloadIsKeptAsItIsAndAChangeOfItsConfigurationIsAHistory(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	view, err := f.svc.Workload(ctx, f.web.ID, 24)
	require.NoError(t, err)
	assert.False(t, view.Reported)
	assert.Empty(t, view.Containers)

	r := f.report(t, "r1", t0.Add(-3*time.Hour), nil, workload("web", 500, 3))
	assert.Equal(t, 1, r.Changes, "the first reading is the first entry of the history")
	r = f.report(t, "r2", t0.Add(-2*time.Hour), nil, workload("web", 500, 3))
	assert.Zero(t, r.Changes, "nothing changed: no entry")
	moved := workload("web", 500, 3)
	moved.Ready, moved.Pods[0].Restarts = 1, 9
	assert.Zero(t, f.report(t, "r3", t0.Add(-90*time.Minute), nil, moved).Changes, "Pods and the ready count are not configuration")
	r = f.report(t, "r4", t0.Add(-time.Hour), nil, workload("web", 750, 4))
	assert.Equal(t, 1, r.Changes)

	*f.clock = t0
	view, err = f.svc.Workload(ctx, f.web.ID, 24)
	require.NoError(t, err)
	assert.True(t, view.Reported)
	assert.Equal(t, 4, view.Desired)
	require.Len(t, view.Containers, 1)
	assert.EqualValues(t, 750, view.Containers[0].Requests.CPUMilli)
	require.Len(t, view.Pods, 2)
	assert.Equal(t, "web-0", view.Pods[0].Name, "Pods by name")
	require.Len(t, view.Changes, 2)
	assert.Contains(t, view.Changes[0].Summary, "containers/app/requests.cpu 500m → 750m", "the newest first")
	assert.Contains(t, view.Changes[0].Summary, "replicas 3 → 4")
	assert.Equal(t, "workload", view.Changes[1].Changes[0].Path)
	assert.Len(t, view.Trend, 4, "the replicas of every report")
	assert.Equal(t, 4, view.Trend[3].Desired)
	assert.Equal(t, 1, view.Trend[0].Restarts, "the restarts are kept for each report")
	assert.Equal(t, 9, view.Trend[2].Restarts)
}

func TestTheHPAOfAWorkloadIsReportedAndItsBoundsAreConfiguration(t *testing.T) {
	f := newFixture(t)
	w := workload("web", 500, 3)
	w.HPA = &inventory.HPA{Kind: "hpa", Name: "web", Min: 2, Max: 10, Current: 3, Desired: 5, Behavior: "scaleDown stabilization 300s"}
	f.report(t, "r1", t0.Add(-2*time.Hour), nil, w)
	w.HPA = &inventory.HPA{Kind: "hpa", Name: "web", Min: 2, Max: 10, Current: 5, Desired: 5}
	assert.Zero(t, f.report(t, "r2", t0.Add(-time.Hour), nil, w).Changes, "what the HPA does is not its configuration")
	w.HPA = &inventory.HPA{Kind: "hpa", Name: "web", Min: 3, Max: 10, Current: 5, Desired: 5}
	assert.Equal(t, 1, f.report(t, "r3", t0, nil, w).Changes)
	view, err := f.svc.Workload(context.Background(), f.web.ID, 24)
	require.NoError(t, err)
	require.NotNil(t, view.HPA)
	assert.EqualValues(t, 3, view.HPA.Min)
	assert.Equal(t, "hpa.min 2 → 3", view.Changes[0].Summary)
	assert.Equal(t, 5, view.Trend[1].HPACurrent)
	assert.Equal(t, "hpa", view.Trend[0].HPAKind)
}

func TestAWorkloadThatIsNotInTheClusterIsSaidSoAndComingBackIsAChange(t *testing.T) {
	f := newFixture(t)
	f.report(t, "r1", t0.Add(-3*time.Hour), nil, workload("web", 500, 3))
	gone := inventory.Workload{Module: "web", Namespace: "prod", Kind: "Deployment", Name: "web", Missing: true}
	f.report(t, "r2", t0.Add(-2*time.Hour), nil, gone)
	view, _ := f.svc.Workload(context.Background(), f.web.ID, 24)
	assert.True(t, view.Missing)
	assert.Equal(t, "workload present → missing", view.Changes[0].Summary)
	f.report(t, "r3", t0.Add(-time.Hour), nil, workload("web", 500, 3))
	view, _ = f.svc.Workload(context.Background(), f.web.ID, 24)
	assert.False(t, view.Missing)
	assert.Equal(t, "workload missing → present", view.Changes[0].Summary)

	denied := workload("web", 500, 3)
	denied.Error = "forbidden: cannot list pods"
	f.report(t, "r4", t0, nil, denied)
	view, _ = f.svc.Workload(context.Background(), f.web.ID, 24)
	assert.Equal(t, "forbidden: cannot list pods", view.Error)
}

func TestAReportThatArrivesLateDoesNotReplaceWhatIsNewerAndAModuleThatIsGoneIsSkipped(t *testing.T) {
	f := newFixture(t)
	f.report(t, "new", t0.Add(-time.Hour), nil, workload("web", 900, 5))
	late := f.report(t, "late", t0.Add(-5*time.Hour), nil, workload("web", 500, 3), workload("ghost", 100, 1))
	assert.Zero(t, late.Changes, "a late reading writes no change")
	assert.Equal(t, []string{"ghost"}, late.Unknown)
	view, _ := f.svc.Workload(context.Background(), f.web.ID, 24)
	assert.Equal(t, 5, view.Desired, "the newer reading stands")
	assert.EqualValues(t, 900, view.Containers[0].Requests.CPUMilli)
	assert.Len(t, view.Trend, 2, "the late reading is still a point of the trend")
}

func TestAReportFromTheFutureIsDatedNowAndOneFromBeforeTheSnapshotsAreKeptIsRefused(t *testing.T) {
	f := newFixture(t)
	*f.clock = t0
	f.report(t, "future", t0.Add(48*time.Hour), nodes(true))
	view, _ := f.svc.Nodes(context.Background())
	require.NotNil(t, view.At)
	assert.Equal(t, t0, view.At.UTC(), "the clock of the operator is not trusted into the future")
	_, err := f.svc.Ingest(context.Background(), inventory.Snapshot{RecordID: "old", ObservedAt: t0.AddDate(0, 0, -60), Nodes: nodes(true)})
	assert.ErrorIs(t, err, ErrTooOld)
	_, err = f.svc.Ingest(context.Background(), inventory.Snapshot{})
	assert.Error(t, err, "no record id")
}

func TestTheOperatorIsToldToReadEveryEnabledModuleWhateverItsMode(t *testing.T) {
	f := newFixture(t)
	targets, err := f.svc.Targets(context.Background())
	require.NoError(t, err)
	names := []string{}
	for _, tg := range targets {
		names = append(names, tg.Name)
		assert.Equal(t, "prod", tg.Namespace)
		assert.Equal(t, "Deployment", tg.Kind)
	}
	assert.ElementsMatch(t, []string{"web", "api"}, names, "a module that is only observed is read; a disabled one is not")
}

func TestSnapshotsPastTheirRetentionAreDeletedAndTheChangesLastLonger(t *testing.T) {
	f := newFixture(t)
	*f.clock = t0
	// an old state, written as the platform did at the time
	require.NoError(t, f.db.Create(&model.ClusterNodeSnapshot{ObservedAt: t0.AddDate(0, 0, -40), Node: "n1"}).Error)
	require.NoError(t, f.db.Create(&model.WorkloadSnapshot{ModuleID: f.web.ID, ObservedAt: t0.AddDate(0, 0, -40)}).Error)
	require.NoError(t, f.db.Create(&model.InventoryReport{RecordID: "old", ObservedAt: t0.AddDate(0, 0, -40), ReceivedAt: t0}).Error)
	require.NoError(t, f.db.Create(&model.WorkloadConfigChange{ModuleID: f.web.ID, ObservedAt: t0.AddDate(0, 0, -40), Kind: "Deployment", Name: "web"}).Error)
	require.NoError(t, f.db.Create(&model.WorkloadConfigChange{ModuleID: f.web.ID, ObservedAt: t0.AddDate(0, 0, -200), Kind: "Deployment", Name: "web"}).Error)
	f.report(t, "now", t0, nodes(true), workload("web", 500, 3))

	pruned, err := f.svc.Prune(context.Background())
	require.NoError(t, err)
	assert.EqualValues(t, 1, pruned.Nodes)
	assert.EqualValues(t, 1, pruned.Snapshots)
	assert.EqualValues(t, 1, pruned.Reports)
	assert.EqualValues(t, 1, pruned.Changes, "the change of 40 days ago stays (180 days), the one of 200 days ago goes")
	view, _ := f.svc.Nodes(context.Background())
	assert.Len(t, view.Nodes, 2, "the newest report is not touched")

	require.NoError(t, f.db.Create(&model.SysConfig{ConfigKey: timing.InventorySnapshotDays.Key, ConfigValue: "1", ConfigName: "x"}).Error)
	*f.clock = t0.AddDate(0, 0, 3)
	pruned, err = f.svc.Prune(context.Background())
	require.NoError(t, err)
	assert.EqualValues(t, 2, pruned.Nodes, "with one day the reports of three days ago are past it")
}

func TestTheTextParametersAreSeededWithTheirDefaults(t *testing.T) {
	keys := map[string]string{}
	for _, c := range DefaultConfigs() {
		keys[c.ConfigKey] = c.ConfigValue
		assert.NotEmpty(t, c.Remark, c.ConfigKey)
	}
	assert.Equal(t, DefaultCluster, keys[KeyClusterName])
	assert.Equal(t, DefaultGrafanaID, keys[KeyGrafanaUID])
	assert.Contains(t, keys, KeyGrafanaURL)
	assert.Contains(t, keys, KeyPlatformURL)
}
