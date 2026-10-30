package query

import (
	"context"
	"errors"
	"github.com/cylonchau/kube-resource-fcst/pkg/logger"
	"github.com/gin-gonic/gin"
	"github.com/go-playground/validator/v10"
	"gorm.io/gorm"
	"net/http"
	"reflect"
)

// SuccessResponse handles successful API requests
func SuccessResponse(ctx *gin.Context, err error, data interface{}) {
	returnCode, message := DecodeErr(err)
	ctx.JSON(http.StatusOK, Response{
		Code: returnCode,
		Msg:  message,
		Data: data,
	})
}

// APIResponse is a generic API response handler
func APIResponse(ctx *gin.Context, err error, data interface{}) {
	returnCode, message := DecodeErr(err)
	ctx.JSON(http.StatusOK, Response{
		Code: returnCode,
		Msg:  message,
		Data: data,
	})
}

// ErrorResponse handles API errors with specific status codes
func ErrorResponse(ctx *gin.Context, statusCode int, err error) {
	returnCode, message := DecodeErr(err)
	ctx.JSON(statusCode, Response{
		Code: returnCode,
		Msg:  message,
	})
}

// StatusClientClosedRequest is the status logged for a request the client abandoned before it was answered.
const StatusClientClosedRequest = 499

// InternalError handles 500 Internal Server Error
func InternalError(ctx *gin.Context, err error) {
	if err == nil {
		err = errors.New("internal server error")
	}
	if errors.Is(err, context.Canceled) {
		// the client hung up (an operator whose request timed out, a closed page): nothing failed on the server
		ctx.AbortWithStatus(StatusClientClosedRequest)
		return
	}
	logger.Error("HTTP request failed with an internal error",
		logger.String("method", ctx.Request.Method),
		logger.String("path", ctx.Request.URL.Path),
		logger.String("client_ip", ctx.ClientIP()),
		logger.Err(err),
	)
	ErrorResponse(ctx, http.StatusInternalServerError, err)
}

// HandleDBError determines whether to return a 400 (Conflict/Duplicate) or 500 (Fatal)
func HandleDBError(ctx *gin.Context, err error) {
	code, _ := DecodeErr(err)
	if code == ErrAlreadyExists.Code {
		BadRequest(ctx, err)
	} else if errors.Is(err, gorm.ErrRecordNotFound) {
		NotFound(ctx, err)
	} else {
		InternalError(ctx, err)
	}
}

// BadRequest handles 400 Bad Request
func BadRequest(ctx *gin.Context, err error) {
	ErrorResponse(ctx, http.StatusBadRequest, err)
}

// NotFound handles 404 Not Found
func NotFound(ctx *gin.Context, err error) {
	ErrorResponse(ctx, http.StatusNotFound, err)
}

// Unauthorized handles 401 Unauthorized
func Unauthorized(ctx *gin.Context, err error) {
	ErrorResponse(ctx, http.StatusUnauthorized, err)
}

// UnauthorizedWithData handles 401 Unauthorized with extra data
func UnauthorizedWithData(ctx *gin.Context, err error, data interface{}) {
	returnCode, message := DecodeErr(err)
	ctx.JSON(http.StatusUnauthorized, Response{
		Code: returnCode,
		Msg:  message,
		Data: data,
	})
}

// Forbidden handles 403 Forbidden
func Forbidden(ctx *gin.Context, err error) {
	ErrorResponse(ctx, http.StatusForbidden, err)
}

// Locked handles 423 Locked — the resource exists and the caller may well be
// allowed to act on it, but the target resource has been frozen, which
// is a distinct condition from Forbidden (caller isn't allowed at all) or
// BadRequest (the request itself is malformed).
func Locked(ctx *gin.Context, err error) {
	ErrorResponse(ctx, http.StatusLocked, err)
}

// GetErrorMsg Get struct msg tag content with reflect
func GetErrorMsg(obj interface{}, err error) string {
	if errs, ok := err.(validator.ValidationErrors); ok {
		// Get object type and resolve pointers
		t := reflect.TypeOf(obj)
		for t.Kind() == reflect.Ptr {
			t = t.Elem()
		}

		if t.Kind() != reflect.Struct {
			return err.Error()
		}

		for _, e := range errs {
			if f, ok := t.FieldByName(e.Field()); ok {
				msg := f.Tag.Get("msg")
				if msg != "" {
					return msg
				}
			}
		}
	}
	return err.Error()
}
