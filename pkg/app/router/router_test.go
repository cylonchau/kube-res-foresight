package router

import (
	"github.com/cylonchau/kube-resource-fcst/pkg/model"
	"github.com/gin-gonic/gin"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProtectedAuthRoutesRequireJWT(t *testing.T) {
	gin.SetMode(gin.TestMode)
	previousDB := model.DB
	model.DB = nil
	t.Cleanup(func() { model.DB = previousDB })

	engine := gin.New()
	RegisteredRouter(engine)

	for _, path := range []string{
		"/api/v1/auth/info",
		"/api/v1/auth/get-routers",
		"/api/v1/auth/session-status",
		"/api/v1/tasks",
		"/api/v1/users",
		"/api/v1/roles",
		"/api/v1/jobs",
	} {
		t.Run(path, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, path, nil)
			engine.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusUnauthorized {
				t.Fatalf("GET %s without a token returned %d, want %d; body=%s", path, recorder.Code, http.StatusUnauthorized, recorder.Body.String())
			}
		})
	}
}
