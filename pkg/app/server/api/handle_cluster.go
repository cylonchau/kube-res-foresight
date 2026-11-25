package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/app/middleware"
	"github.com/cylonchau/kube-res-foresight/pkg/app/query"
	"github.com/cylonchau/kube-res-foresight/pkg/assistant"
	"github.com/cylonchau/kube-res-foresight/pkg/clusteragent"
	"github.com/cylonchau/kube-res-foresight/pkg/clustersvc"
	"github.com/cylonchau/kube-res-foresight/pkg/dao/rdb"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// The clusters operators report from (P12 E1-E4): the administrator registers a cluster, binds access keys to it, and after the operator is connected sets which
// namespaces it collects; the operator asks for these settings and reports the events of its cluster, which are kept for cluster.retention_days.

// ClusterGate says which cluster an operator's request is for (see clustersvc.Service.Resolve): the cluster it was started for (the header), and the cluster its access
// key is bound to. A nil gate, or one without a service, is a platform with no cluster registered: every request is for the one cluster there was.
type ClusterGate struct{ Service *clustersvc.Service }

// For resolves the cluster of the request and answers the request itself when it cannot: 403 for a credential used for a cluster it is not bound to or a cluster that is
// switched off, 404 for one that is not registered. ok is false then, and the caller returns.
func (g *ClusterGate) For(c *gin.Context) (cluster *model.Cluster, ok bool) {
	if g == nil || g.Service == nil {
		return nil, true
	}
	bound, _ := c.Get("developerCluster")
	boundName, _ := bound.(string)
	cluster, err := g.Service.Resolve(c.Request.Context(), c.GetHeader(clusteragent.HeaderCluster), boundName)
	switch {
	case err == nil:
		return cluster, true
	case errors.Is(err, clustersvc.ErrUnknownCluster):
		query.NotFound(c, err)
	case errors.Is(err, clustersvc.ErrForbidden), errors.Is(err, clustersvc.ErrDisabled):
		query.Forbidden(c, err)
	default:
		query.InternalError(c, err)
	}
	return nil, false
}

// Seen records that an operator of the cluster has been heard from.
func (g *ClusterGate) Seen(c *gin.Context, cluster *model.Cluster) {
	if g == nil || g.Service == nil || cluster == nil {
		return
	}
	g.Service.Touch(c.Request.Context(), cluster, c.GetHeader(middleware.ClientNameHeader))
}

// ClusterRouter serves the cluster page and what the operators of a cluster ask and report of it.
type ClusterRouter struct {
	Service     *clustersvc.Service
	Datasources *rdb.DatasourceDAO
	Gate        *ClusterGate
}

// connectedWithin is how recently an operator was heard from for the cluster to be shown as connected: it asks for its settings every minute.
const connectedWithin = 5 * time.Minute

// ClusterDTO is a cluster as the page shows it.
type ClusterDTO struct {
	model.Cluster
	Events      int64      `json:"events"`
	LastEventAt *time.Time `json:"last_event_at"`
	Connected   bool       `json:"connected"`
}

func (r *ClusterRouter) dto(c *gin.Context, row model.Cluster) (ClusterDTO, error) {
	ctx := c.Request.Context()
	row.NamespaceIncludeList, row.NamespaceExcludeList = clusteragent.ParseNames(row.NamespaceInclude), clusteragent.ParseNames(row.NamespaceExclude)
	var err error
	if row.KeyIDs, err = r.Service.DAO.KeyIDs(ctx, row.Name); err != nil {
		return ClusterDTO{}, err
	}
	if row.Modules, err = r.Service.DAO.ModuleCount(ctx, &row); err != nil {
		return ClusterDTO{}, err
	}
	out := ClusterDTO{Cluster: row}
	if out.Events, out.LastEventAt, err = r.Service.DAO.EventCount(ctx, row.Name); err != nil {
		return ClusterDTO{}, err
	}
	out.Connected = row.LastSeenAt != nil && time.Since(*row.LastSeenAt) < connectedWithin
	return out, nil
}

// List serves GET /clusters.
// @Summary The clusters operators report from
// @Tags Cluster
// @Router /api/v1/clusters [get]
func (r *ClusterRouter) List(c *gin.Context) {
	rows, err := r.Service.DAO.List(c.Request.Context())
	if err != nil {
		query.InternalError(c, err)
		return
	}
	out := make([]ClusterDTO, 0, len(rows))
	for _, row := range rows {
		d, err := r.dto(c, row)
		if err != nil {
			query.InternalError(c, err)
			return
		}
		out = append(out, d)
	}
	query.SuccessResponse(c, nil, gin.H{"list": out, "total": len(out)})
}

// clusterReq is what the cluster page sends.
type clusterReq struct {
	Name             string   `json:"name"`
	Description      string   `json:"description"`
	Enabled          *bool    `json:"enabled"`
	DatasourceID     uint     `json:"datasource_id"`
	NamespaceInclude []string `json:"namespace_include"`
	NamespaceExclude []string `json:"namespace_exclude"`
	KeyIDs           *[]int64 `json:"key_ids"`
}

func (q clusterReq) input() clustersvc.Input {
	enabled := true
	if q.Enabled != nil {
		enabled = *q.Enabled
	}
	return clustersvc.Input{Name: q.Name, Description: strings.TrimSpace(q.Description), Enabled: enabled, DatasourceID: q.DatasourceID,
		NamespaceInclude: q.NamespaceInclude, NamespaceExclude: q.NamespaceExclude}
}

func (r *ClusterRouter) clusterError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, clustersvc.ErrInvalid), errors.Is(err, clusteragent.ErrInvalid), errors.Is(err, clustersvc.ErrInUse):
		query.BadRequest(c, err)
	default:
		query.HandleDBError(c, err)
	}
}

// checkDatasource refuses a data source that does not exist; 0 is none.
func (r *ClusterRouter) checkDatasource(c *gin.Context, id uint) bool {
	if id == 0 || r.Datasources == nil {
		return true
	}
	if _, err := r.Datasources.GetByID(c.Request.Context(), id); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			query.BadRequest(c, errors.New("the data source does not exist"))
		} else {
			query.InternalError(c, err)
		}
		return false
	}
	return true
}

// Create serves POST /clusters.
// @Summary Register a cluster
// @Tags Cluster
// @Router /api/v1/clusters [post]
func (r *ClusterRouter) Create(c *gin.Context) {
	var req clusterReq
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}
	if !r.checkDatasource(c, req.DatasourceID) {
		return
	}
	row, err := r.Service.Create(c.Request.Context(), req.input())
	if err != nil {
		r.clusterError(c, err)
		return
	}
	if req.KeyIDs != nil {
		if err := r.Service.DAO.BindKeys(c.Request.Context(), row.Name, *req.KeyIDs); err != nil {
			query.InternalError(c, err)
			return
		}
	}
	d, err := r.dto(c, *row)
	if err != nil {
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, d)
}

func clusterID(c *gin.Context) (uint, bool) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		query.BadRequest(c, errors.New("invalid cluster id"))
		return 0, false
	}
	return uint(id), true
}

// Update serves PUT /clusters/:id: the name, the description, the switch, the data source, the namespaces and the keys bound. A new name carries everything that refers to the cluster.
// @Summary Change a cluster
// @Tags Cluster
// @Router /api/v1/clusters/{id} [put]
func (r *ClusterRouter) Update(c *gin.Context) {
	id, ok := clusterID(c)
	if !ok {
		return
	}
	var req clusterReq
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}
	if !r.checkDatasource(c, req.DatasourceID) {
		return
	}
	row, err := r.Service.Update(c.Request.Context(), id, req.input())
	if err != nil {
		r.clusterError(c, err)
		return
	}
	if req.KeyIDs != nil {
		if err := r.Service.DAO.BindKeys(c.Request.Context(), row.Name, *req.KeyIDs); err != nil {
			query.InternalError(c, err)
			return
		}
	}
	d, err := r.dto(c, *row)
	if err != nil {
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, d)
}

// Delete serves DELETE /clusters/:id.
// @Summary Delete a cluster
// @Tags Cluster
// @Router /api/v1/clusters/{id} [delete]
func (r *ClusterRouter) Delete(c *gin.Context) {
	id, ok := clusterID(c)
	if !ok {
		return
	}
	if err := r.Service.Delete(c.Request.Context(), id); err != nil {
		r.clusterError(c, err)
		return
	}
	query.SuccessResponse(c, nil, "deleted")
}

// Keys serves GET /clusters/keys: the access keys that can be bound, with the cluster each is bound to now.
// @Summary The access keys that can be bound to a cluster
// @Tags Cluster
// @Router /api/v1/clusters/keys [get]
func (r *ClusterRouter) Keys(c *gin.Context) {
	rows, err := r.Service.DAO.KeyChoices(c.Request.Context())
	if err != nil {
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, gin.H{"list": rows, "total": len(rows)})
}

// Events serves GET /clusters/:id/events: the events the operator of the cluster reported, latest first.
// @Summary The events of a cluster
// @Tags Cluster
// @Router /api/v1/clusters/{id}/events [get]
func (r *ClusterRouter) Events(c *gin.Context) {
	id, ok := clusterID(c)
	if !ok {
		return
	}
	cl, err := r.Service.DAO.Get(c.Request.Context(), id)
	if err != nil {
		query.HandleDBError(c, err)
		return
	}
	page, size := pageParams(c)
	f := rdb.EventFilter{Cluster: cl.Name, Type: c.Query("type"), Origin: c.Query("origin"), Category: c.Query("category"), Kind: c.Query("kind"),
		Namespace: c.Query("namespace"), Name: c.Query("name"), Reason: c.Query("reason"), Component: c.Query("component"), Search: c.Query("search")}
	if v, err := time.Parse(time.RFC3339, c.Query("since")); err == nil {
		f.Since = &v
	}
	if v, err := time.Parse(time.RFC3339, c.Query("until")); err == nil {
		f.Until = &v
	}
	rows, total, err := r.Service.DAO.ListEvents(c.Request.Context(), f, page, size)
	if err != nil {
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, gin.H{"list": rows, "total": total, "page": page, "pageSize": size, "retention_days": int(r.Service.Retention(c.Request.Context()).Hours() / 24)})
}

// OperatorSettings serves GET /operator/cluster-settings: what the operator of the cluster is told to collect.
// @Summary What an operator collects of its cluster
// @Tags Cluster
// @Router /api/v1/operator/cluster-settings [get]
func (r *ClusterRouter) OperatorSettings(c *gin.Context) {
	assistant.MarkOperatorSeen(time.Now())
	cl, ok := r.Gate.For(c)
	if !ok {
		return
	}
	if cl == nil { // a platform with no cluster registered: nothing to limit, and no events kept
		query.SuccessResponse(c, nil, clusteragent.Settings{CollectEvents: false, SkipReasons: []string{}})
		return
	}
	r.Gate.Seen(c, cl)
	query.SuccessResponse(c, nil, r.Service.Settings(c.Request.Context(), cl))
}

// maxEventsBody is the biggest batch of events the platform takes.
const maxEventsBody = 4 << 20

// OperatorEvents serves POST /operator/events: a batch of the events of the operator's cluster. A batch that is too big, or an event that has no uid, is refused whole.
// @Summary Take a batch of the events of a cluster
// @Tags Cluster
// @Router /api/v1/operator/events [post]
func (r *ClusterRouter) OperatorEvents(c *gin.Context) {
	cl, ok := r.Gate.For(c)
	if !ok {
		return
	}
	if cl == nil {
		query.ErrorResponse(c, http.StatusServiceUnavailable, errors.New("no cluster is registered on the platform"))
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxEventsBody)
	var batch clusteragent.Batch
	if err := c.ShouldBindJSON(&batch); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			query.ErrorResponse(c, http.StatusRequestEntityTooLarge, errors.New("the batch is bigger than the platform takes"))
			return
		}
		query.BadRequest(c, err)
		return
	}
	res, err := r.Service.IngestEvents(c.Request.Context(), cl, batch)
	if err != nil {
		if errors.Is(err, clusteragent.ErrInvalid) {
			query.BadRequest(c, err)
			return
		}
		query.InternalError(c, err)
		return
	}
	r.Gate.Seen(c, cl)
	query.SuccessResponse(c, nil, res)
}

// maxChangesBody is the biggest batch of changes the platform takes (a change carries a snapshot: the Pods of a node).
const maxChangesBody = 8 << 20

// OperatorChanges serves POST /operator/changes: a batch of the state changes of the objects of the operator's cluster.
// @Summary Take a batch of the changes of a cluster
// @Tags Cluster
// @Router /api/v1/operator/changes [post]
func (r *ClusterRouter) OperatorChanges(c *gin.Context) {
	cl, ok := r.Gate.For(c)
	if !ok {
		return
	}
	if cl == nil {
		query.ErrorResponse(c, http.StatusServiceUnavailable, errors.New("no cluster is registered on the platform"))
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxChangesBody)
	var batch clusteragent.ChangeBatch
	if err := c.ShouldBindJSON(&batch); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			query.ErrorResponse(c, http.StatusRequestEntityTooLarge, errors.New("the batch is bigger than the platform takes"))
			return
		}
		query.BadRequest(c, err)
		return
	}
	res, err := r.Service.IngestChanges(c.Request.Context(), cl, batch)
	if err != nil {
		if errors.Is(err, clusteragent.ErrInvalid) {
			query.BadRequest(c, err)
			return
		}
		query.InternalError(c, err)
		return
	}
	r.Gate.Seen(c, cl)
	query.SuccessResponse(c, nil, res)
}

// Changes serves GET /clusters/:id/changes: the changes of the objects of a cluster, latest first.
// @Summary The changes of a cluster
// @Tags Cluster
// @Router /api/v1/clusters/{id}/changes [get]
func (r *ClusterRouter) Changes(c *gin.Context) {
	id, ok := clusterID(c)
	if !ok {
		return
	}
	cl, err := r.Service.DAO.Get(c.Request.Context(), id)
	if err != nil {
		query.HandleDBError(c, err)
		return
	}
	page, size := pageParams(c)
	f := rdb.ChangeFilter{Cluster: cl.Name, Kind: c.Query("kind"), Type: c.Query("type"), Namespace: c.Query("namespace"), Name: c.Query("name"), Node: c.Query("node"),
		Workload: c.Query("workload"), Search: c.Query("search")}
	if v, err := time.Parse(time.RFC3339, c.Query("since")); err == nil {
		f.Since = &v
	}
	if v, err := time.Parse(time.RFC3339, c.Query("until")); err == nil {
		f.Until = &v
	}
	rows, total, err := r.Service.DAO.ListChanges(c.Request.Context(), f, page, size)
	if err != nil {
		query.InternalError(c, err)
		return
	}
	list := make([]clustersvc.ChangeDTO, 0, len(rows))
	for _, row := range rows {
		list = append(list, clustersvc.View(row))
	}
	query.SuccessResponse(c, nil, gin.H{"list": list, "total": total, "page": page, "pageSize": size, "retention_days": int(r.Service.Retention(c.Request.Context()).Hours() / 24)})
}

// Change serves GET /clusters/:id/changes/:change: one change with the events about its object around that time and the changes of other objects tied to it, each with
// the evidence it rests on.
// @Summary One change of a cluster, explained
// @Tags Cluster
// @Router /api/v1/clusters/{id}/changes/{change} [get]
func (r *ClusterRouter) Change(c *gin.Context) {
	id, ok := clusterID(c)
	if !ok {
		return
	}
	changeID, err := strconv.ParseUint(c.Param("change"), 10, 64)
	if err != nil || changeID == 0 {
		query.BadRequest(c, errors.New("invalid change id"))
		return
	}
	cl, err := r.Service.DAO.Get(c.Request.Context(), id)
	if err != nil {
		query.HandleDBError(c, err)
		return
	}
	out, err := r.Service.Explain(c.Request.Context(), cl, uint(changeID))
	if err != nil {
		query.HandleDBError(c, err)
		return
	}
	query.SuccessResponse(c, nil, out)
}
