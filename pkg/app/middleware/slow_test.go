package middleware

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func fixedLimit(d time.Duration) func() time.Duration { return func() time.Duration { return d } }

func TestSlowRequestsLetEveryRequestThroughAndTheStreamsAlone(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(SlowRequests(fixedLimit(5 * time.Millisecond)))
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

// Every response carries the id of its request: the one the client sent, or a new one; a hostile id is not repeated.
func TestEveryResponseCarriesTheRequestID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(SlowRequests(nil))
	var seen string
	r.GET("/x", func(c *gin.Context) { seen = c.GetString("requestId"); c.String(200, "ok") })

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/x", nil)
	req.Header.Set(RequestIDHeader, "abc123")
	r.ServeHTTP(w, req)
	assert.Equal(t, "abc123", w.Header().Get(RequestIDHeader))
	assert.Equal(t, "abc123", seen)

	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/x", nil))
	assert.True(t, strings.HasPrefix(w.Header().Get(RequestIDHeader), "op-"), "a request with none gets one")

	w = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/x", nil)
	req.Header.Set(RequestIDHeader, strings.Repeat("a", 65))
	r.ServeHTTP(w, req)
	assert.NotEqual(t, strings.Repeat("a", 65), w.Header().Get(RequestIDHeader), "an id too long to be one is replaced")
}

// A request whose client went away still finishes, and is told to the log: nothing here may panic or change the answer.
func TestAnAbandonedRequestIsHandledLikeAnyOther(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(SlowRequests(fixedLimit(time.Hour)))
	r.GET("/x", func(c *gin.Context) { c.String(200, "ok") })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/x", nil).WithContext(ctx))
	assert.Equal(t, 200, w.Code)
	assert.NotEmpty(t, w.Header().Get(RequestIDHeader))
}
