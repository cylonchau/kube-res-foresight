package migration

import (
	"fmt"
	"github.com/cylonchau/kubeforecast/pkg/model"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"testing"
)

func TestPlatformCleanupPreservesGenericTasksAndAuthorization(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(model.Models...))
	require.NoError(t, db.Exec("CREATE TABLE market_kline (id INTEGER PRIMARY KEY)").Error)
	oldDB := model.DB
	model.DB = db
	t.Cleanup(func() { model.DB = oldDB })
	require.NoError(t, seedPlatformPermissions())
	workflowPermission := model.Permission{}
	require.NoError(t, db.Where("code = ?", "workflow").First(&workflowPermission).Error)
	custom := model.Permission{Code: "custom:workflow:run", Type: "BUTTON", ParentID: workflowPermission.ID, Status: true}
	retired := model.Permission{Code: "market", Type: "DIRECTORY", Status: true}
	require.NoError(t, db.Create(&custom).Error)
	require.NoError(t, db.Create(&retired).Error)
	require.NoError(t, db.Create(&model.TaskDefinition{TaskType: "custom.report", CronExpr: "@every 1m"}).Error)
	require.NoError(t, db.Create(&model.TaskDefinition{TaskType: "signal.composite", CronExpr: "@every 1m"}).Error)
	require.NoError(t, db.Create(&model.SysConfig{ConfigKey: "custom.setting", ConfigValue: "kept"}).Error)
	for range 2 {
		require.NoError(t, cleanupRetiredModules(db))
		require.NoError(t, seedPlatformPermissions())
	}
	require.False(t, db.Migrator().HasTable("market_kline"))
	var roots []model.Permission
	require.NoError(t, db.Where("parent_id = 0 AND visible = ? AND type <> ?", true, "BUTTON").Find(&roots).Error)
	require.Len(t, roots, 4)
	for _, root := range roots {
		require.True(t, model.IsPlatformRoute(root.Code))
	}
	var count int64
	require.NoError(t, db.Model(&model.Permission{}).Where("id = ?", custom.ID).Count(&count).Error)
	require.EqualValues(t, 1, count)
	require.NoError(t, db.Model(&model.TaskDefinition{}).Where("task_type = ?", "custom.report").Count(&count).Error)
	require.EqualValues(t, 1, count)
	require.NoError(t, db.Model(&model.TaskDefinition{}).Where("task_type = ?", "signal.composite").Count(&count).Error)
	require.Zero(t, count)
	require.NoError(t, db.Model(&model.SysConfig{}).Where("config_key = ?", "custom.setting").Count(&count).Error)
	require.EqualValues(t, 1, count)
}
