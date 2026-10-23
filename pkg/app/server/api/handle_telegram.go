package api

import (
	"errors"
	"github.com/cylonchau/kubeforecast/pkg/apis"
	"github.com/cylonchau/kubeforecast/pkg/app/query"
	apputils "github.com/cylonchau/kubeforecast/pkg/app/utils"
	"github.com/cylonchau/kubeforecast/pkg/dao/rdb"
	"github.com/cylonchau/kubeforecast/pkg/model"
	"github.com/cylonchau/kubeforecast/pkg/telegram"
	"github.com/cylonchau/kubeforecast/pkg/workflow"
	"github.com/gin-gonic/gin"
	"strconv"
	"strings"
	"time"
)

type TelegramRouter struct {
	UserDAO       *rdb.UserDAO
	BindingDAO    *rdb.TelegramBindingDAO
	RefreshDAO    *rdb.RefreshTokenSessionDAO
	LoginAuditDAO *rdb.LoginAuditLogDAO
	ConfigDAO     *rdb.SysConfigDAO
	TemplateDAO   *rdb.NotifyTemplateDAO
	InstanceDAO   *rdb.WorkflowInstanceDAO
	Engine        *workflow.Engine
}

func hasAnyRole(c *gin.Context, codes ...string) bool {
	raw, ok := c.Get("roles")
	if !ok {
		return false
	}
	roles, ok := raw.([]string)
	if !ok {
		return false
	}
	allowed := make(map[string]struct{}, len(codes))
	for _, code := range codes {
		allowed[code] = struct{}{}
	}
	for _, role := range roles {
		if _, ok := allowed[role]; ok {
			return true
		}
	}
	return false
}

func (h *TelegramRouter) botToken(c *gin.Context) string {
	item, err := h.ConfigDAO.GetByKey(c.Request.Context(), telegram.ConfigBotToken)
	if err != nil || item == nil {
		return ""
	}
	return strings.TrimSpace(item.ConfigValue)
}

func (h *TelegramRouter) botUsername(c *gin.Context) string {
	item, err := h.ConfigDAO.GetByKey(c.Request.Context(), telegram.ConfigBotUsername)
	if err != nil || item == nil {
		return ""
	}
	return strings.TrimPrefix(strings.TrimSpace(item.ConfigValue), "@")
}

func (h *TelegramRouter) Login(c *gin.Context) {
	var req apis.TelegramLoginReq
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}
	initData, err := telegram.ValidateInitData(req.InitData, h.botToken(c), 24*time.Hour)
	if err != nil {
		query.Unauthorized(c, err)
		return
	}
	binding, err := h.BindingDAO.GetBindingByTelegramID(c.Request.Context(), initData.User.ID)
	if err != nil || binding.User == nil || !binding.User.Status {
		query.Unauthorized(c, errors.New("telegram account is not bound to an active user"))
		return
	}
	if conflict, err := checkSessionConflict(c, h.RefreshDAO, binding.User.ID, ClientTypeTMA, req.Force); err != nil {
		query.InternalError(c, err)
		return
	} else if conflict != nil {
		query.UnauthorizedWithData(c, query.ErrSessionConflict, conflict)
		return
	}
	response, err := h.issueTelegramTokens(c, binding.User, binding)
	if err != nil {
		query.InternalError(c, err)
		return
	}
	_ = h.LoginAuditDAO.Record(c.Request.Context(), binding.User.Username, c.ClientIP(), true)
	query.SuccessResponse(c, nil, response)
}

func (h *TelegramRouter) MockLogin(c *gin.Context) {
	var user model.User
	if err := model.DB.Preload("Roles.Permissions").Where("status = ?", true).Order("id").First(&user).Error; err != nil {
		query.InternalError(c, err)
		return
	}
	binding, err := h.BindingDAO.CreateOrUpdateMockBinding(c.Request.Context(), user.ID, &model.TelegramUser{ID: -1, Username: "kubeforecast_platform_dev", FirstName: "Development"})
	if err != nil {
		query.InternalError(c, err)
		return
	}
	binding.User = &user
	response, err := h.issueTelegramTokens(c, &user, binding)
	if err != nil {
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, response)
}

func currentUserID(c *gin.Context) (uint, bool) {
	raw, ok := c.Get("userId")
	if !ok {
		return 0, false
	}
	id, ok := raw.(uint)
	return id, ok && id > 0
}

func (h *TelegramRouter) GenerateBindToken(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok {
		query.Unauthorized(c, query.ErrTokenExpired)
		return
	}
	response, err := h.BindingDAO.GenerateBindToken(c.Request.Context(), userID, h.botUsername(c))
	if err != nil {
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, response)
}

func (h *TelegramRouter) ConfirmBind(c *gin.Context) {
	var req apis.TelegramBindConfirmReq
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}
	initData, err := telegram.ValidateInitData(req.InitData, h.botToken(c), 24*time.Hour)
	if err != nil {
		query.Unauthorized(c, err)
		return
	}
	binding, err := h.BindingDAO.ConfirmBindWithToken(c.Request.Context(), req.Token, initData.User, initData.AuthDate)
	if err != nil {
		query.BadRequest(c, err)
		return
	}
	query.SuccessResponse(c, nil, bindingInfo(binding))
}

func (h *TelegramRouter) Unbind(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok {
		query.Unauthorized(c, query.ErrTokenExpired)
		return
	}
	if err := h.BindingDAO.Unbind(c.Request.Context(), userID); err != nil {
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, nil)
}

func (h *TelegramRouter) GetStatus(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok {
		query.Unauthorized(c, query.ErrTokenExpired)
		return
	}
	binding, err := h.BindingDAO.GetBindingByUserID(c.Request.Context(), userID)
	if err != nil {
		query.SuccessResponse(c, nil, apis.TelegramBindingStatusResp{IsBound: false, BotUser: h.botUsername(c)})
		return
	}
	info := bindingInfo(binding)
	query.SuccessResponse(c, nil, apis.TelegramBindingStatusResp{IsBound: true, Binding: &info, BotUser: h.botUsername(c)})
}

func bindingInfo(binding *model.TelegramBinding) apis.TelegramBindingInfo {
	return apis.TelegramBindingInfo{TelegramUserID: binding.TelegramUserID, TelegramUsername: binding.TelegramUsername, FirstName: binding.FirstName, LastName: binding.LastName, PhotoURL: binding.PhotoURL, BoundAt: binding.CreatedAt}
}

func (h *TelegramRouter) issueTelegramTokens(c *gin.Context, user *model.User, binding *model.TelegramBinding) (*apis.TelegramLoginResp, error) {
	rawRefresh, refreshHash, err := apputils.GenerateRefreshToken()
	if err != nil {
		return nil, err
	}
	familyID, err := apputils.GenerateSessionFamilyID()
	if err != nil {
		return nil, err
	}
	accessMinutes, refreshDays := (&AuthRouter{ConfigDAO: h.ConfigDAO}).tokenDurations(c.Request.Context())
	roles, permissions := userAuthorization(user)
	accessToken, err := apputils.GenerateAccessToken(user.ID, user.Username, roles, permissions, familyID, accessMinutes)
	if err != nil {
		return nil, err
	}
	if err := h.RefreshDAO.Create(c.Request.Context(), &model.RefreshTokenSession{UserID: user.ID, TokenHash: refreshHash, FamilyID: familyID, ClientType: ClientTypeTMA, ExpiresAt: time.Now().Add(time.Duration(refreshDays) * 24 * time.Hour), ClientIP: c.ClientIP(), UserAgent: c.Request.UserAgent()}); err != nil {
		return nil, err
	}
	return &apis.TelegramLoginResp{AccessToken: accessToken, RefreshToken: rawRefresh, ExpiresIn: int64(accessMinutes * 60), User: apis.TelegramUserBrief{ID: user.ID, Username: user.Username, NickName: user.NickName, Avatar: user.Avatar, Roles: roles, Permissions: permissions}, Binding: bindingInfo(binding)}, nil
}

const onlineSessionWindow = 15 * time.Minute

func (h *TelegramRouter) AdminListBindings(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	bindings, total, err := h.BindingDAO.ListPaginated(c.Request.Context(), page, pageSize)
	if err != nil {
		query.InternalError(c, err)
		return
	}
	userIDs := make([]uint, 0, len(bindings))
	usernames := make([]string, 0, len(bindings))
	for _, binding := range bindings {
		userIDs = append(userIDs, binding.UserID)
		if binding.User != nil {
			usernames = append(usernames, binding.User.Username)
		}
	}
	sessions, _ := h.RefreshDAO.ListActiveByUsers(c.Request.Context(), userIDs)
	latest, _ := h.LoginAuditDAO.LatestSuccessByUsernames(c.Request.Context(), usernames)
	items := make([]apis.TelegramAdminBindingItem, 0, len(bindings))
	for _, binding := range bindings {
		item := apis.TelegramAdminBindingItem{ID: binding.ID, CreatedAt: binding.CreatedAt, UserID: binding.UserID, TelegramUserID: binding.TelegramUserID, TelegramUsername: binding.TelegramUsername, FirstName: binding.FirstName, LastName: binding.LastName, Status: binding.Status, BoundAt: binding.CreatedAt}
		if binding.User != nil {
			item.Username, item.NickName, item.LastLoginAt = binding.User.Username, binding.User.NickName, latest[binding.User.Username]
		}
		for _, session := range sessions[binding.UserID] {
			item.ActiveSessions = append(item.ActiveSessions, apis.TelegramAdminSessionItem{ClientIP: session.ClientIP, UserAgent: session.UserAgent, LastUsedAt: session.LastUsedAt})
			if session.LastUsedAt != nil && time.Since(*session.LastUsedAt) <= onlineSessionWindow {
				item.IsOnline = true
			}
		}
		items = append(items, item)
	}
	query.SuccessResponse(c, nil, gin.H{"list": items, "total": total})
}

func (h *TelegramRouter) AdminUnbind(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		query.BadRequest(c, err)
		return
	}
	if err := h.BindingDAO.AdminUnbind(c.Request.Context(), id); err != nil {
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, nil)
}

type TelegramBindingBatchUnbindReq struct {
	IDs []int64 `json:"ids" binding:"required"`
}

func (h *TelegramRouter) AdminBatchUnbind(c *gin.Context) {
	var req TelegramBindingBatchUnbindReq
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}
	if err := h.BindingDAO.BatchAdminUnbind(c.Request.Context(), req.IDs); err != nil {
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, nil)
}

func (h *TelegramRouter) AdminListTemplates(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	rows, total, err := h.TemplateDAO.ListPaginated(c.Request.Context(), page, pageSize, c.Query("keyword"))
	if err != nil {
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, gin.H{"list": rows, "total": total})
}

func (h *TelegramRouter) AdminUpdateTemplate(c *gin.Context) {
	var req apis.NotifyTemplateUpdateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}
	if err := h.TemplateDAO.Update(c.Request.Context(), c.Param("key"), req.TitleTemplate, req.BodyTemplate, req.ButtonsJSON, req.Enabled); err != nil {
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, nil)
}
