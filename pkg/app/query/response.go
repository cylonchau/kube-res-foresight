package query

import (
	"context"
	"errors"
	"github.com/cylonchau/kube-res-foresight/pkg/logger"
	"github.com/gin-gonic/gin"
	"github.com/go-playground/validator/v10"
	"gorm.io/gorm"
	"net/http"
	"reflect"
)

// CappedTotal is the count a list stops counting at (rdb.MaxCountedRows): a total of exactly this many is "at least this many".
const CappedTotal = 100000

// markCapped says, in a page of a list whose total reached the bound, that the total is a floor: the page shows "more than" and not a count that is not true.
// A list that carries total_capped already says it itself.
func markCapped(data interface{}) interface{} {
	m, ok := data.(gin.H)
	if !ok {
		plain, isMap := data.(map[string]interface{})
		if !isMap {
			return data
		}
		m = plain
	}
	if _, isList := m["list"]; !isList {
		return data
	}
	if _, said := m["total_capped"]; said {
		return data
	}
	var total int64
	switch n := m["total"].(type) {
	case int64:
		total = n
	case int:
		total = int64(n)
	default:
		return data
	}
	if total == CappedTotal {
		m["total_capped"] = true
	}
	return m
}

// SuccessResponse handles successful API requests
func SuccessResponse(ctx *gin.Context, err error, data interface{}) {
	returnCode, message := DecodeErr(err)
	ctx.JSON(http.StatusOK, Response{
		Code: returnCode,
		Msg:  message,
		Data: markCapped(data),
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
	// a driver that reports a cancelled statement in its own words (not as the error of the context) is still a request that ran out of its time
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Request.Context().Err(), context.DeadlineExceeded) {
		TimedOut(ctx, err)
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

// TimeoutSourceHeader says who ended a request that took too long: "platform" when the platform stopped it at its own time limit and
// answered with the reason. A bare 504 without it comes from the gateway in front, which is what tells the two apart in a bug report.
const TimeoutSourceHeader = "X-Timeout-Source"

// TimedOut answers a request whose time limit ran out (a pipeline step, a preview, a query of a data source) with a 504 and the reason, so the
// person reads what happened and which parameter sets the limit, instead of a bare timeout or an internal error.
func TimedOut(ctx *gin.Context, err error) {
	logger.Warn("HTTP request stopped at its time limit",
		logger.String("method", ctx.Request.Method),
		logger.String("path", ctx.Request.URL.Path),
		logger.String("client_ip", ctx.ClientIP()),
		logger.Err(err),
	)
	ctx.Header(TimeoutSourceHeader, "platform")
	ErrorResponse(ctx, http.StatusGatewayTimeout, ErrTimeLimit)
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
	// a request that ran out of its time limit is not a mistake of the caller, whichever handler passed the error here
	if errors.Is(err, context.DeadlineExceeded) {
		TimedOut(ctx, err)
		return
	}
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
