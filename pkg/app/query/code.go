package query

import (
	"strings"
)

// Errno defines the error number and message
type Errno struct {
	Code    int
	Message string
}

func (err Errno) Error() string {
	return err.Message
}

// Err represents a detailed error
type Err struct {
	Code    int
	Message string
	Err     error
}

func (err *Err) Error() string {
	return err.Message
}

var (
	// Common errors
	OK                  = &Errno{Code: 10000, Message: "success"}
	InternalServerError = &Errno{Code: 10001, Message: "Internal server error"}
	ErrBind             = &Errno{Code: 10002, Message: "Error occurred while binding the request body to the struct"}
	ErrParam            = &Errno{Code: 10003, Message: "Invalid parameters"}

	ErrDatabase      = &Errno{Code: 20002, Message: "Database error"}
	ErrAlreadyExists = &Errno{Code: 10004, Message: "Resource already exists"}

	// Resource errors
	ErrRecordNotFound = &Errno{Code: 40005, Message: "Record not found"}
	ErrRouteNotFound  = &Errno{Code: 40006, Message: "Route not found"}

	// Auth errors
	ErrNeedAuth            = &Errno{Code: 50107, Message: "Authentication required"}
	ErrNoPermission        = &Errno{Code: 50116, Message: "No permission"}
	ErrInvalidCredentials  = &Errno{Code: 50101, Message: "Invalid username or password"}
	ErrOtpRequired         = &Errno{Code: 50102, Message: "OTP verification is enabled, code is required"}
	ErrOtpInvalid          = &Errno{Code: 50103, Message: "Invalid OTP code"}
	ErrTokenFailed         = &Errno{Code: 50104, Message: "Token generation failed"}
	ErrUserNotFound        = &Errno{Code: 50105, Message: "User not found"}
	ErrOtpNotEnabled       = &Errno{Code: 50106, Message: "OTP is not enabled for this account"}
	ErrCaptchaRequired     = &Errno{Code: 50117, Message: "Captcha verification is required"}
	ErrRefreshTokenInvalid = &Errno{Code: 50118, Message: "Invalid or expired refresh token"}
	ErrRefreshTokenReused  = &Errno{Code: 50119, Message: "Refresh token reuse detected; session revoked"}
	ErrUserDisabled        = &Errno{Code: 50120, Message: "This account has been disabled"}
	ErrCannotDisableSelf   = &Errno{Code: 50121, Message: "You cannot disable your own account"}

	// Middleware errors
	ErrHeaderRequired     = &Errno{Code: 50108, Message: "Authorization header is required"}
	ErrHeaderFormat       = &Errno{Code: 50109, Message: "Authorization header format must be Bearer {token}"}
	ErrTokenExpired       = &Errno{Code: 50110, Message: "Invalid or expired token"}
	ErrSessionInvalid     = &Errno{Code: 50111, Message: "This account has logged in elsewhere, current session is invalid"}
	ErrUserContextMissing = &Errno{Code: 50112, Message: "Unauthorized: no user found in context"}
	ErrCasbinNotInit      = &Errno{Code: 50113, Message: "Access control engine not initialized"}
	ErrEnforcerFailed     = &Errno{Code: 50114, Message: "Authorization check failed"}

	// ErrSessionConflict is returned at login time (not from the auth
	// middleware) when the account already has an active session on the
	// same client type (web or tma); the client should prompt the user to
	// retry the login with force=true to kick the existing session.
	ErrSessionConflict = &Errno{Code: 50124, Message: "This account is already signed in elsewhere on this client"}
)

// DecodeErr decodes an error into code and message
func DecodeErr(err error) (int, string) {
	if err == nil {
		return OK.Code, OK.Message
	}

	switch typed := err.(type) {
	case *Err:
		return typed.Code, typed.Message
	case *Errno:
		return typed.Code, typed.Message
	default:
		// Clean up standard error messages
		msg := strings.ReplaceAll(err.Error(), "\"", "")

		// Map common DB unique constraint violations
		if strings.Contains(msg, "UNIQUE constraint failed") || // SQLite
			strings.Contains(msg, "Duplicate entry") { // MySQL
			return ErrAlreadyExists.Code, ErrAlreadyExists.Message
		}

		return InternalServerError.Code, msg
	}
}

// Response defines the standard API response structure
type Response struct {
	Code int         `json:"code"`
	Msg  string      `json:"msg"`
	Data interface{} `json:"data,omitempty"`
}
