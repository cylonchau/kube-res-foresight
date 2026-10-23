package middleware

import (
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newCORSTestRouter(allowedOrigins []string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(CORS(allowedOrigins))
	router.GET("/ping", func(c *gin.Context) { c.String(http.StatusOK, "pong") })
	return router
}

func TestCORS_EmptyAllowlistSendsNoHeaders(t *testing.T) {
	router := newCORSTestRouter(nil)
	req := httptest.NewRequest(http.MethodGet, "/ping", nil)
	req.Header.Set("Origin", "http://localhost:10000")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Empty(t, rec.Header().Get("Access-Control-Allow-Origin"), "an empty allowlist must not send any CORS headers at all")
}

func TestCORS_AllowedOriginGetsEchoedBack(t *testing.T) {
	router := newCORSTestRouter([]string{"http://localhost:10000"})
	req := httptest.NewRequest(http.MethodGet, "/ping", nil)
	req.Header.Set("Origin", "http://localhost:10000")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "http://localhost:10000", rec.Header().Get("Access-Control-Allow-Origin"))
	assert.Equal(t, "true", rec.Header().Get("Access-Control-Allow-Credentials"))
}

func TestCORS_DisallowedOriginGetsNoHeaders(t *testing.T) {
	router := newCORSTestRouter([]string{"http://localhost:10000"})
	req := httptest.NewRequest(http.MethodGet, "/ping", nil)
	req.Header.Set("Origin", "http://evil.example")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code, "the request itself still succeeds — the browser is what enforces CORS, using the missing header")
	assert.Empty(t, rec.Header().Get("Access-Control-Allow-Origin"))
}

func TestCORS_PreflightOptionsShortCircuits(t *testing.T) {
	router := newCORSTestRouter([]string{"http://localhost:10000"})
	req := httptest.NewRequest(http.MethodOptions, "/ping", nil)
	req.Header.Set("Origin", "http://localhost:10000")
	req.Header.Set("Access-Control-Request-Method", "POST")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusNoContent, rec.Code)
	assert.Equal(t, "http://localhost:10000", rec.Header().Get("Access-Control-Allow-Origin"))
	assert.Contains(t, rec.Header().Get("Access-Control-Allow-Methods"), "POST")
}

func TestCORS_NoWildcard(t *testing.T) {
	router := newCORSTestRouter([]string{"http://localhost:10000"})
	req := httptest.NewRequest(http.MethodGet, "/ping", nil)
	req.Header.Set("Origin", "http://localhost:10000")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	assert.NotEqual(t, "*", rec.Header().Get("Access-Control-Allow-Origin"), "wildcard is incompatible with Allow-Credentials: true")
}
