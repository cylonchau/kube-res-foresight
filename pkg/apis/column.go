package apis

type ColumnConfigUpdateReq struct {
	ViewName string `json:"view_name" binding:"required"`
	Config   string `json:"config" binding:"required"`
}

type ColumnMetaReq struct {
	Page  string `form:"page" json:"page"`
	Model string `form:"model" json:"model"`
}

type ColumnConfigReq struct {
	ViewName string `form:"view_name" json:"view_name"`
	View     string `form:"view" json:"view"` // Alternative field name often used
}
