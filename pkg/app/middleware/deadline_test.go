package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func deadlineRouter(budget func() time.Duration) (*gin.Engine, *[]time.Duration) {
	gin.SetMode(gin.TestMode)
	left := []time.Duration{}
	r := gin.New()
	r.Use(ReadDeadline(budget))
	handler := func(c *gin.Context) {
		if d, ok := c.Request.Context().Deadline(); ok {
			left = append(left, time.Until(d))
		} else {
			left = append(left, -1)
		}
		c.Status(http.StatusOK)
	}
	r.GET("/list", handler)
	r.POST("/list", handler)
	r.GET("/stream", handler)
	return r, &left
}

func call(r *gin.Engine, method, path string, header map[string]string) {
	req := httptest.NewRequest(method, path, nil)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	r.ServeHTTP(httptest.NewRecorder(), req)
}

func TestAReadRequestHasADeadlineAndAWriteALongWaitAndAStreamHaveNone(t *testing.T) {
	r, left := deadlineRouter(func() time.Duration { return 20 * time.Second })
	call(r, "GET", "/list", nil)
	call(r, "POST", "/list", nil)
	call(r, "GET", "/list", map[string]string{"X-Long-Request": "1"})
	call(r, "GET", "/stream", nil)
	got := *left
	assert.Greater(t, got[0], 15*time.Second)
	assert.LessOrEqual(t, got[0], 20*time.Second)
	assert.EqualValues(t, -1, got[1], "a write is not cut half way")
	assert.EqualValues(t, -1, got[2], "a request that asks for a long wait has its own limits")
	assert.EqualValues(t, -1, got[3], "a stream is long by nature")
}

func TestTheDeadlineEndsTheContextOfTheStatementsOfTheRequest(t *testing.T) {
	r, _ := deadlineRouter(func() time.Duration { return 30 * time.Millisecond })
	done := make(chan error, 1)
	r.GET("/slow", func(c *gin.Context) {
		select {
		case <-c.Request.Context().Done():
			done <- c.Request.Context().Err()
		case <-time.After(2 * time.Second):
			done <- nil
		}
	})
	call(r, "GET", "/slow", nil)
	assert.ErrorIs(t, <-done, context.DeadlineExceeded)
}

func TestNoBudgetMeansNoDeadline(t *testing.T) {
	for _, budget := range []func() time.Duration{nil, func() time.Duration { return 0 }} {
		r, left := deadlineRouter(budget)
		call(r, "GET", "/list", nil)
		assert.EqualValues(t, -1, (*left)[0])
	}
}

func TestAListThatPagesTooDeepIsRefusedWithAReasonAndAShallowOneIsNot(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(PageDepth(1000))
	r.GET("/list", func(c *gin.Context) { c.Status(http.StatusOK) })
	r.POST("/list", func(c *gin.Context) { c.Status(http.StatusOK) })
	code := func(method, url string) int {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(method, url, nil))
		return w.Code
	}
	assert.Equal(t, 200, code("GET", "/list"))
	assert.Equal(t, 200, code("GET", "/list?page=1&page_size=200"))
	assert.Equal(t, 200, code("GET", "/list?page=51&page_size=20"), "1000 rows skipped is the limit")
	assert.Equal(t, 400, code("GET", "/list?page=52&page_size=20"))
	assert.Equal(t, 400, code("GET", "/list?page=7&page_size=200"))
	assert.Equal(t, 400, code("GET", "/list?page=100000&page_size=99999"), "a size out of range is the default size")
	assert.Equal(t, 200, code("POST", "/list?page=100000"), "only reads are held to it")
}
