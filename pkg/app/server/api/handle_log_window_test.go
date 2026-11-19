package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/app/query"
	"github.com/cylonchau/kube-res-foresight/pkg/dao/rdb"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var logSeq int32

func TestTheBoundOfTheCountIsTheSameInTheDatabaseLayerAndInTheAnswer(t *testing.T) {
	assert.EqualValues(t, rdb.MaxCountedRows, query.CappedTotal)
}

func logRig(t *testing.T) (*gin.Engine, *gorm.DB) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:logwin%d?mode=memory&cache=shared", atomic.AddInt32(&logSeq, 1))), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.OperationLog{}, &model.LoginAuditLog{}, &model.SysConfig{}, &model.Job{}))
	cfg := rdb.NewSysConfigDAO(db)
	r := gin.New()
	r.GET("/operation-logs", (&OperationLogRouter{DAO: rdb.NewOperationLogDAO(db), Config: cfg}).List)
	r.GET("/login-logs", (&LoginLogRouter{DAO: rdb.NewLoginAuditLogDAO(db), Config: cfg}).List)
	jobs := NewJobRouter(rdb.NewJobDAO(db))
	jobs.Config = cfg
	r.GET("/jobs", jobs.List)
	return r, db
}

func getJSON(t *testing.T, r http.Handler, path string) map[string]any {
	t.Helper()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	return decodeData(t, w.Body.Bytes())
}

func TestATextSearchOfTheOperationLogThatNamesNoTimeReadsTheLastDaysAndSaysSo(t *testing.T) {
	r, db := logRig(t)
	now := time.Now().UTC()
	for _, at := range []time.Time{now.Add(-30 * 24 * time.Hour), now.Add(-20 * 24 * time.Hour), now.Add(-time.Hour)} {
		require.NoError(t, db.Create(&model.OperationLog{CreatedAt: at, Username: "alice", Path: "/api/v1/modules", Method: "POST", StatusCode: 200}).Error)
	}
	all := getJSON(t, r, "/operation-logs?page=1&page_size=20")
	assert.EqualValues(t, 3, all["total"], "a list with no search reads everything")
	assert.EqualValues(t, 0, all["search_window_days"])

	found := getJSON(t, r, "/operation-logs?page=1&page_size=20&keyword=alice")
	assert.EqualValues(t, 1, found["total"], "the search reads the last 7 days: the older rows are for a search that names its range")
	assert.EqualValues(t, 7, found["search_window_days"])

	since := now.AddDate(0, 0, -40).Format(time.RFC3339)
	ranged := getJSON(t, r, "/operation-logs?page=1&page_size=20&keyword=alice&start_time="+since)
	assert.EqualValues(t, 3, ranged["total"], "a search with its own range is read as asked")
	assert.EqualValues(t, 0, ranged["search_window_days"])
}

func TestASearchOfTheLoginHistoryByNameOrAddressReadsTheLastDaysAndSaysSo(t *testing.T) {
	r, db := logRig(t)
	now := time.Now().UTC()
	for _, at := range []time.Time{now.Add(-40 * 24 * time.Hour), now.Add(-time.Hour)} {
		require.NoError(t, db.Create(&model.LoginAuditLog{CreatedAt: at, Username: "bob", ClientIP: "10.0.0.7", Success: true}).Error)
	}
	assert.EqualValues(t, 2, getJSON(t, r, "/login-logs?page=1&page_size=20")["total"])
	byName := getJSON(t, r, "/login-logs?page=1&page_size=20&username=bob")
	assert.EqualValues(t, 1, byName["total"])
	assert.EqualValues(t, 7, byName["search_window_days"])
	byAddress := getJSON(t, r, "/login-logs?page=1&page_size=20&client_ip=10.0")
	assert.EqualValues(t, 1, byAddress["total"])
	assert.EqualValues(t, 7, byAddress["search_window_days"])
}

func TestASearchOfTheJobsByTheirResultReadsTheLastDaysAndSaysSo(t *testing.T) {
	r, db := logRig(t)
	now := time.Now().UTC()
	for i, at := range []time.Time{now.Add(-30 * 24 * time.Hour), now.Add(-time.Hour)} {
		require.NoError(t, db.Create(&model.Job{JobType: "forecast.run", Status: "success", Result: "all fine", CreatedAt: at, IdempotencyKey: fmt.Sprintf("k%d", i)}).Error)
	}
	assert.EqualValues(t, 2, getJSON(t, r, "/jobs?page=1&page_size=20")["total"])
	found := getJSON(t, r, "/jobs?page=1&page_size=20&keyword=fine")
	assert.EqualValues(t, 1, found["total"])
	assert.EqualValues(t, 7, found["search_window_days"])
}
