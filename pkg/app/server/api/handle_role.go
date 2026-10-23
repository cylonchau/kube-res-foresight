package api

import (
	"context"
	"github.com/cylonchau/kubeforecast/pkg/apis"
	"github.com/cylonchau/kubeforecast/pkg/app/query"
	"github.com/cylonchau/kubeforecast/pkg/app/utils"
	"github.com/cylonchau/kubeforecast/pkg/dao/rdb"
	"github.com/cylonchau/kubeforecast/pkg/model"
	"github.com/gin-gonic/gin"
	"strconv"
)

type RoleRouter struct {
	DAO *rdb.RoleDAO
}

func rolePermissionDTO(p *model.Permission) apis.RolePermissionDTO {
	return apis.RolePermissionDTO{
		ID:          p.ID,
		CreatedAt:   p.CreatedAt,
		UpdatedAt:   p.UpdatedAt,
		Name:        p.Name,
		Code:        p.Code,
		Type:        p.Type,
		ParentID:    p.ParentID,
		FeRoute:     p.FeRoute,
		FeComponent: p.FeComponent,
		Sort:        p.Sort,
		Icon:        p.Icon,
		IsLink:      p.IsLink,
		IsCache:     p.IsCache,
		Visible:     p.Visible,
		Status:      p.Status,
		BeApis:      p.BeApis,
		Position:    p.Position,
		BtnType:     p.BtnType,
	}
}

func roleDTO(r *model.Role) *apis.RoleDTO {
	if r == nil {
		return nil
	}
	dto := &apis.RoleDTO{
		ID: r.ID, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
		Name: r.Name, Code: r.Code, Status: r.Status, Remark: r.Remark,
	}
	if len(r.Permissions) > 0 {
		dto.Permissions = make([]apis.RolePermissionDTO, 0, len(r.Permissions))
		for i := range r.Permissions {
			dto.Permissions = append(dto.Permissions, rolePermissionDTO(&r.Permissions[i]))
		}
	}
	return dto
}

func roleDTOs(roles []*model.Role) []*apis.RoleDTO {
	dtos := make([]*apis.RoleDTO, 0, len(roles))
	for _, r := range roles {
		dtos = append(dtos, roleDTO(r))
	}
	return dtos
}

// List gets a list of roles
// @Summary List roles
// @Description Retrieve a list of roles with pagination and filters for name, code, and status.
// @Tags Roles
// @Accept json
// @Produce json
// @Param query query apis.RoleListReq true "Query parameters"
// @Success 200 {object} query.Response{data=map[string]interface{list=[]model.Role,total=int64,page=int,pageSize=int}} "Success"
// @Router /api/v1/roles [get]
func (h *RoleRouter) List(c *gin.Context) {
	var req apis.RoleListReq
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
	roles, total, err := h.DAO.List(context.Background(), req.RoleName, req.RoleCode, status, int(offset), int(req.PageSize))
	if err != nil {
		query.InternalError(c, err)
		return
	}

	query.SuccessResponse(c, nil, gin.H{
		"list":     roleDTOs(roles),
		"total":    total,
		"page":     req.Page,
		"pageSize": req.PageSize,
	})
}

// ListAll gets all roles
// @Summary List all roles
// @Description Retrieve a list of all roles in the system without pagination.
// @Tags Roles
// @Accept json
// @Produce json
// @Success 200 {object} query.Response{data=[]model.Role} "Success"
// @Router /api/v1/roles/all [get]
func (h *RoleRouter) ListAll(c *gin.Context) {
	roles, err := h.DAO.ListAll(context.Background())
	if err != nil {
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, roleDTOs(roles))
}

// Create creates a new role
// @Summary Create role
// @Description Create a new system role.
// @Tags Roles
// @Accept json
// @Produce json
// @Param request body apis.RoleCreateReq true "Role information"
// @Success 200 {object} query.Response "Success"
// @Router /api/v1/roles [post]
func (h *RoleRouter) Create(c *gin.Context) {
	var req apis.RoleCreateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}

	role := &model.Role{
		Name:   req.RoleName,
		Code:   req.RoleCode,
		Status: req.Status,
		Remark: req.Remark,
	}

	if err := h.DAO.Create(context.Background(), role); err != nil {
		query.InternalError(c, err)
		return
	}

	if len(req.Permissions) > 0 {
		_ = h.DAO.UpdatePermissions(context.Background(), role.ID, req.Permissions)
		_ = utils.SyncPolicies()
	}

	query.SuccessResponse(c, nil, "Created")
}

// Update updates an existing role
// @Summary Update role
// @Description Update role details (name, code, status, remark, permissions) by ID.
// @Tags Roles
// @Accept json
// @Produce json
// @Param id path int true "Role ID"
// @Param request body apis.RoleUpdateReq true "Update details"
// @Success 200 {object} query.Response "Success"
// @Router /api/v1/roles/{id} [put]
func (h *RoleRouter) Update(c *gin.Context) {
	idStr := c.Param("id")
	id, _ := strconv.ParseUint(idStr, 10, 64)

	var req apis.RoleUpdateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}

	role, err := h.DAO.GetByID(context.Background(), uint(id))
	if err != nil {
		query.NotFound(c, err)
		return
	}

	if req.RoleName != nil {
		role.Name = *req.RoleName
	}
	if req.RoleCode != nil {
		role.Code = *req.RoleCode
	}
	if req.Status != nil {
		role.Status = *req.Status
	}
	if req.Remark != nil {
		role.Remark = *req.Remark
	}

	if err := h.DAO.Update(context.Background(), role); err != nil {
		query.InternalError(c, err)
		return
	}

	if req.Permissions != nil {
		if err := h.DAO.UpdatePermissions(context.Background(), uint(id), *req.Permissions); err != nil {
			query.InternalError(c, err)
			return
		}
		_ = utils.SyncPolicies()
	}

	query.SuccessResponse(c, nil, nil)
}

// Delete removes a role
// @Summary Delete role
// @Description Permanently deletes a role account by ID.
// @Tags Roles
// @Accept json
// @Produce json
// @Param id path int true "Role ID"
// @Success 200 {object} query.Response "Success"
// @Router /api/v1/roles/{id} [delete]
func (h *RoleRouter) Delete(c *gin.Context) {
	idStr := c.Param("id")
	id, _ := strconv.ParseUint(idStr, 10, 64)

	if err := h.DAO.Delete(context.Background(), uint(id)); err != nil {
		query.InternalError(c, err)
		return
	}

	_ = utils.SyncPolicies()
	query.SuccessResponse(c, nil, nil)
}

// BatchDelete removes multiple roles in one request
// @Summary Batch delete roles
// @Description Permanently deletes multiple roles by providing a list of IDs.
// @Tags Roles
// @Accept json
// @Produce json
// @Param request body apis.QueryByIDs true "List of Role IDs"
// @Success 200 {object} query.Response "Success"
// @Router /api/v1/roles/batch-delete [post]
func (h *RoleRouter) BatchDelete(c *gin.Context) {
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
