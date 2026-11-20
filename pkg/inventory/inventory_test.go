package inventory

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTheProviderIsTheSchemeOfTheProviderIDAndAnUnknownOneIsKeptAsItIs(t *testing.T) {
	cases := map[string]string{
		"aws:///us-east-1a/i-0abc":            "aws",
		"gce://my-project/us-central1-a/node": "gcp",
		"azure:///subscriptions/x/vm":         "azure",
		"aliyun://cn-hangzhou.i-bp1":          "alibaba",
		"alicloud://cn-hangzhou.i-bp1":        "alibaba",
		"tencentcloud:///ap-guangzhou-3/ins":  "tencent",
		"kind://docker/kind/kind-worker":      "kind",
		"someprovider://x":                    "someprovider",
		"":                                    "",
		"no-scheme":                           "",
	}
	for id, want := range cases {
		assert.Equal(t, want, ProviderOf(id), id)
	}
}

func TestQuantitiesAreWrittenTheWayAManifestWritesThem(t *testing.T) {
	assert.Equal(t, "500m", CPU(500))
	assert.Equal(t, "2", CPU(2000))
	assert.Equal(t, "1250m", CPU(1250))
	assert.Equal(t, "-", CPU(0))
	assert.Equal(t, "512Mi", Memory(512<<20))
	assert.Equal(t, "1.5Gi", Memory(3<<29))
	assert.Equal(t, "4Gi", Memory(4<<30))
	assert.Equal(t, "64Ki", Memory(64<<10))
	assert.Equal(t, "100", Memory(100))
	assert.Equal(t, "-", Memory(0))
}

func web(cpu, mem int64) Workload {
	return Workload{Module: "web", Desired: 3, Containers: []Container{
		{Name: "app", Requests: Resources{cpu, mem}, Limits: Resources{cpu * 2, mem * 2}},
		{Name: "sidecar", Requests: Resources{100, 64 << 20}},
	}, HPA: &HPA{Kind: "hpa", Name: "web", Min: 2, Max: 10, Current: 3, Desired: 3}}
}

func TestADifferenceBetweenTwoReadingsNamesWhatChangedAndNothingElse(t *testing.T) {
	assert.Empty(t, Diff(web(500, 512<<20), web(500, 512<<20)), "the same configuration")

	moved := web(500, 512<<20)
	moved.Pods = []Pod{{Name: "web-1", Restarts: 4}}
	moved.Ready = 2
	moved.HPA.Current, moved.HPA.Desired = 5, 6
	assert.Empty(t, Diff(web(500, 512<<20), moved), "Pods, the ready count and what the HPA does are not configuration")

	after := web(750, 1<<30)
	after.Desired = 4
	after.HPA.Max = 20
	changes := Diff(web(500, 512<<20), after)
	got := map[string][2]string{}
	for _, c := range changes {
		got[c.Path] = [2]string{c.From, c.To}
	}
	assert.Equal(t, [2]string{"3", "4"}, got["replicas"])
	assert.Equal(t, [2]string{"500m", "750m"}, got["containers/app/requests.cpu"])
	assert.Equal(t, [2]string{"512Mi", "1Gi"}, got["containers/app/requests.memory"])
	assert.Equal(t, [2]string{"1", "1500m"}, got["containers/app/limits.cpu"])
	assert.Equal(t, [2]string{"1Gi", "2Gi"}, got["containers/app/limits.memory"])
	assert.Equal(t, [2]string{"10", "20"}, got["hpa.max"])
	assert.NotContains(t, got, "containers/sidecar/requests.cpu")
	assert.Contains(t, Summary(changes), "containers/app/requests.cpu 500m → 750m")
}

func TestAContainerThatCameOrWentAndAnHPAThatCameOrWentAreChanges(t *testing.T) {
	before := web(500, 512<<20)
	after := web(500, 512<<20)
	after.Containers = append(after.Containers, Container{Name: "proxy", Requests: Resources{50, 32 << 20}})
	after.Containers = after.Containers[:1+1]
	after.Containers[1] = Container{Name: "proxy", Requests: Resources{50, 32 << 20}}
	after.HPA = nil
	changes := Diff(before, after)
	paths := map[string]bool{}
	for _, c := range changes {
		paths[c.Path] = true
	}
	assert.True(t, paths["containers/proxy"], "added")
	assert.True(t, paths["containers/sidecar"], "removed")
	assert.True(t, paths["hpa"], "the HPA went")
	assert.True(t, paths["hpa.min"])
}

func TestTheFingerprintMovesWhenTheConfigurationDoesAndOnlyThen(t *testing.T) {
	snap := Snapshot{Nodes: []Node{{Name: "n1", Ready: true, Capacity: Capacity{4000, 8 << 30, 110}}, {Name: "n2", Ready: true}}, Workloads: []Workload{web(500, 512<<20)}}
	base := Fingerprint(snap)
	reordered := Snapshot{Nodes: []Node{snap.Nodes[1], snap.Nodes[0]}, Workloads: snap.Workloads}
	assert.Equal(t, base, Fingerprint(reordered), "the order of the nodes is not a change")

	busy := Snapshot{Nodes: append([]Node(nil), snap.Nodes...), Workloads: []Workload{web(500, 512<<20)}}
	busy.Nodes[0].Pods, busy.Nodes[0].Requests = 7, Resources{3000, 1 << 30}
	busy.Workloads[0].Ready, busy.Workloads[0].Pods = 1, []Pod{{Name: "p", Restarts: 9}}
	busy.Workloads[0].HPA.Current = 9
	assert.Equal(t, base, Fingerprint(busy), "what moves all the time is not configuration")

	down := Snapshot{Nodes: append([]Node(nil), snap.Nodes...), Workloads: snap.Workloads}
	down.Nodes[1].Ready = false
	assert.NotEqual(t, base, Fingerprint(down), "a node that is not ready")
	resized := Snapshot{Nodes: snap.Nodes, Workloads: []Workload{web(600, 512<<20)}}
	assert.NotEqual(t, base, Fingerprint(resized), "a request")
	bounded := Snapshot{Nodes: snap.Nodes, Workloads: []Workload{web(500, 512<<20)}}
	bounded.Workloads[0].HPA = &HPA{Kind: "hpa", Name: "web", Min: 2, Max: 12}
	assert.NotEqual(t, base, Fingerprint(bounded), "an HPA bound")
}

func TestAReportThatIsTooBigOrNamesNothingIsRefused(t *testing.T) {
	require.NoError(t, Snapshot{RecordID: "r1"}.Validate())
	assert.Error(t, Snapshot{}.Validate(), "no record id")
	assert.Error(t, Snapshot{RecordID: "r", Nodes: make([]Node, MaxNodes+1)}.Validate())
	assert.Error(t, Snapshot{RecordID: "r", Workloads: make([]Workload, MaxWorkloads+1)}.Validate())
	assert.Error(t, Snapshot{RecordID: "r", Workloads: []Workload{{}}}.Validate(), "a workload with no module")
	assert.Error(t, Snapshot{RecordID: "r", Workloads: []Workload{{Module: "m", Pods: make([]Pod, MaxPods+1)}}}.Validate())
	assert.Error(t, Snapshot{RecordID: "r", Workloads: []Workload{{Module: "m", Containers: make([]Container, MaxContainers+1)}}}.Validate())
}
