package migration

import (
	"context"
	"testing"

	"github.com/cylonchau/kube-res-foresight/pkg/clustersvc"
	"github.com/cylonchau/kube-res-foresight/pkg/dao/rdb"
	"github.com/cylonchau/kube-res-foresight/pkg/inventorysvc"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/cylonchau/kube-res-foresight/pkg/timing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTheClusterAgentSettingsTheTaskAndThePageAreSeeded(t *testing.T) {
	db := setupPlatformDB(t)
	require.NoError(t, EnsureDefaultConfigs())
	require.NoError(t, ensureForecastTasks())

	for _, key := range []string{timing.ClusterRetentionDays.Key, clustersvc.KeyCollectEvents, clustersvc.KeySkipReasons, clustersvc.KeyEventRules} {
		var cfg model.SysConfig
		require.NoError(t, db.Where("config_key = ?", key).First(&cfg).Error, key)
	}
	var retention model.SysConfig
	require.NoError(t, db.Where("config_key = ?", timing.ClusterRetentionDays.Key).First(&retention).Error)
	assert.Equal(t, "3", retention.ConfigValue, "the events are kept 3 days unless an administrator changes it")

	var task model.TaskDefinition
	require.NoError(t, db.Where("task_type = ?", "cluster.prune").First(&task).Error)
	assert.True(t, task.Enabled, "expired events are cleaned up")

	assert.True(t, allowed(t, db, "ADMIN", "POST", "/api/v1/clusters"))
	var menu model.Permission
	require.NoError(t, db.Where("code = ?", "datasource_clusters").First(&menu).Error)
	assert.Equal(t, "/datasource/clusters", menu.FeRoute)
	assert.Contains(t, menu.BeApis, "PUT /api/v1/clusters/*")
	assert.Contains(t, menu.BeApis, "GET /api/v1/cluster-keys")
}

func TestTheOperatorCredentialMayAskForItsSettingsAndSendEventsAndNothingMore(t *testing.T) {
	db := setupPlatformDB(t)
	var p model.Permission
	require.NoError(t, db.Where("code = ?", "forecast:operator:use").First(&p).Error)
	assert.Contains(t, p.BeApis, "GET /api/v1/operator/cluster-settings")
	assert.Contains(t, p.BeApis, "POST /api/v1/operator/events")
	assert.NotContains(t, p.BeApis, "/api/v1/clusters", "the credential of an operator does not manage clusters")
}

func TestTheBusinessRoleReadsTheClustersAModuleCanBeInAndManagesNone(t *testing.T) {
	db := setupPlatformDB(t)
	assert.True(t, allowed(t, db, BusinessRoleCode, "GET", "/api/v1/clusters"), "the module form lists the clusters")
	for _, c := range [][2]string{{"POST", "/api/v1/clusters"}, {"PUT", "/api/v1/clusters/3"}, {"DELETE", "/api/v1/clusters/3"}, {"GET", "/api/v1/clusters/3/events"},
		{"GET", "/api/v1/cluster-keys"}, {"POST", "/api/v1/operator/events"}, {"GET", "/api/v1/operator/cluster-settings"}} {
		assert.False(t, allowed(t, db, BusinessRoleCode, c[0], c[1]), "%s %s is not for the business role", c[0], c[1])
	}
}

func TestTheClusterThatExistedBeforeIsTheDefaultOneNamedAsTheParameterSaid(t *testing.T) {
	db := setupPlatformDB(t)
	require.NoError(t, EnsureDefaultConfigs())
	var def model.Cluster
	require.NoError(t, db.Where("is_default = ?", true).First(&def).Error)
	assert.Equal(t, inventorysvc.DefaultCluster, def.Name)
	assert.True(t, def.Enabled)

	// running it again changes nothing, and a name an administrator gave the system parameter later does not rename the cluster
	require.NoError(t, db.Model(&model.SysConfig{}).Where("config_key = ?", inventorysvc.KeyClusterName).Update("config_value", "other").Error)
	require.NoError(t, EnsureDefaultConfigs())
	var n int64
	require.NoError(t, db.Model(&model.Cluster{}).Count(&n).Error)
	assert.EqualValues(t, 1, n)
	require.NoError(t, db.Where("is_default = ?", true).First(&def).Error)
	assert.Equal(t, inventorysvc.DefaultCluster, def.Name)
}

func TestAPlatformThatNamedItsClusterBeforeKeepsThatNameForTheDefaultCluster(t *testing.T) {
	db := setupPlatformDB(t)
	require.NoError(t, EnsureDefaultConfigs())
	require.NoError(t, db.Model(&model.SysConfig{}).Where("config_key = ?", inventorysvc.KeyClusterName).Update("config_value", "prod-east").Error)
	require.NoError(t, db.Where("is_default = ?", true).Delete(&model.Cluster{}).Error)
	require.NoError(t, ensureDefaultCluster())
	row, err := rdb.NewClusterDAO(db).Default(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "prod-east", row.Name, "the nodes and workloads it reported under that name belong to the default cluster")
}

func TestTheSameWorkloadCanBeAModuleInTwoClustersAfterAnUpgradeOfAnOlderDatabase(t *testing.T) {
	db := setupPlatformDB(t)
	require.NoError(t, EnsureDefaultConfigs())
	// the database as it was before clusters: the unique index of the workload without the cluster, and no marker that it was made again
	require.NoError(t, db.Migrator().DropIndex(&model.ForecastModule{}, "idx_module_workload"))
	require.NoError(t, db.Exec("CREATE UNIQUE INDEX idx_module_workload ON forecast_module (namespace, workload_kind, workload)").Error)
	require.NoError(t, db.Unscoped().Where("config_key = ?", moduleClusterIndexMarker).Delete(&model.SysConfig{}).Error)

	mk := func(name, cluster string) error {
		return db.Create(&model.ForecastModule{Name: name, Cluster: cluster, Enabled: true, Mode: model.ModeObserve, Namespace: "prod", Workload: "api",
			WorkloadKind: "Deployment", Timezone: "UTC"}).Error
	}
	require.NoError(t, mk("api-main", ""))
	assert.Error(t, mk("api-east", "east"), "the old index refuses the same workload in another cluster")
	require.NoError(t, db.Where("name = ?", "api-main").Delete(&model.ForecastModule{}).Error)

	require.NoError(t, upgradeModuleWorkloadIndex())
	require.NoError(t, mk("api-main", ""))
	require.NoError(t, mk("api-east", "east"), "after the upgrade the cluster is part of what makes a module unique")
	assert.Error(t, mk("api-east-2", "east"), "but the same workload twice in one cluster is still refused")

	// it is done once: the marker says so, and running it again does not touch the index or the rows
	var marker int64
	require.NoError(t, db.Model(&model.SysConfig{}).Where("config_key = ?", moduleClusterIndexMarker).Count(&marker).Error)
	assert.EqualValues(t, 1, marker)
	require.NoError(t, upgradeModuleWorkloadIndex())
	var modules int64
	require.NoError(t, db.Model(&model.ForecastModule{}).Count(&modules).Error)
	assert.EqualValues(t, 2, modules)
}
