package forecastsvc

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/clusteragent"
	"github.com/cylonchau/kube-res-foresight/pkg/clustersvc"
	"github.com/cylonchau/kube-res-foresight/pkg/promclient"
	"github.com/cylonchau/kube-res-foresight/pkg/resourcequery"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var usageAt = time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)

func TestTheUsageOfANodeIsTheSeriesWithItsNameAtTheTimeOfTheChange(t *testing.T) {
	f, _ := clusterFixture(t)
	v, name, err := ClusterUsage{Service: f.svc}.UsageAt(context.Background(), 0, clustersvc.UsageSubject{Kind: clusteragent.KindNode, Name: "n1"}, usageAt)
	require.NoError(t, err)
	assert.NotEmpty(t, name, "the data source it was asked of")
	require.NotNil(t, v.CPU)
	require.NotNil(t, v.Memory)
	assert.InDelta(t, 3.0, *v.CPU, 1e-9)
	assert.InDelta(t, 12*float64(costGiB), *v.Memory, 1)
	assert.Empty(t, v.Note)

	// a node with no series is a note and no figure; it is not asked again (no error)
	v, _, err = ClusterUsage{Service: f.svc}.UsageAt(context.Background(), 0, clustersvc.UsageSubject{Kind: clusteragent.KindNode, Name: "n9"}, usageAt)
	require.NoError(t, err)
	assert.Nil(t, v.CPU, "no series is not zero")
	assert.Nil(t, v.Memory)
	assert.Contains(t, v.Note, "has no series")
}

func TestTheNodeFiguresNeedTheirMetricTemplatesAndSayWhenTheyAreMissing(t *testing.T) {
	f, fake := clusterFixture(t, "node_cpu_used", "node_memory_used")
	v, _, err := ClusterUsage{Service: f.svc}.UsageAt(context.Background(), 0, clustersvc.UsageSubject{Kind: clusteragent.KindNode, Name: "n1"}, usageAt)
	require.NoError(t, err)
	assert.Nil(t, v.CPU)
	assert.Nil(t, v.Memory)
	assert.Contains(t, v.Note, "not declared")
	set := resourcequery.Defaults()
	assert.NotContains(t, fake.asked, resourcequery.Expand(set["node_cpu_usage"], nil), "a template that is not declared is not asked")
	assert.NotContains(t, fake.asked, resourcequery.Expand(set["node_memory_usage"], nil))
}

func TestTheUsageOfAPodAndOfAWorkloadComesFromTheSharedQueriesWithTheirNamespaceAndWorkload(t *testing.T) {
	f, fake := clusterFixture(t)
	set := resourcequery.Defaults()
	vars := map[string]string{"namespace": "shop", "workload": "web"}
	pod := func(name string, v float64) promclient.VectorSample {
		return promclient.VectorSample{Labels: map[string]string{"pod": name}, Value: v}
	}
	fake.byQuery[resourcequery.Expand(set["pod_cpu_usage"], vars)] = []promclient.VectorSample{pod("web-1", 0.4), pod("web-2", 0.6)}
	fake.byQuery[resourcequery.Expand(set["pod_memory_usage"], vars)] = []promclient.VectorSample{pod("web-2", 256<<20)}
	fake.byQuery[resourcequery.Expand(set["workload_cpu_usage"], vars)] = []promclient.VectorSample{{Value: 1.0}}

	u := ClusterUsage{Service: f.svc}
	v, _, err := u.UsageAt(context.Background(), 0, clustersvc.UsageSubject{Kind: clusteragent.KindPod, Namespace: "shop", Name: "web-2", Workload: "web"}, usageAt)
	require.NoError(t, err)
	require.NotNil(t, v.CPU)
	assert.InDelta(t, 0.6, *v.CPU, 1e-9, "the series of this Pod, not another of the workload")
	require.NotNil(t, v.Memory)
	assert.InDelta(t, float64(256<<20), *v.Memory, 1)

	v, _, err = u.UsageAt(context.Background(), 0, clustersvc.UsageSubject{Kind: clusteragent.KindPod, Namespace: "shop", Name: "web-1", Workload: "web"}, usageAt)
	require.NoError(t, err)
	assert.InDelta(t, 0.4, *v.CPU, 1e-9)
	assert.Nil(t, v.Memory, "web-1 has no memory series: it is missing, not zero")
	assert.Contains(t, v.Note, "pod_memory_usage")

	v, _, err = u.UsageAt(context.Background(), 0, clustersvc.UsageSubject{Kind: clusteragent.KindWorkload, Namespace: "shop", Name: "web", Workload: "web"}, usageAt)
	require.NoError(t, err)
	require.NotNil(t, v.CPU)
	assert.InDelta(t, 1.0, *v.CPU, 1e-9)
	assert.Nil(t, v.Memory)
}

func TestAQueryThatFailsIsAnErrorSoTheUsageIsAskedAgainAndNoDataSourceIsNot(t *testing.T) {
	f, fake := clusterFixture(t)
	set := resourcequery.Defaults()
	fake.fail = map[string]error{
		resourcequery.Expand(set["node_cpu_usage"], nil):    errors.New("the query timed out"),
		resourcequery.Expand(set["node_memory_usage"], nil): errors.New("the query timed out"),
	}
	_, _, err := ClusterUsage{Service: f.svc}.UsageAt(context.Background(), 0, clustersvc.UsageSubject{Kind: clusteragent.KindNode, Name: "n1"}, usageAt)
	require.Error(t, err)
	assert.NotErrorIs(t, err, clustersvc.ErrNoUsageSource)
	assert.Contains(t, err.Error(), "timed out")

	_, _, err = ClusterUsage{Service: f.svc}.UsageAt(context.Background(), 4242, clustersvc.UsageSubject{Kind: clusteragent.KindNode, Name: "n1"}, usageAt)
	assert.ErrorIs(t, err, clustersvc.ErrNoUsageSource, "a data source that does not exist")
	_, _, err = ClusterUsage{Service: f.svc}.UsageAt(context.Background(), 0, clustersvc.UsageSubject{Kind: "Secret", Name: "x"}, usageAt)
	assert.ErrorIs(t, err, clustersvc.ErrNoUsageSource)
}
