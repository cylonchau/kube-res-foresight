package query

import (
	"errors"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"net/http"
	"net/http/httptest"
	"testing"
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
