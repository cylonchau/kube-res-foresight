package api

import (
	"errors"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/cylonchau/kube-res-foresight/pkg/app/query"
	"github.com/cylonchau/kube-res-foresight/pkg/dao/rdb"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/cylonchau/kube-res-foresight/pkg/preset"
)

// PresetRouter serves the built-in metric presets.
type PresetRouter struct {
	Datasources *rdb.DatasourceDAO
	Mappings    *rdb.MetricMappingDAO
}

// List returns the presets, optionally narrowed by metric, level or a keyword in the id and names.
// @Summary List metric presets
// @Tags Datasource
// @Produce json
// @Router /api/v1/metric-presets [get]
func (r *PresetRouter) List(c *gin.Context) {
	metric, level := c.Query("metric"), c.Query("level")
	keyword := strings.ToLower(strings.TrimSpace(c.Query("keyword")))
	list := []preset.Preset{}
	for _, p := range preset.All() {
		if metric != "" && p.Metric != metric || level != "" && p.Level != level {
			continue
		}
		if keyword != "" && !strings.Contains(strings.ToLower(p.ID+" "+p.Name.ZH+" "+p.Name.EN+" "+p.Source.Component), keyword) {
			continue
		}
		list = append(list, p)
	}
	query.SuccessResponse(c, nil, gin.H{"list": list, "total": len(list)})
}

type applyPresetReq struct {
	DatasourceID uint   `json:"datasource_id" binding:"required"`
	Name         string `json:"name"`
	StepSeconds  int    `json:"step_seconds"`
	// Query replaces the PromQL of the template: the template generates it, this lets the author edit it on the way in.
	Query string `json:"query"`
}

// Apply creates an editable metric mapping from a preset on a datasource.
// @Summary Create a metric mapping from a preset
// @Tags Datasource
// @Router /api/v1/metric-presets/{id}/apply [post]
func (r *PresetRouter) Apply(c *gin.Context) {
	p, err := preset.Get(c.Param("id"))
	if err != nil {
		query.NotFound(c, err)
		return
	}
	var req applyPresetReq
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}
	ctx := c.Request.Context()
	ds, err := r.Datasources.GetByID(ctx, req.DatasourceID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			query.BadRequest(c, errors.New("datasource does not exist"))
			return
		}
		query.InternalError(c, err)
		return
	}
	m := p.Mapping(ds.ID, strings.TrimSpace(req.Name), req.StepSeconds)
	if q := strings.TrimSpace(req.Query); q != "" {
		m.Query = q
	}
	if err := model.ValidateMetricMapping(m); err != nil {
		query.BadRequest(c, err)
		return
	}
	if err := r.Mappings.Create(ctx, m); err != nil {
		if errors.Is(err, rdb.ErrNameTaken) {
			query.BadRequest(c, err)
			return
		}
		query.HandleDBError(c, err)
		return
	}
	query.SuccessResponse(c, nil, metricMappingDTO(m, ds.Name))
}
