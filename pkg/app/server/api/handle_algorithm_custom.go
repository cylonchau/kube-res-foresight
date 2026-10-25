package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/cylonchau/kube-resource-fcst/pkg/apis"
	"github.com/cylonchau/kube-resource-fcst/pkg/app/query"
	"github.com/cylonchau/kube-resource-fcst/pkg/forecast"
	"github.com/cylonchau/kube-resource-fcst/pkg/forecastsvc"
	"github.com/cylonchau/kube-resource-fcst/pkg/model"
)

func paramSpecs(in []apis.ParamSpecReq) []forecast.ParamSpec {
	out := make([]forecast.ParamSpec, len(in))
	for i, p := range in {
		out[i] = forecast.ParamSpec{Name: p.Name, Type: p.Type, Required: p.Required, Default: p.Default, Min: p.Min, Max: p.Max, Unit: p.Unit, Description: p.Description}
	}
	return out
}

// customSpec is the algorithm as the request describes it.
func customSpec(id, category, description, expr string, schema []apis.ParamSpecReq, minSamples int, recursive bool) forecast.CustomSpec {
	if minSamples == 0 {
		minSamples = 12
	}
	return forecast.CustomSpec{ID: id, Category: category, Description: strings.TrimSpace(description), Formula: forecast.FormulaSpec{
		Expr: expr, Params: paramSpecs(schema), MinSamples: minSamples, Recursive: recursive}}
}

// store puts the custom algorithm on a config row.
func store(cfg *model.AlgorithmConfig, spec forecast.CustomSpec) error {
	schema, err := json.Marshal(spec.Formula.Params)
	if err != nil {
		return err
	}
	cfg.Category, cfg.Formula, cfg.ParamSchema = spec.Category, spec.Formula.Expr, string(schema)
	cfg.MinSamples, cfg.Recursive = spec.Formula.MinSamples, spec.Formula.Recursive
	if spec.Description != "" {
		cfg.Description = spec.Description
	}
	return nil
}

// CreateAlgorithm adds a custom algorithm. The formula is compiled and run on a synthetic series first, so a formula
// that cannot run is refused here and not on the first night it is used.
// @Summary Create a custom algorithm
// @Tags Forecast
// @Router /api/v1/algorithms [post]
func (r *ForecastRouter) CreateAlgorithm(c *gin.Context) {
	var req apis.AlgorithmCreateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}
	ctx := c.Request.Context()
	if err := r.Service.EnsureAlgorithms(ctx); err != nil {
		query.InternalError(c, err)
		return
	}
	if req.Base != "" {
		r.createVariant(c, &req)
		return
	}
	if strings.TrimSpace(req.Formula) == "" {
		query.BadRequest(c, errors.New("give a formula, or a built-in algorithm to derive the new one from (base)"))
		return
	}
	spec := customSpec(req.ID, req.Category, req.Description, req.Formula, req.ParamSchema, req.MinSamples, req.Recursive)
	if err := forecast.ValidCustomID(spec.ID); err != nil {
		query.BadRequest(c, err)
		return
	}
	if _, taken := forecast.Lookup(spec.ID); taken {
		query.ErrorResponse(c, http.StatusConflict, fmt.Errorf("the algorithm %q already exists", spec.ID))
		return
	}
	cfg := &model.AlgorithmConfig{AlgorithmID: spec.ID, Weight: 1, Enabled: req.Enabled}
	if req.Weight != nil {
		cfg.Weight = *req.Weight
	}
	if err := store(cfg, spec); err != nil {
		query.InternalError(c, err)
		return
	}
	// register first (this compiles and dry-runs the formula): the parameters are validated against the schema of the
	// algorithm itself
	if err := forecast.SetCustom(spec); err != nil {
		query.BadRequest(c, err)
		return
	}
	params, err := paramsJSON(spec.ID, req.Params)
	if err != nil {
		forecast.RemoveCustom(spec.ID)
		query.BadRequest(c, err)
		return
	}
	cfg.Params = params
	if err := cfg.Validate(); err != nil {
		forecast.RemoveCustom(spec.ID)
		query.BadRequest(c, err)
		return
	}
	if err := r.Configs.Create(ctx, cfg); err != nil {
		forecast.RemoveCustom(spec.ID)
		query.HandleDBError(c, err)
		return
	}
	def, _ := forecast.Lookup(spec.ID)
	query.SuccessResponse(c, nil, newAlgorithmDTO(def, cfg))
}

// createVariant adds an algorithm derived from a built-in one: its parameters are the base's, validated against the
// base's schema, and it runs next to the base for every module once enabled.
func (r *ForecastRouter) createVariant(c *gin.Context, req *apis.AlgorithmCreateReq) {
	ctx := c.Request.Context()
	spec := forecast.VariantSpec{ID: req.ID, Base: req.Base, Description: req.Description}
	if _, taken := forecast.Lookup(spec.ID); taken {
		query.ErrorResponse(c, http.StatusConflict, fmt.Errorf("the algorithm %q already exists", spec.ID))
		return
	}
	if err := forecast.SetVariant(spec); err != nil {
		query.BadRequest(c, err)
		return
	}
	fail := func(err error) {
		forecast.RemoveCustom(spec.ID)
		query.BadRequest(c, err)
	}
	params, err := paramsJSON(spec.ID, req.Params)
	if err != nil {
		fail(err)
		return
	}
	cfg := &model.AlgorithmConfig{AlgorithmID: spec.ID, Base: spec.Base, Description: strings.TrimSpace(req.Description), Params: params, Weight: 1, Enabled: req.Enabled}
	if req.Weight != nil {
		cfg.Weight = *req.Weight
	}
	if err := cfg.Validate(); err != nil {
		fail(err)
		return
	}
	if err := r.Configs.Create(ctx, cfg); err != nil {
		forecast.RemoveCustom(spec.ID)
		query.HandleDBError(c, err)
		return
	}
	def, _ := forecast.Lookup(spec.ID)
	query.SuccessResponse(c, nil, newAlgorithmDTO(def, cfg))
}

// DeleteAlgorithm removes a custom algorithm. A built-in algorithm is only disabled; an enabled one has to be disabled
// first so that a module is never left running an algorithm that vanishes under it. The results it already produced
// stay in the history under its id.
// @Summary Delete a custom algorithm
// @Tags Forecast
// @Router /api/v1/algorithms/{id} [delete]
func (r *ForecastRouter) DeleteAlgorithm(c *gin.Context) {
	_, cfg, ok := r.algorithm(c)
	if !ok {
		return
	}
	if !cfg.UserDefined() {
		query.ErrorResponse(c, http.StatusConflict, errors.New("a built-in algorithm cannot be deleted; disable it instead"))
		return
	}
	if cfg.Enabled {
		query.ErrorResponse(c, http.StatusConflict, errors.New("disable the algorithm before deleting it"))
		return
	}
	if err := r.Configs.Delete(c.Request.Context(), cfg.AlgorithmID); err != nil {
		query.InternalError(c, err)
		return
	}
	forecast.RemoveCustom(cfg.AlgorithmID)
	query.SuccessResponse(c, nil, gin.H{"id": cfg.AlgorithmID})
}

// editCustom applies the formula fields of an update to a custom algorithm and returns its new definition, or nil when
// the update does not touch them.
func editCustom(cfg *model.AlgorithmConfig, req *apis.AlgorithmUpdateReq) (*forecast.CustomSpec, error) {
	touched := req.Formula != nil || req.ParamSchema != nil || req.MinSamples != nil || req.Recursive != nil || req.Category != nil
	if !touched {
		return nil, nil
	}
	if !cfg.Custom() {
		return nil, errors.New("only a formula algorithm has a formula; a built-in algorithm is changed through its parameters")
	}
	spec, err := forecastsvc.SpecOf(cfg)
	if err != nil {
		return nil, err
	}
	if req.Formula != nil {
		spec.Formula.Expr = *req.Formula
	}
	if req.ParamSchema != nil {
		spec.Formula.Params = paramSpecs(*req.ParamSchema)
	}
	if req.MinSamples != nil {
		spec.Formula.MinSamples = *req.MinSamples
	}
	if req.Recursive != nil {
		spec.Formula.Recursive = *req.Recursive
	}
	if req.Category != nil {
		spec.Category = *req.Category
	}
	if req.Description != nil {
		spec.Description = *req.Description
	}
	return &spec, nil
}

// FormulaCheck compiles a formula and forecasts a synthetic series with it, for the editor of the page: the author
// sees the error with its position, or the curve the formula draws, before saving.
// @Summary Check a formula
// @Tags Forecast
// @Router /api/v1/algorithm-formulas/check [post]
func (r *ForecastRouter) FormulaCheck(c *gin.Context) {
	var req apis.FormulaCheckReq
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}
	if req.Horizon <= 0 {
		req.Horizon = 48
	}
	if req.Horizon > 500 {
		query.BadRequest(c, errors.New("horizon must be at most 500"))
		return
	}
	spec := customSpec("preview", "", "", req.Formula, req.ParamSchema, req.MinSamples, req.Recursive)
	history, out, err := forecast.TryFormula(spec.Formula, req.Params, req.Horizon)
	if err != nil {
		query.SuccessResponse(c, nil, gin.H{"ok": false, "error": err.Error()})
		return
	}
	query.SuccessResponse(c, nil, gin.H{"ok": true, "history": history, "forecast": out, "step_seconds": 900})
}

// FormulaFunctions lists what a formula may call, for the help of the editor.
func (r *ForecastRouter) FormulaFunctions(c *gin.Context) {
	query.SuccessResponse(c, nil, gin.H{"functions": forecast.FormulaFunctions(), "variables": []string{"x", "k", "h", "n", "c_<source key>"}})
}
