package apis

import "time"

type RoleDTO struct {
	ID          uint                `json:"id"`
	CreatedAt   time.Time           `json:"createdAt"`
	UpdatedAt   time.Time           `json:"updatedAt"`
	Name        string              `json:"name"`
	Code        string              `json:"code"`
	Status      bool                `json:"status"`
	Remark      string              `json:"remark"`
	Permissions []RolePermissionDTO `json:"permissions,omitempty"`
}

// RolePermissionDTO mirrors model.Permission's exact field set and json tags
// (distinct from the snake_case PermissionDTO used by /permissions) since
// that's the shape /roles has always returned for its nested Permissions.
type RolePermissionDTO struct {
	ID          uint      `json:"id"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
	Name        string    `json:"name"`
	Code        string    `json:"code"`
	Type        string    `json:"type"`
	ParentID    uint      `json:"parentId"`
	FeRoute     string    `json:"feRoute"`
	FeComponent string    `json:"feComponent"`
	Sort        int       `json:"sort"`
	Icon        string    `json:"icon"`
	IsLink      bool      `json:"isLink"`
	IsCache     bool      `json:"isCache"`
	Visible     bool      `json:"visible"`
	Status      bool      `json:"status"`
	BeApis      string    `json:"beApis"`
	Position    string    `json:"position"`
	BtnType     string    `json:"btnType"`
}

type RoleListReq struct {
	ListOptions
	RoleName string `form:"role_name" json:"role_name"`
	RoleCode string `form:"role_code" json:"role_code"`
	Status   string `form:"status" json:"status"`
}

type RoleCreateReq struct {
	RoleName    string `json:"name" binding:"required"`
	RoleCode    string `json:"code" binding:"required"`
	Status      bool   `json:"status"`
	Remark      string `json:"remark"`
	Permissions []uint `json:"permissions"`
}

type RoleUpdateReq struct {
	RoleName    *string `json:"name"`
	RoleCode    *string `json:"code"`
	Status      *bool   `json:"status"`
	Remark      *string `json:"remark"`
	Permissions *[]uint `json:"permissions"`
}
