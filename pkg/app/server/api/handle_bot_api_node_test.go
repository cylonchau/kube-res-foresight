package api

import (
	"encoding/json"
	"github.com/cylonchau/kubeforecast/pkg/apis"
	"github.com/cylonchau/kubeforecast/pkg/dao/rdb"
	"github.com/cylonchau/kubeforecast/pkg/model"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"net/http"
	"testing"
)

func newBotAPINodeAdminTestRouter(t *testing.T, db *gorm.DB) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	handler := &BotAPINodeAdminRouter{
		DAO: rdb.NewTelegramBotAPINodeDAO(db),
	}
	group := router.Group("/api/v1/bot-api-nodes")
	{
		group.GET("", handler.List)
		group.GET("/:id", handler.Get)
		group.PUT("/:id/desired-config", handler.UpdateDesiredConfig)
		group.POST("/:id/approve", handler.Approve)
		group.POST("/:id/reject", handler.Reject)
		group.PUT("/:id/enable", handler.SetEnabled)
		group.DELETE("/:id", handler.Delete)
	}
	return router
}

func TestBotAPINodeAdminAPI(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.TelegramBotAPINode{}))

	node := &model.TelegramBotAPINode{
		Name:            "node-alpha",
		Status:          "pending",
		Enabled:         true,
		Hostname:        "host-1",
		OperatingSystem: "linux",
		Port:            8081,
	}
	require.NoError(t, db.Create(node).Error)

	router := newBotAPINodeAdminTestRouter(t, db)

	// 1. List
	rec := performJSON(t, router, http.MethodGet, "/api/v1/bot-api-nodes", "", nil)
	assert.Equal(t, http.StatusOK, rec.Code)
	var listResp map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &listResp))
	data := listResp["data"].(map[string]interface{})
	assert.Equal(t, float64(1), data["total"])

	// 2. Get
	rec = performJSON(t, router, http.MethodGet, "/api/v1/bot-api-nodes/1", "", nil)
	assert.Equal(t, http.StatusOK, rec.Code)

	// 3. Approve
	rec = performJSON(t, router, http.MethodPost, "/api/v1/bot-api-nodes/1/approve", "", nil)
	assert.Equal(t, http.StatusOK, rec.Code)
	var updated model.TelegramBotAPINode
	require.NoError(t, db.First(&updated, 1).Error)
	assert.Equal(t, "approved", updated.Status)

	// 4. Update Desired Config
	newPort := 8085
	desiredRun := true
	configReq := apis.TelegramBotAPINodeDesiredConfigReq{
		Port:           &newPort,
		DesiredRunning: &desiredRun,
	}
	rec = performJSON(t, router, http.MethodPut, "/api/v1/bot-api-nodes/1/desired-config", "", configReq)
	assert.Equal(t, http.StatusOK, rec.Code)

	require.NoError(t, db.First(&updated, 1).Error)
	assert.Equal(t, 8085, updated.Port)
	assert.True(t, updated.DesiredRunning)

	// 5. SetEnabled
	enabledFalse := false
	rec = performJSON(t, router, http.MethodPut, "/api/v1/bot-api-nodes/1/enable", "", apis.TelegramBotAPINodeSetEnabledReq{
		Enabled: &enabledFalse,
	})
	assert.Equal(t, http.StatusOK, rec.Code)
	require.NoError(t, db.First(&updated, 1).Error)
	assert.False(t, updated.Enabled)

	// 6. Delete
	rec = performJSON(t, router, http.MethodDelete, "/api/v1/bot-api-nodes/1", "", nil)
	assert.Equal(t, http.StatusOK, rec.Code)
	err = db.First(&updated, 1).Error
	assert.Error(t, err)
}
