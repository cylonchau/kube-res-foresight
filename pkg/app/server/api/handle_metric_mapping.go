package api

import (
	"errors"
	"fmt"
	"net/http"
	"sort"

	"github.com/cylonchau/kube-res-foresight/pkg/apis"
	"github.com/cylonchau/kube-res-foresight/pkg/app/query"
	"github.com/cylonchau/kube-res-foresight/pkg/dao/rdb"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/gin-gonic/gin"
)

// MetricMappingRouter serves metric mapping management and the static metric catalog.
type MetricMappingRouter struct {
	DAO *rdb.MetricMappingDAO
}

func metricMappingDTO(m *model.MetricMapping, datasourceName string) *apis.MetricMappingDTO {
	return &apis.MetricMappingDTO{
		ID: m.ID, CreatedAt: m.CreatedAt, UpdatedAt: m.UpdatedAt,
		DatasourceID: m.DatasourceID, DatasourceName: datasourceName,
		Name: m.Name, Metric: m.Metric, Level: m.Level, Query: m.Query,
		Variables: model.TemplateVariables(m.Query), Unit: m.Unit,
		StepSeconds: m.StepSeconds, Enabled: m.Enabled, Remark: m.Remark, PresetID: m.PresetID,
	}
}

func (r *MetricMappingRouter) dto(c *gin.Context, m *model.MetricMapping) (*apis.MetricMappingDTO, error) {
	names, err := r.DAO.DatasourceNames(c.Request.Context(), []uint{m.DatasourceID})
	if err != nil {
		return nil, err
	}
	return metricMappingDTO(m, names[m.DatasourceID]), nil
}

func (r *MetricMappingRouter) List(c *gin.Context) {
	var req apis.MetricMappingListReq
	if err := c.ShouldBindQuery(&req); err != nil {
		query.BadRequest(c, err)
		return
	}
	list, total, err := r.DAO.List(c.Request.Context(), &req, boolQuery(c, "enabled"))
	if err != nil {
		query.InternalError(c, err)
		return
	}
	ids := make([]uint, 0, len(list))
	for _, m := range list {
		ids = append(ids, m.DatasourceID)
	}
	names, err := r.DAO.DatasourceNames(c.Request.Context(), ids)
	if err != nil {
		query.InternalError(c, err)
		return
	}
	dtos := make([]*apis.MetricMappingDTO, 0, len(list))
	for _, m := range list {
		dtos = append(dtos, metricMappingDTO(m, names[m.DatasourceID]))
	}
	query.SuccessResponse(c, nil, gin.H{"list": dtos, "total": total, "page": req.Page, "pageSize": req.PageSize})
}

func (r *MetricMappingRouter) Get(c *gin.Context) {
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	m, err := r.DAO.GetByID(c.Request.Context(), id)
	if err != nil {
		query.HandleDBError(c, err)
		return
	}
	dto, err := r.dto(c, m)
	if err != nil {
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, dto)
}

func (r *MetricMappingRouter) Create(c *gin.Context) {
	var req apis.MetricMappingCreateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}
	m := &model.MetricMapping{
		DatasourceID: req.DatasourceID, Name: req.Name, Metric: req.Metric, Level: req.Level,
		Query: req.Query, Unit: req.Unit, StepSeconds: req.StepSeconds, Enabled: true, Remark: req.Remark,
	}
	if req.Enabled != nil {
		m.Enabled = *req.Enabled
	}
	if err := model.ValidateMetricMapping(m); err != nil {
		query.BadRequest(c, err)
		return
	}
	if err := r.DAO.Create(c.Request.Context(), m); err != nil {
		r.writeError(c, err)
		return
	}
	dto, err := r.dto(c, m)
	if err != nil {
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, dto)
}

func (r *MetricMappingRouter) Update(c *gin.Context) {
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	var req apis.MetricMappingUpdateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}
	m, err := r.DAO.GetByID(c.Request.Context(), id)
	if err != nil {
		query.HandleDBError(c, err)
		return
	}
	if req.DatasourceID != nil {
		m.DatasourceID = *req.DatasourceID
	}
	if req.Name != nil {
		m.Name = *req.Name
	}
	if req.Metric != nil {
		m.Metric = *req.Metric
		if req.Unit == nil {
			m.Unit = "" // follow the new metric's canonical unit
		}
	}
	if req.Level != nil {
		m.Level = *req.Level
	}
	if req.Query != nil {
		m.Query = *req.Query
	}
	if req.Unit != nil {
		m.Unit = *req.Unit
	}
	if req.StepSeconds != nil {
		m.StepSeconds = *req.StepSeconds
	}
	if req.Enabled != nil {
		m.Enabled = *req.Enabled
	}
	if req.Remark != nil {
		m.Remark = *req.Remark
	}
	if err := model.ValidateMetricMapping(m); err != nil {
		query.BadRequest(c, err)
		return
	}
	if err := r.DAO.Update(c.Request.Context(), m); err != nil {
		r.writeError(c, err)
		return
	}
	dto, err := r.dto(c, m)
	if err != nil {
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, dto)
}

func (r *MetricMappingRouter) Delete(c *gin.Context) {
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	if !r.refuseWhenUsed(c, []uint{id}) {
		return
	}
	if err := r.DAO.Delete(c.Request.Context(), []uint{id}); err != nil {
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, "ok")
}

// refuseWhenUsed answers 409 and returns false when a module still uses one of the templates: deleting it would leave the
// metric of that module pointing at nothing. The modules have to drop the template first.
func (r *MetricMappingRouter) refuseWhenUsed(c *gin.Context, ids []uint) bool {
	used, err := r.DAO.UsedBy(c.Request.Context(), ids)
	if err != nil {
		query.InternalError(c, err)
		return false
	}
	for id, total := range used {
		query.ErrorResponse(c, http.StatusConflict, fmt.Errorf("the metric template %d is used by %d module metric(s): remove it from those modules first", id, total))
		return false
	}
	return true
}

func (r *MetricMappingRouter) BatchDelete(c *gin.Context) {
	var req apis.QueryByIDs
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}
	ids := make([]uint, 0, len(req.IDs))
	for _, id := range req.IDs {
		ids = append(ids, uint(id))
	}
	if !r.refuseWhenUsed(c, ids) {
		return
	}
	if err := r.DAO.Delete(c.Request.Context(), ids); err != nil {
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, "ok")
}

// Catalog lists the metrics, levels and template variables a mapping may use.
func (r *MetricMappingRouter) Catalog(c *gin.Context) {
	catalog := apis.MetricCatalogDTO{
		Variables: model.MetricVariables, MinStep: model.MinStepSeconds, MaxStep: model.MaxStepSeconds,
	}
	for metric, unit := range model.MetricKinds {
		catalog.Metrics = append(catalog.Metrics, apis.MetricCatalogMetric{Metric: metric, Unit: unit})
	}
	for level, required := range model.MetricLevels {
		catalog.Levels = append(catalog.Levels, apis.MetricCatalogLevel{Level: level, Required: append([]string{}, required...)})
	}
	sort.Slice(catalog.Metrics, func(i, j int) bool { return catalog.Metrics[i].Metric < catalog.Metrics[j].Metric })
	sort.Slice(catalog.Levels, func(i, j int) bool { return len(catalog.Levels[i].Required) < len(catalog.Levels[j].Required) })
	query.SuccessResponse(c, nil, catalog)
}

func (r *MetricMappingRouter) writeError(c *gin.Context, err error) {
	if errors.Is(err, rdb.ErrNameTaken) {
		query.BadRequest(c, err)
		return
	}
	query.HandleDBError(c, err)
}
