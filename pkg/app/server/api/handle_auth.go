package api

import (
	"context"
	"errors"
	"fmt"
	"github.com/cylonchau/kube-resource-fcst/pkg/apis"
	"github.com/cylonchau/kube-resource-fcst/pkg/app/query"
	"github.com/cylonchau/kube-resource-fcst/pkg/app/utils"
	"github.com/cylonchau/kube-resource-fcst/pkg/dao/rdb"
	"github.com/cylonchau/kube-resource-fcst/pkg/ldapauth"
	"github.com/cylonchau/kube-resource-fcst/pkg/logger"
	"github.com/cylonchau/kube-resource-fcst/pkg/model"
	"github.com/gin-gonic/gin"
	"time"
)

type AuthRouter struct {
	UserDAO       *rdb.UserDAO
	PermDAO       *rdb.PermissionDAO
	ConfigDAO     *rdb.SysConfigDAO
	RefreshDAO    *rdb.RefreshTokenSessionDAO
	SecurityDAO   *rdb.LoginSecurityDAO
	LoginAuditDAO *rdb.LoginAuditLogDAO
	// LDAP is optional; nil keeps every account on local password authentication.
	LDAP *ldapauth.Service
}

func authenticatedUsername(c *gin.Context) (string, bool) {
	value, exists := c.Get("username")
	username, ok := value.(string)
	if !exists || !ok || username == "" {
		query.Unauthorized(c, query.ErrUserContextMissing)
		return "", false
	}
	return username, true
}

func authenticatedUserID(c *gin.Context) (uint, bool) {
	value, exists := c.Get("userId")
	userID, ok := value.(uint)
	if !exists || !ok || userID == 0 {
		query.Unauthorized(c, query.ErrUserContextMissing)
		return 0, false
	}
	return userID, true
}

// LoginRequest is deprecated, use apis.LoginReq
type LoginRequest = apis.LoginReq

// Login handles user authentication
// @Summary User login
// @Description Authenticate user with username and password. Returns 2FA ticket if OTP is enabled.
// @Tags Auth
// @Accept json
// @Produce json
// @Param request body apis.LoginReq true "Login credentials"
// @Success 200 {object} query.Response "Success"
// @Failure 401 {object} query.Response "Unauthorized - Invalid credentials or OTP required"
// @Router /api/v1/auth/login [post]
func (h *AuthRouter) Login(c *gin.Context) {
	var req LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}
	loginKey := loginAttemptKey(c, req.Username)
	if h.loginCaptchaRequired(c, loginKey) && !h.verifyLoginCaptcha(c.Request.Context(), loginKey, req.CaptchaID, req.CaptchaCode) {
		h.recordLoginAudit(c, req.Username, false, loginMethodPassword, loginReasonCaptchaRequired)
		query.UnauthorizedWithData(c, query.ErrCaptchaRequired, gin.H{"captcha_required": true})
		return
	}

	// 1. Find user
	user, err := h.UserDAO.GetByUsername(context.Background(), req.Username)

	// 1.5 Directory accounts. A local account (the break-glass path) is always checked locally; a name that is
	// unknown here, or belongs to a directory account, is checked against the directory when LDAP is enabled.
	directoryVerified := false
	if h.LDAP != nil && (err != nil || user.Source == model.UserSourceLDAP) && h.LDAP.Enabled(c.Request.Context()) {
		ldapUser, ldapErr := h.LDAP.Login(c.Request.Context(), req.Username, req.Password)
		switch {
		case ldapErr == nil:
			user, err, directoryVerified = ldapUser, nil, true
		case errors.Is(ldapErr, ldapauth.ErrAccountDisabled):
			h.recordLoginAudit(c, req.Username, false, loginMethodLDAP, loginReasonUserDisabled)
			query.Unauthorized(c, query.ErrUserDisabled)
			return
		default:
			// The reason is logged for the administrator; the user only sees "invalid credentials".
			logger.Warn("ldap login refused", logger.String("username", req.Username), logger.String("reason", ldapErr.Error()))
			h.loginFailed(c, loginKey, req.Username, loginMethodLDAP, loginReasonInvalidCredentials)
			return
		}
	}
	if err != nil {
		h.loginFailed(c, loginKey, req.Username, loginMethodPassword, loginReasonInvalidCredentials)
		return
	}
	loginMethod := loginMethodPassword
	if directoryVerified || user.Source == model.UserSourceLDAP {
		loginMethod = loginMethodLDAP
	}
	if !user.Status {
		h.recordLoginAudit(c, req.Username, false, loginMethod, loginReasonUserDisabled)
		query.Unauthorized(c, query.ErrUserDisabled)
		return
	}

	// 2. Verify password (bcrypt hashing); directory accounts were verified by the directory
	if !directoryVerified && !utils.ComparePassword(user.PasswordHash, req.Password) {
		h.loginFailed(c, loginKey, req.Username, loginMethod, loginReasonInvalidCredentials)
		return
	}
	h.clearLoginFailures(c, loginKey)

	// 2.5 Verify OTP if enabled
	if user.OtpEnabled {
		ticket, err := utils.GenerateLoginTicket(user.ID)
		if err != nil {
			query.InternalError(c, err)
			return
		}
		query.UnauthorizedWithData(c, query.ErrOtpRequired, gin.H{
			"login_ticket": ticket,
		})
		return
	}

	if conflict, err := checkSessionConflict(c, h.RefreshDAO, user.ID, ClientTypeWeb, req.Force); err != nil {
		query.InternalError(c, err)
		return
	} else if conflict != nil {
		query.UnauthorizedWithData(c, query.ErrSessionConflict, gin.H{
			"session_conflict": true,
			"active_session":   conflict,
		})
		return
	}

	tokens, err := h.issueTokenPair(c, user)
	if err != nil {
		query.InternalError(c, query.ErrTokenFailed)
		return
	}
	h.recordLoginAudit(c, req.Username, true, loginMethod, loginReasonOK)
	query.SuccessResponse(c, nil, tokens)
}

// LoginOTP handles the second step of authentication
// @Summary OTP Login
// @Description Complete login using a 2FA ticket and OTP code.
// @Tags Auth
// @Accept json
// @Produce json
// @Param request body apis.LoginOTPReq true "OTP credentials"
// @Success 200 {object} query.Response "Success"
// @Failure 401 {object} query.Response "Unauthorized - Invalid ticket or OTP"
// @Router /api/v1/auth/login/otp [post]
func (h *AuthRouter) LoginOTP(c *gin.Context) {
	var req apis.LoginOTPReq
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}

	// 1. Verify ticket and get user ID
	userId, err := utils.VerifyLoginTicket(req.Ticket)
	if err != nil {
		query.Unauthorized(c, query.ErrTokenExpired)
		return
	}

	// 2. Find user
	user, err := h.UserDAO.GetByID(context.Background(), userId)
	if err != nil {
		query.NotFound(c, query.ErrUserNotFound)
		return
	}
	if !user.Status {
		h.recordLoginAudit(c, user.Username, false, loginMethodOTP, loginReasonUserDisabled)
		query.Unauthorized(c, query.ErrInvalidCredentials)
		return
	}

	// 3. Verify OTP code
	if !utils.VerifyOTP(user.OtpSecret, req.OtpCode) {
		h.recordLoginAudit(c, user.Username, false, loginMethodOTP, loginReasonOTPInvalid)
		query.Unauthorized(c, query.ErrOtpInvalid)
		return
	}

	if conflict, err := checkSessionConflict(c, h.RefreshDAO, user.ID, ClientTypeWeb, req.Force); err != nil {
		query.InternalError(c, err)
		return
	} else if conflict != nil {
		query.UnauthorizedWithData(c, query.ErrSessionConflict, gin.H{
			"session_conflict": true,
			"active_session":   conflict,
		})
		return
	}

	tokens, err := h.issueTokenPair(c, user)
	if err != nil {
		query.InternalError(c, query.ErrTokenFailed)
		return
	}
	h.recordLoginAudit(c, user.Username, true, loginMethodOTP, loginReasonOK)
	query.SuccessResponse(c, nil, tokens)
}

// GetInfo gets current user profile and permission codes
// @Summary Get user info
// @Description Retrieves profile information and permissions for the currently authenticated user.
// @Tags Auth
// @Accept json
// @Produce json
// @Success 200 {object} query.Response "Success"
// @Failure 404 {object} query.Response "User not found"
// @Router /api/v1/auth/info [get]
func (h *AuthRouter) GetInfo(c *gin.Context) {
	username, ok := authenticatedUsername(c)
	if !ok {
		return
	}

	user, err := h.UserDAO.GetByUsername(c.Request.Context(), username)
	if err != nil {
		query.NotFound(c, query.ErrUserNotFound)
		return
	}

	var roles []string
	var permissions []string

	for _, r := range user.Roles {
		roles = append(roles, r.Code)
		for _, p := range r.Permissions {
			permissions = append(permissions, p.Code)
		}
	}

	// "This session's" login info shown on the dashboard card — reuses the
	// same LoginAuditLog lookup the admin user list already uses to show
	// other accounts' last login, just scoped to the caller themselves.
	var lastLoginAt *time.Time
	var lastLoginIP string
	if h.LoginAuditDAO != nil {
		if last, err := h.LoginAuditDAO.LatestSuccessRecordByUsername(c.Request.Context(), user.Username); err == nil && last != nil {
			lastLoginAt = &last.CreatedAt
			lastLoginIP = last.ClientIP
		}
	}

	query.SuccessResponse(c, nil, gin.H{
		"userId":      fmt.Sprintf("%d", user.ID),
		"userName":    user.Username,
		"nickName":    user.NickName,
		"email":       user.Email,
		"phone":       user.Phone,
		"otpEnabled":  user.OtpEnabled,
		"language":    user.Language,
		"roles":       roles,
		"buttons":     permissions, // mapped to buttons inside Soybean models
		"lastLoginAt": lastLoginAt,
		"lastLoginIp": lastLoginIP,
	})
}

// SessionStatus is a deliberately trivial "am I still logged in" endpoint.
// It carries no logic of its own — all the work is JWTAuthMiddleware's
// existing per-request family-active check running ahead of this handler.
// Both frontends poll it every ~30s so a session that gets kicked while
// idle finds out well before the user's next real action, instead of only
// on their next click.
// @Summary Session liveness check
// @Description Cheap authenticated no-op; a 401 here means this session was revoked or expired.
// @Tags Auth
// @Produce json
// @Success 200 {object} query.Response "Success"
// @Router /api/v1/auth/session-status [get]
func (h *AuthRouter) SessionStatus(c *gin.Context) {
	query.SuccessResponse(c, nil, nil)
}

// UpdateLanguage saves the current user's UI language preference so it
// follows their account instead of just the browser they picked it in.
// @Summary Update language preference
// @Description Sets or clears the UI language preference for the currently authenticated user.
// @Tags Auth
// @Accept json
// @Produce json
// @Param request body apis.UpdateLanguageReq true "Language preference"
// @Success 200 {object} query.Response "Success"
// @Router /api/v1/auth/language [put]
func (h *AuthRouter) UpdateLanguage(c *gin.Context) {
	userID, ok := authenticatedUserID(c)
	if !ok {
		return
	}
	var req apis.UpdateLanguageReq
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}

	user, err := h.UserDAO.GetByID(c.Request.Context(), userID)
	if err != nil {
		query.NotFound(c, query.ErrUserNotFound)
		return
	}

	user.Language = req.Language
	if err := h.UserDAO.Update(context.Background(), user); err != nil {
		query.InternalError(c, err)
		return
	}

	query.SuccessResponse(c, nil, nil)
}

// UpdatePassword updates user password
// @Summary Update password
// @Description Changes the password for the currently authenticated user.
// @Tags Auth
// @Accept json
// @Produce json
// @Param request body apis.UpdatePasswordReq true "Password information"
// @Success 200 {object} query.Response "Success"
// @Failure 400 {object} query.Response "Bad Request - Invalid old password"
// @Router /api/v1/auth/password [put]
func (h *AuthRouter) UpdatePassword(c *gin.Context) {
	userID, ok := authenticatedUserID(c)
	if !ok {
		return
	}
	var req apis.UpdatePasswordReq
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}

	user, err := h.UserDAO.GetByID(c.Request.Context(), userID)
	if err != nil {
		query.NotFound(c, query.ErrUserNotFound)
		return
	}

	if user.Source == model.UserSourceLDAP {
		query.BadRequest(c, errors.New("this account is managed by the directory: change the password there"))
		return
	}

	if !utils.ComparePassword(user.PasswordHash, req.OldPassword) {
		query.BadRequest(c, query.ErrInvalidCredentials)
		return
	}

	hash, err := utils.HashPassword(req.Password)
	if err != nil {
		query.InternalError(c, err)
		return
	}
	user.PasswordHash = hash
	if err := h.UserDAO.Update(context.Background(), user); err != nil {
		query.InternalError(c, err)
		return
	}
	if err := h.RefreshDAO.RevokeUser(c.Request.Context(), user.ID); err != nil {
		query.InternalError(c, err)
		return
	}

	query.SuccessResponse(c, nil, nil)
}

// UpdateProfile updates user profile info
// @Summary Update profile
// @Description Updates profile information (email, phone) for the currently authenticated user.
// @Tags Auth
// @Accept json
// @Produce json
// @Param request body apis.UpdateProfileReq true "Profile information"
// @Success 200 {object} query.Response "Success"
// @Router /api/v1/auth/profile [put]
func (h *AuthRouter) UpdateProfile(c *gin.Context) {
	userID, ok := authenticatedUserID(c)
	if !ok {
		return
	}
	var req apis.UpdateProfileReq
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}

	user, err := h.UserDAO.GetByID(c.Request.Context(), userID)
	if err != nil {
		query.NotFound(c, query.ErrUserNotFound)
		return
	}

	user.Email = req.Email
	user.Phone = req.Phone
	if err := h.UserDAO.Update(context.Background(), user); err != nil {
		query.InternalError(c, err)
		return
	}

	query.SuccessResponse(c, nil, nil)
}

// GetRouters gets dynamic route menu list
// @Summary Get available menus
// @Description Retrieves all available menu permissions.
// @Tags Auth
// @Accept json
// @Produce json
// @Success 200 {object} query.Response "Success"
// @Router /api/v1/auth/get-routers [get]
func (h *AuthRouter) GetRouters(c *gin.Context) {
	// Fetch all available menus
	allPerms, err := h.PermDAO.GetByType(context.Background(), "MENU")
	if err != nil {
		query.InternalError(c, err)
		return
	}

	query.SuccessResponse(c, nil, gin.H{
		"list": allPerms,
	})
}

// GenerateOTP generates a secret and QR code for visual binding
// @Summary Generate OTP secret
// @Description Generates a new TOTP secret and QR code data for the authenticated user.
// @Tags Auth
// @Accept json
// @Produce json
// @Success 200 {object} query.Response "Success"
// @Router /api/v1/auth/otp/generate [post]
func (h *AuthRouter) GenerateOTP(c *gin.Context) {
	username, ok := authenticatedUsername(c)
	if !ok {
		return
	}

	secret, qrCode, err := utils.GenerateOTPSecret(username)
	if err != nil {
		query.InternalError(c, err)
		return
	}

	query.SuccessResponse(c, nil, gin.H{
		"secret":       secret,
		"qr_code_data": qrCode,
	})
}

// BindOTP verifies code with secret, saves to DB to enable 2FA
// @Summary Bind OTP
// @Description Verifies and binds a TOTP secret to the user's account to enable 2FA.
// @Tags Auth
// @Accept json
// @Produce json
// @Param request body apis.BindOTPReq true "OTP binding information"
// @Success 200 {object} query.Response "Success"
// @Failure 401 {object} query.Response "Unauthorized - Invalid OTP code"
// @Router /api/v1/auth/otp/bind [post]
func (h *AuthRouter) BindOTP(c *gin.Context) {
	userID, ok := authenticatedUserID(c)
	if !ok {
		return
	}
	var req apis.BindOTPReq
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}

	if !utils.VerifyOTP(req.Secret, req.Code) {
		query.Unauthorized(c, query.ErrOtpInvalid)
		return
	}

	user, err := h.UserDAO.GetByID(c.Request.Context(), userID)
	if err != nil {
		query.NotFound(c, query.ErrUserNotFound)
		return
	}

	user.OtpSecret = req.Secret
	user.OtpEnabled = true
	if err := h.UserDAO.Update(context.Background(), user); err != nil {
		query.InternalError(c, err)
		return
	}

	query.SuccessResponse(c, nil, gin.H{"message": "OTP bound successfully"})
}

// UnbindOTP disables 2FA verified by valid code
// @Summary Unbind OTP
// @Description Disables TOTP 2FA for the user's account after verifying a final code.
// @Tags Auth
// @Accept json
// @Produce json
// @Param request body apis.UnbindOTPReq true "OTP unbinding information"
// @Success 200 {object} query.Response "Success"
// @Failure 401 {object} query.Response "Unauthorized - Invalid OTP code"
// @Router /api/v1/auth/otp/unbind [post]
func (h *AuthRouter) UnbindOTP(c *gin.Context) {
	userID, ok := authenticatedUserID(c)
	if !ok {
		return
	}
	var req apis.UnbindOTPReq
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}

	user, err := h.UserDAO.GetByID(c.Request.Context(), userID)
	if err != nil {
		query.NotFound(c, query.ErrUserNotFound)
		return
	}

	if !user.OtpEnabled {
		query.BadRequest(c, query.ErrOtpNotEnabled)
		return
	}

	if !utils.VerifyOTP(user.OtpSecret, req.Code) {
		query.Unauthorized(c, query.ErrOtpInvalid)
		return
	}

	user.OtpSecret = ""
	user.OtpEnabled = false
	if err := h.UserDAO.Update(context.Background(), user); err != nil {
		query.InternalError(c, err)
		return
	}

	query.SuccessResponse(c, nil, gin.H{"message": "OTP unbound successfully"})
}
