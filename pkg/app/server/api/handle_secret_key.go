package api

import (
	"errors"
	"github.com/cylonchau/kube-res-foresight/pkg/apis"
	"github.com/cylonchau/kube-res-foresight/pkg/app/query"
	"github.com/cylonchau/kube-res-foresight/pkg/dao/rdb"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/gin-gonic/gin"
)

type SecretKeyRouter struct {
	DAO     *rdb.SecretKeyDAO
	PermDAO *rdb.PermissionDAO
}

func secretKeyDTO(k *model.SecretKey) *apis.SecretKeyDTO {
	if k == nil {
		return nil
	}
	return &apis.SecretKeyDTO{
		ID:            k.ID,
		CreatedAt:     k.CreatedAt,
		UpdatedAt:     k.UpdatedAt,
		Name:          k.Name,
		KeyType:       k.KeyType,
		MaskedValue:   k.MaskedValue,
		Status:        k.Status,
		Remark:        k.Remark,
		AccessKey:     k.AccessKey,
		AccessSecret:  k.AccessSecret,
		ServerEnabled: k.ServerEnabled,
		PermissionIDs: k.PermissionIDs,
		ExpiresAt:     k.ExpiresAt,
	}
}

func secretKeyDTOs(rows []model.SecretKey) []*apis.SecretKeyDTO {
	dtos := make([]*apis.SecretKeyDTO, 0, len(rows))
	for i := range rows {
		dtos = append(dtos, secretKeyDTO(&rows[i]))
	}
	return dtos
}

func (r *SecretKeyRouter) List(c *gin.Context) {
	req := &apis.SecretKeyListReq{}
	if err := c.ShouldBindQuery(req); err != nil {
		query.BadRequest(c, errors.New(query.GetErrorMsg(req, err)))
		return
	}
	rows, total, err := r.DAO.List(c.Request.Context(), req)
	if err != nil {
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, map[string]interface{}{"list": secretKeyDTOs(rows), "total": total})
}

func (r *SecretKeyRouter) PermissionOptions(c *gin.Context) {
	permissions, err := r.PermDAO.GetAll(c.Request.Context())
	if err != nil {
		query.InternalError(c, err)
		return
	}
	options := make([]map[string]interface{}, 0, len(permissions))
	for _, permission := range permissions {
		if !permission.Status || permission.BeApis == "" {
			continue
		}
		options = append(options, map[string]interface{}{
			"id": permission.ID, "name": permission.Name, "code": permission.Code,
			"be_apis": permission.BeApis,
		})
	}
	query.SuccessResponse(c, nil, options)
}

func (r *SecretKeyRouter) Get(c *gin.Context) {
	id := &apis.QueryByID{}
	if err := c.ShouldBindUri(id); err != nil {
		query.BadRequest(c, errors.New(query.GetErrorMsg(id, err)))
		return
	}
	row, err := r.DAO.GetByID(c.Request.Context(), id.ID)
	if err != nil {
		query.NotFound(c, query.ErrParam)
		return
	}
	query.SuccessResponse(c, nil, secretKeyDTO(row))
}

func (r *SecretKeyRouter) Create(c *gin.Context) {
	req := &apis.SecretKeyCreateReq{}
	if err := c.ShouldBindJSON(req); err != nil {
		query.BadRequest(c, errors.New(query.GetErrorMsg(req, err)))
		return
	}
	row, err := r.DAO.Create(c.Request.Context(), req)
	if err != nil {
		query.HandleDBError(c, err)
		return
	}
	query.SuccessResponse(c, nil, secretKeyDTO(row))
}

func (r *SecretKeyRouter) Update(c *gin.Context) {
	id := &apis.QueryByID{}
	if err := c.ShouldBindUri(id); err != nil {
		query.BadRequest(c, errors.New(query.GetErrorMsg(id, err)))
		return
	}
	req := &apis.SecretKeyUpdateReq{}
	if err := c.ShouldBindJSON(req); err != nil {
		query.BadRequest(c, errors.New(query.GetErrorMsg(req, err)))
		return
	}
	row, err := r.DAO.Update(c.Request.Context(), id.ID, req)
	if err != nil {
		query.HandleDBError(c, err)
		return
	}
	query.SuccessResponse(c, nil, secretKeyDTO(row))
}

func (r *SecretKeyRouter) Delete(c *gin.Context) {
	id := &apis.QueryByID{}
	if err := c.ShouldBindUri(id); err != nil {
		query.BadRequest(c, errors.New(query.GetErrorMsg(id, err)))
		return
	}
	if err := r.DAO.Delete(c.Request.Context(), id.ID); err != nil {
		query.HandleDBError(c, err)
		return
	}
	query.SuccessResponse(c, nil, nil)
}
