package api

import (
	"context"
	"encoding/json"
	"github.com/coder/websocket"
	"github.com/cylonchau/kube-res-foresight/pkg/app/middleware"
	"github.com/cylonchau/kube-res-foresight/pkg/dao/rdb"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestParseStatsTopicsKeepsKnownTopicsOnce(t *testing.T) {
	assert.Equal(t, []string{"tasks"}, parseStatsTopics("workflow, nope,tasks,tasks,"))
	assert.Equal(t, []string{"async", "tasks"}, parseStatsTopics("async,tasks"))
	assert.Empty(t, parseStatsTopics("nope"))
	assert.Empty(t, parseStatsTopics(""))
}

func TestStatsStreamPushesTheTopicThatChanged(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := apiTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.TaskDefinition{}))
	h := &StatsRouter{DB: db}
	r := gin.New()
	r.GET("/stream", h.StreamStats)
	server := httptest.NewServer(r)
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	url := "ws" + strings.TrimPrefix(server.URL, "http") + "/stream"
	conn, _, err := websocket.Dial(ctx, url+"?topics=tasks", &websocket.DialOptions{Subprotocols: []string{middleware.WebSocketAuthSubprotocol}})
	require.NoError(t, err)
	defer conn.CloseNow()

	require.NoError(t, db.Create(&model.TaskDefinition{TaskType: "test", CronExpr: "@every 1h"}).Error)

	_, payload, err := conn.Read(ctx)
	require.NoError(t, err)
	var message struct {
		Type string            `json:"type"`
		Data map[string]string `json:"data"`
	}
	require.NoError(t, json.Unmarshal(payload, &message))
	assert.Equal(t, "stats", message.Type)
	assert.Equal(t, "tasks", message.Data["topic"])
}

func TestStatsStreamRejectsUnknownTopics(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &StatsRouter{DB: apiTestDB(t)}
	r := gin.New()
	r.GET("/stream", h.StreamStats)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/stream?topics=nope", nil))
	assert.Equal(t, 400, w.Code)
}

// a background task that reports progress or ends is announced to the pages that watch the topic, so they re-read their list instead of polling it
func TestStatsStreamPushesWhenABackgroundTaskChangesStateOrReportsProgress(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := apiTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.AsyncTask{}))
	task := model.AsyncTask{OwnerID: 1, Kind: "forecast.step", Status: model.AsyncRunning, MaxAttempts: 1}
	require.NoError(t, db.Create(&task).Error)
	dao := rdb.NewAsyncTaskDAO(db)

	h := &StatsRouter{DB: db}
	r := gin.New()
	r.GET("/stream", h.StreamStats)
	server := httptest.NewServer(r)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/stream?topics=async", &websocket.DialOptions{Subprotocols: []string{middleware.WebSocketAuthSubprotocol}})
	require.NoError(t, err)
	defer conn.CloseNow()

	read := func() string {
		_, payload, err := conn.Read(ctx)
		require.NoError(t, err)
		var message struct {
			Data map[string]string `json:"data"`
		}
		require.NoError(t, json.Unmarshal(payload, &message))
		return message.Data["topic"]
	}
	time.Sleep(1100 * time.Millisecond) // the stamps of the database are in seconds
	require.NoError(t, dao.Progress(ctx, task.ID, 40, "half"))
	assert.Equal(t, "async", read(), "progress is a change")
	time.Sleep(1100 * time.Millisecond)
	_, err = dao.Finish(ctx, task.ID, rdb.FinishInfo{Status: model.AsyncSucceeded, Result: "{}", At: time.Now().UTC()})
	require.NoError(t, err)
	assert.Equal(t, "async", read(), "so is the end")
}
