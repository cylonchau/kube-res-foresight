package api

import (
	"fmt"
	"github.com/cylonchau/kube-resource-fcst/pkg/dao/rdb"
	"github.com/cylonchau/kube-resource-fcst/pkg/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestJobTypesListRegisteredTasksAndTypesAlreadyInTheTable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := apiTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.Job{}))
	require.NoError(t, db.Create(&model.Job{JobType: "llm_rating", IdempotencyKey: "a"}).Error)
	require.NoError(t, db.Create(&model.Job{JobType: "llm_rating", IdempotencyKey: "b"}).Error)
	require.NoError(t, db.Create(&model.Job{JobType: "tagging", IdempotencyKey: "c"}).Error)

	router := gin.New()
	router.GET("/jobs/types", NewJobRouter(rdb.NewJobDAO(db)).Types)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("GET", "/jobs/types", nil))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var out struct {
		Data []string `json:"data"`
	}
	decode(t, w.Body.Bytes(), &out)
	assert.Contains(t, out.Data, "llm_rating")
	assert.Contains(t, out.Data, "tagging")
	count := 0
	for _, kind := range out.Data {
		if kind == "llm_rating" {
			count++
		}
	}
	assert.Equal(t, 1, count, "each type once")
}

func TestJobBatchResetRequeuesFailedJobsAndSkipsRunningOnes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := apiTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.Job{}))
	failed := model.Job{JobType: "x", IdempotencyKey: "f", Status: "failed"}
	running := model.Job{JobType: "x", IdempotencyKey: "r", Status: "running", ClaimedBy: "w1"}
	require.NoError(t, db.Create(&failed).Error)
	require.NoError(t, db.Create(&running).Error)

	router := gin.New()
	router.POST("/jobs/batch-reset", NewJobRouter(rdb.NewJobDAO(db)).BatchReset)
	body := fmt.Sprintf(`{"ids":[%d,%d]}`, failed.ID, running.ID)
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/jobs/batch-reset", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var out struct {
		Data struct{ Requested, Reset int64 } `json:"data"`
	}
	decode(t, w.Body.Bytes(), &out)
	assert.EqualValues(t, 2, out.Data.Requested)
	assert.EqualValues(t, 1, out.Data.Reset)

	var gotFailed, gotRunning model.Job
	require.NoError(t, db.First(&gotFailed, failed.ID).Error)
	assert.Equal(t, "pending", gotFailed.Status)
	require.NoError(t, db.First(&gotRunning, running.ID).Error)
	assert.Equal(t, "running", gotRunning.Status)
}
