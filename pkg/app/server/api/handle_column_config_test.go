package api

import (
	"encoding/json"
	"github.com/cylonchau/kubeforecast/pkg/dao/rdb"
	"github.com/cylonchau/kubeforecast/pkg/model"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"net/http"
	"testing"
)

func newColumnConfigTestRouter(t *testing.T, db *gorm.DB) *gin.Engine {
	t.Helper()
	handler := &ColumnConfigRouter{
		MetaDAO:      &rdb.ColumnMetaDAO{},
		ConfigDAO:    rdb.NewUserColumnConfigDAO(db),
		SysConfigDAO: rdb.NewSysConfigDAO(db),
	}
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("userId", uint(1))
		c.Next()
	})
	router.POST("/columns/config", handler.UpdateConfig)
	return router
}

func TestUpdateColumnConfigRejectsTooManyCheckedBusinessColumns(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.SysConfig{}, &model.SystemMarker{}, &model.UserColumnConfig{}, &model.Job{}))
	require.NoError(t, db.Create(&model.SysConfig{ConfigKey: "ui.table.default_visible_columns", ConfigValue: "2"}).Error)

	router := newColumnConfigTestRouter(t, db)

	// id/selection/expand/operate are all structural and free; three real business
	// columns against a limit of 2 must still be rejected.
	payload := map[string]string{
		"view_name": "task_center_audit",
		"config":    `[{"key":"id","checked":true},{"key":"job_type","checked":true},{"key":"status","checked":true},{"key":"target_type","checked":true},{"key":"operate","checked":true}]`,
	}
	recorder := performJSON(t, router, http.MethodPost, "/columns/config", "", payload)
	require.Equal(t, http.StatusBadRequest, recorder.Code)

	var count int64
	require.NoError(t, db.Model(&model.UserColumnConfig{}).Count(&count).Error)
	require.Zero(t, count, "rejected config must not be persisted")
}

func TestUpdateColumnConfigAllowsUpToTheConfiguredLimit(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.SysConfig{}, &model.SystemMarker{}, &model.UserColumnConfig{}, &model.Job{}))
	require.NoError(t, db.Create(&model.SysConfig{ConfigKey: "ui.table.default_visible_columns", ConfigValue: "2"}).Error)

	router := newColumnConfigTestRouter(t, db)

	payload := map[string]string{
		"view_name": "task_center_audit",
		"config":    `[{"key":"job_type","checked":true},{"key":"status","checked":true},{"key":"retry_count","checked":false}]`,
	}
	recorder := performJSON(t, router, http.MethodPost, "/columns/config", "", payload)
	require.Equal(t, http.StatusOK, recorder.Code)

	var saved model.UserColumnConfig
	require.NoError(t, db.Where("user_id = ? AND view_name = ?", uint(1), "task_center_audit").First(&saved).Error)
	var parsed []map[string]any
	require.NoError(t, json.Unmarshal([]byte(saved.Config), &parsed))
	require.Len(t, parsed, 3)
}

func TestUpdateColumnConfigDoesNotCountStructuralColumns(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.SysConfig{}, &model.SystemMarker{}, &model.UserColumnConfig{}, &model.Job{}))
	require.NoError(t, db.Create(&model.SysConfig{ConfigKey: "ui.table.default_visible_columns", ConfigValue: "2"}).Error)

	router := newColumnConfigTestRouter(t, db)

	// Two business columns reach the configured limit. Selection, expand, id and operation
	// columns remain checked without consuming any of those two business-column slots.
	payload := map[string]string{
		"view_name": "task_center_audit",
		"config":    `[{"key":"__selection__","checked":true},{"key":"expand","checked":true},{"key":"id","checked":true},{"key":"job_type","checked":true},{"key":"status","checked":true},{"key":"operate","checked":true}]`,
	}
	recorder := performJSON(t, router, http.MethodPost, "/columns/config", "", payload)
	require.Equal(t, http.StatusOK, recorder.Code)

	var saved model.UserColumnConfig
	require.NoError(t, db.Where("user_id = ? AND view_name = ?", uint(1), "task_center_audit").First(&saved).Error)
}

// TestUpdateColumnConfigDoesNotCountIDColumn guards against a design change: id
// used to consume a business-column slot (see the original comment this test
// replaced), but the id column is purely structural/identifying and must not
// count against ui.table.default_visible_columns, matching selection/expand/operate.
func TestUpdateColumnConfigDoesNotCountIDColumn(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.SysConfig{}, &model.SystemMarker{}, &model.UserColumnConfig{}, &model.Job{}))
	require.NoError(t, db.Create(&model.SysConfig{ConfigKey: "ui.table.default_visible_columns", ConfigValue: "2"}).Error)

	router := newColumnConfigTestRouter(t, db)

	// id + 2 real business columns must fit within a limit of 2, since id is structural.
	payload := map[string]string{
		"view_name": "task_center_audit",
		"config":    `[{"key":"id","checked":true},{"key":"job_type","checked":true},{"key":"status","checked":true}]`,
	}
	recorder := performJSON(t, router, http.MethodPost, "/columns/config", "", payload)
	require.Equal(t, http.StatusOK, recorder.Code)
}

func TestUpdateColumnConfigFallsBackToDefaultLimitWhenConfigMissing(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.SysConfig{}, &model.SystemMarker{}, &model.UserColumnConfig{}, &model.Job{}))
	// No ui.table.default_visible_columns row seeded; handler must fall back to defaultMaxVisibleColumns (5).

	router := newColumnConfigTestRouter(t, db)

	payload := map[string]string{
		"view_name": "task_center_audit",
		"config":    `[{"key":"job_type","checked":true},{"key":"status","checked":true},{"key":"retry_count","checked":true},{"key":"scheduled_at","checked":true},{"key":"started_at","checked":true},{"key":"finished_at","checked":true}]`,
	}
	recorder := performJSON(t, router, http.MethodPost, "/columns/config", "", payload)
	require.Equal(t, http.StatusBadRequest, recorder.Code, "6 checked business columns must exceed the default fallback limit of 5")
}
