package forecastsvc

import (
	"context"
	"testing"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/cost"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/cylonchau/kube-res-foresight/pkg/promclient"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const costGiB = int64(1 << 30)

// costCluster is a canned kube-state-metrics: the answers by query.
type costCluster struct {
	answers map[string][]promclient.VectorSample
	fail    error
}

func (c *costCluster) QueryVector(_ context.Context, query string, _ time.Time) ([]promclient.VectorSample, error) {
	if c.fail != nil {
		return nil, c.fail
	}
	return c.answers[query], nil
}

func vec(value float64, kv ...string) promclient.VectorSample {
	labels := map[string]string{}
	for i := 0; i+1 < len(kv); i += 2 {
		labels[kv[i]] = kv[i+1]
	}
	return promclient.VectorSample{Labels: labels, Value: value}
}

// nodes builds the node answers: each node has cores and GiB allocatable.
func nodesOf(spec map[string][2]float64) []promclient.VectorSample {
	var out []promclient.VectorSample
	for name, s := range spec {
		out = append(out, vec(s[0], "node", name, "resource", "cpu"), vec(s[1]*float64(costGiB), "node", name, "resource", "memory"), vec(110, "node", name, "resource", "pods"))
	}
	return out
}

// costFixture is a module "prod/api" of 4 replicas that request 4 cores and 4 GiB a Pod and use far less, with a recommendation that asks
// for 400m and 2 GiB, and a priced cluster of two nodes of 8 cores and 32 GiB at 1 a node-hour.
func costFixture(t *testing.T) (*fixture, *costCluster) {
	t.Helper()
	f := aheadFixture(t)
	require.NoError(t, f.db.AutoMigrate(&model.CostPriceRule{}))
	f.addAux(t, "memory")
	f.ensureSource(t, "cpu")
	f.ensureSource(t, "memory")
	// the totals of the 4 Pods: CPU swings between 1.2 and 2.8 cores (0.3-0.7 a Pod), memory between 3 and 5 GiB (0.75-1.25 GiB a Pod)
	f.querier.value = func(query string, ts time.Time) float64 {
		switch {
		case contains(query, "kube_deployment"):
			return 4
		case contains(query, "memory_series{"):
			return daily(4*float64(costGiB), 1*float64(costGiB), ts)
		case contains(query, "cpu{"):
			return daily(2, 0.8, ts)
		}
		return 300
	}
	f.setModule(t, func(m *model.ForecastModule) {
		m.PodCPURequestMilli, m.PodMemoryRequestBytes = 4000, 4*costGiB
	})
	f.recommendation(t, f.module, 2*time.Minute, func(r *model.Recommendation) {
		r.Version = 2000
		r.CPUSet, r.CPUMilli, r.CPUSource = true, 400, "algorithm"
		r.MemorySet, r.MemoryBytes, r.MemorySource = true, 2*costGiB, "algorithm"
		r.Replicas = 4
	})
	_, err := f.collect(context.Background())
	require.NoError(t, err)

	cluster := &costCluster{answers: map[string][]promclient.VectorSample{cost.QueryAllocatable: nodesOf(map[string][2]float64{"n1": {8, 32}, "n2": {8, 32}})}}
	f.svc.NewCostQuerier = func(*model.Datasource) (cost.Querier, error) { return cluster, nil }
	require.NoError(t, f.db.Create(&model.CostPriceRule{Name: "all nodes", MatchKind: cost.MatchAll, HourlyPrice: 1, Currency: "USD", Enabled: true}).Error)
	return f, cluster
}

func moduleOf(t *testing.T, got *CostModules, name string) *ModuleCost {
	t.Helper()
	for _, m := range got.Modules {
		if m.Name == name {
			return m
		}
	}
	require.FailNow(t, "module not in the answer: "+name)
	return nil
}

// A Pod that requests 4 cores and 4 GiB and uses a fraction of them has a surplus above what the platform recommends, priced at what the
// cluster pays for a core and a GiB.
func TestTheCostOfAWorkloadSurplusIsPricedAtWhatTheClusterPays(t *testing.T) {
	f, _ := costFixture(t)
	got, err := f.svc.CostModules(context.Background(), 3, f.cpuMap.DatasourceID)
	require.NoError(t, err)
	assert.Equal(t, "USD", got.Currency)
	assert.Empty(t, got.MoneyNote)
	assert.InDelta(t, 0.0625, got.CPUCoreHour, 1e-9, "2 node-hours at 1, half on 16 cores")
	assert.InDelta(t, 1.0/64, got.MemoryGiBHour, 1e-9)

	m := moduleOf(t, got, "api")
	assert.Equal(t, 4, m.Replicas)
	assert.False(t, m.External)

	cpu := m.CPU
	require.NotNil(t, cpu)
	assert.True(t, cpu.Enough)
	assert.InDelta(t, 4.0, cpu.Request, 1e-9)
	assert.InDelta(t, 0.4, cpu.Safe, 1e-9)
	assert.InDelta(t, 3.6, cpu.Surplus, 1e-9, "4 cores requested, 0.4 recommended")
	assert.InDelta(t, 0.7, cpu.Usage.Max, 0.01, "the Pods use 0.3 to 0.7 cores")
	assert.False(t, cpu.Idle)
	// 3.6 cores x 4 replicas x 0.0625 a core-hour x 730 hours
	assert.InDelta(t, 3.6*4*0.0625*730, cpu.SurplusMonthly, 1e-6)

	require.Len(t, cpu.Candidates, 3)
	prev := cpu.Safe
	for _, c := range cpu.Candidates {
		assert.GreaterOrEqual(t, c.Value, cpu.Safe, c.Level)
		assert.LessOrEqual(t, c.Value, cpu.Request, c.Level)
		assert.GreaterOrEqual(t, c.Value, prev, "the levels rise")
		assert.InDelta(t, (cpu.Request-c.Value)*4*0.0625*730, c.MonthlySaving, 1e-6, c.Level)
		prev = c.Value
	}
	assert.InDelta(t, 0.4, cpu.Candidates[0].Value, 1e-9, "aggressive is the safe value")
	assert.InDelta(t, 0.77, cpu.Candidates[1].Value, 0.02, "balanced is the P99 plus the 10 percent margin")

	mem := m.Memory
	require.NotNil(t, mem)
	assert.True(t, mem.Enough)
	assert.InDelta(t, float64(2*costGiB), mem.Surplus, 1, "4 GiB requested, 2 GiB recommended")
	assert.InDelta(t, 2.0*4*(1.0/64)*730, mem.SurplusMonthly, 1e-6)
}

// Without a price rule or a cluster the figures of the workload still stand; only the money is missing, and the answer says why.
func TestTheCostFiguresStandWithoutMoney(t *testing.T) {
	f, cluster := costFixture(t)
	ctx := context.Background()

	require.NoError(t, f.db.Where("1 = 1").Delete(&model.CostPriceRule{}).Error)
	got, err := f.svc.CostModules(ctx, 3, f.cpuMap.DatasourceID)
	require.NoError(t, err)
	assert.Equal(t, MoneyNoRules, got.MoneyNote)
	assert.Empty(t, got.Currency)
	m := moduleOf(t, got, "api")
	assert.InDelta(t, 3.6, m.CPU.Surplus, 1e-9)
	assert.Zero(t, m.CPU.SurplusMonthly, "no price, no money: not a free one")
	require.NotEmpty(t, m.CPU.Candidates)
	assert.Zero(t, m.CPU.Candidates[0].MonthlySaving)

	require.NoError(t, f.db.Create(&model.CostPriceRule{Name: "all", MatchKind: cost.MatchAll, HourlyPrice: 1, Currency: "USD", Enabled: true}).Error)
	cluster.fail = assert.AnError
	got, err = f.svc.CostModules(ctx, 3, f.cpuMap.DatasourceID)
	require.NoError(t, err)
	assert.Equal(t, MoneyNoCluster, got.MoneyNote, "an unreadable cluster costs the money, not the diagnosis")
	assert.InDelta(t, 3.6, moduleOf(t, got, "api").CPU.Surplus, 1e-9)

	cluster.fail = nil
	got, err = f.svc.CostModules(ctx, 3, 0)
	require.NoError(t, err)
	assert.Equal(t, MoneyNoCluster, got.MoneyNote, "no data source chosen: the money cannot be priced")
}

// The candidates need a recommendation to take the safe value from, a request to compare, and enough measured points.
func TestTheCandidatesNeedASafeValueARequestAndEvidence(t *testing.T) {
	f, _ := costFixture(t)
	ctx := context.Background()

	f.setModule(t, func(m *model.ForecastModule) { m.PodCPURequestMilli = 0 })
	got, err := f.svc.CostModules(ctx, 3, f.cpuMap.DatasourceID)
	require.NoError(t, err)
	cpu := moduleOf(t, got, "api").CPU
	assert.False(t, cpu.Enough)
	assert.Equal(t, cost.ReasonNoRequest, cpu.Note)
	assert.Empty(t, cpu.Candidates)
	assert.True(t, moduleOf(t, got, "api").Memory.Enough, "the memory request is known")

	// a recommendation that came from the fallback is not what the evidence supports
	f.setModule(t, func(m *model.ForecastModule) { m.PodCPURequestMilli = 4000 })
	require.NoError(t, f.db.Model(&model.Recommendation{}).Where("module_id = ?", f.module.ID).Update("cpu_source", "fallback").Error)
	got, err = f.svc.CostModules(ctx, 3, f.cpuMap.DatasourceID)
	require.NoError(t, err)
	cpu = moduleOf(t, got, "api").CPU
	assert.True(t, cpu.Enough)
	assert.Zero(t, cpu.Safe)
	assert.Zero(t, cpu.Surplus)
	assert.Equal(t, cost.ReasonNoSafeValue, cpu.CandidateNote)
	assert.Empty(t, cpu.Candidates)

	// a window with no samples at all says so
	require.NoError(t, f.db.Where("1 = 1").Delete(&model.MetricSample{}).Error)
	got, err = f.svc.CostModules(ctx, 3, f.cpuMap.DatasourceID)
	require.NoError(t, err)
	cpu = moduleOf(t, got, "api").CPU
	assert.False(t, cpu.Enough)
	assert.Equal(t, cost.ReasonNoUsage, cpu.Note)
}

// A target another scaler lately fought for is left alone: no candidate, whatever the surplus.
func TestAWorkloadAnotherScalerCompetesForGetsNoCandidate(t *testing.T) {
	f, _ := costFixture(t)
	require.NoError(t, f.db.Create(&model.ScalingExecution{ModuleID: f.module.ID, Namespace: "prod", Scaler: "api", TargetKind: "Deployment", TargetName: "api",
		Action: "None", Result: model.ExecConflict, Reason: "an HPA owns the replicas", CreatedAt: testNow.Add(-2 * time.Hour)}).Error)
	got, err := f.svc.CostModules(context.Background(), 3, f.cpuMap.DatasourceID)
	require.NoError(t, err)
	m := moduleOf(t, got, "api")
	assert.True(t, m.External)
	assert.Empty(t, m.CPU.Candidates)
	assert.Equal(t, cost.ReasonExternalScaler, m.CPU.CandidateNote)
	assert.InDelta(t, 3.6, m.CPU.Surplus, 1e-9, "the surplus is still told")

	// an old conflict is history
	require.NoError(t, f.db.Model(&model.ScalingExecution{}).Where("module_id = ?", f.module.ID).Update("created_at", testNow.Add(-3*24*time.Hour)).Error)
	got, err = f.svc.CostModules(context.Background(), 3, f.cpuMap.DatasourceID)
	require.NoError(t, err)
	assert.False(t, moduleOf(t, got, "api").External)
}

func TestTheCostOverviewSharesTheNodePricesAmongTheWorkloads(t *testing.T) {
	f, cluster := costFixture(t)
	ctx := context.Background()
	cluster.answers[cost.QueryPods] = []promclient.VectorSample{
		vec(1, "namespace", "prod", "pod", "api-6d4-a", "node", "n1"), vec(1, "namespace", "prod", "pod", "api-6d4-b", "node", "n2"),
	}
	cluster.answers[cost.QueryRequests] = []promclient.VectorSample{
		vec(4, "namespace", "prod", "pod", "api-6d4-a", "resource", "cpu"), vec(float64(4*costGiB), "namespace", "prod", "pod", "api-6d4-a", "resource", "memory"),
		vec(4, "namespace", "prod", "pod", "api-6d4-b", "resource", "cpu"), vec(float64(4*costGiB), "namespace", "prod", "pod", "api-6d4-b", "resource", "memory"),
	}
	cluster.answers[cost.QueryOwners] = []promclient.VectorSample{
		vec(1, "namespace", "prod", "pod", "api-6d4-a", "owner_kind", "ReplicaSet", "owner_name", "api-6d4"),
		vec(1, "namespace", "prod", "pod", "api-6d4-b", "owner_kind", "ReplicaSet", "owner_name", "api-6d4"),
	}
	got, err := f.svc.CostOverview(ctx, f.cpuMap.DatasourceID)
	require.NoError(t, err)
	assert.Equal(t, 2, got.Nodes)
	assert.Equal(t, 2, got.Pods)
	assert.Equal(t, 50, got.CPUSharePercent)
	require.Len(t, got.Currencies, 1)
	c := got.Currencies[0]
	assert.InDelta(t, 2.0, c.Hourly, 1e-9)
	assert.InDelta(t, 2.0, c.Allocated+c.Idle, 1e-9)
	require.Len(t, c.Workloads, 1)
	assert.Equal(t, "api", c.Workloads[0].Workload)
	assert.Equal(t, 2, c.Workloads[0].Pods)
	// each Pod: 4 of 8 cores at half of 1 = 0.25, 4 of 32 GiB at half of 1 = 0.0625
	assert.InDelta(t, 2*(0.25+0.0625), c.Workloads[0].Hourly, 1e-9)
	assert.NotEmpty(t, got.Notes, "the node labels were not exported in this cluster")

	// the share of the price put on CPU is a parameter
	f.setParameter(t, "cost.cpu_share_percent", "80")
	again, err := f.svc.CostOverview(ctx, f.cpuMap.DatasourceID)
	require.NoError(t, err)
	assert.Equal(t, 80, again.CPUSharePercent)
	// each Pod: 4 of 8 cores at 0.8 of 1 = 0.4, 4 of 32 GiB at 0.2 of 1 = 0.025
	assert.InDelta(t, 2*(0.4+0.025), again.Currencies[0].Workloads[0].Hourly, 1e-9)

	_, err = f.svc.CostOverview(ctx, 0)
	assert.ErrorIs(t, err, ErrCostNoDatasource)
	f.setParameter(t, "cost.datasource_id", "1")
	_, err = f.svc.CostOverview(ctx, 0)
	assert.NoError(t, err, "the data source of the parameter is used when none is asked for")
	cluster.fail = assert.AnError
	_, err = f.svc.CostOverview(ctx, 1)
	assert.ErrorIs(t, err, ErrCostCluster)
}

// With the candidates of a level the Pods ask for less, so more nodes can be emptied than as the requests are.
func TestTheSimulationEmptiesMoreNodesWithTheCandidates(t *testing.T) {
	f, cluster := costFixture(t)
	cluster.answers[cost.QueryAllocatable] = nodesOf(map[string][2]float64{"n1": {8, 32}, "n2": {8, 32}, "n3": {8, 32}})
	pod := func(name, node string) (promclient.VectorSample, []promclient.VectorSample, promclient.VectorSample) {
		return vec(1, "namespace", "prod", "pod", name, "node", node),
			[]promclient.VectorSample{vec(4, "namespace", "prod", "pod", name, "resource", "cpu"), vec(float64(4*costGiB), "namespace", "prod", "pod", name, "resource", "memory")},
			vec(1, "namespace", "prod", "pod", name, "owner_kind", "ReplicaSet", "owner_name", "api-6d4")
	}
	cluster.answers[cost.QueryPods], cluster.answers[cost.QueryRequests], cluster.answers[cost.QueryOwners] = nil, nil, nil
	for name, node := range map[string]string{"api-6d4-1": "n1", "api-6d4-2": "n1", "api-6d4-3": "n2", "api-6d4-4": "n3"} {
		info, reqs, owner := pod(name, node)
		cluster.answers[cost.QueryPods] = append(cluster.answers[cost.QueryPods], info)
		cluster.answers[cost.QueryRequests] = append(cluster.answers[cost.QueryRequests], reqs...)
		cluster.answers[cost.QueryOwners] = append(cluster.answers[cost.QueryOwners], owner)
	}

	got, err := f.svc.Simulate(context.Background(), f.cpuMap.DatasourceID, cost.LevelAggressive, 3)
	require.NoError(t, err)
	assert.Equal(t, 3, got.Now.NodesBefore)
	assert.Equal(t, 2, got.Now.NodesAfter, "as the Pods are, 16 cores of requests need two nodes of 8")
	assert.Len(t, got.Now.Released, 1)
	require.Len(t, got.Changes, 1)
	assert.Equal(t, int64(400), got.Changes[0].CPUMilli)
	assert.Equal(t, 2*costGiB, got.Changes[0].MemoryBytes)
	assert.Equal(t, 4, got.With.PodsChanged)
	assert.Equal(t, 1, got.With.NodesAfter, "at 0.4 cores and 2 GiB the four Pods fit one node")
	assert.Len(t, got.With.Released, 2)
	require.Len(t, got.With.Savings, 1)
	assert.InDelta(t, 2.0, got.With.Savings[0].Hourly, 1e-9)
	assert.Equal(t, "USD", got.With.Savings[0].Currency)
	assert.NotEmpty(t, got.With.Unmodeled)

	_, err = f.svc.Simulate(context.Background(), f.cpuMap.DatasourceID, "reckless", 3)
	assert.ErrorIs(t, err, ErrCostLevel)
}

// The simulation reads: it does not write a recommendation, a price rule or anything else of the platform.
func TestTheCostCallsWriteNothing(t *testing.T) {
	f, _ := costFixture(t)
	ctx := context.Background()
	before := f.counts(t)
	var rules int64
	require.NoError(t, f.db.Model(&model.CostPriceRule{}).Count(&rules).Error)

	_, err := f.svc.CostModules(ctx, 3, f.cpuMap.DatasourceID)
	require.NoError(t, err)
	_, err = f.svc.CostOverview(ctx, f.cpuMap.DatasourceID)
	require.NoError(t, err)
	_, err = f.svc.Simulate(ctx, f.cpuMap.DatasourceID, cost.LevelBalanced, 3)
	require.NoError(t, err)

	assert.Equal(t, before, f.counts(t))
	var after int64
	require.NoError(t, f.db.Model(&model.CostPriceRule{}).Count(&after).Error)
	assert.Equal(t, rules, after)
}
