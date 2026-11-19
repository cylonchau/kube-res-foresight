package query

import (
	"context"
	"errors"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestDecodeErrVariants(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code int
	}{{nil, OK.Code}, {ErrParam, ErrParam.Code}, {&Err{Code: 7, Message: "x"}, 7}, {errors.New(`UNIQUE constraint failed: x`), ErrAlreadyExists.Code}, {errors.New(`Duplicate entry x`), ErrAlreadyExists.Code}, {errors.New(`bad "value"`), InternalServerError.Code}} {
		code, _ := DecodeErr(tc.err)
		assert.Equal(t, tc.code, code)
	}
	assert.Equal(t, "Invalid parameters", ErrParam.Error())
	assert.Equal(t, "x", (&Err{Message: "x"}).Error())
}

func TestResponseHelpers(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name    string
		status  int
		handler func(*gin.Context)
	}{
		{"success", 200, func(c *gin.Context) { SuccessResponse(c, nil, "ok") }}, {"api", 200, func(c *gin.Context) { APIResponse(c, nil, nil) }}, {"bad", 400, func(c *gin.Context) { BadRequest(c, ErrParam) }}, {"notfound", 404, func(c *gin.Context) { NotFound(c, ErrRecordNotFound) }}, {"unauthorized", 401, func(c *gin.Context) { Unauthorized(c, ErrNeedAuth) }}, {"unauthorized-data", 401, func(c *gin.Context) { UnauthorizedWithData(c, ErrNeedAuth, map[string]bool{"x": true}) }}, {"forbidden", 403, func(c *gin.Context) { Forbidden(c, ErrNoPermission) }}, {"locked", 423, func(c *gin.Context) { Locked(c, errors.New("locked")) }}, {"internal", 500, func(c *gin.Context) { InternalError(c, nil) }}, {"db-conflict", 400, func(c *gin.Context) { HandleDBError(c, errors.New("UNIQUE constraint failed")) }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := gin.New()
			r.GET("/", tc.handler)
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
			assert.Equal(t, tc.status, rec.Code)
			assert.Contains(t, rec.Body.String(), `"code"`)
		})
	}
}

// A request that ran out of its time limit is answered with a 504 and the reason, marked as the platform's own, instead of an internal error;
// any other error is still a 500, and a client that hung up is still not an error at all.
func TestARequestThatRanOutOfTimeIsAnsweredWithTheReason(t *testing.T) {
	gin.SetMode(gin.TestMode)
	run := func(err error) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/modules/1/run", nil)
		InternalError(c, err)
		return w
	}

	w := run(fmt.Errorf("collect: %w", context.DeadlineExceeded))
	assert.Equal(t, http.StatusGatewayTimeout, w.Code)
	assert.Equal(t, "platform", w.Header().Get(TimeoutSourceHeader), "the platform's own 504 is told from the gateway's")
	assert.Contains(t, w.Body.String(), "http.gateway_timeout_seconds", "the message names the parameter that sets the limit")
	assert.Contains(t, w.Body.String(), "10005")

	assert.Equal(t, http.StatusInternalServerError, run(errors.New("boom")).Code)
	assert.Equal(t, StatusClientClosedRequest, run(context.Canceled).Code)
}

// A handler that reports every failure as a bad request still answers a request that ran out of time as a timeout with the reason.
func TestABadRequestThatIsATimeoutIsAnsweredAsOne(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/modules/1/compare", nil)
	BadRequest(c, fmt.Errorf("compare: %w", context.DeadlineExceeded))
	assert.Equal(t, http.StatusGatewayTimeout, w.Code)
	assert.Equal(t, "platform", w.Header().Get(TimeoutSourceHeader))

	w = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/x", nil)
	BadRequest(c, errors.New("from must be before to"))
	assert.Equal(t, http.StatusBadRequest, w.Code, "any other bad request stays one")
}

func TestAnErrorOfARequestWhoseDeadlinePassedIsATimeoutWhateverTheDriverSays(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	c.Request = httptest.NewRequest("GET", "/x", nil).WithContext(ctx)
	// pq reports a cancelled statement as its own error, not as the error of the context
	InternalError(c, errors.New("pq: canceling statement due to user request"))
	if w.Code != http.StatusGatewayTimeout || w.Header().Get(TimeoutSourceHeader) != "platform" {
		t.Fatalf("want a 504 from the platform, got %d %q", w.Code, w.Header().Get(TimeoutSourceHeader))
	}
}
