package cost

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/promclient"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const gib = int64(1 << 30)

func node(name string, cpuMilli int64, memGiB int64) Node {
	return Node{Name: name, CPUMilli: cpuMilli, MemoryBytes: memGiB * gib, Pods: 110}
}

func pod(ns, name, kind, workload, nodeName string, cpuMilli int64, memGiB float64) Pod {
	return Pod{Namespace: ns, Name: name, Kind: kind, Workload: workload, Node: nodeName, CPUMilli: cpuMilli, MemoryBytes: int64(memGiB * float64(gib))}
}

func TestRulesAreValidatedAndTheMostSpecificOnePrices(t *testing.T) {
	good := Rule{Name: "all", MatchKind: MatchAll, HourlyPrice: 0.5, Currency: "usd"}
	good.Normalize()
	require.NoError(t, good.Validate())
	assert.Equal(t, "USD", good.Currency)

	for name, r := range map[string]Rule{
		"no name":         {MatchKind: MatchAll, Currency: "USD"},
		"label no key":    {Name: "x", MatchKind: MatchLabel, Currency: "USD"},
		"name no pattern": {Name: "x", MatchKind: MatchName, Currency: "USD"},
		"bad pattern":     {Name: "x", MatchKind: MatchName, NamePattern: "[", Currency: "USD"},
		"negative":        {Name: "x", MatchKind: MatchAll, HourlyPrice: -1, Currency: "USD"},
		"nan":             {Name: "x", MatchKind: MatchAll, HourlyPrice: math.NaN(), Currency: "USD"},
		"bad currency":    {Name: "x", MatchKind: MatchAll, Currency: "$"},
		"bad priority":    {Name: "x", MatchKind: MatchAll, Currency: "USD", Priority: MaxRulePriority + 1},
		"unknown kind":    {Name: "x", MatchKind: "regex", Currency: "USD"},
	} {
		r.Normalize()
		assert.Error(t, r.Validate(), name)
	}

	n := Node{Name: "gpu-1", Labels: map[string]string{"node_kubernetes_io_instance_type": "m5.xlarge"}}
	rules := []Rule{
		{ID: 1, Name: "default", MatchKind: MatchAll, HourlyPrice: 0.1, Currency: "USD"},
		{ID: 2, Name: "by label", MatchKind: MatchLabel, LabelKey: "node.kubernetes.io/instance-type", LabelValue: "m5.xlarge", HourlyPrice: 0.2, Currency: "USD"},
		{ID: 3, Name: "by name", MatchKind: MatchName, NamePattern: "gpu-*", HourlyPrice: 0.3, Currency: "USD"},
	}
	got, ok := PriceOf(n, rules)
	require.True(t, ok)
	assert.Equal(t, "by name", got.Name, "on equal priority the name rule is the most specific")

	rules[1].Priority = 10
	got, _ = PriceOf(n, rules)
	assert.Equal(t, "by label", got.Name, "a higher priority beats specificity; the real label key matches the sanitized exported one")

	_, ok = PriceOf(Node{Name: "other"}, []Rule{rules[1], rules[2]})
	assert.False(t, ok, "a node no rule matches is unpriced")
}

func TestAllocateSharesThePriceByRequestsAndTheIdleRestIsKept(t *testing.T) {
	// 1 USD an hour, 4 cores and 16 GiB, half of the price on CPU: a core-hour is 0.125, a GiB-hour 0.03125.
	snap := Snapshot{
		Nodes: []Node{node("a", 4000, 16)},
		Pods: []Pod{
			pod("shop", "web-1", "Deployment", "web", "a", 1000, 4),  // 0.125 + 0.125
			pod("shop", "web-2", "Deployment", "web", "a", 1000, 4),  // the same
			pod("kube-system", "dns", "DaemonSet", "dns", "a", 0, 0), // no request: costs nothing
		},
	}
	got := Allocate(snap, []Rule{{ID: 1, Name: "all", MatchKind: MatchAll, HourlyPrice: 1, Currency: "USD"}}, 0.5)
	require.Len(t, got.Currencies, 1)
	c := got.Currencies[0]
	assert.InDelta(t, 1.0, c.Hourly, 1e-9)
	assert.InDelta(t, 0.5, c.Allocated, 1e-9)
	assert.InDelta(t, 0.5, c.Idle, 1e-9)
	assert.InDelta(t, 0.125, c.CPUCoreHour, 1e-9)
	assert.InDelta(t, 1.0/32, c.MemoryGiBHour, 1e-9)
	assert.InDelta(t, 730.0, c.Monthly, 1e-9)
	require.NotEmpty(t, c.Workloads)
	assert.Equal(t, "web", c.Workloads[0].Workload)
	assert.Equal(t, 2, c.Workloads[0].Pods)
	assert.InDelta(t, 0.5, c.Workloads[0].Hourly, 1e-9)
}

func TestAllocateAddsUpToThePriceAndNeverCountsAnUnpricedNodeAsFree(t *testing.T) {
	snap := Snapshot{
		Nodes: []Node{node("a", 8000, 32), node("b", 4000, 8), node("c", 4000, 8), {Name: "empty"}},
		Pods: []Pod{
			pod("x", "p1", "Deployment", "x", "a", 3000, 10),
			pod("x", "p2", "Deployment", "x", "b", 1000, 1),
			pod("y", "p3", "StatefulSet", "y", "b", 2500, 6),
			pod("y", "p4", "StatefulSet", "y", "c", 500, 1),
			pod("z", "bare", "", "", "c", 100, 0.1),
			pod("z", "ghost", "Deployment", "z", "nowhere", 100, 1),
		},
	}
	rules := []Rule{
		{ID: 1, Name: "usd", MatchKind: MatchName, NamePattern: "[ab]", HourlyPrice: 0.8, Currency: "USD"},
		{ID: 2, Name: "cny", MatchKind: MatchName, NamePattern: "c", HourlyPrice: 3, Currency: "CNY"},
		{ID: 3, Name: "free", MatchKind: MatchName, NamePattern: "empty", HourlyPrice: 0, Currency: "USD"},
	}
	got := Allocate(snap, rules, 0.4)
	require.Len(t, got.Currencies, 2, "two currencies stay two blocks")
	for _, c := range got.Currencies {
		var nodes, workloads float64
		for _, n := range c.Nodes {
			nodes += n.Hourly
			assert.InDelta(t, n.Hourly, n.Allocated+n.Idle, 1e-9, "a node: allocated and idle add up to its price")
		}
		for _, w := range c.Workloads {
			workloads += w.Hourly
		}
		assert.InDelta(t, c.Hourly, nodes, 1e-9)
		assert.InDelta(t, c.Allocated, workloads, 1e-9, "the workload costs add up to what was allocated")
		assert.InDelta(t, c.Hourly, c.Allocated+c.Idle, 1e-9, c.Currency)
	}
	assert.Contains(t, got.Notes, NoteUnknownNode)

	none := Allocate(snap, nil, 0.4)
	assert.Empty(t, none.Currencies)
	assert.Len(t, none.Unpriced, 4, "with no price every node is unpriced, none is free")
	assert.Contains(t, none.Notes, NoteUnpricedNodes)
}

func TestAllocateScalesDownARequestedAboveWhatTheNodeCanGive(t *testing.T) {
	snap := Snapshot{Nodes: []Node{node("a", 2000, 4)}, Pods: []Pod{
		pod("x", "p1", "Deployment", "x", "a", 3000, 3),
		pod("x", "p2", "Deployment", "y", "a", 3000, 3),
	}}
	got := Allocate(snap, []Rule{{ID: 1, Name: "all", MatchKind: MatchAll, HourlyPrice: 2, Currency: "USD"}}, 0.5)
	c := got.Currencies[0]
	assert.InDelta(t, 2.0, c.Allocated, 1e-9)
	assert.InDelta(t, 0.0, c.Idle, 1e-9)
	assert.Equal(t, NoteOvercommitted, c.Nodes[0].Note)

	// a node with no allocatable figure cannot be shared out: all of it is idle
	empty := Allocate(Snapshot{Nodes: []Node{{Name: "a"}}}, []Rule{{ID: 1, Name: "all", MatchKind: MatchAll, HourlyPrice: 2, Currency: "USD"}}, 0.5)
	assert.InDelta(t, 2.0, empty.Currencies[0].Idle, 1e-9)
	assert.Equal(t, NoteNoAllocatable, empty.Currencies[0].Nodes[0].Note)
}

func TestUsageOfTakesNearestRankQuantilesAndDropsBadValues(t *testing.T) {
	values := make([]float64, 0, 110)
	for i := 1; i <= 100; i++ {
		values = append(values, float64(i))
	}
	values = append(values, math.NaN(), math.Inf(1), -3)
	u := UsageOf(values)
	assert.Equal(t, 100, u.Points)
	assert.Equal(t, 50.0, u.P50)
	assert.Equal(t, 95.0, u.P95)
	assert.Equal(t, 99.0, u.P99)
	assert.Equal(t, 100.0, u.Max)
	assert.Equal(t, Usage{}, UsageOf(nil))
}

func TestDiagnoseSaysNothingWithoutARequestOrEnoughSamples(t *testing.T) {
	u := Usage{Points: 100, P50: 0.2, P95: 0.5, P99: 0.6, Max: 0.9}

	w := Diagnose("cpu", 4, 1.5, u, 0.1, 50)
	assert.True(t, w.Enough)
	assert.InDelta(t, 0.125, w.Utilization, 1e-9)
	assert.InDelta(t, 2.5, w.Surplus, 1e-9, "the request is 2.5 cores above the safe value")
	assert.False(t, w.Idle)

	assert.True(t, Diagnose("cpu", 8, 1.5, u, 0.1, 50).Idle, "P99 of 0.6 is under 10% of 8")
	assert.Equal(t, ReasonNoRequest, Diagnose("cpu", 0, 1, u, 0.1, 50).Note)
	assert.Equal(t, ReasonTooFewSamples, Diagnose("cpu", 4, 1, Usage{Points: 10}, 0.1, 50).Note)
	noSafe := Diagnose("cpu", 4, 0, u, 0.1, 50)
	assert.True(t, noSafe.Enough)
	assert.Equal(t, ReasonNoSafeValue, noSafe.Note)
	assert.Zero(t, noSafe.Surplus, "no safe value, no surplus to name")
	assert.Zero(t, Diagnose("cpu", 1, 3, u, 0.1, 50).Surplus, "a request under the safe value has no surplus")
}

func TestCandidatesStayBetweenTheSafeValueAndWhatRunsNow(t *testing.T) {
	u := Usage{Points: 100, P99: 2000, Max: 2500}
	got, why := Candidates(4000, 1500, u, 0.1, 50, nil)
	require.Empty(t, why)
	require.Len(t, got, 3)
	assert.Equal(t, LevelAggressive, got[0].Level)
	assert.InDelta(t, 1500, got[0].Value, 1e-9)
	assert.InDelta(t, 2200, got[1].Value, 1e-9)
	assert.InDelta(t, 2750, got[2].Value, 1e-9)
	assert.InDelta(t, 2500, got[0].Saving, 1e-9)

	// a use above the current request cannot push a candidate above it
	high, _ := Candidates(2000, 1500, Usage{Points: 100, P99: 5000, Max: 9000}, 0.1, 50, nil)
	for _, c := range high {
		assert.LessOrEqual(t, c.Value, 2000.0)
		assert.GreaterOrEqual(t, c.Value, 1500.0)
	}

	// rounding up to a spec is applied and the range still holds
	round := func(v float64) float64 { return math.Ceil(v/500) * 500 }
	rounded, _ := Candidates(2200, 1300, u, 0.1, 50, round)
	require.Len(t, rounded, 3)
	assert.Equal(t, 1500.0, rounded[0].Value)
	assert.Equal(t, 2200.0, rounded[2].Value, "2750 rounds to 3000 and is brought back to the 2200 that runs")

	for _, tc := range []struct {
		current, safe float64
		u             Usage
		reason        string
	}{
		{0, 1500, u, ReasonNoRequest},
		{4000, 0, u, ReasonNoSafeValue},
		{4000, 1500, Usage{Points: 3}, ReasonTooFewSamples},
		{1000, 1500, u, ReasonBelowSafe},
		{1500, 1500, u, ReasonNotAboveSafe},
	} {
		none, why := Candidates(tc.current, tc.safe, tc.u, 0.1, 50, nil)
		assert.Nil(t, none)
		assert.Equal(t, tc.reason, why)
	}
}

func TestCandidatesNeverGoBelowTheSafeValueOrAboveTheCurrentOneWhateverTheInput(t *testing.T) {
	// a property over a grid: monotone from aggressive to conservative, and inside [safe, current]
	for current := 1000.0; current <= 8000; current += 700 {
		for safe := 200.0; safe < current; safe += 450 {
			for _, p99 := range []float64{0, 300, 1500, 9000} {
				for _, margin := range []float64{0, 0.1, 0.5} {
					u := Usage{Points: 100, P99: p99, Max: p99 * 1.3}
					got, why := Candidates(current, safe, u, margin, 50, func(v float64) float64 { return math.Ceil(v/100) * 100 })
					if why != "" {
						continue
					}
					prev := safe
					for _, c := range got {
						assert.GreaterOrEqual(t, c.Value, safe, "%v %v %v", current, safe, p99)
						assert.LessOrEqual(t, c.Value, current)
						assert.GreaterOrEqual(t, c.Value, prev, "the levels rise from aggressive to conservative")
						assert.InDelta(t, current-c.Value, c.Saving, 1e-9)
						prev = c.Value
					}
				}
			}
		}
	}
}

// a cluster of three nodes of 4 cores and 8 GiB: web has a Pod on each of two of them, a database on the third.
func smallCluster() Snapshot {
	return Snapshot{
		Nodes: []Node{node("a", 4000, 8), node("b", 4000, 8), node("c", 4000, 8)},
		Pods: []Pod{
			pod("shop", "web-1", "Deployment", "web", "a", 1000, 2),
			pod("shop", "web-2", "Deployment", "web", "b", 1000, 2),
			pod("shop", "db-0", "StatefulSet", "db", "c", 2000, 4),
		},
	}
}

func TestSimulateEmptiesTheNodesTheRestCanHold(t *testing.T) {
	snap := smallCluster()
	before := reflect.ValueOf(snap).Interface()
	rules := []Rule{{ID: 1, Name: "all", MatchKind: MatchAll, HourlyPrice: 0.5, Currency: "USD"}}

	got := Simulate(snap, nil, rules)
	assert.Equal(t, 3, got.NodesBefore)
	assert.Equal(t, 1, got.NodesAfter)
	assert.ElementsMatch(t, []string{"a", "b"}, got.Released)
	require.Len(t, got.Savings, 1)
	assert.Equal(t, 2, got.Savings[0].Nodes)
	assert.InDelta(t, 1.0, got.Savings[0].Hourly, 1e-9)
	assert.InDelta(t, 730.0, got.Savings[0].Monthly, 1e-9)
	assert.NotEmpty(t, got.Unmodeled)
	assert.Equal(t, before, reflect.ValueOf(snap).Interface(), "the snapshot is not changed")
	assert.Equal(t, got, Simulate(snap, nil, rules), "the same input gives the same answer")
}

func TestSimulateUsesTheNewRequestsOfTheChangesAndOnlyThose(t *testing.T) {
	snap := Snapshot{
		Nodes: []Node{node("a", 4000, 8), node("b", 4000, 8)},
		Pods: []Pod{
			pod("shop", "web-1", "Deployment", "web", "a", 3000, 2),
			pod("shop", "web-2", "Deployment", "web", "b", 3000, 2),
		},
	}
	none := Simulate(snap, nil, nil)
	assert.Empty(t, none.Released, "3 + 3 cores do not fit one node of 4")
	require.Len(t, none.Blocked, 2)
	assert.Equal(t, BlockedNoRoom, none.Blocked[0].Reason)

	lower := Simulate(snap, []Change{{Namespace: "shop", Kind: "Deployment", Workload: "web", CPUMilli: 1500, MemoryBytes: 2 * gib}}, nil)
	assert.Equal(t, 2, lower.PodsChanged)
	assert.Len(t, lower.Released, 1, "with 1.5 cores each both fit one node")
	assert.Equal(t, 1, lower.UnpricedReleased, "no price: the node is released but no money is claimed")
	assert.Empty(t, lower.Savings)

	other := Simulate(snap, []Change{{Namespace: "shop", Kind: "Deployment", Workload: "nope", CPUMilli: 1}}, nil)
	assert.Equal(t, 0, other.PodsChanged)
}

func TestSimulateKeepsANodeItCannotEmptyAndSaysWhy(t *testing.T) {
	t.Run("a Pod nobody controls is lost, not moved", func(t *testing.T) {
		snap := smallCluster()
		snap.Pods = append(snap.Pods, pod("ops", "debug", "", "", "a", 10, 0.1))
		got := Simulate(snap, nil, nil)
		var reason string
		for _, b := range got.Blocked {
			if b.Node == "a" {
				reason = b.Reason
				assert.Equal(t, "ops/debug", b.Pod)
			}
		}
		assert.Equal(t, BlockedUncontrolled, reason)
		assert.NotContains(t, got.Released, "a")
	})

	t.Run("a tainted node takes only the Pods of a node with the same taints", func(t *testing.T) {
		snap := Snapshot{
			Nodes: []Node{
				node("plain", 4000, 8),
				{Name: "gpu", CPUMilli: 4000, MemoryBytes: 8 * gib, Pods: 110, Taints: []Taint{{Key: "gpu", Value: "true", Effect: "NoSchedule"}}},
				node("tiny", 1000, 2),
			},
			Pods: []Pod{
				pod("ml", "train-1", "Deployment", "train", "tiny", 500, 1),
				pod("app", "web-1", "Deployment", "web", "plain", 3800, 6),
			},
		}
		got := Simulate(snap, nil, nil)
		// the only room left is on the tainted node, where the Pod of tiny may not go (it may not tolerate the taint): tiny stays
		assert.NotContains(t, got.Released, "tiny")
		assert.Contains(t, got.Released, "gpu", "the tainted node is empty")
	})

	t.Run("DaemonSet Pods leave with their node and do not block it", func(t *testing.T) {
		snap := Snapshot{
			Nodes: []Node{node("a", 4000, 8), node("b", 4000, 8)},
			Pods: []Pod{
				pod("kube-system", "agent-a", "DaemonSet", "agent", "a", 200, 0.5),
				pod("kube-system", "agent-b", "DaemonSet", "agent", "b", 200, 0.5),
				pod("shop", "web-1", "Deployment", "web", "a", 1000, 1),
			},
		}
		snap.Pods[0].DaemonSet, snap.Pods[1].DaemonSet = true, true
		got := Simulate(snap, nil, nil)
		assert.Len(t, got.Released, 1)
		assert.Equal(t, 1, got.NodesAfter)
	})

	t.Run("a Pod limit keeps a node full of small Pods", func(t *testing.T) {
		a := Node{Name: "a", CPUMilli: 8000, MemoryBytes: 16 * gib, Pods: 2}
		b := Node{Name: "b", CPUMilli: 8000, MemoryBytes: 16 * gib, Pods: 2}
		snap := Snapshot{Nodes: []Node{a, b}, Pods: []Pod{
			pod("x", "p1", "Deployment", "x", "a", 1, 0), pod("x", "p2", "Deployment", "x", "a", 1, 0),
			pod("x", "p3", "Deployment", "x", "b", 1, 0), pod("x", "p4", "Deployment", "x", "b", 1, 0),
		}}
		assert.Empty(t, Simulate(snap, nil, nil).Released, "four Pods do not fit the two places of one node")
	})
}

type fakeQuerier map[string][]promclient.VectorSample

func (f fakeQuerier) QueryVector(_ context.Context, query string, _ time.Time) ([]promclient.VectorSample, error) {
	v, ok := f[query]
	if !ok {
		return nil, fmt.Errorf("unexpected query %s", query)
	}
	return v, nil
}

func sample(value float64, kv ...string) promclient.VectorSample {
	labels := map[string]string{}
	for i := 0; i+1 < len(kv); i += 2 {
		labels[kv[i]] = kv[i+1]
	}
	return promclient.VectorSample{Labels: labels, Value: value}
}

func TestCollectReadsTheClusterFromKubeStateMetrics(t *testing.T) {
	q := fakeQuerier{
		QueryAllocatable: {
			sample(3.9, "node", "n1", "resource", "cpu"), sample(8e9, "node", "n1", "resource", "memory"), sample(110, "node", "n1", "resource", "pods"),
			sample(8, "node", "n2", "resource", "cpu"), sample(16e9, "node", "n2", "resource", "memory"),
		},
		QueryTaints:     {sample(1, "node", "n2", "key", "gpu", "value", "true", "effect", "NoSchedule")},
		QueryNodeLabels: {sample(1, "node", "n1", "label_node_kubernetes_io_instance_type", "m5.large")},
		QueryPods: {
			sample(1, "namespace", "shop", "pod", "web-7d9f8-abcde", "node", "n1"),
			sample(1, "namespace", "shop", "pod", "web-7d9f8-abcde", "node", "n1"), // a duplicate series is read once
			sample(1, "namespace", "kube-system", "pod", "agent-x", "node", "n2"),
			sample(1, "namespace", "ops", "pod", "debug", "node", "n1"),
			sample(1, "namespace", "ops", "pod", "pending", "node", ""), // not scheduled: not on any node
		},
		QueryRequests: {
			sample(0.25, "namespace", "shop", "pod", "web-7d9f8-abcde", "resource", "cpu"),
			sample(268435456, "namespace", "shop", "pod", "web-7d9f8-abcde", "resource", "memory"),
		},
		QueryOwners: {
			sample(1, "namespace", "shop", "pod", "web-7d9f8-abcde", "owner_kind", "ReplicaSet", "owner_name", "web-7d9f8"),
			sample(1, "namespace", "kube-system", "pod", "agent-x", "owner_kind", "DaemonSet", "owner_name", "agent"),
		},
	}
	snap, err := Collect(context.Background(), q, time.Unix(1700000000, 0))
	require.NoError(t, err)
	require.Len(t, snap.Nodes, 2)
	assert.Equal(t, int64(3900), snap.Nodes[0].CPUMilli)
	assert.Equal(t, int64(8000000000), snap.Nodes[0].MemoryBytes)
	assert.Equal(t, int64(110), snap.Nodes[0].Pods)
	assert.Equal(t, "m5.large", snap.Nodes[0].Labels["node_kubernetes_io_instance_type"])
	require.Len(t, snap.Nodes[1].Taints, 1)
	assert.Equal(t, "gpu", snap.Nodes[1].Taints[0].Key)
	assert.NotContains(t, snap.Notes, SnapshotNoNodeLabels)

	require.Len(t, snap.Pods, 3)
	byName := map[string]Pod{}
	for _, p := range snap.Pods {
		byName[p.Name] = p
	}
	web := byName["web-7d9f8-abcde"]
	assert.Equal(t, "Deployment", web.Kind)
	assert.Equal(t, "web", web.Workload, "the ReplicaSet hash is dropped")
	assert.Equal(t, int64(250), web.CPUMilli)
	assert.Equal(t, int64(268435456), web.MemoryBytes)
	assert.True(t, byName["agent-x"].DaemonSet)
	assert.Empty(t, byName["debug"].Workload, "a Pod with no controller stays bare")

	// a rule written with the real label key prices the node that kube-state-metrics exported with underscores
	rules := []Rule{{ID: 1, Name: "m5", MatchKind: MatchLabel, LabelKey: "node.kubernetes.io/instance-type", LabelValue: "m5.large", HourlyPrice: 0.1, Currency: "USD"}}
	_, ok := PriceOf(snap.Nodes[0], rules)
	assert.True(t, ok)
}

func TestCollectSaysWhatItCouldNotSeeAndFailsWithoutNodes(t *testing.T) {
	q := fakeQuerier{
		QueryAllocatable: {sample(4, "node", "n1", "resource", "cpu"), sample(8e9, "node", "n1", "resource", "memory")},
		QueryTaints:      nil, QueryNodeLabels: nil, QueryPods: nil, QueryRequests: nil, QueryOwners: nil,
	}
	snap, err := Collect(context.Background(), q, time.Now())
	require.NoError(t, err)
	assert.Contains(t, snap.Notes, SnapshotNoNodeLabels)

	q[QueryAllocatable] = nil
	_, err = Collect(context.Background(), q, time.Now())
	assert.Error(t, err)

	delete(q, QueryOwners)
	q[QueryAllocatable] = []promclient.VectorSample{sample(4, "node", "n1", "resource", "cpu")}
	_, err = Collect(context.Background(), q, time.Now())
	assert.Error(t, err, "a failing query is an error, not a half snapshot")
	assert.False(t, errors.Is(err, nil))
}

func TestAChangeOnlyLowersARequestAndZeroKeepsIt(t *testing.T) {
	snap := Snapshot{
		Nodes: []Node{node("a", 4000, 8), node("b", 4000, 8)},
		Pods: []Pod{
			pod("shop", "web-1", "Deployment", "web", "a", 3000, 2),
			pod("shop", "web-2", "Deployment", "web", "b", 3000, 2),
		},
	}
	// a candidate above what the Pods request does nothing, and a zero memory keeps the memory they have
	up := Simulate(snap, []Change{{Namespace: "shop", Kind: "Deployment", Workload: "web", CPUMilli: 3500, MemoryBytes: 0}}, nil)
	assert.Equal(t, 0, up.PodsChanged)
	assert.Empty(t, up.Released)

	cpuOnly := Simulate(snap, []Change{{Namespace: "shop", Kind: "Deployment", Workload: "web", CPUMilli: 1500}}, nil)
	assert.Equal(t, 2, cpuOnly.PodsChanged)
	assert.Len(t, cpuOnly.Released, 1)
}
