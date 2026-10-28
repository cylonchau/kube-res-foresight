package api

import (
	"github.com/gin-gonic/gin"

	"github.com/cylonchau/kube-resource-fcst/pkg/app/query"
	"github.com/cylonchau/kube-resource-fcst/pkg/model"
)

// sourceDTO is a metric source of a module's dataset with what the page needs to show without more calls: its
// category, the query behind it and the grid it is sampled on.
type sourceDTO struct {
	*model.MetricSource
	Category       string `json:"category"`
	MappingName    string `json:"mapping_name"`
	Query          string `json:"query"`
	Unit           string `json:"unit"`
	Level          string `json:"level"`
	DatasourceID   uint   `json:"datasource_id"`
	DatasourceName string `json:"datasource_name"`
	// QueryStepSeconds is the step of the query; StepSeconds the step of the dataset (the module's, else the query's).
	QueryStepSeconds int `json:"query_step_seconds"`
	StepSeconds      int `json:"step_seconds"`
}

// sourceDTOs describes the sources of a module. A source whose mapping is gone is listed without those fields.
func (r *ForecastRouter) sourceDTOs(c *gin.Context, m *model.ForecastModule) []sourceDTO {
	ctx := c.Request.Context()
	out := make([]sourceDTO, 0, len(m.Sources))
	mappings := map[uint]*model.MetricMapping{}
	var dsIDs []uint
	for _, s := range m.Sources {
		if _, seen := mappings[s.MappingID]; seen {
			continue
		}
		if mp, err := r.Mappings.GetByID(ctx, s.MappingID); err == nil {
			mappings[s.MappingID] = mp
			dsIDs = append(dsIDs, mp.DatasourceID)
		} else {
			mappings[s.MappingID] = nil
		}
	}
	names, _ := r.Mappings.DatasourceNames(ctx, dsIDs)
	for _, s := range m.Sources {
		d := sourceDTO{MetricSource: s, Category: s.Category()}
		if mp := mappings[s.MappingID]; mp != nil {
			d.MappingName, d.Query, d.Unit, d.DatasourceID, d.DatasourceName = mp.Name, mp.Query, mp.Unit, mp.DatasourceID, names[mp.DatasourceID]
			d.Level = mp.Level
			d.QueryStepSeconds, d.StepSeconds = mp.StepSeconds, mp.StepSeconds
			if m.StepSeconds > 0 {
				d.StepSeconds = m.StepSeconds
			}
		}
		out = append(out, d)
	}
	return out
}

// DatasetCatalog describes what a dataset can be made of: the categories with their kinds and units, the ways to
// aggregate and to fill gaps, and the preset periods.
// @Summary Dataset catalog
// @Tags Forecast
// @Router /api/v1/dataset/catalog [get]
func (r *ForecastRouter) DatasetCatalog(c *gin.Context) {
	type kind struct {
		Kind           string `json:"kind"`
		Unit           string `json:"unit"`
		Resource       string `json:"resource"`
		Basis          string `json:"basis"`
		Missing        string `json:"missing"`
		Auxiliary      bool   `json:"auxiliary"`
		UnitIsNotFixed bool   `json:"unit_is_not_fixed"`
	}
	type category struct {
		ID    string `json:"id"`
		Kinds []kind `json:"kinds"`
	}
	var cats []category
	for _, id := range model.MetricCategories {
		cat := category{ID: id, Kinds: []kind{}}
		for _, k := range datasetKinds() {
			if model.KindCategory(k) != id {
				continue
			}
			cat.Kinds = append(cat.Kinds, kind{Kind: k, Unit: model.MetricKinds[k], Resource: model.DefaultResource(k), Basis: model.DefaultBasis(k),
				Missing: model.DefaultMissing(k), Auxiliary: model.IsAuxiliaryMetric(k), UnitIsNotFixed: k == model.CustomMetric})
		}
		cats = append(cats, cat)
	}
	query.SuccessResponse(c, nil, gin.H{
		"categories": cats, "aggregations": model.SourceAggregations, "missing": model.SourceMissing,
		"steps_seconds": []int{60, 300, 900, 3600, 86400}, "max_step_seconds": model.MaxDatasetStepSeconds,
		"periods": []string{"5m", "15m", "1h", "1d", "30d"},
	})
}

// datasetKinds lists every metric kind in a stable order: the forecast kinds first, then the rest.
func datasetKinds() []string {
	first := []string{"cpu", "memory", "gpu", "rps"}
	seen := map[string]bool{}
	var out []string
	for _, k := range first {
		seen[k] = true
		out = append(out, k)
	}
	for _, k := range append([]string{"gpu_memory", "replicas"}, model.AuxiliaryMetrics...) {
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	return out
}

// ModuleRequirements says, per enabled algorithm and source, how much history the algorithm needs on the dataset grid
// and whether the history range of the module holds it.
// @Summary Dataset requirements of the algorithms
// @Tags Forecast
// @Router /api/v1/modules/{id}/requirements [get]
func (r *ForecastRouter) ModuleRequirements(c *gin.Context) {
	m, ok := r.module(c)
	if !ok {
		return
	}
	list, err := r.Service.Requirements(c.Request.Context(), m.ID)
	if err != nil {
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, list)
}
