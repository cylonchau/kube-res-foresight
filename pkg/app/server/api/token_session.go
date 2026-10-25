package api

import (
	"context"
	"errors"
	"fmt"
	"github.com/cylonchau/kube-resource-fcst/pkg/apis"
	"github.com/cylonchau/kube-resource-fcst/pkg/app/query"
	"github.com/cylonchau/kube-resource-fcst/pkg/app/utils"
	"github.com/cylonchau/kube-resource-fcst/pkg/dao/rdb"
	"github.com/cylonchau/kube-resource-fcst/pkg/model"
	"github.com/gin-gonic/gin"
	"time"
)

func userAuthorization(user *model.User) ([]string, []string) {
	roles := make([]string, 0, len(user.Roles))
	permissions := make([]string, 0)
	seen := make(map[string]bool)
	for _, role := range user.Roles {
		roles = append(roles, role.Code)
		for _, permission := range role.Permissions {
			if !seen[permission.Code] {
				seen[permission.Code] = true
				permissions = append(permissions, permission.Code)
			}
		}
	}
	return roles, permissions
}

func (h *AuthRouter) tokenDurations(ctx context.Context) (accessMinutes, refreshDays int) {
	accessMinutes, refreshDays = 30, 30
	if config, err := h.ConfigDAO.GetByKey(ctx, "sys.auth.access_token_minutes"); err == nil {
		_, _ = fmt.Sscanf(config.ConfigValue, "%d", &accessMinutes)
	}
	if config, err := h.ConfigDAO.GetByKey(ctx, "sys.auth.refresh_token_days"); err == nil {
		_, _ = fmt.Sscanf(config.ConfigValue, "%d", &refreshDays)
	}
	if accessMinutes <= 0 {
		accessMinutes = 30
	}
	if refreshDays <= 0 {
		refreshDays = 30
	}
	return
}

// ClientTypeWeb identifies the surface a RefreshTokenSession was issued to, for single-session-per-surface
// enforcement.
const ClientTypeWeb = "web"

// sessionConflictInfo is what gets sent back to a login request when the
// account already has an active session on this client type — enough for
// the client to render "already signed in from <ip> at <time>, kick it?".
type sessionConflictInfo struct {
	ClientIP   string     `json:"clientIp"`
	UserAgent  string     `json:"userAgent"`
	LastUsedAt *time.Time `json:"lastUsedAt"`
}

// checkSessionConflict enforces "only one active session per client type":
// if force is false and an active session already exists for userID on
// clientType, it reports the conflict (for the login handler to surface as
// ErrSessionConflict) instead of letting a new token pair get issued. If
// force is true, it instead revokes that existing session (tagged "kicked"
// so the other tab/device's next request can say why) and lets login
// proceed. Shared as a free function (rather than a method on AuthRouter)
// so every login flow can use it.
func checkSessionConflict(c *gin.Context, refreshDAO *rdb.RefreshTokenSessionDAO, userID uint, clientType string, force bool) (*sessionConflictInfo, error) {
	existing, err := refreshDAO.HasActiveByClientType(c.Request.Context(), userID, clientType)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, nil
	}
	if !force {
		return &sessionConflictInfo{ClientIP: existing.ClientIP, UserAgent: existing.UserAgent, LastUsedAt: existing.LastUsedAt}, nil
	}
	if err := refreshDAO.RevokeActiveByClientType(c.Request.Context(), userID, clientType, "kicked"); err != nil {
		return nil, err
	}
	return nil, nil
}

func (h *AuthRouter) issueTokenPair(c *gin.Context, user *model.User) (gin.H, error) {
	rawRefresh, refreshHash, err := utils.GenerateRefreshToken()
	if err != nil {
		return nil, err
	}
	familyID, err := utils.GenerateSessionFamilyID()
	if err != nil {
		return nil, err
	}
	accessMinutes, refreshDays := h.tokenDurations(c.Request.Context())
	roles, permissions := userAuthorization(user)
	accessToken, err := utils.GenerateAccessToken(user.ID, user.Username, roles, permissions, familyID, accessMinutes)
	if err != nil {
		return nil, err
	}
	if err = h.RefreshDAO.Create(c.Request.Context(), &model.RefreshTokenSession{
		UserID: user.ID, TokenHash: refreshHash, FamilyID: familyID, ClientType: ClientTypeWeb,
		ExpiresAt: time.Now().Add(time.Duration(refreshDays) * 24 * time.Hour),
		ClientIP:  c.ClientIP(), UserAgent: c.Request.UserAgent(),
	}); err != nil {
		return nil, err
	}
	return gin.H{
		"token": accessToken, "refreshToken": rawRefresh,
		"accessExpiresIn": accessMinutes * 60, "refreshExpiresIn": refreshDays * 86400,
		"user": gin.H{"username": user.Username, "nick_name": user.NickName},
	}, nil
}

func (h *AuthRouter) RefreshToken(c *gin.Context) {
	var req apis.RefreshTokenReq
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}
	rawReplacement, replacementHash, err := utils.GenerateRefreshToken()
	if err != nil {
		query.InternalError(c, err)
		return
	}
	_, refreshDays := h.tokenDurations(c.Request.Context())
	replacement := &model.RefreshTokenSession{
		TokenHash: replacementHash, ExpiresAt: time.Now().Add(time.Duration(refreshDays) * 24 * time.Hour),
		ClientIP: c.ClientIP(), UserAgent: c.Request.UserAgent(),
	}
	current, err := h.RefreshDAO.Rotate(c.Request.Context(), utils.HashRefreshToken(req.RefreshToken), replacement)
	if err != nil {
		switch {
		case errors.Is(err, rdb.ErrRefreshTokenReused):
			query.Unauthorized(c, query.ErrRefreshTokenReused)
		case errors.Is(err, rdb.ErrRefreshTokenInvalid):
			query.Unauthorized(c, query.ErrRefreshTokenInvalid)
		default:
			query.InternalError(c, err)
		}
		return
	}
	user, err := h.UserDAO.GetByID(c.Request.Context(), current.UserID)
	if err != nil || !user.Status {
		_ = h.RefreshDAO.RevokeToken(c.Request.Context(), replacementHash)
		query.Unauthorized(c, query.ErrRefreshTokenInvalid)
		return
	}
	accessMinutes, _ := h.tokenDurations(c.Request.Context())
	roles, permissions := userAuthorization(user)
	accessToken, err := utils.GenerateAccessToken(user.ID, user.Username, roles, permissions, current.FamilyID, accessMinutes)
	if err != nil {
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, gin.H{
		"token": accessToken, "refreshToken": rawReplacement,
		"accessExpiresIn": accessMinutes * 60, "refreshExpiresIn": refreshDays * 86400,
	})
}

func (h *AuthRouter) Logout(c *gin.Context) {
	var req apis.LogoutReq
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}
	if err := h.RefreshDAO.RevokeFamilyByTokenHash(c.Request.Context(), utils.HashRefreshToken(req.RefreshToken)); err != nil {
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, nil)
}
