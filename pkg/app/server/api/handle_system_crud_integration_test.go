package api

import (
	"fmt"
	"github.com/cylonchau/kubeforecast/pkg/dao/rdb"
	"github.com/cylonchau/kubeforecast/pkg/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"net/http"
	"testing"
)

func systemCRUDRouter(t *testing.T) (*gin.Engine, *model.Role) {
	t.Helper()
	db := apiTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.SecretKey{}, &model.RefreshTokenSession{}, &model.LoginAuditLog{}))
	previousDB := model.DB
	model.DB = db
	t.Cleanup(func() { model.DB = previousDB })
	role := &model.Role{Name: "Operator", Code: "OPS", Status: true}
	require.NoError(t, db.Create(role).Error)
	permission := model.Permission{Name: "Read APIs", Code: "api-read", Type: "BUTTON", BeApis: `["GET /api/v1/*"]`, Status: true, Visible: true}
	require.NoError(t, db.Create(&permission).Error)

	router := gin.New()
	router.Use(func(c *gin.Context) { c.Set("userId", uint(999)); c.Next() })
	users := &UserRouter{DAO: rdb.NewUserDAO(db), RefreshDAO: rdb.NewRefreshTokenSessionDAO(db), LoginAuditDAO: rdb.NewLoginAuditLogDAO(db)}
	roles := &RoleRouter{DAO: rdb.NewRoleDAO(db)}
	permissions := &PermissionRouter{DAO: rdb.NewPermissionDAO(db)}
	secrets := &SecretKeyRouter{DAO: rdb.NewSecretKeyDAO(db), PermDAO: rdb.NewPermissionDAO(db)}
	router.GET("/users", users.List)
	router.POST("/users", users.Create)
	router.PUT("/users/:id", users.Update)
	router.POST("/users/otp/reset/:id", users.ResetOTP)
	router.DELETE("/users/:id", users.Delete)
	router.POST("/users/batch-delete", users.BatchDelete)
	router.GET("/roles", roles.List)
	router.GET("/roles/all", roles.ListAll)
	router.POST("/roles", roles.Create)
	router.PUT("/roles/:id", roles.Update)
	router.DELETE("/roles/:id", roles.Delete)
	router.POST("/roles/batch-delete", roles.BatchDelete)
	router.GET("/permissions", permissions.ListTree)
	router.POST("/permissions", permissions.Create)
	router.PUT("/permissions/:id", permissions.Update)
	router.DELETE("/permissions/:id", permissions.Delete)
	router.GET("/secret-keys", secrets.List)
	router.GET("/secret-keys/permission-options", secrets.PermissionOptions)
	router.GET("/secret-keys/:id", secrets.Get)
	router.POST("/secret-keys", secrets.Create)
	router.PUT("/secret-keys/:id", secrets.Update)
	router.DELETE("/secret-keys/:id", secrets.Delete)
	return router, role
}

func TestSystemCRUDHandlersEndToEnd(t *testing.T) {
	router, role := systemCRUDRouter(t)
	rec := performJSON(t, router, http.MethodPost, "/users", "", map[string]any{"username": "analyst", "password": "secret123", "nick_name": "Analyst", "status": true, "role_ids": []uint{role.ID}})
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var user model.User
	require.NoError(t, model.DB.Where("username = ?", "analyst").First(&user).Error)
	rec = performJSON(t, router, http.MethodGet, "/users?page=1&page_size=10&username=anal&status=true", "", nil)
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rec = performJSON(t, router, http.MethodPut, fmt.Sprintf("/users/%d", user.ID), "", map[string]any{"password": "new-secret", "nick_name": "Updated", "status": true, "role_ids": []uint{role.ID}})
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rec = performJSON(t, router, http.MethodPost, fmt.Sprintf("/users/otp/reset/%d", user.ID), "", nil)
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	rec = performJSON(t, router, http.MethodPost, "/permissions", "", map[string]any{"name": "Write APIs", "code": "api-write", "type": "BUTTON", "visible": true, "status": true, "be_apis": `["POST /api/v1/*"]`})
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var permission model.Permission
	require.NoError(t, model.DB.Where("code = ?", "api-write").First(&permission).Error)
	sortValue := 2
	rec = performJSON(t, router, http.MethodPut, fmt.Sprintf("/permissions/%d", permission.ID), "", map[string]any{"name": "Write API", "code": "api-write", "type": "BUTTON", "sort": sortValue, "visible": true, "status": true, "be_apis": `["POST /api/v1/*"]`})
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rec = performJSON(t, router, http.MethodGet, "/permissions", "", nil)
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	rec = performJSON(t, router, http.MethodPost, "/roles", "", map[string]any{"name": "Reviewer", "code": "REVIEWER", "status": true, "remark": "review", "permissions": []uint{permission.ID}})
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var reviewer model.Role
	require.NoError(t, model.DB.Where("code = ?", "REVIEWER").First(&reviewer).Error)
	rec = performJSON(t, router, http.MethodGet, "/roles?page=1&page_size=10&role_name=Review&status=true", "", nil)
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rec = performJSON(t, router, http.MethodGet, "/roles/all", "", nil)
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rec = performJSON(t, router, http.MethodPut, fmt.Sprintf("/roles/%d", reviewer.ID), "", map[string]any{"name": "Reviewer Updated", "status": true, "permissions": []uint{permission.ID}})
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	rec = performJSON(t, router, http.MethodPost, "/secret-keys", "", map[string]any{"name": "collector", "server_enabled": true, "permission_ids": []uint{permission.ID}, "status": true})
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var key model.SecretKey
	require.NoError(t, model.DB.Where("name = ?", "collector").First(&key).Error)
	rec = performJSON(t, router, http.MethodGet, "/secret-keys?page=1&page_size=10&keyword=collect", "", nil)
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rec = performJSON(t, router, http.MethodGet, fmt.Sprintf("/secret-keys/%d", key.ID), "", nil)
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rec = performJSON(t, router, http.MethodGet, "/secret-keys/permission-options", "", nil)
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rec = performJSON(t, router, http.MethodPut, fmt.Sprintf("/secret-keys/%d", key.ID), "", map[string]any{"name": "collector-updated", "rotate_secret": true})
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rec = performJSON(t, router, http.MethodDelete, fmt.Sprintf("/secret-keys/%d", key.ID), "", nil)
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	rec = performJSON(t, router, http.MethodDelete, fmt.Sprintf("/permissions/%d", permission.ID), "", nil)
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rec = performJSON(t, router, http.MethodDelete, fmt.Sprintf("/roles/%d", reviewer.ID), "", nil)
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rec = performJSON(t, router, http.MethodDelete, fmt.Sprintf("/users/%d", user.ID), "", nil)
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

func TestSystemCRUDHandlersValidation(t *testing.T) {
	router, _ := systemCRUDRouter(t)
	cases := []struct {
		method, path string
		body         any
	}{
		{http.MethodPost, "/users", map[string]any{}},
		{http.MethodPut, "/users/999", map[string]any{"nick_name": "missing"}},
		{http.MethodPost, "/roles", map[string]any{}},
		{http.MethodPut, "/roles/999", map[string]any{"name": "missing"}},
		{http.MethodPost, "/permissions", map[string]any{}},
		{http.MethodPut, "/permissions/not-number", map[string]any{}},
		{http.MethodPost, "/secret-keys", map[string]any{}},
		{http.MethodGet, "/secret-keys/not-number", nil},
	}
	for _, tc := range cases {
		rec := performJSON(t, router, tc.method, tc.path, "", tc.body)
		assert.GreaterOrEqual(t, rec.Code, 400, "%s %s: %s", tc.method, tc.path, rec.Body.String())
	}
}
