package api

import (
	"context"
	"encoding/json"
	"github.com/coder/websocket"
	"github.com/cylonchau/kubeforecast/pkg/app/middleware"
	"github.com/cylonchau/kubeforecast/pkg/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestParseStatsTopicsKeepsKnownTopicsOnce(t *testing.T) {
	assert.Equal(t, []string{"workflow", "tasks"}, parseStatsTopics("workflow, nope,tasks,workflow,"))
	assert.Empty(t, parseStatsTopics("nope"))
	assert.Empty(t, parseStatsTopics(""))
}

func TestStatsStreamPushesTheTopicThatChanged(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := workflowAPIDB(t)
	require.NoError(t, db.AutoMigrate(&model.WorkflowDef{}))
	h := &StatsRouter{DB: db}
	r := gin.New()
	r.GET("/stream", h.StreamStats)
	server := httptest.NewServer(r)
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	url := "ws" + strings.TrimPrefix(server.URL, "http") + "/stream"
	conn, _, err := websocket.Dial(ctx, url+"?topics=workflow,tasks", &websocket.DialOptions{Subprotocols: []string{middleware.WebSocketAuthSubprotocol}})
	require.NoError(t, err)
	defer conn.CloseNow()

	require.NoError(t, db.Create(&model.WorkflowDef{WorkflowKey: "test", Name: "Test"}).Error)

	_, payload, err := conn.Read(ctx)
	require.NoError(t, err)
	var message struct {
		Type string            `json:"type"`
		Data map[string]string `json:"data"`
	}
	require.NoError(t, json.Unmarshal(payload, &message))
	assert.Equal(t, "stats", message.Type)
	assert.Equal(t, "workflow", message.Data["topic"])
}

func TestStatsStreamRejectsUnknownTopics(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &StatsRouter{DB: workflowAPIDB(t)}
	r := gin.New()
	r.GET("/stream", h.StreamStats)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/stream?topics=nope", nil))
	assert.Equal(t, 400, w.Code)
}
