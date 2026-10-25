package api

import (
	"fmt"
	"github.com/cylonchau/kube-resource-fcst/pkg/apis"
	"github.com/cylonchau/kube-resource-fcst/pkg/app/query"
	"github.com/cylonchau/kube-resource-fcst/pkg/app/utils"
	"github.com/cylonchau/kube-resource-fcst/pkg/dao/rdb"
	"github.com/cylonchau/kube-resource-fcst/pkg/model"
	"github.com/gin-gonic/gin"
	"strconv"
	"strings"
	"time"
)

// PermissionDTO is deprecated, use apis.PermissionDTO
type PermissionDTO = apis.PermissionDTO

type PermissionRouter struct {
	DAO *rdb.PermissionDAO
}

// ListTree gets a tree structure of all permissions
// @Summary List permissions tree
// @Description Retrieve all permissions in a flat list (to be structured as a tree by the frontend).
// @Tags Permissions
// @Accept json
// @Produce json
// @Success 200 {object} query.Response{data=[]apis.PermissionDTO} "Success"
// @Router /api/v1/permissions [get]
func (r *PermissionRouter) ListTree(c *gin.Context) {
	perms, err := r.DAO.GetAll(c)
	if err != nil {
		query.InternalError(c, err)
		return
	}

	var dtos []PermissionDTO
	for _, p := range perms {
		dtos = append(dtos, PermissionDTO{
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
		})
	}

	query.SuccessResponse(c, nil, dtos)
}

// Create creates a new permission
// @Summary Create permission
// @Description Create a new system permission (Menu, Button, or Directory).
// @Tags Permissions
// @Accept json
// @Produce json
// @Param request body apis.PermissionCreateReq true "Permission information"
// @Success 200 {object} query.Response{data=apis.PermissionDTO} "Success"
// @Router /api/v1/permissions [post]
func (r *PermissionRouter) Create(c *gin.Context) {
	var req apis.PermissionCreateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}

	code := strings.TrimSpace(req.Code)
	if code == "" {
		code = fmt.Sprintf("perm_%d", time.Now().UnixNano())
	}

	perm := &model.Permission{
		Name:        req.Name,
		Code:        code,
		Type:        req.Type,
		ParentID:    req.ParentID,
		FeRoute:     req.FeRoute,
		FeComponent: req.FeComponent,
		Sort:        req.Sort,
		Icon:        req.Icon,
		IsLink:      req.IsLink,
		IsCache:     req.IsCache,
		Visible:     req.Visible,
		Status:      req.Status,
		BeApis:      req.BeApis,
	}

	if err := r.DAO.Create(c, perm); err != nil {
		query.InternalError(c, err)
		return
	}

	_ = utils.SyncPolicies()

	dto := PermissionDTO{
		ID:          perm.ID,
		CreatedAt:   perm.CreatedAt,
		UpdatedAt:   perm.UpdatedAt,
		Name:        perm.Name,
		Code:        perm.Code,
		Type:        perm.Type,
		ParentID:    perm.ParentID,
		FeRoute:     perm.FeRoute,
		FeComponent: perm.FeComponent,
		Sort:        perm.Sort,
		Icon:        perm.Icon,
		IsLink:      perm.IsLink,
		IsCache:     perm.IsCache,
		Visible:     perm.Visible,
		Status:      perm.Status,
		BeApis:      perm.BeApis,
	}
	query.SuccessResponse(c, nil, dto)
}

// Update updates an existing permission
// @Summary Update permission
// @Description Update permission details by ID.
// @Tags Permissions
// @Accept json
// @Produce json
// @Param id path int true "Permission ID"
// @Param request body apis.PermissionUpdateReq true "Update details"
// @Success 200 {object} query.Response{data=apis.PermissionDTO} "Success"
// @Failure 404 {object} query.Response "Permission Not Found"
// @Router /api/v1/permissions/{id} [put]
func (r *PermissionRouter) Update(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		query.BadRequest(c, err)
		return
	}

	var req apis.PermissionUpdateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}

	perms, err := r.DAO.GetAll(c)
	if err != nil {
		query.InternalError(c, err)
		return
	}

	var targetPerm *model.Permission
	for _, p := range perms {
		if p.ID == uint(id) {
			targetPerm = p
			break
		}
	}
	if targetPerm == nil {
		query.NotFound(c, err)
		return
	}

	targetPerm.Name = req.Name
	if req.Code != "" {
		targetPerm.Code = req.Code
	}
	targetPerm.Type = req.Type
	targetPerm.ParentID = req.ParentID
	targetPerm.FeRoute = req.FeRoute
	targetPerm.FeComponent = req.FeComponent
	if req.Sort != nil {
		targetPerm.Sort = *req.Sort
	}
	targetPerm.Icon = req.Icon
	targetPerm.IsLink = req.IsLink
	targetPerm.IsCache = req.IsCache
	targetPerm.Visible = req.Visible
	targetPerm.Status = req.Status
	targetPerm.BeApis = req.BeApis

	if err := r.DAO.Update(c, targetPerm); err != nil {
		query.InternalError(c, err)
		return
	}

	_ = utils.SyncPolicies()

	dto := PermissionDTO{
		ID:          targetPerm.ID,
		CreatedAt:   targetPerm.CreatedAt,
		UpdatedAt:   targetPerm.UpdatedAt,
		Name:        targetPerm.Name,
		Code:        targetPerm.Code,
		Type:        targetPerm.Type,
		ParentID:    targetPerm.ParentID,
		FeRoute:     targetPerm.FeRoute,
		FeComponent: targetPerm.FeComponent,
		Sort:        targetPerm.Sort,
		Icon:        targetPerm.Icon,
		IsLink:      targetPerm.IsLink,
		IsCache:     targetPerm.IsCache,
		Visible:     targetPerm.Visible,
		Status:      targetPerm.Status,
		BeApis:      targetPerm.BeApis,
	}
	query.SuccessResponse(c, nil, dto)
}

// Delete removes a permission
// @Summary Delete permission
// @Description Permanently deletes a permission by ID.
// @Tags Permissions
// @Accept json
// @Produce json
// @Param id path int true "Permission ID"
// @Success 200 {object} query.Response "Success"
// @Router /api/v1/permissions/{id} [delete]
func (r *PermissionRouter) Delete(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		query.BadRequest(c, err)
		return
	}

	if err := r.DAO.Delete(c, uint(id)); err != nil {
		query.InternalError(c, err)
		return
	}

	_ = utils.SyncPolicies()
	query.SuccessResponse(c, nil, "success")
}
