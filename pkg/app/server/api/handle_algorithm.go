package api

import (
	"errors"
	"fmt"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/cylonchau/kube-resource-fcst/pkg/apis"
	"github.com/cylonchau/kube-resource-fcst/pkg/app/query"
	"github.com/cylonchau/kube-resource-fcst/pkg/forecast"
	"github.com/cylonchau/kube-resource-fcst/pkg/forecastsvc"
	"github.com/cylonchau/kube-resource-fcst/pkg/model"
)

// algorithmDTO is a catalog algorithm with its platform-wide setting: what the algorithm is (the definition and its
// parameter schema) and how it is run for every module (enabled, parameter values, weight, budget, promotion rule).
type algorithmDTO struct {
	forecast.Definition
	// Params shadows the definition's so an algorithm without parameters lists [] rather than nothing
	Params []forecast.ParamSpec `json:"params"`
	// Origin is where the algorithm comes from: builtin, variant (derived from Base) or formula (written on the pages).
	Origin                string         `json:"origin"`
	Enabled               bool           `json:"enabled"`
	Note                  string         `json:"note"`
	Values                map[string]any `json:"values"`
	Weight                float64        `json:"weight"`
	TrainBudgetSeconds    int            `json:"train_budget_seconds"`
	PromoteMinPoints      int            `json:"promote_min_points"`
	PromoteMinImprovement float64        `json:"promote_min_improvement"`
	PromotePeakTolerance  float64        `json:"promote_peak_tolerance"`
	UpdatedAt             string         `json:"updated_at"`
}

func newAlgorithmDTO(def forecast.Definition, c *model.AlgorithmConfig) algorithmDTO {
	values, _, _ := forecastsvc.NormalizedParams(def.ID, c.Params)
	params := def.Params
	if params == nil {
		params = []forecast.ParamSpec{}
	}
	origin := "builtin"
	switch {
	case def.Custom:
		origin = "formula"
	case def.Base != "":
		origin = "variant"
	}
	return algorithmDTO{Definition: def, Params: params, Origin: origin, Enabled: c.Enabled, Note: c.Description, Values: values, Weight: c.Weight,
		TrainBudgetSeconds: c.TrainBudgetSeconds, PromoteMinPoints: c.PromoteMinPoints, PromoteMinImprovement: c.PromoteMinImprovement,
		PromotePeakTolerance: c.PromotePeakTolerance, UpdatedAt: c.UpdatedAt.Format("2006-01-02T15:04:05Z07:00")}
}

// Algorithms lists the algorithms of the catalog with their platform-wide settings, filtered and paged.
// @Summary Algorithms
// @Tags Forecast
// @Router /api/v1/algorithms [get]
func (r *ForecastRouter) Algorithms(c *gin.Context) {
	var req apis.AlgorithmListReq
	if err := c.ShouldBindQuery(&req); err != nil {
		query.BadRequest(c, err)
		return
	}
	ctx := c.Request.Context()
	if err := r.Service.EnsureAlgorithms(ctx); err != nil {
		query.InternalError(c, err)
		return
	}
	configs, err := r.Configs.All(ctx)
	if err != nil {
		query.InternalError(c, err)
		return
	}
	by := make(map[string]*model.AlgorithmConfig, len(configs))
	for _, cfg := range configs {
		by[cfg.AlgorithmID] = cfg
	}
	keyword := strings.ToLower(strings.TrimSpace(req.Keyword))
	var out []algorithmDTO
	for _, def := range forecast.Definitions() {
		cfg := by[def.ID]
		if cfg == nil {
			continue
		}
		switch {
		case req.Category != "" && def.Category != req.Category:
			continue
		case req.Enabled == "true" && !cfg.Enabled, req.Enabled == "false" && cfg.Enabled:
			continue
		case keyword != "" && !strings.Contains(strings.ToLower(def.ID+" "+def.Description), keyword):
			continue
		}
		out = append(out, newAlgorithmDTO(def, cfg))
	}
	page, size := int(req.Page), int(req.PageSize)
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 200 {
		size = 10
	}
	total := len(out)
	start := min((page-1)*size, total)
	end := min(start+size, total)
	list := out[start:end]
	if list == nil {
		list = []algorithmDTO{}
	}
	query.SuccessResponse(c, nil, gin.H{"list": list, "total": total, "page": page, "pageSize": size})
}

// GetAlgorithm serves one algorithm with its setting.
func (r *ForecastRouter) GetAlgorithm(c *gin.Context) {
	def, cfg, ok := r.algorithm(c)
	if ok {
		query.SuccessResponse(c, nil, newAlgorithmDTO(def, cfg))
	}
}

func (r *ForecastRouter) algorithm(c *gin.Context) (forecast.Definition, *model.AlgorithmConfig, bool) {
	if err := r.Service.EnsureAlgorithms(c.Request.Context()); err != nil {
		query.InternalError(c, err)
		return forecast.Definition{}, nil, false
	}
	def, ok := forecast.Lookup(c.Param("id"))
	if !ok {
		query.NotFound(c, query.ErrRecordNotFound)
		return def, nil, false
	}
	cfg, err := r.Configs.Get(c.Request.Context(), def.ID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			query.NotFound(c, query.ErrRecordNotFound)
		} else {
			query.InternalError(c, err)
		}
		return def, nil, false
	}
	return def, cfg, true
}

// UpdateAlgorithm changes the platform-wide setting of an algorithm. It applies to every module on the next cycle: an
// enabled algorithm is run for all of them, so nothing is created or bound by hand.
// @Summary Update an algorithm's setting
// @Tags Forecast
// @Router /api/v1/algorithms/{id} [put]
func (r *ForecastRouter) UpdateAlgorithm(c *gin.Context) {
	def, cfg, ok := r.algorithm(c)
	if !ok {
		return
	}
	var req apis.AlgorithmUpdateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}
	if req.Description != nil {
		cfg.Description = *req.Description
	}
	if cfg.Variant() && req.Description != nil {
		if err := forecast.SetVariant(forecast.VariantSpec{ID: cfg.AlgorithmID, Base: cfg.Base, Description: cfg.Description}); err != nil {
			query.BadRequest(c, err)
			return
		}
		def, _ = forecast.Lookup(def.ID)
	}
	spec, err := editCustom(cfg, &req)
	if err != nil {
		query.BadRequest(c, err)
		return
	}
	if spec != nil {
		if err := store(cfg, *spec); err != nil {
			query.InternalError(c, err)
			return
		}
		// registering compiles and dry-runs the formula; the parameters are validated against the new schema
		if err := forecast.SetCustom(*spec); err != nil {
			query.BadRequest(c, err)
			return
		}
		def, _ = forecast.Lookup(def.ID)
		if req.Params == nil {
			if _, err := forecastsvc.ValidateParams(def.ID, cfg.Params); err != nil {
				query.BadRequest(c, fmt.Errorf("the stored values do not fit the new parameters (send params too): %w", err))
				return
			}
		}
	}
	if req.Enabled != nil {
		cfg.Enabled = *req.Enabled
	}
	if req.Params != nil {
		canonical, err := paramsJSON(def.ID, req.Params)
		if err != nil {
			query.BadRequest(c, err)
			return
		}
		cfg.Params = canonical
	}
	if req.Weight != nil {
		cfg.Weight = *req.Weight
	}
	if req.TrainBudgetSeconds != nil {
		cfg.TrainBudgetSeconds = *req.TrainBudgetSeconds
	}
	if req.PromoteMinPoints != nil {
		cfg.PromoteMinPoints = *req.PromoteMinPoints
	}
	if req.PromoteMinImprovement != nil {
		cfg.PromoteMinImprovement = *req.PromoteMinImprovement
	}
	if req.PromotePeakTolerance != nil {
		cfg.PromotePeakTolerance = *req.PromotePeakTolerance
	}
	if err := cfg.Validate(); err != nil {
		query.BadRequest(c, err)
		return
	}
	if err := r.Configs.Save(c.Request.Context(), cfg); err != nil {
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, newAlgorithmDTO(def, cfg))
}
