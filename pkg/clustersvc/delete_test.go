package clustersvc

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cylonchau/kube-res-foresight/pkg/model"
)

// A cluster that still has modules is refused without the person's word; with it the modules go first (a module that is on is switched off, then deleted) and everything that
// names the cluster goes too: the events, the changes, what the operator reported of nodes and workloads. The default cluster is never deleted.
func TestAClusterIsDeletedWithItsModulesAndEverythingThatNamesIt(t *testing.T) {
	f := newFixture(t)
	f.withModules(t)
	f.withDeletable(t)
	require.NoError(t, f.db.AutoMigrate(&model.RegistrationTombstone{}))
	ctx := context.Background()

	c, err := f.svc.Create(ctx, Input{Name: "edge", Enabled: true})
	require.NoError(t, err)
	on := &model.ForecastModule{Name: "m-on", Cluster: "edge", Namespace: "a", WorkloadKind: "Deployment", Workload: "on", Mode: "observe", Timezone: "UTC", Enabled: true}
	off := &model.ForecastModule{Name: "m-off", Cluster: "edge", Namespace: "a", WorkloadKind: "Deployment", Workload: "off", Mode: "observe", Timezone: "UTC"}
	other := &model.ForecastModule{Name: "m-other", Cluster: "", Namespace: "a", WorkloadKind: "Deployment", Workload: "other", Mode: "observe", Timezone: "UTC"}
	for _, m := range []*model.ForecastModule{on, off, other} {
		require.NoError(t, f.db.Create(m).Error)
	}
	require.NoError(t, f.db.Create(&model.ClusterNodeSnapshot{Cluster: "edge", Node: "n1", ObservedAt: time.Now()}).Error)
	require.NoError(t, f.db.Create(&model.WorkloadConfig{ModuleID: off.ID, Cluster: "edge", Namespace: "a", Kind: "Deployment", Name: "off", ObservedAt: time.Now()}).Error)
	require.NoError(t, f.db.Create(&model.RegistrationTombstone{Cluster: "edge", Namespace: "a", WorkloadKind: "Deployment", Workload: "gone", ModuleName: "gone"}).Error)

	assert.ErrorIs(t, f.svc.Delete(ctx, c.ID), ErrInUse, "without the person's word the modules keep the cluster")
	_, err = f.svc.DeleteWithModules(ctx, f.def.ID)
	assert.ErrorIs(t, err, ErrInvalid, "the default cluster is not deleted, with its modules or without")

	n, err := f.svc.DeleteWithModules(ctx, c.ID)
	require.NoError(t, err)
	assert.Equal(t, 2, n, "the module that was on and the one that was off")

	var modules, nodes, workloads, tombstones, clusters int64
	require.NoError(t, f.db.Model(&model.ForecastModule{}).Where("cluster = ?", "edge").Count(&modules).Error)
	require.NoError(t, f.db.Model(&model.ClusterNodeSnapshot{}).Where("cluster = ?", "edge").Count(&nodes).Error)
	require.NoError(t, f.db.Model(&model.WorkloadConfig{}).Where("cluster = ?", "edge").Count(&workloads).Error)
	require.NoError(t, f.db.Model(&model.RegistrationTombstone{}).Where("cluster = ?", "edge").Count(&tombstones).Error)
	require.NoError(t, f.db.Model(&model.Cluster{}).Where("name = ?", "edge").Count(&clusters).Error)
	assert.Zero(t, modules)
	assert.Zero(t, nodes)
	assert.Zero(t, workloads)
	assert.Zero(t, tombstones)
	assert.Zero(t, clusters)
	var others int64
	require.NoError(t, f.db.Model(&model.ForecastModule{}).Where("name = ?", "m-other").Count(&others).Error)
	assert.EqualValues(t, 1, others, "the modules of another cluster are not touched")
}

// A cluster with no module is deleted either way.
func TestAClusterWithoutModulesIsDeletedByBothWays(t *testing.T) {
	f := newFixture(t)
	f.withModules(t)
	f.withDeletable(t)
	ctx := context.Background()
	a, err := f.svc.Create(ctx, Input{Name: "one", Enabled: true})
	require.NoError(t, err)
	b, err := f.svc.Create(ctx, Input{Name: "two", Enabled: true})
	require.NoError(t, err)
	require.NoError(t, f.svc.Delete(ctx, a.ID))
	n, err := f.svc.DeleteWithModules(ctx, b.ID)
	require.NoError(t, err)
	assert.Zero(t, n)
}
