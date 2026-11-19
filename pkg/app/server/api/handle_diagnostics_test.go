package api

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/dao/rdb"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/cylonchau/kube-res-foresight/pkg/reqstat"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var diagSeq int32

func TestTheRequestDiagnosisAnswersTheBaselineTheLayerAndTheFindingsOfTheLastHours(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:diag%d?mode=memory&cache=shared", atomic.AddInt32(&diagSeq, 1))), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.RequestStat{}, &model.ProcessBoot{}, &model.SysConfig{}))
	svc := reqstat.New(rdb.NewRequestStatDAO(db), rdb.NewProcessBootDAO(db), reqstat.NewRecorder(), "test")
	reqstat.SetDefault(svc)
	t.Cleanup(func() { reqstat.SetDefault(nil) })
	for i := 0; i < 12; i++ {
		svc.Recorder.Observe(reqstat.Observation{Method: "GET", Route: "/api/v1/modules/:id/compare", Status: 200, Took: 45 * time.Second, DBMs: 40000, DBCalls: 3, RequestID: "op-1"})
	}
	router := &DiagnosticsRouter{Diagnostics: rdb.NewDiagnosticsDAO(db), Config: rdb.NewSysConfigDAO(db)}
	engine := gin.New()
	engine.GET("/diagnostics/requests", router.Requests)

	w := httptest.NewRecorder()
	engine.ServeHTTP(w, httptest.NewRequest("GET", "/diagnostics/requests?hours=6", nil))
	require.Equal(t, 200, w.Code, w.Body.String())
	var body struct {
		Data struct {
			Hours    int `json:"hours"`
			GatewayS int `json:"gateway_seconds"`
			Routes   []struct {
				Route string `json:"route"`
				Count int64  `json:"count"`
				P95   int64  `json:"p95_ms"`
				Layer string `json:"layer"`
			} `json:"routes"`
			Findings []struct {
				Code  string `json:"code"`
				Layer string `json:"layer"`
			} `json:"findings"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, 6, body.Data.Hours)
	assert.Equal(t, 60, body.Data.GatewayS, "the gateway time is the system parameter, 60 s by default")
	require.Len(t, body.Data.Routes, 1)
	assert.EqualValues(t, 12, body.Data.Routes[0].Count)
	assert.EqualValues(t, 45000, body.Data.Routes[0].P95)
	assert.Equal(t, "database", body.Data.Routes[0].Layer)
	require.NotEmpty(t, body.Data.Findings)
	assert.Equal(t, reqstat.FindingHalfGateway, body.Data.Findings[0].Code)
	assert.Equal(t, "database", body.Data.Findings[0].Layer)
}

func TestTheRequestDiagnosisSaysWhenTheStatisticsAreNotRunning(t *testing.T) {
	gin.SetMode(gin.TestMode)
	reqstat.SetDefault(nil)
	engine := gin.New()
	engine.GET("/diagnostics/requests", (&DiagnosticsRouter{}).Requests)
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, httptest.NewRequest("GET", "/diagnostics/requests", nil))
	assert.Equal(t, 500, w.Code)
}
