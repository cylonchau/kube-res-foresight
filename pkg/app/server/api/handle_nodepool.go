package api

import (
	"errors"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/cylonchau/kube-res-foresight/pkg/apis"
	"github.com/cylonchau/kube-res-foresight/pkg/app/query"
	"github.com/cylonchau/kube-res-foresight/pkg/dao/rdb"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
)

// NodePoolRouter serves the node pools: the groups of nodes a workload can be steered to, with the labels that select them and the size of one node.
type NodePoolRouter struct {
	DAO *rdb.NodePoolDAO
}

// NodePoolReq is a pool as it is written: a create gives every field, an update only the ones it changes.
type NodePoolReq struct {
	Cluster         *string           `json:"cluster"`
	Name            *string           `json:"name"`
	Kind            *string           `json:"kind"`
	NodeSelector    map[string]string `json:"node_selector"`
	NodeCPUMilli    *int64            `json:"node_cpu_milli"`
	NodeMemoryBytes *int64            `json:"node_memory_bytes"`
	NodeGPU         *int              `json:"node_gpu"`
	Enabled         *bool             `json:"enabled"`
	Priority        *int              `json:"priority"`
	Remark          *string           `json:"remark"`
}

func applyNodePool(p *model.NodePool, req *NodePoolReq) {
	if req.Cluster != nil {
		p.Cluster = *req.Cluster
	}
	if req.Name != nil {
		p.Name = *req.Name
	}
	if req.Kind != nil {
		p.Kind = *req.Kind
	}
	if req.NodeSelector != nil {
		p.NodeSelector = req.NodeSelector
	}
	if req.NodeCPUMilli != nil {
		p.NodeCPUMilli = *req.NodeCPUMilli
	}
	if req.NodeMemoryBytes != nil {
		p.NodeMemoryBytes = *req.NodeMemoryBytes
	}
	if req.NodeGPU != nil {
		p.NodeGPU = *req.NodeGPU
	}
	if req.Enabled != nil {
		p.Enabled = *req.Enabled
	}
	if req.Priority != nil {
		p.Priority = *req.Priority
	}
	if req.Remark != nil {
		p.Remark = *req.Remark
	}
}

func (r *NodePoolRouter) writeError(c *gin.Context, err error) {
	if errors.Is(err, rdb.ErrNameTaken) {
		query.BadRequest(c, errors.New("a node pool of that name already exists in the cluster"))
		return
	}
	query.HandleDBError(c, err)
}

// List serves GET /node-pools.
func (r *NodePoolRouter) List(c *gin.Context) {
	var opts apis.ListOptions
	if err := c.ShouldBindQuery(&opts); err != nil {
		query.BadRequest(c, err)
		return
	}
	list, total, err := r.DAO.List(c.Request.Context(), rdb.NodePoolFilter{Keyword: strings.TrimSpace(opts.Keyword), Kind: c.Query("kind"), Cluster: c.Query("cluster"),
		Enabled: boolQuery(c, "enabled"), Page: int(opts.Page), Size: int(opts.PageSize)})
	if err != nil {
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, gin.H{"list": list, "total": total, "page": opts.Page, "pageSize": opts.PageSize})
}

// Get serves GET /node-pools/:id.
func (r *NodePoolRouter) Get(c *gin.Context) {
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	p, err := r.DAO.Get(c.Request.Context(), id)
	if err != nil {
		query.HandleDBError(c, err)
		return
	}
	query.SuccessResponse(c, nil, p)
}

// Create serves POST /node-pools.
func (r *NodePoolRouter) Create(c *gin.Context) {
	var req NodePoolReq
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}
	p := &model.NodePool{Enabled: true}
	applyNodePool(p, &req)
	if err := p.Validate(); err != nil {
		query.BadRequest(c, err)
		return
	}
	if err := r.DAO.Create(c.Request.Context(), p); err != nil {
		r.writeError(c, err)
		return
	}
	query.SuccessResponse(c, nil, p)
}

// Update serves PUT /node-pools/:id.
func (r *NodePoolRouter) Update(c *gin.Context) {
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	var req NodePoolReq
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}
	p, err := r.DAO.Get(c.Request.Context(), id)
	if err != nil {
		query.HandleDBError(c, err)
		return
	}
	applyNodePool(p, &req)
	if err := p.Validate(); err != nil {
		query.BadRequest(c, err)
		return
	}
	if err := r.DAO.Update(c.Request.Context(), p); err != nil {
		r.writeError(c, err)
		return
	}
	query.SuccessResponse(c, nil, p)
}

// Delete serves DELETE /node-pools/:id.
func (r *NodePoolRouter) Delete(c *gin.Context) {
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	if err := r.DAO.Delete(c.Request.Context(), []uint{id}); err != nil {
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, "ok")
}

// BatchDelete serves POST /node-pools/batch-delete.
func (r *NodePoolRouter) BatchDelete(c *gin.Context) {
	var req apis.QueryByIDs
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}
	ids := make([]uint, 0, len(req.IDs))
	for _, id := range req.IDs {
		ids = append(ids, uint(id))
	}
	if err := r.DAO.Delete(c.Request.Context(), ids); err != nil {
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, "ok")
}
