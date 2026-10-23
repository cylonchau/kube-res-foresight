package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	apputils "github.com/cylonchau/kubeforecast/pkg/app/utils"
	"github.com/cylonchau/kubeforecast/pkg/model"
	cryptoutils "github.com/cylonchau/kubeforecast/pkg/utils"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"net/http"
	"net/http/httptest"
	"testing"
)

func nodeRouter(t *testing.T) (*gin.Engine, *gorm.DB) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.TelegramBotAPINode{}))
	h := &BotAPINodeAgentRouter{DB: db}
	r := gin.New()
	r.POST("/nodes/register", h.Register)
	r.POST("/nodes/:id/heartbeat", h.Heartbeat)
	r.GET("/nodes/:id/config", h.GetConfigSnapshot)
	return r, db
}

func nodeRequest(t *testing.T, r http.Handler, method, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	req := httptest.NewRequest(method, path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func TestBotAPINodeAgentLifecycle(t *testing.T) {
	router, db := nodeRouter(t)
	enrollment := "01234567890123456789012345678901"
	rec := nodeRequest(t, router, http.MethodPost, "/nodes/register", enrollment, map[string]any{"name": "node-a", "token": enrollment, "hostname": "host"})
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "pending")
	var node model.TelegramBotAPINode
	require.NoError(t, db.Where("name = ?", "node-a").First(&node).Error)
	rec = nodeRequest(t, router, http.MethodPost, "/nodes/register", enrollment, map[string]any{"node_id": node.ID, "name": "node-a", "token": enrollment})
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	issued := "issued-agent-token"
	encrypted, err := cryptoutils.EncryptValue(issued)
	require.NoError(t, err)
	require.NoError(t, db.Model(&node).Updates(map[string]any{"status": "approved", "issued_token_encrypted": encrypted, "token_hash": apputils.HashToken(issued), "desired_running": true, "api_id": "123", "api_hash_encrypted": encrypted}).Error)
	rec = nodeRequest(t, router, http.MethodPost, "/nodes/register", enrollment, map[string]any{"node_id": node.ID, "name": "node-a", "token": enrollment})
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), issued)
	rec = nodeRequest(t, router, http.MethodPost, fmt.Sprintf("/nodes/%d/heartbeat", node.ID), issued, map[string]any{"observed_running": true, "observed_container": "container", "hostname": "new-host"})
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rec = nodeRequest(t, router, http.MethodGet, fmt.Sprintf("/nodes/%d/config", node.ID), issued, nil)
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"api_hash":"issued-agent-token"`)

	rec = nodeRequest(t, router, http.MethodGet, fmt.Sprintf("/nodes/%d/config", node.ID), "wrong", nil)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	require.NoError(t, db.Model(&node).Update("enabled", false).Error)
	rec = nodeRequest(t, router, http.MethodGet, fmt.Sprintf("/nodes/%d/config", node.ID), issued, nil)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestBotAPINodeBootstrapAndValidation(t *testing.T) {
	router, _ := nodeRouter(t)
	t.Setenv("KUBEFORECAST_AGENT_BOOTSTRAP_TOKEN", "bootstrap-secret")
	rec := nodeRequest(t, router, http.MethodPost, "/nodes/register", "bootstrap-secret", map[string]any{"name": "auto-node"})
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "online")
	rec = nodeRequest(t, router, http.MethodPost, "/nodes/register", "", map[string]any{})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	rec = nodeRequest(t, router, http.MethodPost, "/nodes/register", "short", map[string]any{"name": "bad", "token": "short"})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	rec = nodeRequest(t, router, http.MethodPost, "/nodes/999/heartbeat", "missing", nil)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}
