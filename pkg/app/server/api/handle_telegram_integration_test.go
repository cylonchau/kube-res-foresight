package api

import (
	"fmt"
	"github.com/cylonchau/kubeforecast/pkg/app/utils"
	"github.com/cylonchau/kubeforecast/pkg/dao/rdb"
	"github.com/cylonchau/kubeforecast/pkg/model"
	"github.com/cylonchau/kubeforecast/pkg/telegram"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"net/http"
	"testing"
)

func telegramAPIRouter(t *testing.T) (*gin.Engine, *model.User) {
	t.Helper()
	db := workflowAPIDB(t)
	require.NoError(t, db.AutoMigrate(&model.SysConfig{}, &model.SystemMarker{}, &model.RefreshTokenSession{}, &model.LoginAuditLog{}, &model.TelegramBinding{}, &model.NotifyTemplate{}, &model.WorkflowInstance{}))
	previousDB := model.DB
	model.DB = db
	t.Cleanup(func() { model.DB = previousDB })
	previousSecret := append([]byte(nil), utils.SecretKey...)
	utils.SetSecretKey([]byte("01234567890123456789012345678901"))
	t.Cleanup(func() { utils.SetSecretKey(previousSecret) })
	role := model.Role{Name: "Admin", Code: "ADMIN", Status: true}
	user := &model.User{Username: "admin", PasswordHash: "x", NickName: "Administrator", Status: true, Roles: []model.Role{role}}
	require.NoError(t, db.Create(user).Error)
	require.NoError(t, db.Create(&model.SysConfig{ConfigKey: telegram.ConfigBotUsername, ConfigValue: "@kubeforecast_bot", ConfigName: "Bot"}).Error)
	require.NoError(t, db.Create(&model.NotifyTemplate{Key: "welcome", Name: "Welcome", BodyTemplate: "Hello", Enabled: true}).Error)
	h := &TelegramRouter{UserDAO: rdb.NewUserDAO(db), BindingDAO: rdb.NewTelegramBindingDAO(db), RefreshDAO: rdb.NewRefreshTokenSessionDAO(db), LoginAuditDAO: rdb.NewLoginAuditLogDAO(db), ConfigDAO: rdb.NewSysConfigDAO(db), TemplateDAO: rdb.NewNotifyTemplateDAO(db), InstanceDAO: rdb.NewWorkflowInstanceDAO(db)}
	r := gin.New()
	r.POST("/telegram/mock-login", h.MockLogin)
	g := r.Group("/telegram")
	g.Use(func(c *gin.Context) { c.Set("userId", user.ID); c.Set("roles", []string{"ADMIN"}); c.Next() })
	g.POST("/bind/generate", h.GenerateBindToken)
	g.POST("/unbind", h.Unbind)
	g.GET("/status", h.GetStatus)
	g.GET("/admin/bindings", h.AdminListBindings)
	g.DELETE("/admin/bindings/:id", h.AdminUnbind)
	g.POST("/admin/bindings/batch-delete", h.AdminBatchUnbind)
	g.GET("/admin/templates", h.AdminListTemplates)
	g.PUT("/admin/templates/:key", h.AdminUpdateTemplate)
	return r, user
}

func TestTelegramManagementAPIEndToEnd(t *testing.T) {
	router, user := telegramAPIRouter(t)
	rec := performJSON(t, router, http.MethodPost, "/telegram/mock-login", "", nil)
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "access_token")
	var binding model.TelegramBinding
	require.NoError(t, model.DB.Where("user_id = ?", user.ID).First(&binding).Error)
	rec = performJSON(t, router, http.MethodGet, "/telegram/status", "", nil)
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "kubeforecast_bot")
	rec = performJSON(t, router, http.MethodGet, "/telegram/admin/bindings?page=1&page_size=10", "", nil)
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rec = performJSON(t, router, http.MethodPost, "/telegram/bind/generate", "", nil)
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "kubeforecast_bot")
	rec = performJSON(t, router, http.MethodGet, "/telegram/admin/templates?keyword=Welcome", "", nil)
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rec = performJSON(t, router, http.MethodPut, "/telegram/admin/templates/welcome", "", map[string]any{"title_template": "Title", "body_template": "Updated", "buttons_json": "[]", "enabled": true})
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rec = performJSON(t, router, http.MethodDelete, fmt.Sprintf("/telegram/admin/bindings/%d", binding.ID), "", nil)
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rec = performJSON(t, router, http.MethodGet, "/telegram/status", "", nil)
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"is_bound":false`)

	rec = performJSON(t, router, http.MethodPost, "/telegram/mock-login", "", nil)
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NoError(t, model.DB.Where("user_id = ?", user.ID).First(&binding).Error)
	rec = performJSON(t, router, http.MethodPost, "/telegram/admin/bindings/batch-delete", "", map[string]any{"ids": []int64{binding.ID}})
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rec = performJSON(t, router, http.MethodPost, "/telegram/unbind", "", nil)
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

func TestTelegramManagementAPIValidation(t *testing.T) {
	router, _ := telegramAPIRouter(t)
	rec := performJSON(t, router, http.MethodDelete, "/telegram/admin/bindings/nope", "", nil)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	rec = performJSON(t, router, http.MethodPost, "/telegram/admin/bindings/batch-delete", "", map[string]any{})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	rec = performJSON(t, router, http.MethodPut, "/telegram/admin/templates/welcome", "", map[string]any{})
	assert.Equal(t, http.StatusOK, rec.Code)
}
