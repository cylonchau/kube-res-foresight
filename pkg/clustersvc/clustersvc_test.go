package clustersvc

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/clusteragent"
	"github.com/cylonchau/kube-res-foresight/pkg/dao/rdb"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/cylonchau/kube-res-foresight/pkg/timing"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var seq int32

var t0 = time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)

type fixture struct {
	svc   *Service
	db    *gorm.DB
	clock *time.Time
	def   *model.Cluster
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:cl%d?mode=memory&cache=shared", atomic.AddInt32(&seq, 1))), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Cluster{}, &model.ClusterEvent{}, &model.ClusterChange{}, &model.SecretKey{}, &model.ForecastModule{}, &model.SysConfig{}))
	clock := t0
	dao := rdb.NewClusterDAO(db)
	def, err := dao.EnsureDefault(context.Background(), "main")
	require.NoError(t, err)
	return &fixture{db: db, clock: &clock, def: def, svc: &Service{DAO: dao, Config: rdb.NewSysConfigDAO(db), Now: func() time.Time { return clock }}}
}

func (f *fixture) setConfig(t *testing.T, key, value string) {
	t.Helper()
	require.NoError(t, f.db.Create(&model.SysConfig{ConfigKey: key, ConfigValue: value, ConfigName: key}).Error)
}

func event(uid, reason, component string, last time.Time, count int) clusteragent.Event {
	return clusteragent.Event{UID: uid, Type: "Normal", Reason: reason, Message: reason + " happened", Component: component, Count: count,
		Kind: "Node", Name: "n1", FirstAt: last.Add(-time.Minute), LastAt: last}
}

func TestAnOperatorWithoutAClusterBelongsToTheDefaultOne(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	c, err := f.svc.Resolve(ctx, "", "")
	require.NoError(t, err)
	assert.Equal(t, "main", c.Name)
	c, err = f.svc.Resolve(ctx, "main", "")
	require.NoError(t, err, "naming the default cluster is the same")
	assert.True(t, c.IsDefault)
}

func TestACredentialActsForItsOwnClusterOnly(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	_, err := f.svc.Create(ctx, Input{Name: "prod-east", Enabled: true})
	require.NoError(t, err)

	c, err := f.svc.Resolve(ctx, "prod-east", "prod-east")
	require.NoError(t, err)
	assert.Equal(t, "prod-east", c.Name)
	c, err = f.svc.Resolve(ctx, "", "prod-east")
	require.NoError(t, err, "a bound credential with no cluster named acts for its own")
	assert.Equal(t, "prod-east", c.Name)

	_, err = f.svc.Resolve(ctx, "main", "prod-east")
	assert.ErrorIs(t, err, ErrForbidden, "a bound credential cannot act for another cluster")
	_, err = f.svc.Resolve(ctx, "prod-east", "")
	assert.ErrorIs(t, err, ErrForbidden, "an unbound credential acts for the default cluster only")
	_, err = f.svc.Resolve(ctx, "prod-west", "prod-west")
	assert.ErrorIs(t, err, ErrUnknownCluster, "a cluster that is not registered")

	row, _ := f.svc.DAO.ByName(ctx, "prod-east")
	_, err = f.svc.Update(ctx, row.ID, Input{Enabled: false})
	require.NoError(t, err)
	_, err = f.svc.Resolve(ctx, "prod-east", "prod-east")
	assert.ErrorIs(t, err, ErrDisabled, "a cluster that is switched off")
}

func TestClustersAreNamedOnceAndTheDefaultOneStays(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	for _, bad := range []string{"", "Prod", "a_b", "-a", "a-", "has space", "a.b"} {
		_, err := f.svc.Create(ctx, Input{Name: bad, Enabled: true})
		assert.ErrorIs(t, err, ErrInvalid, bad)
	}
	_, err := f.svc.Create(ctx, Input{Name: "main", Enabled: true})
	assert.ErrorIs(t, err, ErrInvalid, "the name is taken")
	_, err = f.svc.Create(ctx, Input{Name: "x", Enabled: true, NamespaceInclude: []string{"Bad Name"}})
	assert.ErrorIs(t, err, ErrInvalid, "a namespace entry that is not a name")

	_, err = f.svc.Update(ctx, f.def.ID, Input{Enabled: false})
	assert.ErrorIs(t, err, ErrInvalid, "the default cluster cannot be switched off")
	assert.ErrorIs(t, f.svc.Delete(ctx, f.def.ID), ErrInvalid, "the default cluster cannot be deleted")

	c, err := f.svc.Create(ctx, Input{Name: "edge", Enabled: true})
	require.NoError(t, err)
	require.NoError(t, f.db.Create(&model.ForecastModule{Name: "m1", Cluster: "edge", Namespace: "a", WorkloadKind: "Deployment", Workload: "w", Mode: "observe", Timezone: "UTC"}).Error)
	assert.ErrorIs(t, f.svc.Delete(ctx, c.ID), ErrInUse, "a cluster with a module")
	require.NoError(t, f.db.Where("name = ?", "m1").Delete(&model.ForecastModule{}).Error)
	assert.NoError(t, f.svc.Delete(ctx, c.ID))
}

func TestTheDefaultClusterOwnsTheModulesThatNameNone(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	require.NoError(t, f.db.Create(&model.ForecastModule{Name: "old", Cluster: "", Namespace: "a", WorkloadKind: "Deployment", Workload: "w1", Mode: "observe", Timezone: "UTC"}).Error)
	require.NoError(t, f.db.Create(&model.ForecastModule{Name: "named", Cluster: "main", Namespace: "a", WorkloadKind: "Deployment", Workload: "w2", Mode: "observe", Timezone: "UTC"}).Error)
	n, err := f.svc.DAO.ModuleCount(ctx, f.def)
	require.NoError(t, err)
	assert.EqualValues(t, 2, n)
}

func TestWhatAnOperatorIsToldIsTheSettingsOfItsCluster(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	c, err := f.svc.Create(ctx, Input{Name: "prod-east", Enabled: true, NamespaceInclude: []string{"Shop*", "billing"}, NamespaceExclude: []string{"shop-test"}})
	require.NoError(t, err)

	got := f.svc.Settings(ctx, c)
	assert.Equal(t, "prod-east", got.Cluster)
	assert.Equal(t, []string{"billing", "shop*"}, got.Namespaces.Include)
	assert.Equal(t, []string{"shop-test"}, got.Namespaces.Exclude)
	assert.True(t, got.CollectEvents, "events are collected until the switch is turned off")
	assert.Equal(t, []string{"Pulling", "Pulled", "Created", "Started"}, got.SkipReasons)
	assert.False(t, got.Namespaces.Allows("shop-test"))
	assert.True(t, got.Namespaces.Allows("shop-api"))
	assert.False(t, got.Namespaces.Allows("kube-system"), "not in the include list")
	assert.True(t, got.Namespaces.Allows(""), "a node is never left out")

	f.setConfig(t, KeyCollectEvents, "false")
	f.setConfig(t, KeySkipReasons, " Killing , ")
	got = f.svc.Settings(ctx, c)
	assert.False(t, got.CollectEvents)
	assert.Equal(t, []string{"Killing"}, got.SkipReasons)

	// the filter is changed on the platform and the next answer has it
	_, err = f.svc.Update(ctx, c.ID, Input{Enabled: true, NamespaceExclude: []string{"kube-system"}})
	require.NoError(t, err)
	row, _ := f.svc.DAO.Get(ctx, c.ID)
	got = f.svc.Settings(ctx, row)
	assert.Empty(t, got.Namespaces.Include)
	assert.Equal(t, []string{"kube-system"}, got.Namespaces.Exclude)
}

func TestEventsAreKeptAsSaidAndReadWithTheRules(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	batch := clusteragent.Batch{Operator: "op-1", Events: []clusteragent.Event{
		event("u1", "ScaleDown", "cluster-autoscaler", t0.Add(-time.Hour), 1),
		event("u2", "NodeNotReady", "node-controller", t0.Add(-time.Hour), 3),
		event("u3", "Recycled", "acme-node-lifecycle", t0.Add(-time.Hour), 1),
	}}
	res, err := f.svc.IngestEvents(ctx, f.def, batch)
	require.NoError(t, err)
	assert.Equal(t, 3, res.Added)

	rows, total, err := f.svc.DAO.ListEvents(ctx, rdb.EventFilter{Cluster: "main"}, 1, 20)
	require.NoError(t, err)
	require.EqualValues(t, 3, total)
	by := map[string]model.ClusterEvent{}
	for _, r := range rows {
		by[r.UID] = r
	}
	assert.Equal(t, clusteragent.OriginAuto, by["u1"].Origin)
	assert.Equal(t, "autoscaler", by["u1"].Category)
	assert.Equal(t, "ScaleDown happened", by["u1"].Message, "the message is as it came")
	assert.Equal(t, "controller", by["u2"].Category)
	assert.Equal(t, clusteragent.OriginUnclassified, by["u3"].Origin, "no rule knows this component: nothing is guessed")
	assert.Empty(t, by["u3"].Rule)
	assert.Equal(t, "op-1", by["u1"].Operator)
	assert.Contains(t, by["u1"].Raw, `"reason":"ScaleDown"`)

	// a rule added later teaches the platform another cloud; the next event of that component is read with it
	f.setConfig(t, KeyEventRules, `[{"name":"acme-cloud","component":"acme-node-lifecycle","origin":"auto","category":"cloud"}]`)
	res, err = f.svc.IngestEvents(ctx, f.def, clusteragent.Batch{Events: []clusteragent.Event{event("u4", "Recycled", "acme-node-lifecycle", t0.Add(-time.Minute), 1)}})
	require.NoError(t, err)
	assert.Equal(t, 1, res.Added)
	rows, _, _ = f.svc.DAO.ListEvents(ctx, rdb.EventFilter{Cluster: "main", Reason: "Recycled", Origin: clusteragent.OriginAuto}, 1, 20)
	require.Len(t, rows, 1)
	assert.Equal(t, "cloud", rows[0].Category)
	assert.Equal(t, "acme-cloud", rows[0].Rule)

	// a list that cannot be read is said, and the built-in rules are still used
	require.NoError(t, f.svc.Config.UpdateValue(ctx, KeyEventRules, "not json"))
	res, err = f.svc.IngestEvents(ctx, f.def, clusteragent.Batch{Events: []clusteragent.Event{event("u5", "ScaleDown", "cluster-autoscaler", t0.Add(-time.Minute), 1)}})
	require.NoError(t, err)
	assert.NotEmpty(t, res.RulesError)
	rows, _, _ = f.svc.DAO.ListEvents(ctx, rdb.EventFilter{Cluster: "main", Search: "scaledown"}, 1, 50)
	assert.NotEmpty(t, rows)
}

func TestTheSameEventHappeningAgainIsOneRowAndALateBatchNeverTakesACountBack(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	at := t0.Add(-2 * time.Hour)
	_, err := f.svc.IngestEvents(ctx, f.def, clusteragent.Batch{Events: []clusteragent.Event{event("u1", "BackOff", "kubelet", at, 1)}})
	require.NoError(t, err)

	res, err := f.svc.IngestEvents(ctx, f.def, clusteragent.Batch{Events: []clusteragent.Event{event("u1", "BackOff", "kubelet", at.Add(time.Hour), 7)}})
	require.NoError(t, err)
	assert.Equal(t, 0, res.Added)
	assert.Equal(t, 1, res.Updated)

	// an older report of the same event, arriving after an outage
	res, err = f.svc.IngestEvents(ctx, f.def, clusteragent.Batch{Events: []clusteragent.Event{event("u1", "BackOff", "kubelet", at.Add(10*time.Minute), 3)}})
	require.NoError(t, err)
	assert.Equal(t, 0, res.Updated, "the stored count is newer")

	rows, total, _ := f.svc.DAO.ListEvents(ctx, rdb.EventFilter{Cluster: "main"}, 1, 20)
	require.EqualValues(t, 1, total)
	assert.Equal(t, 7, rows[0].Count)
	assert.Equal(t, at.Add(time.Hour), rows[0].LastAt.UTC())
	assert.Equal(t, at.Add(-time.Minute), rows[0].FirstAt.UTC(), "the first time is kept")

	// the same uid twice in one batch is one event
	res, err = f.svc.IngestEvents(ctx, f.def, clusteragent.Batch{Events: []clusteragent.Event{
		event("u9", "Killing", "kubelet", t0.Add(-time.Minute), 1), event("u9", "Killing", "kubelet", t0.Add(-time.Second), 2)}})
	require.NoError(t, err)
	assert.Equal(t, 1, res.Added)
}

func TestEventsOfOneClusterAreNotSeenInAnother(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	east, err := f.svc.Create(ctx, Input{Name: "east", Enabled: true})
	require.NoError(t, err)
	// the same Event uid in two clusters (uids are only unique inside a cluster) is two rows
	_, err = f.svc.IngestEvents(ctx, f.def, clusteragent.Batch{Events: []clusteragent.Event{event("same", "A", "kubelet", t0.Add(-time.Minute), 1)}})
	require.NoError(t, err)
	_, err = f.svc.IngestEvents(ctx, east, clusteragent.Batch{Events: []clusteragent.Event{event("same", "B", "kubelet", t0.Add(-time.Minute), 1)}})
	require.NoError(t, err)

	mainRows, n1, _ := f.svc.DAO.ListEvents(ctx, rdb.EventFilter{Cluster: "main"}, 1, 20)
	eastRows, n2, _ := f.svc.DAO.ListEvents(ctx, rdb.EventFilter{Cluster: "east"}, 1, 20)
	require.EqualValues(t, 1, n1)
	require.EqualValues(t, 1, n2)
	assert.Equal(t, "A", mainRows[0].Reason)
	assert.Equal(t, "B", eastRows[0].Reason)
}

func TestEventsAreKeptThreeDaysUnlessTheParameterSaysOtherwiseAndThenCleanedUp(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	assert.Equal(t, 3*24*time.Hour, f.svc.Retention(ctx), "the default is 3 days")

	_, err := f.svc.IngestEvents(ctx, f.def, clusteragent.Batch{Events: []clusteragent.Event{
		event("new", "A", "kubelet", t0.Add(-time.Hour), 1),
		event("edge", "B", "kubelet", t0.Add(-71*time.Hour), 1),
	}})
	require.NoError(t, err)

	// an event already past the retention is not stored
	res, err := f.svc.IngestEvents(ctx, f.def, clusteragent.Batch{Events: []clusteragent.Event{event("gone", "C", "kubelet", t0.Add(-73*time.Hour), 1)}})
	require.NoError(t, err)
	assert.Equal(t, 1, res.Expired)
	assert.Equal(t, 0, res.Added)

	// time passes: the 71-hour-old event is now past 3 days
	*f.clock = t0.Add(2 * time.Hour)
	removed, err := f.svc.Prune(ctx)
	require.NoError(t, err)
	assert.EqualValues(t, 1, removed)
	_, total, _ := f.svc.DAO.ListEvents(ctx, rdb.EventFilter{Cluster: "main"}, 1, 20)
	assert.EqualValues(t, 1, total)

	// the parameter changes the time, in days, and is kept in its range
	f.setConfig(t, timing.ClusterRetentionDays.Key, "7")
	assert.Equal(t, 7*24*time.Hour, f.svc.Retention(ctx))
	require.NoError(t, f.svc.Config.UpdateValue(ctx, timing.ClusterRetentionDays.Key, "0"))
	assert.Equal(t, 24*time.Hour, f.svc.Retention(ctx), "below the range: the lowest allowed")
	require.NoError(t, f.svc.Config.UpdateValue(ctx, timing.ClusterRetentionDays.Key, "soon"))
	assert.Equal(t, 3*24*time.Hour, f.svc.Retention(ctx), "not a number: the default")
}

func TestABatchThatIsRefusedStoresNothing(t *testing.T) {
	f := newFixture(t)
	_, err := f.svc.IngestEvents(context.Background(), f.def, clusteragent.Batch{Events: []clusteragent.Event{event("ok", "A", "kubelet", t0, 1), {UID: "", LastAt: t0}}})
	assert.True(t, errors.Is(err, clusteragent.ErrInvalid))
	_, total, _ := f.svc.DAO.ListEvents(context.Background(), rdb.EventFilter{Cluster: "main"}, 1, 20)
	assert.EqualValues(t, 0, total)
}

func TestKeysAreBoundToOneClusterAndMoveWhenBoundAgain(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	east, err := f.svc.Create(ctx, Input{Name: "east", Enabled: true})
	require.NoError(t, err)
	k1 := &model.SecretKey{Name: "k1", KeyType: model.SecretKeyTypeDeveloperAccess, EncryptedValue: "x", MaskedValue: "m1"}
	k2 := &model.SecretKey{Name: "k2", KeyType: model.SecretKeyTypeDeveloperAccess, EncryptedValue: "x", MaskedValue: "m2"}
	other := &model.SecretKey{Name: "login", KeyType: "other", EncryptedValue: "x", MaskedValue: "m3"}
	for _, k := range []*model.SecretKey{k1, k2, other} {
		require.NoError(t, f.db.Create(k).Error)
	}

	require.NoError(t, f.svc.DAO.BindKeys(ctx, "east", []int64{k1.ID, k2.ID, other.ID}))
	ids, _ := f.svc.DAO.KeyIDs(ctx, east.Name)
	assert.Equal(t, []int64{k1.ID, k2.ID}, ids, "only a developer access key can be bound")

	require.NoError(t, f.svc.DAO.BindKeys(ctx, "main", []int64{k2.ID}))
	ids, _ = f.svc.DAO.KeyIDs(ctx, "east")
	assert.Equal(t, []int64{k1.ID}, ids, "a key acts for one cluster: binding it elsewhere moves it")
	ids, _ = f.svc.DAO.KeyIDs(ctx, "main")
	assert.Equal(t, []int64{k2.ID}, ids)

	require.NoError(t, f.svc.DAO.BindKeys(ctx, "east", nil))
	ids, _ = f.svc.DAO.KeyIDs(ctx, "east")
	assert.Empty(t, ids, "binding none sets them free")

	require.NoError(t, f.svc.DAO.BindKeys(ctx, "east", []int64{k1.ID}))
	require.NoError(t, f.svc.Delete(ctx, east.ID))
	var got model.SecretKey
	require.NoError(t, f.db.First(&got, k1.ID).Error)
	assert.Equal(t, "", got.Cluster, "deleting a cluster sets its keys free")
}
