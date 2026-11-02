package api

import (
	"context"
	"errors"
	"github.com/cylonchau/kube-res-foresight/pkg/apis"
	"github.com/cylonchau/kube-res-foresight/pkg/app/query"
	"github.com/cylonchau/kube-res-foresight/pkg/app/utils"
	"github.com/cylonchau/kube-res-foresight/pkg/dao/rdb"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/gin-gonic/gin"
	"strconv"
	"strings"
	"time"
)

type UserRouter struct {
	DAO           *rdb.UserDAO
	RefreshDAO    *rdb.RefreshTokenSessionDAO
	LoginAuditDAO *rdb.LoginAuditLogDAO
}

func userDTO(u *model.User) *apis.UserDTO {
	if u == nil {
		return nil
	}
	dto := &apis.UserDTO{
		ID:         u.ID,
		CreatedAt:  u.CreatedAt,
		UpdatedAt:  u.UpdatedAt,
		Username:   u.Username,
		NickName:   u.NickName,
		Avatar:     u.Avatar,
		Email:      u.Email,
		Phone:      u.Phone,
		Language:   u.Language,
		Status:     u.Status,
		OtpEnabled: u.OtpEnabled,
		Source:     u.Source,
		Contacts:   make([]apis.UserContactDTO, 0, len(u.Contacts)),
	}
	for _, ct := range u.Contacts {
		dto.Contacts = append(dto.Contacts, apis.UserContactDTO{Type: ct.Type, Value: ct.Value})
	}
	if len(u.Roles) > 0 {
		dto.Roles = make([]apis.RoleDTO, 0, len(u.Roles))
		for i := range u.Roles {
			dto.Roles = append(dto.Roles, *roleDTO(&u.Roles[i]))
		}
	}
	return dto
}

// cleanContacts turns the contacts of a request into rows: types are lower case names, values are trimmed, empty ones and
// repeats are dropped, and a type or value that is too long is refused.
func cleanContacts(in []apis.UserContactDTO) ([]model.UserContact, error) {
	out := make([]model.UserContact, 0, len(in))
	seen := map[string]bool{}
	for _, c := range in {
		typ, value := strings.ToLower(strings.TrimSpace(c.Type)), strings.TrimSpace(c.Value)
		if typ == "" && value == "" {
			continue
		}
		if typ == "" || value == "" {
			return nil, errors.New("a contact needs both a type and a value")
		}
		if len([]rune(typ)) > 32 || len([]rune(value)) > 190 {
			return nil, errors.New("a contact type is at most 32 characters and a value at most 190")
		}
		if seen[typ+"|"+value] {
			continue
		}
		seen[typ+"|"+value] = true
		out = append(out, model.UserContact{Type: typ, Value: value})
	}
	return out, nil
}

func userDTOs(users []*model.User) []*apis.UserDTO {
	dtos := make([]*apis.UserDTO, 0, len(users))
	for _, u := range users {
		dtos = append(dtos, userDTO(u))
	}
	return dtos
}

// List gets a list of users
// @Summary List users
// @Description Retrieve a list of users with pagination and filters for username, nickname, and status.
// @Tags Users
// @Accept json
// @Produce json
// @Param query query apis.UserListReq true "Query parameters"
// @Success 200 {object} query.Response{data=map[string]interface{list=[]model.User,total=int64,page=int,pageSize=int}} "Success"
// @Failure 500 {object} query.Response "Internal Error"
// @Router /api/v1/users [get]
func (h *UserRouter) List(c *gin.Context) {
	var req apis.UserListReq
	if err := c.ShouldBindQuery(&req); err != nil {
		query.BadRequest(c, err)
		return
	}

	var status *bool
	if req.Status != "" {
		b, err := strconv.ParseBool(req.Status)
		if err == nil {
			status = &b
		}
	}

	offset := (req.Page - 1) * req.PageSize
	users, total, err := h.DAO.List(context.Background(), req.Username, req.NickName, status, req.Source, int(offset), int(req.PageSize))
	if err != nil {
		query.InternalError(c, err)
		return
	}

	items := userDTOs(users)
	now := time.Now()
	for i, u := range users {
		if h.LoginAuditDAO != nil {
			if last, err := h.LoginAuditDAO.LatestSuccessRecordByUsername(c.Request.Context(), u.Username); err == nil && last != nil {
				items[i].LastLoginAt = &last.CreatedAt
				items[i].LastLoginIP = last.ClientIP
			}
		}
		if h.RefreshDAO == nil {
			continue
		}
		sessions, err := h.RefreshDAO.ListActiveByUser(c.Request.Context(), u.ID)
		if err != nil {
			continue
		}
		items[i].ActiveSessions = make([]apis.UserActiveSessionItem, 0, len(sessions))
		for _, s := range sessions {
			items[i].ActiveSessions = append(items[i].ActiveSessions, apis.UserActiveSessionItem{
				ClientIP: s.ClientIP, UserAgent: s.UserAgent, LastUsedAt: s.LastUsedAt,
			})
			lastActive := s.CreatedAt
			if s.LastUsedAt != nil {
				lastActive = *s.LastUsedAt
			}
			if now.Sub(lastActive) <= onlineSessionWindow {
				items[i].IsOnline = true
			}
		}
	}

	query.SuccessResponse(c, nil, gin.H{
		"list":     items,
		"total":    total,
		"page":     req.Page,
		"pageSize": req.PageSize,
	})
}

// Create creates a new user
// @Summary Create user
// @Description Create a new system user with assigned roles.
// @Tags Users
// @Accept json
// @Produce json
// @Param request body apis.UserCreateReq true "User information"
// @Success 200 {object} query.Response "Success"
// @Failure 400 {object} query.Response "Bad Request"
// @Failure 500 {object} query.Response "Internal Error"
// @Router /api/v1/users [post]
func (h *UserRouter) Create(c *gin.Context) {
	var req apis.UserCreateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}

	contacts, err := cleanContacts(req.Contacts)
	if err != nil {
		query.BadRequest(c, err)
		return
	}
	hash, err := utils.HashPassword(req.Password)
	if err != nil {
		query.InternalError(c, err)
		return
	}

	var roles []model.Role
	if len(req.RoleIDs) > 0 {
		model.DB.Find(&roles, req.RoleIDs)
	}

	user := &model.User{
		Username:     req.Username,
		PasswordHash: hash,
		NickName:     req.NickName,
		Status:       req.Status,
		Roles:        roles,
	}

	if err := h.DAO.Create(context.Background(), user); err != nil {
		query.InternalError(c, err)
		return
	}
	if len(contacts) > 0 {
		if err := h.DAO.ReplaceContacts(context.Background(), user.ID, contacts); err != nil {
			query.InternalError(c, err)
			return
		}
	}

	_ = utils.SyncPolicies()
	query.SuccessResponse(c, nil, "Created")
}

// Update updates an existing user
// @Summary Update user
// @Description Update user account details and roles by ID.
// @Tags Users
// @Accept json
// @Produce json
// @Param id path int true "User ID"
// @Param request body apis.UserUpdateReq true "Update details"
// @Success 200 {object} query.Response "Success"
// @Failure 400 {object} query.Response "Bad Request"
// @Failure 404 {object} query.Response "User Not Found"
// @Router /api/v1/users/{id} [put]
func (h *UserRouter) Update(c *gin.Context) {
	idStr := c.Param("id")
	id, _ := strconv.ParseUint(idStr, 10, 64)

	var req apis.UserUpdateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}
	if currentUserID, exists := c.Get("userId"); exists && req.Status != nil && !*req.Status {
		if userID, ok := currentUserID.(uint); ok && userID == uint(id) {
			query.Forbidden(c, query.ErrCannotDisableSelf)
			return
		}
	}

	user, err := h.DAO.GetByID(context.Background(), uint(id))
	if err != nil {
		query.NotFound(c, err)
		return
	}

	if req.Password != "" {
		hash, err := utils.HashPassword(req.Password)
		if err != nil {
			query.InternalError(c, err)
			return
		}
		user.PasswordHash = hash
	}
	if req.NickName != "" {
		user.NickName = req.NickName
	}
	if req.Status != nil {
		user.Status = *req.Status
	}

	// Update roles
	if req.RoleIDs != nil {
		var roles []model.Role
		model.DB.Find(&roles, req.RoleIDs)
		if err := model.DB.Model(user).Association("Roles").Replace(roles); err != nil {
			query.InternalError(c, err)
			return
		}
	}

	// Save other fields (excluding roles which are handled above)
	if err := h.DAO.Update(context.Background(), user); err != nil {
		query.InternalError(c, err)
		return
	}
	if req.Contacts != nil {
		contacts, err := cleanContacts(req.Contacts)
		if err != nil {
			query.BadRequest(c, err)
			return
		}
		if err := h.DAO.ReplaceContacts(context.Background(), user.ID, contacts); err != nil {
			query.InternalError(c, err)
			return
		}
	}
	if req.Password != "" || (req.Status != nil && !*req.Status) {
		if err := h.RefreshDAO.RevokeUser(c.Request.Context(), user.ID); err != nil {
			query.InternalError(c, err)
			return
		}
	}

	_ = utils.SyncPolicies()
	query.SuccessResponse(c, nil, nil)
}

// ResetOTP disables 2FA for a user
// @Summary Reset user OTP
// @Description Disables 2FA and clears the OTP secret for a specific user.
// @Tags Users
// @Accept json
// @Produce json
// @Param id path int true "User ID"
// @Success 200 {object} query.Response "Success"
// @Failure 404 {object} query.Response "User Not Found"
// @Router /api/v1/users/otp/reset/{id} [post]
func (h *UserRouter) ResetOTP(c *gin.Context) {
	idStr := c.Param("id")
	id, _ := strconv.ParseUint(idStr, 10, 64)

	user, err := h.DAO.GetByID(context.Background(), uint(id))
	if err != nil {
		query.NotFound(c, err)
		return
	}

	user.OtpEnabled = false
	user.OtpSecret = ""

	if err := h.DAO.Update(context.Background(), user); err != nil {
		query.InternalError(c, err)
		return
	}

	query.SuccessResponse(c, nil, "OTP Reset Successful")
}

// Delete removes a user
// @Summary Delete user
// @Description Permanently deletes a user account by ID.
// @Tags Users
// @Accept json
// @Produce json
// @Param id path int true "User ID"
// @Success 200 {object} query.Response "Success"
// @Router /api/v1/users/{id} [delete]
func (h *UserRouter) Delete(c *gin.Context) {
	idStr := c.Param("id")
	id, _ := strconv.ParseUint(idStr, 10, 64)

	if err := h.DAO.Delete(context.Background(), uint(id)); err != nil {
		query.InternalError(c, err)
		return
	}

	_ = utils.SyncPolicies()
	query.SuccessResponse(c, nil, nil)
}

// BatchDelete removes multiple users in one request
// @Summary Batch delete users
// @Description Permanently deletes multiple users by providing a list of IDs.
// @Tags Users
// @Accept json
// @Produce json
// @Param request body apis.QueryByIDs true "List of User IDs"
// @Success 200 {object} query.Response "Success"
// @Failure 400 {object} query.Response "Bad Request"
// @Router /api/v1/users/batch-delete [post]
func (h *UserRouter) BatchDelete(c *gin.Context) {
	var req apis.QueryByIDs
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}

	ids := make([]uint, len(req.IDs))
	for i, id := range req.IDs {
		ids[i] = uint(id)
	}
	if err := h.DAO.BatchDelete(context.Background(), ids); err != nil {
		query.InternalError(c, err)
		return
	}

	_ = utils.SyncPolicies()
	query.SuccessResponse(c, nil, nil)
}

// onlineSessionWindow is how recently a session must have been used for its user to count as online.
const onlineSessionWindow = 15 * time.Minute
