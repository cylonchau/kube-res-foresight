package apis

type QueryByID struct {
	ID uint64 `json:"id" form:"id" uri:"id" query:"id" binding:"required" msg:"id is required"`
}

type QueryByIDs struct {
	IDs []uint64 `json:"ids" form:"ids" query:"ids" binding:"required"`
}

type QuertByName struct {
	ID string `json:"name" form:"name" query:"name" binding:"required" msg:"name is required"`
}

// ListOptions list params
type ListOptions struct {
	Page     uint16 `form:"page,default=1" json:"page"`
	PageSize uint16 `form:"page_size,default=10" json:"page_size"`
	SortBy   string `form:"sort_by,default=asc" json:"sort_by"`
	Keyword  string `form:"keyword" json:"keyword"`
}

func (o *ListOptions) Offset() uint16 {
	if o.Page <= 1 {
		return 0
	}
	return (o.Page - 1) * o.PageSize
}
