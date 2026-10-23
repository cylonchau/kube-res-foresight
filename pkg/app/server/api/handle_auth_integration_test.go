package api

import (
	"encoding/json"
	"github.com/cylonchau/kubeforecast/pkg/app/utils"
	"github.com/cylonchau/kubeforecast/pkg/dao/rdb"
	"github.com/cylonchau/kubeforecast/pkg/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"net/http"
	"testing"
)

func authAPIRouter(t *testing.T) (*gin.Engine, *model.User) {
	t.Helper()
	db := workflowAPIDB(t)
	require.NoError(t, db.AutoMigrate(&model.SysConfig{}, &model.SystemMarker{}, &model.RefreshTokenSession{}, &model.LoginFailureRecord{}, &model.LoginCaptchaChallenge{}, &model.LoginAuditLog{}))
	previousDB := model.DB
	model.DB = db
	t.Cleanup(func() { model.DB = previousDB })
	previousSecret := append([]byte(nil), utils.SecretKey...)
	utils.SetSecretKey([]byte("01234567890123456789012345678901"))
	t.Cleanup(func() { utils.SetSecretKey(previousSecret) })
	permission := model.Permission{Name: "Home", Code: "home", Type: "MENU", FeRoute: "/home", Status: true, Visible: true}
	require.NoError(t, db.Create(&permission).Error)
	role := model.Role{Name: "Admin", Code: "ADMIN", Status: true, Permissions: []model.Permission{permission}}
	hash, err := utils.HashPassword("secret123")
	require.NoError(t, err)
	user := &model.User{Username: "admin", PasswordHash: hash, NickName: "Administrator", Status: true, Roles: []model.Role{role}}
	require.NoError(t, db.Create(user).Error)
	h := &AuthRouter{UserDAO: rdb.NewUserDAO(db), PermDAO: rdb.NewPermissionDAO(db), ConfigDAO: rdb.NewSysConfigDAO(db), RefreshDAO: rdb.NewRefreshTokenSessionDAO(db), SecurityDAO: rdb.NewLoginSecurityDAO(db), LoginAuditDAO: rdb.NewLoginAuditLogDAO(db)}
	r := gin.New()
	r.POST("/auth/login", h.Login)
	r.POST("/auth/login/otp", h.LoginOTP)
	r.POST("/auth/refresh", h.RefreshToken)
	r.POST("/auth/logout", h.Logout)
	protected := r.Group("/auth")
	protected.Use(func(c *gin.Context) { c.Set("userId", user.ID); c.Set("username", user.Username); c.Next() })
	protected.GET("/info", h.GetInfo)
	protected.GET("/routers", h.GetRouters)
	protected.GET("/session-status", h.SessionStatus)
	protected.POST("/otp/generate", h.GenerateOTP)
	protected.POST("/otp/bind", h.BindOTP)
	protected.POST("/otp/unbind", h.UnbindOTP)
	protected.PUT("/password", h.UpdatePassword)
	protected.PUT("/profile", h.UpdateProfile)
	protected.PUT("/language", h.UpdateLanguage)
	return r, user
}

func responseData(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var payload struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(body, &payload))
	return payload.Data
}

func TestAuthAPIAccountAndTokenLifecycle(t *testing.T) {
	router, user := authAPIRouter(t)
	rec := performJSON(t, router, http.MethodPost, "/auth/login", "", map[string]any{"username": "admin", "password": "secret123"})
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	data := responseData(t, rec.Body.Bytes())
	refresh, ok := data["refreshToken"].(string)
	require.True(t, ok)
	require.NotEmpty(t, refresh)

	rec = performJSON(t, router, http.MethodPost, "/auth/login", "", map[string]any{"username": "admin", "password": "secret123"})
	assert.Equal(t, http.StatusUnauthorized, rec.Code, rec.Body.String())
	rec = performJSON(t, router, http.MethodPost, "/auth/login", "", map[string]any{"username": "admin", "password": "secret123", "force": true})
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	data = responseData(t, rec.Body.Bytes())
	refresh = data["refreshToken"].(string)
	rec = performJSON(t, router, http.MethodPost, "/auth/refresh", "", map[string]any{"refresh_token": refresh})
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	data = responseData(t, rec.Body.Bytes())
	rotated := data["refreshToken"].(string)
	rec = performJSON(t, router, http.MethodPost, "/auth/logout", "", map[string]any{"refresh_token": rotated})
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	for _, path := range []string{"/auth/info", "/auth/routers", "/auth/session-status"} {
		rec = performJSON(t, router, http.MethodGet, path, "", nil)
		assert.Equal(t, http.StatusOK, rec.Code, "%s: %s", path, rec.Body.String())
	}
	rec = performJSON(t, router, http.MethodPut, "/auth/profile", "", map[string]any{"email": "admin@example.com", "phone": "123"})
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rec = performJSON(t, router, http.MethodPut, "/auth/language", "", map[string]any{"language": "zh-CN"})
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rec = performJSON(t, router, http.MethodPost, "/auth/otp/generate", "", nil)
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "qr_code_data")
	rec = performJSON(t, router, http.MethodPut, "/auth/password", "", map[string]any{"old_password": "wrong", "password": "new-secret"})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	rec = performJSON(t, router, http.MethodPut, "/auth/password", "", map[string]any{"old_password": "secret123", "password": "new-secret"})
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var updated model.User
	require.NoError(t, model.DB.First(&updated, user.ID).Error)
	assert.Equal(t, "zh-CN", updated.Language)
	assert.Equal(t, "admin@example.com", updated.Email)
	assert.True(t, utils.ComparePassword(updated.PasswordHash, "new-secret"))
}

func TestAuthAPIRejectsInvalidRequests(t *testing.T) {
	router, _ := authAPIRouter(t)
	cases := []struct {
		method, path string
		body         any
		status       int
	}{
		{http.MethodPost, "/auth/login", map[string]any{}, http.StatusBadRequest},
		{http.MethodPost, "/auth/login", map[string]any{"username": "admin", "password": "wrong"}, http.StatusUnauthorized},
		{http.MethodPost, "/auth/login/otp", map[string]any{"login_ticket": "invalid", "otp_code": "000000"}, http.StatusUnauthorized},
		{http.MethodPost, "/auth/refresh", map[string]any{"refresh_token": "invalid"}, http.StatusUnauthorized},
		{http.MethodPost, "/auth/otp/bind", map[string]any{"secret": "invalid", "code": "000000"}, http.StatusUnauthorized},
		{http.MethodPost, "/auth/otp/unbind", map[string]any{"code": "000000"}, http.StatusBadRequest},
	}
	for _, tc := range cases {
		rec := performJSON(t, router, tc.method, tc.path, "", tc.body)
		assert.Equal(t, tc.status, rec.Code, "%s %s: %s", tc.method, tc.path, rec.Body.String())
	}
}
