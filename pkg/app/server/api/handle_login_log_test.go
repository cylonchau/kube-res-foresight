package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cylonchau/kubeforecast/pkg/dao/rdb"
	"github.com/cylonchau/kubeforecast/pkg/model"
)

func TestLoginLogListFiltersAndReportsTotal(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := apiTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.LoginAuditLog{}))
	require.NoError(t, db.Create(&[]model.LoginAuditLog{
		{Username: "alice", ClientIP: "10.0.0.1", Success: true, Method: "password", Reason: "ok"},
		{Username: "alice", ClientIP: "10.0.0.9", Success: false, Method: "password", Reason: "invalid_credentials"},
		{Username: "bob", ClientIP: "10.0.0.9", Success: false, Method: "ldap", Reason: "user_disabled"},
	}).Error)
	r := gin.New()
	r.GET("/login-logs", (&LoginLogRouter{DAO: rdb.NewLoginAuditLogDAO(db)}).List)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/login-logs?page=1&page_size=1&result=failed&client_ip=10.0.0.9", nil))
	require.Equal(t, http.StatusOK, w.Code)
	var out struct {
		Data struct {
			List  []model.LoginAuditLog `json:"list"`
			Total int                   `json:"total"`
		} `json:"data"`
	}
	decode(t, w.Body.Bytes(), &out)
	assert.Equal(t, 2, out.Data.Total)
	require.Len(t, out.Data.List, 1)
	assert.Equal(t, "user_disabled", out.Data.List[0].Reason)
}

func TestLoginPolicyReadsSettingsWithinRange(t *testing.T) {
	db := apiTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.SysConfig{}))
	h := &AuthRouter{ConfigDAO: rdb.NewSysConfigDAO(db)}
	ctx := t.Context()
	assert.Equal(t, loginPolicy{threshold: 3, window: 15 * time.Minute}, h.loginPolicy(ctx))

	require.NoError(t, db.Create(&model.SysConfig{ConfigKey: configLoginFailureThreshold, ConfigValue: "5"}).Error)
	require.NoError(t, db.Create(&model.SysConfig{ConfigKey: configLoginFailureWindow, ConfigValue: "30"}).Error)
	assert.Equal(t, loginPolicy{threshold: 5, window: 30 * time.Minute}, h.loginPolicy(ctx))

	require.NoError(t, db.Model(&model.SysConfig{}).Where("config_key = ?", configLoginFailureThreshold).Update("config_value", "0").Error)
	require.NoError(t, db.Model(&model.SysConfig{}).Where("config_key = ?", configLoginFailureWindow).Update("config_value", "abc").Error)
	// config reads are cached per DAO for a few seconds, so a fresh DAO sees the edit
	h = &AuthRouter{ConfigDAO: rdb.NewSysConfigDAO(db)}
	assert.Equal(t, loginPolicy{threshold: 3, window: 15 * time.Minute}, h.loginPolicy(ctx), "invalid values fall back to defaults")
}
