package api

import (
	"encoding/json"
	"github.com/cylonchau/kubeforecast/pkg/app/query"
	"github.com/cylonchau/kubeforecast/pkg/dao/rdb"
	"github.com/cylonchau/kubeforecast/pkg/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestNotifyRouter(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := workflowAPIDB(t)
	require.NoError(t, db.AutoMigrate(&model.NotificationDelivery{}))

	dao := rdb.NewNotificationDAO(db)
	handler := &NotifyRouter{DAO: dao}

	r := gin.New()
	r.GET("/notify/deliveries", handler.ListDeliveries)
	r.POST("/notify/deliveries/:id/ack", handler.AckDelivery)
	r.GET("/notify/stats", handler.GetStats)

	// Seed test delivery
	require.NoError(t, dao.RecordDelivery(t.Context(), &model.NotificationDelivery{
		NotificationID:  "notif_test_1",
		Channel:         "telegram",
		Recipient:       "12345",
		Title:           "Flash Crash Alert",
		Content:         "BTC dropped 8%",
		Severity:        "P0",
		Status:          "SENT",
		SentAt:          time.Now().UTC(),
		EscalateChannel: "voice",
	}))

	// 1. List deliveries
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/notify/deliveries?page=1&page_size=10&severity=P0", nil)
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	var listResp struct {
		Code int `json:"code"`
		Data struct {
			List  []model.NotificationDelivery `json:"list"`
			Total int64                        `json:"total"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &listResp))
	assert.Equal(t, query.OK.Code, listResp.Code)
	assert.Equal(t, int64(1), listResp.Data.Total)
	assert.Equal(t, "notif_test_1", listResp.Data.List[0].NotificationID)

	// 2. Ack delivery
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/notify/deliveries/notif_test_1/ack", nil)
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	// Check status updated to ACKED
	record, err := dao.GetByNotificationID(t.Context(), "notif_test_1")
	require.NoError(t, err)
	assert.Equal(t, "ACKED", record.Status)

	// 3. Get Stats
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/notify/stats", nil)
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	var statsResp struct {
		Data rdb.NotificationStats `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &statsResp))
	assert.Equal(t, int64(1), statsResp.Data.Total)
	assert.Equal(t, int64(1), statsResp.Data.Acked)

	// 4. Nil DAO tests
	nilHandler := &NotifyRouter{DAO: nil}
	nilR := gin.New()
	nilR.GET("/notify/deliveries", nilHandler.ListDeliveries)
	nilR.GET("/notify/stats", nilHandler.GetStats)

	w = httptest.NewRecorder()
	nilR.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/notify/deliveries", nil))
	assert.Equal(t, http.StatusOK, w.Code)

	w = httptest.NewRecorder()
	nilR.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/notify/stats", nil))
	assert.Equal(t, http.StatusOK, w.Code)
}
