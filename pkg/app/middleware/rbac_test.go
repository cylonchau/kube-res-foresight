package middleware

import (
	"context"
	"github.com/cylonchau/kubeforecast/pkg/apis"
	"github.com/cylonchau/kubeforecast/pkg/dao/rdb"
	"github.com/cylonchau/kubeforecast/pkg/model"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDeveloperCredentialPermissionBoundary(t *testing.T) {
	t.Setenv("KUBEFORECAST_SECRET_KEY", "12345678901234567890123456789012")
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.SecretKey{}, &model.Permission{}, &model.RefreshTokenSession{}))

	permission := &model.Permission{
		Name: "List jobs", Code: "task:job:list", Type: "BUTTON", Status: true,
		BeApis: `["GET /api/v1/jobs","GET /api/v1/secret-keys"]`,
	}
	require.NoError(t, db.Create(permission).Error)
	enabled := true
	created, err := rdb.NewSecretKeyDAO(db).Create(context.Background(), &apis.SecretKeyCreateReq{
		Name: "automation", ServerEnabled: true, PermissionIDs: []uint{permission.ID}, Status: &enabled,
	})
	require.NoError(t, err)
	accessKey, accessSecret := created.AccessKey, created.AccessSecret

	gin.SetMode(gin.TestMode)
	router := gin.New()
	group := router.Group("/api/v1")
	group.Use(JWTAuthMiddleware(rdb.NewRefreshTokenSessionDAO(db), rdb.NewSecretKeyDAO(db)))
	group.Use(CasbinMiddleware(rdb.NewPermissionDAO(db)))
	group.GET("/jobs", func(c *gin.Context) { c.Status(http.StatusNoContent) })
	group.GET("/secret-keys", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	allowed := httptest.NewRequest(http.MethodGet, "/api/v1/jobs", nil)
	allowed.Header.Set("X-KubeForecast-AccessKey", accessKey)
	allowed.Header.Set("X-KubeForecast-AccessSecret", accessSecret)
	allowedResponse := httptest.NewRecorder()
	router.ServeHTTP(allowedResponse, allowed)
	require.Equal(t, http.StatusNoContent, allowedResponse.Code)

	forbidden := httptest.NewRequest(http.MethodGet, "/api/v1/secret-keys", nil)
	forbidden.Header.Set("Authorization", "KubeForecast-AK "+accessKey+":"+accessSecret)
	forbiddenResponse := httptest.NewRecorder()
	router.ServeHTTP(forbiddenResponse, forbidden)
	require.Equal(t, http.StatusForbidden, forbiddenResponse.Code)

	invalid := httptest.NewRequest(http.MethodGet, "/api/v1/jobs", nil)
	invalid.Header.Set("X-KubeForecast-AccessKey", accessKey)
	invalid.Header.Set("X-KubeForecast-AccessSecret", "wrong")
	invalidResponse := httptest.NewRecorder()
	router.ServeHTTP(invalidResponse, invalid)
	require.Equal(t, http.StatusUnauthorized, invalidResponse.Code)
}
