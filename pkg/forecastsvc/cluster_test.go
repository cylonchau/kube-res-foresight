package forecastsvc

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/cost"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/cylonchau/kube-res-foresight/pkg/promclient"
	"github.com/cylonchau/kube-res-foresight/pkg/resourcequery"
	"github.com/cylonchau/kube-res-foresight/pkg/timing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// answers is a Prometheus that answers an instant query by its text: what is not in the table has no series.
type answers struct {
	mu      sync.Mutex
	byQuery map[string][]promclient.VectorSample
	asked   []string
	fail    map[string]error
}

func (a *answers) QueryVector(_ context.Context, query string, _ time.Time) ([]promclient.VectorSample, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.asked = append(a.asked, query)
	if err := a.fail[query]; err != nil {
		return nil, err
	}
	return a.byQuery[query], nil
}

// clusterAnswers has the default queries answer a cluster of three nodes, two of them ready, with the node figures of each.
func clusterAnswers() *answers {
	set := resourcequery.Defaults()
	text := func(name string) string { return resourcequery.Expand(set[name], nil) }
	one := func(v float64) []promclient.VectorSample { return []promclient.VectorSample{{Value: v}} }
	node := func(name string, v float64) promclient.VectorSample {
		return promclient.VectorSample{Labels: map[string]string{"node": name}, Value: v}
	}
	gib := float64(costGiB)
	return &answers{byQuery: map[string][]promclient.VectorSample{
		text("cluster_nodes"):              one(3),
		text("cluster_nodes_ready"):        one(2),
		text("cluster_pods"):               one(41),
		text("cluster_allocatable_cpu"):    one(24),
		text("cluster_allocatable_memory"): one(96 * gib),
		text("cluster_requests_cpu"):       one(14.5),
		text("cluster_requests_memory"):    one(60 * gib),
		text("cluster_used_cpu"):           one(6.25),
		text("cluster_used_memory"):        one(33 * gib),
		text("node_cpu_usage"):             {node("n1", 3), node("n2", 2.5), node("n3", 0.75)},
		text("node_memory_usage"):          {node("n1", 12*gib), node("n2", 11*gib), node("n3", 10*gib)},
		text("node_cpu_requests"):          {node("n1", 6), node("n2", 5.5), node("n3", 3)},
		text("node_memory_requests"):       {node("n1", 24*gib), node("n2", 20*gib), node("n3", 16*gib)},
		text("node_cpu_allocatable"):       {node("n1", 8), node("n2", 8), node("n3", 8)},
		text("node_memory_allocatable"):    {node("n1", 32*gib), node("n2", 32*gib), node("n3", 32*gib)},
		text("node_ready"):                 {node("n1", 1), node("n2", 1), node("n3", 0)},
	}}
}

func clusterFixture(t *testing.T) (*fixture, *answers) {
	t.Helper()
	f, _ := costFixture(t)
	require.NoError(t, f.db.AutoMigrate(&model.SysConfig{}))
	fake := clusterAnswers()
	f.svc.NewCostQuerier = func(*model.Datasource) (cost.Querier, error) { return fake, nil }
	return f, fake
}

func TestTheClusterFiguresAreTheAnswersOfTheDataSourceToTheQueriesOfTheSharedSet(t *testing.T) {
	f, fake := clusterFixture(t)
	got, err := f.svc.ClusterCapacityOf(context.Background(), f.module.ID, 0)
	require.NoError(t, err)
	assert.Equal(t, 3, got.Nodes)
	assert.Equal(t, 2, got.NodesReady)
	assert.Equal(t, 41, got.Pods)
	assert.InDelta(t, 24.0, got.AllocatableCPU, 1e-9)
	assert.InDelta(t, 96*float64(costGiB), got.AllocatableMemory, 1)
	assert.InDelta(t, 14.5, got.RequestedCPU, 1e-9)
	assert.InDelta(t, 6.25, got.UsedCPU, 1e-9)
	assert.InDelta(t, 33*float64(costGiB), got.UsedMemory, 1)
	assert.Empty(t, got.Notes)
	assert.NotEmpty(t, got.Datasource.Name)

	names := map[string]string{}
	for _, q := range got.Queries {
		names[q.Name] = q.Query
	}
	assert.Len(t, got.Queries, 9, "every figure shows the query it was asked with")
	assert.Equal(t, resourcequery.Expand(resourcequery.Defaults()["cluster_used_cpu"], nil), names["cluster_used_cpu"])
	assert.Equal(t, "cluster_nodes", got.Queries[0].Name, "in the order of the set")
	assert.Len(t, fake.asked, 9)

	_, err = f.svc.ClusterCapacityOf(context.Background(), 9999, 0)
	assert.Error(t, err)
}

func TestAQueryTheSystemRewroteIsTheOneThatIsAskedAndTheOneThePageShows(t *testing.T) {
	f, fake := clusterFixture(t)
	mine := `sum(my_cpu_used_cores)`
	fake.byQuery[mine] = []promclient.VectorSample{{Value: 9.5}}
	require.NoError(t, f.db.Create(&model.SysConfig{ConfigKey: resourcequery.Key("cluster_used_cpu"), ConfigValue: mine, ConfigName: "x"}).Error)
	got, err := f.svc.ClusterCapacityOf(context.Background(), f.module.ID, 0)
	require.NoError(t, err)
	assert.InDelta(t, 9.5, got.UsedCPU, 1e-9, "the figure is the answer to the system's own query")
	var shown string
	for _, q := range got.Queries {
		if q.Name == "cluster_used_cpu" {
			shown = q.Query
		}
	}
	assert.Equal(t, mine, shown)
	assert.Contains(t, fake.asked, mine)
	assert.NotContains(t, fake.asked, resourcequery.Expand(resourcequery.Defaults()["cluster_used_cpu"], nil), "the default is not asked when the system has its own")
}

func TestAFigureThatCannotBeReadLeavesTheOthersAndSaysWhy(t *testing.T) {
	f, fake := clusterFixture(t)
	set := resourcequery.Defaults()
	fake.fail = map[string]error{resourcequery.Expand(set["cluster_used_memory"], nil): errors.New("the query timed out")}
	delete(fake.byQuery, resourcequery.Expand(set["cluster_requests_cpu"], nil))
	got, err := f.svc.ClusterCapacityOf(context.Background(), f.module.ID, 0)
	require.NoError(t, err)
	assert.Equal(t, 3, got.Nodes, "the rest is there")
	assert.Zero(t, got.UsedMemory)
	assert.Zero(t, got.RequestedCPU)
	require.Len(t, got.Notes, 2)
	assert.Contains(t, got.Notes[0], "cluster_requests_cpu")
	assert.Contains(t, got.Notes[1], "cluster_used_memory: the query timed out")
}

func TestTheClusterDataSourceIsTheParameterWhenItIsSetAndTheOneOfAModuleWhenItIsNot(t *testing.T) {
	f, _ := clusterFixture(t)
	ctx := context.Background()
	auto, err := f.svc.ClusterDatasourceID(ctx, 0)
	require.NoError(t, err)
	assert.Equal(t, f.cpuMap.DatasourceID, auto)

	require.NoError(t, f.db.Create(&model.SysConfig{ConfigKey: timing.ClusterDatasource.Key, ConfigValue: "4242", ConfigName: "x"}).Error)
	set, err := f.svc.ClusterDatasourceID(ctx, 0)
	require.NoError(t, err)
	assert.EqualValues(t, 4242, set)
	_, err = f.svc.ClusterCapacityOf(ctx, 0, 0)
	assert.ErrorContains(t, err, "4242", "a data source that does not exist is said so")
}

func TestTheNodesAreEachGivenTheirFiguresFromTheSeriesWithTheirName(t *testing.T) {
	f, fake := clusterFixture(t)
	got, err := f.svc.NodeUsageOf(context.Background(), 0)
	require.NoError(t, err)
	require.Len(t, got.Nodes, 3)
	n1 := got.Nodes["n1"]
	require.NotNil(t, n1.CPUUsed)
	assert.InDelta(t, 3.0, *n1.CPUUsed, 1e-9)
	assert.InDelta(t, 12*float64(costGiB), *n1.MemoryUsed, 1)
	assert.InDelta(t, 6.0, *n1.CPURequested, 1e-9)
	assert.InDelta(t, 8.0, *n1.CPUAllocatable, 1e-9)
	require.NotNil(t, n1.Ready)
	assert.True(t, *n1.Ready)
	assert.False(t, *got.Nodes["n3"].Ready, "n3 is not ready")
	assert.Empty(t, got.Notes)
	assert.Len(t, got.Queries, 7)
	assert.Len(t, fake.asked, 7)
}

func TestANodeQueryThatGivesNoNodeLabelOrNoSeriesIsToldInsteadOfGuessed(t *testing.T) {
	f, fake := clusterFixture(t)
	set := resourcequery.Defaults()
	fake.byQuery[resourcequery.Expand(set["node_cpu_usage"], nil)] = []promclient.VectorSample{{Value: 5}} // one figure, no label
	delete(fake.byQuery, resourcequery.Expand(set["node_memory_usage"], nil))
	got, err := f.svc.NodeUsageOf(context.Background(), 0)
	require.NoError(t, err)
	require.Len(t, got.Notes, 2)
	assert.Contains(t, got.Notes[0], "node_cpu_usage: a series has no node label")
	assert.Contains(t, got.Notes[1], "node_memory_usage: the data source has no series for it")
	assert.Nil(t, got.Nodes["n1"].CPUUsed)
	assert.NotNil(t, got.Nodes["n1"].MemoryRequested, "the other figures are there")
}
