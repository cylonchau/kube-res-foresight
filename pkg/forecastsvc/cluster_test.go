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
	got, err := f.svc.ClusterCapacityOf(ctx, 0, 0)
	require.NoError(t, err, "a cluster that cannot be read is an answer with a reason, not an error")
	assert.Equal(t, ClusterUnreachable, got.Reason)
	assert.Contains(t, got.Message, "4242", "a data source that does not exist is said so")
}

func TestWithNoDataSourceTheClusterAnswersWithTheReasonAndWhatToSetUp(t *testing.T) {
	f, _ := clusterFixture(t)
	require.NoError(t, f.db.Model(&model.ForecastModule{}).Where("1 = 1").Update("enabled", false).Error)
	got, err := f.svc.ClusterCapacityOf(context.Background(), 0, 0)
	require.NoError(t, err)
	assert.Equal(t, ClusterNoDatasource, got.Reason)
	assert.Contains(t, got.Message, "Metric Templates")
	assert.Empty(t, got.Queries)
	assert.NotNil(t, got.Notes)
	nodes, err := f.svc.NodeUsageOf(context.Background(), 0)
	require.NoError(t, err)
	assert.Equal(t, ClusterNoDatasource, nodes.Reason)
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

// scopedMapping makes the mapping of a cluster or node figure on the data source of the fixture.
func scopedMapping(t *testing.T, f *fixture, name, kind, level, query string) *model.MetricMapping {
	t.Helper()
	m := &model.MetricMapping{DatasourceID: f.cpuMap.DatasourceID, Name: name, Metric: kind, Level: level, Query: query, StepSeconds: 60, Enabled: true}
	require.NoError(t, model.ValidateMetricMapping(m))
	require.NoError(t, f.svc.Mappings.Create(context.Background(), m))
	return m
}

func TestAMetricMappingOfTheDataSourceIsTheQueryOfTheFigureAndBeatsTheParameter(t *testing.T) {
	f, fake := clusterFixture(t)
	fromMapping := `sum(my_cluster_cpu_used{window="{{range}}"})`
	fromParameter := `sum(param_cpu_used)`
	fake.byQuery[`sum(my_cluster_cpu_used{window="5m"})`] = []promclient.VectorSample{{Value: 7.5}}
	fake.byQuery[fromParameter] = []promclient.VectorSample{{Value: 1}}
	require.NoError(t, f.db.Create(&model.SysConfig{ConfigKey: resourcequery.Key("cluster_used_cpu"), ConfigValue: fromParameter, ConfigName: "x"}).Error)
	mp := scopedMapping(t, f, "cluster cpu used", "cluster_cpu_used", "cluster", fromMapping)

	got, err := f.svc.ClusterCapacityOf(context.Background(), f.module.ID, 0)
	require.NoError(t, err)
	assert.InDelta(t, 7.5, got.UsedCPU, 1e-9, "the figure is the answer to the query of the mapping, {{range}} filled")
	assert.True(t, got.Configured)
	for _, q := range got.Queries {
		switch q.Name {
		case "cluster_used_cpu":
			assert.Equal(t, QueryFromMapping, q.Source)
			assert.Equal(t, "cluster cpu used", q.Mapping)
			assert.Equal(t, mp.ID, q.MappingID)
			require.NotNil(t, q.Value)
			assert.InDelta(t, 7.5, *q.Value, 1e-9)
		case "cluster_used_memory":
			assert.Equal(t, QueryFromDefault, q.Source, "a figure with no mapping and no edit is the default")
		}
	}
	assert.NotContains(t, fake.asked, fromParameter, "the parameter is not asked when a mapping has the figure")
	assert.NotContains(t, got.Missing, "cluster_cpu_used")
	assert.Contains(t, got.Missing, "cluster_memory_used", "what has no mapping is listed, so the page can say what to create")
	assert.Len(t, got.Missing, 15, "the 9 of the cluster and the 7 of the nodes, less the one that has a mapping")
}

func TestTheNodeFiguresAreReadFromTheNodeMappingsToo(t *testing.T) {
	f, fake := clusterFixture(t)
	mine := `sum by (node) (my_node_cpu)`
	fake.byQuery[mine] = []promclient.VectorSample{{Labels: map[string]string{"node": "n1"}, Value: 4.5}}
	scopedMapping(t, f, "node cpu", "node_cpu_used", "node", mine)
	got, err := f.svc.NodeUsageOf(context.Background(), 0)
	require.NoError(t, err)
	require.NotNil(t, got.Nodes["n1"].CPUUsed)
	assert.InDelta(t, 4.5, *got.Nodes["n1"].CPUUsed, 1e-9)
	for _, q := range got.Queries {
		if q.Name == "node_cpu_usage" {
			assert.Equal(t, QueryFromMapping, q.Source)
		}
	}
}

func TestTheDataSourceWithTheClusterMappingsIsTheClusterDataSource(t *testing.T) {
	f, _ := clusterFixture(t)
	ctx := context.Background()
	scopedMapping(t, f, "nodes", "cluster_nodes", "cluster", `count(my_nodes)`)
	id, err := f.svc.ClusterDatasourceID(ctx, 0)
	require.NoError(t, err)
	assert.Equal(t, f.cpuMap.DatasourceID, id)
}

func TestAClusterFigureCannotBeAModuleSourceAndTheLevelsAreKept(t *testing.T) {
	src := &model.MetricSource{Name: "n", Kind: "cluster_nodes", MappingID: 1, Resource: "signal"}
	assert.ErrorContains(t, src.Validate(), "not of a module")
	wrongLevel := &model.MetricMapping{DatasourceID: 1, Name: "x", Metric: "cluster_nodes", Level: "workload", Query: `count(x{namespace="{{namespace}}",pod=~"{{workload}}"})`, Enabled: true}
	assert.ErrorContains(t, model.ValidateMetricMapping(wrongLevel), `level must be "cluster"`)
	notNode := &model.MetricMapping{DatasourceID: 1, Name: "x", Metric: "cpu", Level: "node", Query: `sum(x)`, Enabled: true}
	assert.ErrorContains(t, model.ValidateMetricMapping(notNode), "node level is for the metrics of the nodes")
}
