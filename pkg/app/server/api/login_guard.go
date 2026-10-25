package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"github.com/cylonchau/kubeforecast/pkg/app/query"
	"github.com/cylonchau/kubeforecast/pkg/logger"
	"github.com/cylonchau/kubeforecast/pkg/model"
	"github.com/gin-gonic/gin"
	"html"
	"strconv"
	"strings"
	"time"
)

const (
	// Defaults of the lockout policy; sys.auth.login_failure_threshold / sys.auth.login_failure_window_minutes
	// override them without a restart.
	defaultLoginFailureThreshold = 3
	defaultLoginFailureWindow    = 15 * time.Minute
	captchaLifetime              = 5 * time.Minute

	configLoginFailureThreshold = "sys.auth.login_failure_threshold"
	configLoginFailureWindow    = "sys.auth.login_failure_window_minutes"

	loginMethodPassword = "password"
	loginMethodLDAP     = "ldap"
	loginMethodOTP      = "otp"

	loginReasonOK                 = "ok"
	loginReasonInvalidCredentials = "invalid_credentials"
	loginReasonCaptchaRequired    = "captcha_required"
	loginReasonUserDisabled       = "user_disabled"
	loginReasonOTPInvalid         = "otp_invalid"
)

// loginPolicy is how many failures within which window require a captcha for the account and address.
type loginPolicy struct {
	threshold int
	window    time.Duration
}

// loginPolicy reads the lockout settings on every attempt so an administrator's change applies at once. Missing
// or invalid values fall back to the defaults; values outside a sane range are clamped.
func (h *AuthRouter) loginPolicy(ctx context.Context) loginPolicy {
	policy := loginPolicy{threshold: defaultLoginFailureThreshold, window: defaultLoginFailureWindow}
	if h.ConfigDAO == nil {
		return policy
	}
	if cfg, err := h.ConfigDAO.GetByKey(ctx, configLoginFailureThreshold); err == nil && cfg != nil {
		if n, err := strconv.Atoi(strings.TrimSpace(cfg.ConfigValue)); err == nil && n >= 1 && n <= 100 {
			policy.threshold = n
		}
	}
	if cfg, err := h.ConfigDAO.GetByKey(ctx, configLoginFailureWindow); err == nil && cfg != nil {
		if n, err := strconv.Atoi(strings.TrimSpace(cfg.ConfigValue)); err == nil && n >= 1 && n <= 24*60 {
			policy.window = time.Duration(n) * time.Minute
		}
	}
	return policy
}

func loginAttemptKey(c *gin.Context, username string) string {
	return strings.ToLower(strings.TrimSpace(username)) + "|" + c.ClientIP()
}

// loginCaptchaRequired reports whether key is currently past the failure
// threshold. DB errors fail closed (captcha required) rather than silently
// disabling the lockout.
func (h *AuthRouter) loginCaptchaRequired(c *gin.Context, key string) bool {
	required, err := h.SecurityDAO.CaptchaRequired(c.Request.Context(), key, h.loginPolicy(c.Request.Context()).threshold)
	if err != nil {
		logger.Error("failed to check login captcha requirement", logger.Err(err), logger.String("key", key))
		return true
	}
	return required
}

func (h *AuthRouter) loginFailed(c *gin.Context, key, username, method, reason string) {
	policy := h.loginPolicy(c.Request.Context())
	count, err := h.SecurityDAO.RegisterFailure(c.Request.Context(), key, policy.window)
	if err != nil {
		logger.Error("failed to register login failure", logger.Err(err), logger.String("key", key))
	}
	h.recordLoginAudit(c, username, false, method, reason)
	query.UnauthorizedWithData(c, query.ErrInvalidCredentials, gin.H{
		"captcha_required": count >= policy.threshold,
	})
}

// recordLoginAudit appends to the append-only login history used by the
// login history used by the login-log page and the admin user list (see
// pkg/dao/rdb.LoginAuditLogDAO — distinct from SecurityDAO's rolling
// lockout counter, which gets pruned/overwritten and can't answer "how many
// failures happened in the last 7 days"). Best-effort: a logging failure
// must never block or fail the login response itself.
func (h *AuthRouter) recordLoginAudit(c *gin.Context, username string, success bool, method, reason string) {
	if h.LoginAuditDAO == nil {
		return
	}
	entry := &model.LoginAuditLog{Username: username, ClientIP: c.ClientIP(), Success: success, Method: method, Reason: reason, UserAgent: c.Request.UserAgent()}
	if err := h.LoginAuditDAO.RecordEntry(c.Request.Context(), entry); err != nil {
		logger.Error("failed to record login audit log", logger.Err(err), logger.String("username", username))
	}
}

func (h *AuthRouter) clearLoginFailures(c *gin.Context, key string) {
	if err := h.SecurityDAO.ClearFailures(c.Request.Context(), key); err != nil {
		logger.Error("failed to clear login failures", logger.Err(err), logger.String("key", key))
	}
}

func captchaDigest(salt, code string) string {
	sum := sha256.Sum256([]byte(salt + strings.ToUpper(strings.TrimSpace(code))))
	return hex.EncodeToString(sum[:])
}

func (h *AuthRouter) verifyLoginCaptcha(ctx context.Context, loginKey, captchaID, code string) bool {
	challenge, ok, err := h.SecurityDAO.ConsumeCaptcha(ctx, captchaID)
	if err != nil {
		logger.Error("failed to consume login captcha", logger.Err(err))
		return false
	}
	if !ok {
		return false
	}
	if challenge.LoginKey != loginKey || time.Now().After(challenge.ExpiresAt) {
		return false
	}
	return challenge.Hash == captchaDigest(challenge.Salt, code)
}

func randomCaptchaCode() (string, error) {
	const alphabet = "23456789ABCDEFGHJKLMNPQRSTUVWXYZ"
	raw := make([]byte, 5)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	for i := range raw {
		raw[i] = alphabet[int(raw[i])%len(alphabet)]
	}
	return string(raw), nil
}

func (h *AuthRouter) LoginCaptcha(c *gin.Context) {
	username := c.Query("username")
	if strings.TrimSpace(username) == "" {
		query.BadRequest(c, fmt.Errorf("username is required"))
		return
	}

	code, err := randomCaptchaCode()
	if err != nil {
		query.InternalError(c, err)
		return
	}
	idBytes := make([]byte, 16)
	saltBytes := make([]byte, 16)
	if _, err = rand.Read(idBytes); err != nil {
		query.InternalError(c, err)
		return
	}
	if _, err = rand.Read(saltBytes); err != nil {
		query.InternalError(c, err)
		return
	}
	id := hex.EncodeToString(idBytes)
	salt := hex.EncodeToString(saltBytes)
	if err := h.SecurityDAO.StoreCaptcha(c.Request.Context(), &model.LoginCaptchaChallenge{
		ID:        id,
		LoginKey:  loginAttemptKey(c, username),
		Salt:      salt,
		Hash:      captchaDigest(salt, code),
		ExpiresAt: time.Now().Add(captchaLifetime),
	}); err != nil {
		query.InternalError(c, err)
		return
	}

	var chars strings.Builder
	for i, char := range code {
		angle := []int{-12, 8, -5, 10, -9}[i%5]
		chars.WriteString(fmt.Sprintf(`<text x="%d" y="27" transform="rotate(%d %d 20)" font-family="monospace" font-size="22" font-weight="700" fill="#252b35">%s</text>`, 25+i*20, angle, 25+i*20, html.EscapeString(string(char))))
	}
	svg := fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" width="132" height="40" viewBox="0 0 132 40"><rect width="132" height="40" rx="2" fill="#fafafa"/><path d="M5 29C28 8 48 34 72 12S105 30 128 8M5 10C28 28 49 4 74 27S105 8 128 29" fill="none" stroke="#343a43" stroke-width="1.6"/><path d="M8 6H124M8 35H124" stroke="#d4d7dc" stroke-width="1"/>%s</svg>`, chars.String())
	query.SuccessResponse(c, nil, gin.H{
		"captcha_id":    id,
		"captcha_image": "data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString([]byte(svg)),
		"expires_in":    int(captchaLifetime.Seconds()),
	})
}
