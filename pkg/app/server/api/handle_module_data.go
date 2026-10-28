package api

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/cylonchau/kube-resource-fcst/pkg/apis"
	"github.com/cylonchau/kube-resource-fcst/pkg/app/query"
	"github.com/cylonchau/kube-resource-fcst/pkg/dao/rdb"
	"github.com/cylonchau/kube-resource-fcst/pkg/forecastsvc"
	"github.com/cylonchau/kube-resource-fcst/pkg/model"
)

// checkSource verifies that a source's mapping exists and measures the kind of the source.
func (r *ForecastRouter) checkSource(ctx context.Context, s *model.MetricSource) error {
	mapping, err := r.Mappings.GetByID(ctx, s.MappingID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("metric mapping %d does not exist", s.MappingID)
		}
		return err
	}
	if mapping.Metric != s.Kind {
		return fmt.Errorf("mapping %q measures %s and cannot be used for a %s source", mapping.Name, mapping.Metric, s.Kind)
	}
	return nil
}

// checkDataset refuses a source whose query step cannot be turned into the dataset step of the module: the dataset is
// aggregated from the samples of the query, so it can only be coarser, by a whole multiple.
func (r *ForecastRouter) checkDataset(ctx context.Context, m *model.ForecastModule, s *model.MetricSource) error {
	if m.StepSeconds <= 0 || !s.Enabled || s.MappingID == 0 {
		return nil
	}
	mapping, err := r.Mappings.GetByID(ctx, s.MappingID)
	if err != nil {
		return nil // a missing mapping is reported by checkSource
	}
	query, step := time.Duration(mapping.StepSeconds)*time.Second, time.Duration(m.StepSeconds)*time.Second
	switch {
	case step < query:
		return fmt.Errorf("the module samples every %ds, finer than the %ds step of the query %q: use a coarser module step or a finer query step", m.StepSeconds, mapping.StepSeconds, mapping.Name)
	case step%query != 0:
		return fmt.Errorf("the module step (%ds) is not a whole multiple of the %ds step of the query %q", m.StepSeconds, mapping.StepSeconds, mapping.Name)
	}
	return nil
}

// dropMappings removes the mappings written for custom queries of a request that then failed.
func (r *ForecastRouter) dropMappings(ctx context.Context, ids []uint) {
	if len(ids) > 0 {
		_ = r.Mappings.Delete(ctx, ids)
	}
}

// customMapping writes the metric mapping behind a PromQL the user typed for a source.
func (r *ForecastRouter) customMapping(ctx context.Context, req *apis.SourceReq, renameOnTaken bool) (uint, error) {
	c := req.Custom
	mapping := &model.MetricMapping{DatasourceID: c.DatasourceID, Name: req.Name, Metric: req.Kind, Level: c.Level, Query: c.Query, Unit: c.Unit,
		StepSeconds: c.StepSeconds, Enabled: true, Remark: "written for the source " + req.Name}
	if mapping.Level == "" {
		mapping.Level = "workload"
	}
	if mapping.StepSeconds == 0 {
		mapping.StepSeconds = 60
	}
	if mapping.Metric != model.CustomMetric {
		mapping.Unit = model.MetricKinds[mapping.Metric] // the unit of a known kind is fixed by the kind
	}
	if err := model.ValidateMetricMapping(mapping); err != nil {
		return 0, err
	}
	if err := r.Mappings.Create(ctx, mapping); err != nil {
		if errors.Is(err, rdb.ErrNameTaken) {
			if !renameOnTaken {
				return 0, fmt.Errorf("a metric mapping named %q already exists on that datasource: rename the source or pick the mapping", req.Name)
			}
			// an edited query is a new mapping next to the one it replaces, so the name gets a number
			for n := 2; n < 50; n++ {
				mapping.ID, mapping.Name = 0, fmt.Sprintf("%s (%d)", req.Name, n)
				if err = r.Mappings.Create(ctx, mapping); err == nil {
					return mapping.ID, nil
				}
				if !errors.Is(err, rdb.ErrNameTaken) {
					return 0, err
				}
			}
		}
		return 0, err
	}
	return mapping.ID, nil
}

// sourceFromReq builds and checks a source of a module. When the request carries a PromQL of its own the mapping is
// written first; its id is returned (0 otherwise) so the caller can remove it again when the request then fails.
func (r *ForecastRouter) sourceFromReq(ctx context.Context, moduleID uint, req *apis.SourceReq) (*model.MetricSource, uint, error) {
	var made uint
	if req.Custom != nil {
		if req.MappingID != 0 {
			return nil, 0, fmt.Errorf("give either mapping_id or custom, not both")
		}
		id, err := r.customMapping(ctx, req, false)
		if err != nil {
			return nil, 0, err
		}
		req.MappingID, made = id, id
	}
	if req.MappingID == 0 {
		return nil, 0, fmt.Errorf("a metric mapping is required: pick one or write a PromQL")
	}
	s := &model.MetricSource{ModuleID: moduleID, Name: req.Name, Kind: req.Kind, Resource: req.Resource, MappingID: req.MappingID, Priority: req.Priority,
		Weight: 1, Stat: req.Stat, Basis: req.Basis, WindowHours: req.WindowHours, Threshold: req.Threshold, Enabled: req.Enabled == nil || *req.Enabled,
		Remark: req.Remark, Scale: req.Scale, Aggregation: req.Aggregation, Missing: req.Missing}
	s.Precision = 6
	if req.Precision != nil {
		s.Precision = *req.Precision
	}
	if req.Weight != nil {
		s.Weight = *req.Weight
	}
	fail := func(err error) (*model.MetricSource, uint, error) {
		if made != 0 {
			r.dropMappings(ctx, []uint{made})
		}
		return nil, 0, err
	}
	if err := s.Validate(); err != nil {
		return fail(err)
	}
	if err := r.checkSource(ctx, s); err != nil {
		return fail(err)
	}
	return s, made, nil
}

// ListSources serves the metric sources of a module.
func (r *ForecastRouter) ListSources(c *gin.Context) {
	a, ok := r.module(c)
	if !ok {
		return
	}
	query.SuccessResponse(c, nil, r.sourceDTOs(c, a))
}

// CreateSource adds a metric source to a module.
func (r *ForecastRouter) CreateSource(c *gin.Context) {
	a, ok := r.module(c)
	if !ok {
		return
	}
	var req apis.SourceReq
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}
	s, made, err := r.sourceFromReq(c.Request.Context(), a.ID, &req)
	if err == nil {
		err = r.checkDataset(c.Request.Context(), a, s)
	}
	if err != nil {
		if made != 0 {
			r.dropMappings(c.Request.Context(), []uint{made})
		}
		query.BadRequest(c, err)
		return
	}
	if err := r.Modules.SaveSource(c.Request.Context(), s); err != nil {
		if made != 0 {
			r.dropMappings(c.Request.Context(), []uint{made})
		}
		r.writeError(c, err)
		return
	}
	query.SuccessResponse(c, nil, s)
}

// UpdateSource patches a source; its kind (and so its stored history) cannot change.
func (r *ForecastRouter) UpdateSource(c *gin.Context) {
	a, ok := r.module(c)
	if !ok {
		return
	}
	sid, ok := parseUintParam(c, "sid")
	if !ok {
		return
	}
	var req apis.SourceUpdateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}
	s, err := r.Modules.GetSource(c.Request.Context(), sid)
	if err != nil || s.ModuleID != a.ID {
		query.NotFound(c, query.ErrRecordNotFound)
		return
	}
	if req.Name != nil {
		s.Name = *req.Name
	}
	if req.Resource != nil {
		s.Resource = *req.Resource
	}
	if req.MappingID != nil {
		s.MappingID = *req.MappingID
	}
	var replaced, made uint
	if req.Custom != nil {
		if req.MappingID != nil {
			query.BadRequest(c, fmt.Errorf("give either mapping_id or custom, not both"))
			return
		}
		id, err := r.customMapping(c.Request.Context(), &apis.SourceReq{Name: s.Name, Kind: s.Kind, Custom: req.Custom}, true)
		if err != nil {
			query.BadRequest(c, err)
			return
		}
		replaced, made, s.MappingID = s.MappingID, id, id
	}
	if req.Priority != nil {
		s.Priority = *req.Priority
	}
	if req.Weight != nil {
		s.Weight = *req.Weight
	}
	if req.Stat != nil {
		s.Stat = *req.Stat
	}
	if req.Basis != nil {
		s.Basis = *req.Basis
	}
	if req.WindowHours != nil {
		s.WindowHours = *req.WindowHours
	}
	if req.Threshold != nil {
		s.Threshold = *req.Threshold
	}
	if req.Enabled != nil {
		s.Enabled = *req.Enabled
	}
	if req.Remark != nil {
		s.Remark = *req.Remark
	}
	if req.Scale != nil {
		s.Scale = *req.Scale
	}
	if req.Precision != nil {
		s.Precision = *req.Precision
	}
	if req.Aggregation != nil {
		s.Aggregation = *req.Aggregation
	}
	if req.Missing != nil {
		s.Missing = *req.Missing
	}
	// a new mapping written for this update is taken back when the update then fails
	rollback := func() {
		if made != 0 {
			r.dropMappings(c.Request.Context(), []uint{made})
		}
	}
	if err := s.Validate(); err != nil {
		rollback()
		query.BadRequest(c, err)
		return
	}
	if err := r.checkDataset(c.Request.Context(), a, s); err != nil {
		rollback()
		query.BadRequest(c, err)
		return
	}
	if err := r.checkSource(c.Request.Context(), s); err != nil {
		rollback()
		query.BadRequest(c, err)
		return
	}
	if err := r.Modules.SaveSource(c.Request.Context(), s); err != nil {
		rollback()
		r.writeError(c, err)
		return
	}
	if replaced != 0 && replaced != made {
		_, _ = r.Mappings.DeleteIfUnused(c.Request.Context(), replaced)
	}
	query.SuccessResponse(c, nil, s)
}

// DeleteSource removes a source and the history stored for it.
func (r *ForecastRouter) DeleteSource(c *gin.Context) {
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	sid, ok := parseUintParam(c, "sid")
	if !ok {
		return
	}
	if err := r.Modules.DeleteSource(c.Request.Context(), id, sid); err != nil {
		r.writeError(c, err)
		return
	}
	query.SuccessResponse(c, nil, "ok")
}

// ---- the data side of a module: history, collect, predict, backtest, evaluate

// ModuleHistory serves the stored samples of one source with the quality report.
func (r *ForecastRouter) ModuleHistory(c *gin.Context) {
	a, ok := r.module(c)
	if !ok {
		return
	}
	metric := c.Query("metric")
	if a.Data().MappingID(metric) == 0 {
		query.BadRequest(c, fmt.Errorf("metric must be the key of one of the module's metric sources"))
		return
	}
	to := time.Now().UTC()
	if t := timeQuery(c, "to"); t != nil {
		to = *t
	}
	from := to.Add(-24 * time.Hour)
	if t := timeQuery(c, "from"); t != nil {
		from = *t
	}
	maxPoints, _ := strconv.Atoi(c.Query("max_points"))
	history, err := r.Service.History(c.Request.Context(), a.ID, metric, from, to, maxPoints)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			query.SuccessResponse(c, nil, forecastsvc.History{Metric: metric, Unit: forecastsvc.MetricUnit(a.Data().KindOf(metric)), Points: []forecastsvc.HistoryPoint{}, NotCollected: true})
			return
		}
		query.BadRequest(c, err)
		return
	}
	query.SuccessResponse(c, nil, history)
}

// ModuleCompare serves the actual values of a metric of the module, read from Prometheus for the window (the history from
// before the module existed included), next to the forecasts of the algorithms for the same time points and the actual
// and forecast replica counts. @Router /api/v1/modules/{id}/compare [get]
func (r *ForecastRouter) ModuleCompare(c *gin.Context) {
	a, ok := r.module(c)
	if !ok {
		return
	}
	to := time.Now().UTC()
	if t := timeQuery(c, "to"); t != nil {
		to = *t
	}
	from := to.Add(-24 * time.Hour)
	if t := timeQuery(c, "from"); t != nil {
		from = *t
	}
	if !from.Before(to) {
		query.BadRequest(c, fmt.Errorf("from must be before to"))
		return
	}
	maxPoints, _ := strconv.Atoi(c.Query("max_points"))
	var algorithms []string
	for _, id := range strings.Split(c.Query("algorithms"), ",") {
		if id = strings.TrimSpace(id); id != "" {
			algorithms = append(algorithms, id)
		}
	}
	ctx, cancel := r.stepCtx(c)
	defer cancel()
	out, err := r.Service.Compare(ctx, a.ID, forecastsvc.CompareOptions{Metric: c.Query("metric"), From: from, To: to, Algorithms: algorithms,
		MaxPoints: maxPoints, WithPredictions: c.Query("predictions") != "false"})
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			query.NotFound(c, query.ErrRecordNotFound)
			return
		}
		query.BadRequest(c, err)
		return
	}
	query.SuccessResponse(c, nil, out)
}

func (r *ForecastRouter) stepCtx(c *gin.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(c.Request.Context(), pipelineTimeout)
}

// ModuleCollect pulls the history of the module's sources.
func (r *ForecastRouter) ModuleCollect(c *gin.Context) {
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	ctx, cancel := r.stepCtx(c)
	defer cancel()
	results, err := r.Service.Collect(ctx, id)
	if err != nil {
		r.writeError(c, err)
		return
	}
	query.SuccessResponse(c, nil, results)
}

// ModulePredict runs every enabled algorithm on the module's sources.
func (r *ForecastRouter) ModulePredict(c *gin.Context) {
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	ctx, cancel := r.stepCtx(c)
	defer cancel()
	outcomes, err := r.Service.Predict(ctx, id)
	if err != nil {
		r.writeError(c, err)
		return
	}
	query.SuccessResponse(c, nil, outcomes)
}

// ModuleRun collects and predicts in one call, as the scheduled task does.
func (r *ForecastRouter) ModuleRun(c *gin.Context) {
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	ctx, cancel := r.stepCtx(c)
	defer cancel()
	res, err := r.Service.RunData(ctx, id, nil)
	if err != nil {
		r.writeError(c, err)
		return
	}
	query.SuccessResponse(c, nil, res)
}

// ModuleBacktest replays every enabled algorithm on one source against the baselines.
func (r *ForecastRouter) ModuleBacktest(c *gin.Context) {
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	var req apis.BacktestReq
	if err := c.ShouldBindJSON(&req); err != nil {
		query.BadRequest(c, err)
		return
	}
	ctx, cancel := r.stepCtx(c)
	defer cancel()
	runs, err := r.Service.Backtest(ctx, id, req.Metric, req.Cutoffs)
	if err != nil {
		r.writeError(c, err)
		return
	}
	query.SuccessResponse(c, nil, runs)
}

// ModuleEvaluate scores the finished prediction runs of a module's algorithms against the collected actuals.
func (r *ForecastRouter) ModuleEvaluate(c *gin.Context) {
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	ctx, cancel := r.stepCtx(c)
	defer cancel()
	results, err := r.Service.Evaluate(ctx, id)
	if err != nil {
		r.writeError(c, err)
		return
	}
	query.SuccessResponse(c, nil, results)
}

// ModuleBacktests lists the backtest records of one module.
func (r *ForecastRouter) ModuleBacktests(c *gin.Context) {
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	page, size := pageParams(c)
	f := backtestFilter(c)
	f.ModuleID = id
	list, total, err := r.Predictions.SearchBacktests(c.Request.Context(), f, page, size)
	if err != nil {
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, gin.H{"list": list, "total": total, "page": page, "pageSize": size})
}

func backtestFilter(c *gin.Context) rdb.BacktestFilter {
	return rdb.BacktestFilter{ModuleID: uintQuery(c, "module_id"), Metric: c.Query("metric"), AlgorithmID: c.Query("algorithm_id"), Promoted: boolQuery(c, "promoted"),
		Since: timeQuery(c, "from"), Until: timeQuery(c, "to"), Sort: c.Query("sort_by"), Dir: c.Query("sort_order")}
}

// SearchBacktests serves GET /backtests: the backtest records of every instance, or of one with module_id.
func (r *ForecastRouter) SearchBacktests(c *gin.Context) {
	page, size := pageParams(c)
	list, total, err := r.Predictions.SearchBacktests(c.Request.Context(), backtestFilter(c), page, size)
	if err != nil {
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, gin.H{"list": list, "total": total, "page": page, "pageSize": size})
}

// ListPredictions serves the stored forecasts.
func (r *ForecastRouter) ListPredictions(c *gin.Context) {
	page, size := pageParams(c)
	list, total, err := r.Predictions.ListPredictions(c.Request.Context(), rdb.PredictionFilter{
		ModuleID: uintQuery(c, "module_id"), Metric: c.Query("metric"), AlgorithmID: c.Query("algorithm_id"),
		From: timeQuery(c, "from"), To: timeQuery(c, "to"),
	}, page, size)
	if err != nil {
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, gin.H{"list": list, "total": total, "page": page, "pageSize": size})
}

// sinceHours reads an optional look-back in hours as a time.
func sinceHours(c *gin.Context) *time.Time {
	hours, err := strconv.Atoi(c.Query("since_hours"))
	if err != nil || hours <= 0 {
		// a page that picks a start time sends it as from
		return timeQuery(c, "from")
	}
	t := time.Now().UTC().Add(-time.Duration(hours) * time.Hour)
	return &t
}

// ListEvaluations serves the measured accuracy of finished prediction runs.
func (r *ForecastRouter) ListEvaluations(c *gin.Context) {
	page, size := pageParams(c)
	list, total, err := r.Predictions.ListEvaluations(c.Request.Context(), rdb.EvaluationFilter{
		ModuleID: uintQuery(c, "module_id"), Metric: c.Query("metric"), AlgorithmID: c.Query("algorithm_id"), Since: sinceHours(c),
		Until: timeQuery(c, "to"), Sort: c.Query("sort_by"), Dir: c.Query("sort_order"),
	}, page, size)
	if err != nil {
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, gin.H{"list": list, "total": total, "page": page, "pageSize": size})
}

func paramsJSON(algorithmID string, params map[string]any) (string, error) {
	raw := ""
	if params != nil {
		data, err := jsonMarshal(params)
		if err != nil {
			return "", err
		}
		raw = data
	}
	return forecastsvc.ValidateParams(algorithmID, raw)
}
