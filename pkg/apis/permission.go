package apis

import "time"

type PermissionDTO struct {
	ID          uint      `json:"id"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	Name        string    `json:"name"`
	Code        string    `json:"code"`
	Type        string    `json:"type"`
	ParentID    uint      `json:"parent_id"`
	FeRoute     string    `json:"fe_route"`
	FeComponent string    `json:"fe_component"`
	Sort        int       `json:"sort"`
	Icon        string    `json:"icon"`
	IsLink      bool      `json:"is_link"`
	IsCache     bool      `json:"is_cache"`
	Visible     bool      `json:"visible"`
	Status      bool      `json:"status"`
	BeApis      string    `json:"be_apis"`
}

type PermissionCreateReq struct {
	Name        string `json:"name" binding:"required"`
	Code        string `json:"code"`
	Type        string `json:"type" binding:"required"`
	ParentID    uint   `json:"parent_id"`
	FeRoute     string `json:"fe_route"`
	FeComponent string `json:"fe_component"`
	Sort        int    `json:"sort"`
	Icon        string `json:"icon"`
	IsLink      bool   `json:"is_link"`
	IsCache     bool   `json:"is_cache"`
	Visible     bool   `json:"visible"`
	Status      bool   `json:"status"`
	BeApis      string `json:"be_apis"`
}

type PermissionUpdateReq struct {
	Name        string `json:"name" binding:"required"`
	Code        string `json:"code"`
	Type        string `json:"type" binding:"required"`
	ParentID    uint   `json:"parent_id"`
	FeRoute     string `json:"fe_route"`
	FeComponent string `json:"fe_component"`
	Sort        *int   `json:"sort"`
	Icon        string `json:"icon"`
	IsLink      bool   `json:"is_link"`
	IsCache     bool   `json:"is_cache"`
	Visible     bool   `json:"visible"`
	Status      bool   `json:"status"`
	BeApis      string `json:"be_apis"`
}
