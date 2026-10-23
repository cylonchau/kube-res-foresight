package middleware

import (
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSlowRequestsLetEveryRequestThroughAndTheStreamsAlone(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(SlowRequests(5 * time.Millisecond))
	r.GET("/slow", func(c *gin.Context) { time.Sleep(15 * time.Millisecond); c.String(200, "ok") })
	r.GET("/fast", func(c *gin.Context) { c.String(200, "ok") })
	r.GET("/x/stream", func(c *gin.Context) { time.Sleep(15 * time.Millisecond); c.String(200, "ok") })
	for _, path := range []string{"/slow", "/fast", "/x/stream", "/unknown"} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if path == "/unknown" {
			assert.Equal(t, 404, w.Code)
		} else {
			assert.Equal(t, 200, w.Code, path)
		}
	}
}
