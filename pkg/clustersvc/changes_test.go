package clustersvc

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/clusteragent"
	"github.com/cylonchau/kube-res-foresight/pkg/dao/rdb"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/cylonchau/kube-res-foresight/pkg/timing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func change(id, kind, ns, name, typ, from, to string, at time.Time) clusteragent.Change {
	return clusteragent.Change{RecordID: id, Kind: kind, Namespace: ns, Name: name, ObjectUID: "uid-" + name, Type: typ, From: from, To: to, OccurredAt: at, ObservedAt: at.Add(time.Second)}
}

func (f *fixture) ingest(t *testing.T, c *model.Cluster, list ...clusteragent.Change) *ChangeResult {
	t.Helper()
	res, err := f.svc.IngestChanges(context.Background(), c, clusteragent.ChangeBatch{Operator: "op-1", Changes: list})
	require.NoError(t, err)
	return res
}

func (f *fixture) idOf(t *testing.T, cluster, recordID string) uint {
	t.Helper()
	var row model.ClusterChange
	require.NoError(t, f.db.Where("cluster = ? AND record_id = ?", cluster, recordID).First(&row).Error)
	return row.ID
}

func TestAChangeIsKeptAsTheOperatorSaidItAndSentTwiceIsOneRow(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	at := t0.Add(-time.Hour)
	node := change("n-ready", clusteragent.KindNode, "", "ip-10-0-1-9", "ready", "True", "Unknown", at)
	node.Reasons = []clusteragent.Reason{{Source: clusteragent.SourceNodeCondition, Reason: "NodeStatusUnknown", Message: "Kubelet stopped posting node status.", Component: "Ready"}}
	node.Snapshot = &clusteragent.Snapshot{Capacity: &clusteragent.Res{CPUMilli: 4000, MemoryBytes: 16 << 30}, PodCount: 2, Pods: []clusteragent.PodRef{{Namespace: "shop", Name: "web-1"}}}

	res := f.ingest(t, f.def, node, node)
	assert.Equal(t, 1, res.Added, "the same record twice in one batch is one")
	res = f.ingest(t, f.def, node)
	assert.Equal(t, 0, res.Added, "a retry stores nothing new")

	rows, total, err := f.svc.DAO.ListChanges(ctx, rdbFilter("main"), 1, 20)
	require.NoError(t, err)
	require.EqualValues(t, 1, total)
	v := View(rows[0])
	assert.Equal(t, "ready", v.Type)
	assert.Equal(t, "True", v.From)
	assert.Equal(t, "Unknown", v.To)
	assert.Equal(t, at, v.OccurredAt.UTC())
	assert.Equal(t, "NodeStatusUnknown", v.Reasons[0].Reason)
	assert.Equal(t, "Kubelet stopped posting node status.", v.Reasons[0].Message, "the words of the object, as they came")
	assert.EqualValues(t, 4000, v.Snapshot.Capacity.CPUMilli)
	assert.Equal(t, 2, v.Snapshot.PodCount)
	assert.Equal(t, model.UsagePending, v.UsageStatus, "the usage is asked later")
	assert.Equal(t, "op-1", v.Operator)

	// the same record id in another cluster is another change
	east, err := f.svc.Create(ctx, Input{Name: "east", Enabled: true})
	require.NoError(t, err)
	assert.Equal(t, 1, f.ingest(t, east, node).Added)
	_, total, _ = f.svc.DAO.ListChanges(ctx, rdbFilter("main"), 1, 20)
	assert.EqualValues(t, 1, total)
}

func TestAChangePastTheRetentionIsNotKeptAndOneFromTheFutureIsBroughtBack(t *testing.T) {
	f := newFixture(t)
	res := f.ingest(t, f.def,
		change("old", "Pod", "shop", "p", "phase", "Running", "Failed", t0.Add(-4*24*time.Hour)),
		change("new", "Pod", "shop", "p", "phase", "Running", "Failed", t0.Add(-time.Minute)),
		change("later", "Pod", "shop", "q", "phase", "Running", "Failed", t0.Add(3*time.Hour)))
	assert.Equal(t, 2, res.Added)
	assert.Equal(t, 1, res.Expired)
	var row model.ClusterChange
	require.NoError(t, f.db.Where("record_id = ?", "later").First(&row).Error)
	assert.False(t, row.OccurredAt.After(t0.Add(time.Minute)), "a change cannot be from the future")

	_, err := f.svc.IngestChanges(context.Background(), f.def, clusteragent.ChangeBatch{Changes: []clusteragent.Change{{RecordID: "x", Kind: "Secret", Name: "s", Type: "t", OccurredAt: t0}}})
	assert.ErrorIs(t, err, clusteragent.ErrInvalid, "only a node, a Pod, a workload and an HPA")
	_, err = f.svc.IngestChanges(context.Background(), f.def, clusteragent.ChangeBatch{Changes: []clusteragent.Change{{RecordID: "", Kind: "Pod", Name: "s", Type: "t", OccurredAt: t0}}})
	assert.ErrorIs(t, err, clusteragent.ErrInvalid)
}

func TestAPodThatSaysTheNodeIsEvictedBecauseOfItAndOneThatDoesNotIsOnlyRelated(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	at := t0.Add(-30 * time.Minute)

	node := change("n-notready", clusteragent.KindNode, "", "ip-10-0-1-9", "ready", "True", "False", at)
	named := change("p-evicted", clusteragent.KindPod, "shop", "web-1", "status", "", "Evicted", at.Add(2*time.Minute))
	named.Node = "ip-10-0-1-9"
	named.Reasons = []clusteragent.Reason{{Source: clusteragent.SourcePodStatus, Reason: "Evicted", Message: "The node ip-10-0-1-9 was low on resource: memory."}}
	silent := change("p-gone", clusteragent.KindPod, "shop", "web-2", "terminating", "", "terminating", at.Add(3*time.Minute))
	silent.Node = "ip-10-0-1-9"
	elsewhere := change("p-other", clusteragent.KindPod, "shop", "web-3", "terminating", "", "terminating", at.Add(3*time.Minute))
	elsewhere.Node = "ip-10-0-7-7"
	late := change("p-late", clusteragent.KindPod, "shop", "web-4", "terminating", "", "terminating", at.Add(40*time.Minute))
	late.Node = "ip-10-0-1-9"
	f.ingest(t, f.def, node, named, silent, elsewhere, late)

	// an event about the second Pod that names the node
	_, err := f.svc.IngestEvents(ctx, f.def, clusteragent.Batch{Events: []clusteragent.Event{{UID: "e1", Type: "Normal", Reason: "Killing", Message: "Stopping container app on ip-10-0-1-9",
		Component: "kubelet", Kind: "Pod", Namespace: "shop", Name: "web-2", ObjectUID: "uid-web-2", FirstAt: at.Add(3 * time.Minute), LastAt: at.Add(3 * time.Minute), Count: 1}}})
	require.NoError(t, err)

	ex, err := f.svc.Explain(ctx, f.def, f.idOf(t, "main", "n-notready"))
	require.NoError(t, err)
	byName := map[string]Link{}
	for _, l := range ex.Effects {
		byName[l.Change.Name] = l
	}
	require.Len(t, byName, 2, "the Pod on another node, and the one a long time after, are not tied to it")
	assert.Equal(t, RelationCausedBy, byName["web-1"].Relation, "the Pod's own reason names the node")
	assert.Contains(t, byName["web-1"].Evidence, "ip-10-0-1-9")
	assert.Equal(t, RelationCausedBy, byName["web-2"].Relation, "an event about the Pod names the node")
	assert.Contains(t, byName["web-2"].Evidence, "Killing")
	assert.Empty(t, ex.Causes)

	// seen from the Pod: the node change is its cause
	ex, err = f.svc.Explain(ctx, f.def, f.idOf(t, "main", "p-evicted"))
	require.NoError(t, err)
	require.Len(t, ex.Causes, 1)
	assert.Equal(t, RelationCausedBy, ex.Causes[0].Relation)
	assert.Equal(t, DirectionCause, ex.Causes[0].Direction)
	assert.Equal(t, "ip-10-0-1-9", ex.Causes[0].Change.Name)
	assert.Equal(t, "Evicted", ex.Change.To)

	// a Pod that says nothing of the node: near it, and no more
	f2 := newFixture(t)
	f2.ingest(t, f2.def, node, silent)
	ex, err = f2.svc.Explain(ctx, f2.def, f2.idOf(t, "main", "p-gone"))
	require.NoError(t, err)
	require.Len(t, ex.Causes, 1)
	assert.Equal(t, RelationRelated, ex.Causes[0].Relation, "not a reason")
	assert.Contains(t, ex.Causes[0].Evidence, "nothing in its reasons or events names the node")
}

func TestTheEventsAboutAnObjectAroundTheChangeAreItsReasons(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	at := t0.Add(-20 * time.Minute)
	pod := change("p-oom", clusteragent.KindPod, "shop", "api-1", "restart", "1", "2", at)
	f.ingest(t, f.def, pod)
	ev := func(uid, reason, name string, when time.Time) clusteragent.Event {
		return clusteragent.Event{UID: uid, Type: "Warning", Reason: reason, Message: reason + " happened", Component: "kubelet", Kind: "Pod", Namespace: "shop", Name: name, ObjectUID: "uid-" + name, FirstAt: when, LastAt: when, Count: 1}
	}
	_, err := f.svc.IngestEvents(ctx, f.def, clusteragent.Batch{Events: []clusteragent.Event{
		ev("e-near", "BackOff", "api-1", at.Add(2*time.Minute)),
		ev("e-far", "Pulled", "api-1", at.Add(time.Hour)),
		ev("e-other", "BackOff", "api-2", at.Add(time.Minute)),
	}})
	require.NoError(t, err)
	ex, err := f.svc.Explain(ctx, f.def, f.idOf(t, "main", "p-oom"))
	require.NoError(t, err)
	require.Len(t, ex.Events, 1)
	assert.Equal(t, "BackOff", ex.Events[0].Reason)
	assert.Equal(t, "kubelet", ex.Events[0].Component)
	assert.Equal(t, clusteragent.OriginAuto, ex.Events[0].Origin, "the events come with who the rules say did them")
}

func TestAWorkloadWhoseReplicasFollowTheHPAIsCausedByItAndOnlyRelatedWhenItDoesNot(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	at := t0.Add(-10 * time.Minute)
	hpa := change("h-desired", clusteragent.KindHPA, "shop", "web", "desired", "3", "5", at)
	hpa.Workload = "web"
	followed := change("w-5", clusteragent.KindWorkload, "shop", "web", "replicas", "3", "5", at.Add(20*time.Second))
	followed.Workload = "web"
	other := change("w-7", clusteragent.KindWorkload, "shop", "web", "replicas", "5", "7", at.Add(40*time.Second))
	other.Workload = "web"
	f.ingest(t, f.def, hpa, followed, other)

	ex, err := f.svc.Explain(ctx, f.def, f.idOf(t, "main", "w-5"))
	require.NoError(t, err)
	require.Len(t, ex.Causes, 1)
	assert.Equal(t, RelationCausedBy, ex.Causes[0].Relation, "the replicas are what the HPA decided")
	assert.Contains(t, ex.Causes[0].Evidence, "decided on 5")
	ex, err = f.svc.Explain(ctx, f.def, f.idOf(t, "main", "w-7"))
	require.NoError(t, err)
	require.Len(t, ex.Causes, 1)
	assert.Equal(t, RelationRelated, ex.Causes[0].Relation, "7 is not what the HPA decided")

	ex, err = f.svc.Explain(ctx, f.def, f.idOf(t, "main", "h-desired"))
	require.NoError(t, err)
	require.Len(t, ex.Effects, 2)
	assert.Equal(t, DirectionEffect, ex.Effects[0].Direction)
}

func TestAPodOfAStatefulSetWhoseReplicasWereLoweredIsCausedByItAndADeploymentsPodIsOnlyRelated(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	at := t0.Add(-10 * time.Minute)
	scale := change("db-down", clusteragent.KindWorkload, "shop", "db", "replicas", "3", "2", at)
	scale.Workload = "db"
	pod := change("db-2-gone", clusteragent.KindPod, "shop", "db-2", "terminating", "", "terminating", at.Add(5*time.Second))
	pod.Workload = "db"
	pod.Snapshot = &clusteragent.Snapshot{Owner: "StatefulSet/db"}
	dscale := change("web-down", clusteragent.KindWorkload, "shop", "web", "replicas", "5", "4", at)
	dscale.Workload = "web"
	dpod := change("web-x-gone", clusteragent.KindPod, "shop", "web-6f7d-x", "terminating", "", "terminating", at.Add(5*time.Second))
	dpod.Workload = "web"
	dpod.Snapshot = &clusteragent.Snapshot{Owner: "ReplicaSet/web-6f7d"}
	grew := change("web-up", clusteragent.KindWorkload, "shop", "web", "replicas", "4", "6", at.Add(30*time.Second))
	grew.Workload = "web"
	f.ingest(t, f.def, scale, pod, dscale, dpod, grew)

	ex, err := f.svc.Explain(ctx, f.def, f.idOf(t, "main", "db-2-gone"))
	require.NoError(t, err)
	require.Len(t, ex.Causes, 1)
	assert.Equal(t, RelationCausedBy, ex.Causes[0].Relation)
	ex, err = f.svc.Explain(ctx, f.def, f.idOf(t, "main", "web-x-gone"))
	require.NoError(t, err)
	require.Len(t, ex.Causes, 1, "only the change that lowered the replicas, not the one that raised them")
	assert.Equal(t, RelationRelated, ex.Causes[0].Relation)
	assert.Equal(t, "web-down", ex.Causes[0].Change.RecordID)
}

type fakeUsage struct {
	calls    []time.Time
	datasets []uint
	fail     error
	byTime   func(at time.Time) UsageValue
}

func (f *fakeUsage) UsageAt(_ context.Context, ds uint, s UsageSubject, at time.Time) (UsageValue, string, error) {
	f.calls = append(f.calls, at)
	f.datasets = append(f.datasets, ds)
	if f.fail != nil {
		return UsageValue{}, "", f.fail
	}
	return f.byTime(at), "prom-east", nil
}

func ptr(v float64) *float64 { return &v }

func TestTheUsageAroundAChangeIsAskedOfPrometheusBeforeAtAndAfterOnceTheTimeAfterHasPassed(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	east, err := f.svc.Create(ctx, Input{Name: "east", Enabled: true, DatasourceID: 7})
	require.NoError(t, err)
	at := t0.Add(-2 * time.Minute) // the 5 minutes after it have not passed
	f.ingest(t, east, change("recent", clusteragent.KindNode, "", "n1", "ready", "True", "False", at))
	old := t0.Add(-time.Hour)
	f.ingest(t, east, change("old", clusteragent.KindNode, "", "n1", "ready", "True", "False", old))

	src := &fakeUsage{byTime: func(at time.Time) UsageValue {
		return UsageValue{CPU: ptr(float64(at.Sub(old)/time.Minute) + 100), Memory: ptr(2 << 30)}
	}}
	res, err := f.svc.FillUsage(ctx, src)
	require.NoError(t, err)
	assert.Equal(t, 1, res.Filled, "only the change whose time after it has passed")
	require.Len(t, src.calls, 3)
	assert.Equal(t, []time.Time{old.Add(-5 * time.Minute), old, old.Add(5 * time.Minute)}, src.calls, "5 minutes before, at the change, 5 minutes after")
	assert.Equal(t, []uint{7, 7, 7}, src.datasets, "the data source of the cluster")

	var row model.ClusterChange
	require.NoError(t, f.db.Where("record_id = ?", "old").First(&row).Error)
	v := View(row)
	assert.Equal(t, model.UsageFilled, v.UsageStatus)
	require.NotNil(t, v.Usage)
	assert.Equal(t, "node n1", v.Usage.Subject)
	assert.Equal(t, "prom-east", v.Usage.Source)
	require.Len(t, v.Usage.Points, 3)
	assert.Equal(t, []string{"before", "at", "after"}, []string{v.Usage.Points[0].Label, v.Usage.Points[1].Label, v.Usage.Points[2].Label})
	assert.InDelta(t, 95, *v.Usage.Points[0].CPU, 0.001)
	assert.InDelta(t, 105, *v.Usage.Points[2].CPU, 0.001)
	assert.Equal(t, old.Add(5*time.Minute), v.Usage.Points[2].At.UTC())

	// time passes: the recent one is asked too, and the filled one is not asked again
	*f.clock = t0.Add(10 * time.Minute)
	src.calls = nil
	res, err = f.svc.FillUsage(ctx, src)
	require.NoError(t, err)
	assert.Equal(t, 1, res.Filled)
	assert.Len(t, src.calls, 3)
}

func TestAnswersWithNoFigureAreSaidAndNeverWrittenAsZero(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	at := t0.Add(-time.Hour)
	f.ingest(t, f.def,
		change("none", clusteragent.KindPod, "shop", "p", "phase", "Running", "Failed", at),
		change("some", clusteragent.KindPod, "shop", "q", "phase", "Running", "Failed", at.Add(time.Second)))
	src := &fakeUsage{byTime: func(when time.Time) UsageValue {
		return UsageValue{Note: "the pod_cpu_usage query has no series for it"}
	}}
	res, err := f.svc.FillUsage(ctx, src)
	require.NoError(t, err)
	assert.Equal(t, 2, res.Unavailable)
	var row model.ClusterChange
	require.NoError(t, f.db.Where("record_id = ?", "none").First(&row).Error)
	assert.Equal(t, model.UsageUnavailable, row.UsageStatus)
	assert.Equal(t, "the pod_cpu_usage query has no series for it", row.UsageNote)
	v := View(row)
	for _, p := range v.Usage.Points {
		assert.Nil(t, p.CPU, "no figure is not zero")
		assert.Nil(t, p.Memory)
	}

	// a change with some figures is filled, and says what is missing
	f3 := newFixture(t)
	f3.ingest(t, f3.def, change("part", clusteragent.KindPod, "shop", "p", "phase", "Running", "Failed", at))
	src = &fakeUsage{byTime: func(when time.Time) UsageValue {
		if when.Before(at) {
			return UsageValue{Note: "no sample that long before"}
		}
		return UsageValue{CPU: ptr(0.5)}
	}}
	res, err = f3.svc.FillUsage(ctx, src)
	require.NoError(t, err)
	assert.Equal(t, 1, res.Filled)
	require.NoError(t, f3.db.Where("record_id = ?", "part").First(&row).Error)
	assert.Equal(t, "no sample that long before", row.UsageNote)
	assert.Nil(t, View(row).Usage.Points[0].CPU)
	assert.NotNil(t, View(row).Usage.Points[1].CPU)
}

func TestADataSourceThatCannotBeReachedIsAskedAgainAndGivenUpAfterSeveralTries(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.ingest(t, f.def, change("c", clusteragent.KindWorkload, "shop", "web", "replicas", "3", "5", t0.Add(-time.Hour)))
	src := &fakeUsage{fail: errors.New("connection refused")}
	for i := 1; i < usageTries; i++ {
		res, err := f.svc.FillUsage(ctx, src)
		require.NoError(t, err)
		assert.Equal(t, 1, res.Waiting, "try %d: it is asked again", i)
	}
	var row model.ClusterChange
	require.NoError(t, f.db.Where("record_id = ?", "c").First(&row).Error)
	assert.Equal(t, model.UsagePending, row.UsageStatus)
	assert.Equal(t, usageTries-1, row.UsageTries)
	res, err := f.svc.FillUsage(ctx, src)
	require.NoError(t, err)
	assert.Equal(t, 1, res.Unavailable, "given up")
	require.NoError(t, f.db.Where("record_id = ?", "c").First(&row).Error)
	assert.Equal(t, model.UsageUnavailable, row.UsageStatus)
	assert.Contains(t, row.UsageNote, "connection refused")

	// no data source at all is not asked again
	f2 := newFixture(t)
	f2.ingest(t, f2.def, change("d", clusteragent.KindNode, "", "n", "ready", "True", "False", t0.Add(-time.Hour)))
	res, err = f2.svc.FillUsage(ctx, &fakeUsage{fail: ErrNoUsageSource})
	require.NoError(t, err)
	assert.Equal(t, 1, res.Unavailable)
	res, err = f2.svc.FillUsage(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, FillResult{}, res)
}

func TestTheSubjectOfTheUsageIsTheObjectOrTheWorkloadAnHPATargets(t *testing.T) {
	s, label := subjectOf(model.ClusterChange{Kind: "Pod", Namespace: "shop", Name: "web-1", Workload: "web"})
	assert.Equal(t, UsageSubject{Kind: "Pod", Namespace: "shop", Name: "web-1", Workload: "web"}, s)
	assert.Equal(t, "pod shop/web-1", label)
	s, label = subjectOf(model.ClusterChange{Kind: "HPA", Namespace: "shop", Name: "web-hpa", Workload: "web"})
	assert.Equal(t, UsageSubject{Kind: "Workload", Namespace: "shop", Name: "web", Workload: "web"}, s)
	assert.Contains(t, label, "target of the HPA")
}

func TestTheUsageOffsetsAreSystemParameters(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	before, after := f.svc.usageOffsets(ctx)
	assert.Equal(t, 5*time.Minute, before)
	assert.Equal(t, 5*time.Minute, after)
	f.setConfig(t, timing.ClusterUsageBefore.Key, "900")
	f.setConfig(t, timing.ClusterUsageAfter.Key, "5")
	before, after = f.svc.usageOffsets(ctx)
	assert.Equal(t, 15*time.Minute, before)
	assert.Equal(t, 30*time.Second, after, "below the range: the lowest allowed")
}

func TestTheChangesAreCleanedUpWithTheEventsAndTheirChangesGoWithTheCluster(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.ingest(t, f.def,
		change("keep", clusteragent.KindPod, "shop", "p", "phase", "Running", "Failed", t0.Add(-time.Hour)),
		change("drop", clusteragent.KindPod, "shop", "q", "phase", "Running", "Failed", t0.Add(-71*time.Hour)))
	*f.clock = t0.Add(2 * time.Hour)
	removed, err := f.svc.Prune(ctx)
	require.NoError(t, err)
	assert.EqualValues(t, 1, removed)
	_, total, _ := f.svc.DAO.ListChanges(ctx, rdbFilter("main"), 1, 20)
	assert.EqualValues(t, 1, total)

	east, err := f.svc.Create(ctx, Input{Name: "east", Enabled: true})
	require.NoError(t, err)
	f.ingest(t, east, change("e1", clusteragent.KindNode, "", "n", "ready", "True", "False", *f.clock))
	require.NoError(t, f.svc.Delete(ctx, east.ID))
	_, total, _ = f.svc.DAO.ListChanges(ctx, rdbFilter("east"), 1, 20)
	assert.EqualValues(t, 0, total)
}

func rdbFilter(cluster string) rdb.ChangeFilter { return rdb.ChangeFilter{Cluster: cluster} }
