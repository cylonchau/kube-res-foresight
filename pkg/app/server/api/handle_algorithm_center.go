package api

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/cylonchau/kube-res-foresight/pkg/app/query"
	"github.com/cylonchau/kube-res-foresight/pkg/dao/rdb"
	"github.com/cylonchau/kube-res-foresight/pkg/forecast"
	"github.com/cylonchau/kube-res-foresight/pkg/forecastsvc"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/cylonchau/kube-res-foresight/pkg/scoring"
	"github.com/cylonchau/kube-res-foresight/pkg/timing"
)

// AlgorithmCenterRouter serves the usage of an algorithm across the modules, the trace from an algorithm on a module
// to the evaluation of its predictions, and the scores and ranking of the algorithms of a module.
type AlgorithmCenterRouter struct {
	Modules     *rdb.ModuleDAO
	Algorithms  *rdb.AlgorithmDAO
	Predictions *rdb.PredictionDAO
	Tasks       *rdb.TaskDefinitionDAO
	Jobs        *rdb.JobDAO
	RunStates   *rdb.RunStateDAO
	Service     *forecastsvc.Service
}

// SourceUsage is how an algorithm has performed on one source of a module.
type SourceUsage struct {
	Source   string `json:"source"`
	Backtest *struct {
		At        time.Time `json:"at"`
		WAPE      float64   `json:"wape"`
		PeakUnder float64   `json:"peak_under"`
		Points    int       `json:"points"`
		Promoted  bool      `json:"promoted"`
		Reason    string    `json:"reason"`
	} `json:"backtest,omitempty"`
	Measured *struct {
		Runs      int     `json:"runs"`
		WAPE      float64 `json:"wape"`
		PeakUnder float64 `json:"peak_under"`
		Points    int     `json:"points"`
	} `json:"measured,omitempty"`
}

// UsageModule is how one algorithm performs for one module: its accuracy on each of the module's forecast sources.
type UsageModule struct {
	ModuleID   uint          `json:"module_id"`
	ModuleName string        `json:"module_name"`
	Namespace  string        `json:"namespace"`
	Workload   string        `json:"workload"`
	Enabled    bool          `json:"enabled"`
	Score      *float64      `json:"score"`
	Rank       int           `json:"rank"`
	Sources    []SourceUsage `json:"source_usage"`
	// Mode and Dataset say what the algorithm runs on for this module: the metric sources of the module's dataset.
	Mode    string        `json:"mode"`
	Dataset []DatasetInfo `json:"dataset"`
}

// DatasetInfo is one metric of the dataset of a module, with the Prometheus query behind it.
type DatasetInfo struct {
	Key         string `json:"key"`
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	Category    string `json:"category"`
	Unit        string `json:"unit"`
	StepSeconds int    `json:"step_seconds"`
	Mapping     string `json:"mapping"`
	Query       string `json:"query"`
	Enabled     bool   `json:"enabled"`
}

const usageRecentRuns = 20

// sourceUsage reads the latest backtest and the recent measured accuracy of an instance on a source.
func (r *AlgorithmCenterRouter) sourceUsage(ctx context.Context, inst *model.AlgorithmRun, source string) SourceUsage {
	out := SourceUsage{Source: source}
	if run, err := r.Predictions.LatestBacktest(ctx, inst.ID, source, inst.Key()); err == nil && run != nil {
		var score struct {
			WAPE      float64 `json:"wape"`
			PeakUnder float64 `json:"peak_under"`
			Points    int     `json:"points"`
		}
		_ = json.Unmarshal([]byte(run.Metrics), &score)
		out.Backtest = &struct {
			At        time.Time `json:"at"`
			WAPE      float64   `json:"wape"`
			PeakUnder float64   `json:"peak_under"`
			Points    int       `json:"points"`
			Promoted  bool      `json:"promoted"`
			Reason    string    `json:"reason"`
		}{At: run.CreatedAt, WAPE: score.WAPE, PeakUnder: score.PeakUnder, Points: score.Points, Promoted: run.Promoted, Reason: run.Reason}
	}
	evals, _, err := r.Predictions.ListEvaluations(ctx, rdb.EvaluationFilter{ModuleID: inst.ID, Metric: source, AlgorithmID: inst.Key()}, 1, usageRecentRuns)
	if err == nil {
		var points, runs int
		var wape, peak float64
		for _, e := range evals {
			if e.Points == 0 {
				continue
			}
			runs++
			points += e.Points
			wape += e.WAPE * float64(e.Points)
			peak += e.PeakUnder * float64(e.Points)
		}
		if points > 0 {
			out.Measured = &struct {
				Runs      int     `json:"runs"`
				WAPE      float64 `json:"wape"`
				PeakUnder float64 `json:"peak_under"`
				Points    int     `json:"points"`
			}{Runs: runs, WAPE: wape / float64(points), PeakUnder: peak / float64(points), Points: points}
		}
	}
	return out
}

// Usage lists how a catalog algorithm performs for each module it runs on (every module, while it is enabled).
// @Summary Algorithm usage
// @Tags Forecast
// @Router /api/v1/algorithms/{id}/usage [get]
func (r *AlgorithmCenterRouter) Usage(c *gin.Context) {
	algorithmID := c.Param("id")
	if err := r.Service.EnsureAlgorithms(c.Request.Context()); err != nil {
		query.InternalError(c, err)
		return
	}
	def, ok := forecast.Lookup(algorithmID)
	if !ok {
		query.NotFound(c, query.ErrRecordNotFound)
		return
	}
	ctx := c.Request.Context()
	cfg, err := r.Algorithms.Get(ctx, algorithmID)
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		query.InternalError(c, err)
		return
	}
	if cfg == nil {
		cfg = &model.AlgorithmConfig{AlgorithmID: algorithmID, Weight: 1}
	}
	modules, err := r.Modules.ListEnabled(ctx)
	if err != nil {
		query.InternalError(c, err)
		return
	}
	out := make([]UsageModule, 0, len(modules))
	for _, m := range modules {
		run := model.NewRun(m, cfg)
		item := UsageModule{ModuleID: m.ID, ModuleName: m.Name, Namespace: m.Namespace, Workload: m.Workload, Enabled: cfg.Enabled, Mode: m.Mode, Dataset: []DatasetInfo{}}
		for _, src := range m.Sources {
			info := DatasetInfo{Key: src.Key, Name: src.Name, Kind: src.Kind, Category: model.KindCategory(src.Kind), Unit: forecastsvc.MetricUnit(src.Kind), StepSeconds: m.StepSeconds, Enabled: src.Enabled}
			if mp, err := r.Service.Mappings.GetByID(ctx, src.MappingID); err == nil {
				info.Mapping, info.Query = mp.Name, mp.Query
				if info.Unit == "" {
					info.Unit = mp.Unit
				}
				if info.StepSeconds == 0 {
					info.StepSeconds = mp.StepSeconds
				}
			}
			item.Dataset = append(item.Dataset, info)
		}
		for _, key := range run.SourceKeys() {
			item.Sources = append(item.Sources, r.sourceUsage(ctx, run, key))
		}
		if scores, err := r.Predictions.LatestScores(ctx, m.ID); err == nil {
			if sc, ok := scores[algorithmID]; ok {
				score := sc.Score
				item.Score, item.Rank = &score, sc.Rank
			}
		}
		out = append(out, item)
	}
	query.SuccessResponse(c, nil, gin.H{"algorithm": def, "enabled": cfg.Enabled, "modules": out})
}

// Trace follows one algorithm on one module down the chain its forecasts take: the module, the scheduled task that
// runs it, the predictions it produced on a source, how those were evaluated, and its score in the module.
// @Summary Trace an algorithm on a module
// @Tags Forecast
// @Router /api/v1/modules/{id}/trace [get]
func (r *AlgorithmCenterRouter) Trace(c *gin.Context) {
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	ctx := c.Request.Context()
	m, err := r.Modules.Get(ctx, id)
	if err != nil {
		query.HandleDBError(c, err)
		return
	}
	algorithmID := c.Query("algorithm")
	if _, known := forecast.Lookup(algorithmID); !known {
		query.BadRequest(c, errors.New("algorithm must be an algorithm of the catalog"))
		return
	}
	cfg, err := r.Algorithms.Get(ctx, algorithmID)
	if err != nil {
		query.HandleDBError(c, err)
		return
	}
	inst := model.NewRun(m, cfg)
	source := c.Query("source")
	if source == "" {
		if keys := inst.SourceKeys(); len(keys) > 0 {
			source = keys[0]
		}
	}
	trace := gin.H{"module": gin.H{"id": m.ID, "name": m.Name, "enabled": m.Enabled, "mode": m.Mode}, "algorithm": algorithmID, "algorithm_enabled": cfg.Enabled, "source": source}

	task := gin.H{"task_type": "forecast.run", "defined": false}
	if def, err := r.Tasks.GetByType(ctx, "forecast.run"); err == nil && def != nil {
		task["defined"], task["cron_expr"], task["enabled"] = true, def.CronExpr, def.Enabled
	} else if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		query.InternalError(c, err)
		return
	}
	trace["task"] = task

	prediction := gin.H{"present": false}
	if source != "" {
		if rows, err := r.Predictions.LatestRun(ctx, inst.ID, source, inst.Key()); err == nil && len(rows) > 0 {
			version, bindErr := r.Service.RunVersion(ctx, inst)
			prediction = gin.H{"present": true, "prediction_time": rows[0].PredictionTime, "model_version": rows[0].ModelVersion, "points": len(rows),
				"first_target": rows[0].TargetTime, "last_target": rows[len(rows)-1].TargetTime, "unit": rows[0].Unit,
				"current_version": version, "version_current": bindErr == nil && version == rows[0].ModelVersion}
		}
		if evals, _, err := r.Predictions.ListEvaluations(ctx, rdb.EvaluationFilter{ModuleID: inst.ID, Metric: source, AlgorithmID: inst.Key()}, 1, 1); err == nil && len(evals) > 0 {
			trace["evaluation"] = evals[0]
		}
		if run, err := r.Predictions.LatestBacktest(ctx, inst.ID, source, inst.Key()); err == nil && run != nil {
			canonical, _ := forecastsvc.ValidateParams(inst.AlgorithmID, inst.Params)
			trace["backtest"] = gin.H{"id": run.ID, "at": run.CreatedAt, "promoted": run.Promoted, "reason": run.Reason, "params_current": run.Params == canonical}
		}
	}
	trace["prediction"] = prediction
	if scores, err := r.Predictions.LatestScores(ctx, m.ID); err == nil {
		if sc, ok := scores[inst.Key()]; ok {
			trace["score"] = gin.H{"score": sc.Score, "rank": sc.Rank, "at": sc.CreatedAt, "windows": sc.Windows, "selected": sc.Selected}
		}
	}
	query.SuccessResponse(c, nil, trace)
}

// RunScore replays the recent windows for every algorithm instance bound to a module and stores their scores.
// @Summary Score the algorithm instances of a module
// @Tags Forecast
// @Router /api/v1/modules/{id}/score [post]
func (r *AlgorithmCenterRouter) RunScore(c *gin.Context) {
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), r.Service.Timing(c.Request.Context(), timing.PipelineTimeout))
	defer cancel()
	rows, err := r.Service.ScoreModule(ctx, id)
	if err != nil {
		if errors.Is(err, forecastsvc.ErrNotConfigured) {
			query.BadRequest(c, err)
			return
		}
		query.HandleDBError(c, err)
		return
	}
	query.SuccessResponse(c, nil, rows)
}

// RankingRow is one algorithm in the ranking of a module.
type RankingRow struct {
	Name        string          `json:"name"`
	AlgorithmID string          `json:"algorithm_id"`
	Enabled     bool            `json:"enabled"`
	Weight      float64         `json:"weight"`
	Score       *float64        `json:"score"`
	Rank        int             `json:"rank"`
	Windows     int             `json:"windows"`
	Selected    bool            `json:"selected"`
	At          *time.Time      `json:"at"`
	Detail      *scoring.Result `json:"detail,omitempty"`
	Breakdown   []BreakdownRow  `json:"breakdown"`
	// PreviousRank and PreviousScore are what the algorithm had at its evaluation before the latest one, to show how it
	// moved; nil when it was evaluated once.
	PreviousRank  *int     `json:"previous_rank"`
	PreviousScore *float64 `json:"previous_score"`
	// Status is ready for a scored algorithm; an enabled one without a score says why (skipped, failed, not_run, no_result).
	Status string `json:"status"`
	Reason string `json:"reason"`
}

// Ranking lists the algorithms of a module with their latest score, best first, and which one is in use.
// @Summary Ranking of the algorithms of a module
// @Tags Forecast
// @Router /api/v1/modules/{id}/ranking [get]
func (r *AlgorithmCenterRouter) Ranking(c *gin.Context) {
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	ctx := c.Request.Context()
	if err := r.Service.EnsureAlgorithms(ctx); err != nil {
		query.InternalError(c, err)
		return
	}
	configs, err := r.Algorithms.All(ctx)
	if err != nil {
		query.InternalError(c, err)
		return
	}
	scores, err := r.Predictions.LatestScores(ctx, id)
	if err != nil {
		query.InternalError(c, err)
		return
	}
	previous, err := r.Predictions.PreviousScores(ctx, id)
	if err != nil {
		query.InternalError(c, err)
		return
	}
	state, err := r.RunStates.RunState(ctx, id)
	if err != nil {
		query.InternalError(c, err)
		return
	}
	out := make([]RankingRow, 0, len(configs))
	for _, cfg := range configs {
		row := RankingRow{Breakdown: []BreakdownRow{}, Name: cfg.AlgorithmID, AlgorithmID: cfg.AlgorithmID, Enabled: cfg.Enabled, Weight: cfg.Weight, Selected: cfg.AlgorithmID == state.Selected}
		sc, scored := scores[cfg.AlgorithmID]
		if !cfg.Enabled && !scored {
			continue // a disabled algorithm that never ran has nothing to rank
		}
		if scored {
			score := sc.Score
			at := sc.CreatedAt
			row.Score, row.Rank, row.Windows, row.At = &score, sc.Rank, sc.Windows, &at
			var detail scoring.Result
			if json.Unmarshal([]byte(sc.Detail), &detail) == nil {
				row.Detail = &detail
				row.Breakdown = breakdownOf(detail, scoring.DefaultWeights())
			}
		}
		row.Status = forecastsvc.StatusReady
		if !scored {
			st := r.Service.AlgorithmStatus(ctx, id, "", cfg.AlgorithmID, false, "it ran, but has not been scored yet: scoring replays the algorithm over its history and needs enough finished windows")
			row.Status, row.Reason = st.Status, st.Reason
		}
		if prev, ok := previous[cfg.AlgorithmID]; ok && scored {
			rank, score := prev.Rank, prev.Score
			row.PreviousRank, row.PreviousScore = &rank, &score
		}
		out = append(out, row)
	}
	sortRanking(out)
	query.SuccessResponse(c, nil, gin.H{"list": out, "selected": state.Selected, "challenger": state.Challenger, "challenger_cycles": state.ChallengerCycles})
}

func sortRanking(rows []RankingRow) {
	value := func(r RankingRow) float64 {
		w := r.Weight
		if w <= 0 {
			w = 1
		}
		return *r.Score * w
	}
	less := func(a, b RankingRow) bool {
		switch {
		case (a.Score == nil) != (b.Score == nil):
			return a.Score != nil
		case a.Score != nil && b.Score != nil && value(a) != value(b):
			return value(a) > value(b)
		}
		return a.Name < b.Name
	}
	for i := 1; i < len(rows); i++ {
		for j := i; j > 0 && less(rows[j], rows[j-1]); j-- {
			rows[j], rows[j-1] = rows[j-1], rows[j]
		}
	}
}

func uintQuery(c *gin.Context, name string) uint {
	v, _ := strconv.ParseUint(c.Query(name), 10, 64)
	return uint(v)
}

// ListScores serves the score history of the algorithm instances.
// @Summary Score history
// @Tags Forecast
// @Router /api/v1/algorithm-scores [get]
func (r *AlgorithmCenterRouter) ListScores(c *gin.Context) {
	page, size := pageParams(c)
	f := rdb.ScoreFilter{ModuleID: uintQuery(c, "module_id"), Algorithm: c.Query("algorithm"), Selected: boolQuery(c, "selected"),
		Since: timeQuery(c, "from"), Until: timeQuery(c, "to"), Sort: c.Query("sort_by"), Dir: c.Query("sort_order")}
	list, total, err := r.Predictions.SearchScores(c.Request.Context(), f, page, size)
	if err != nil {
		query.InternalError(c, err)
		return
	}
	ids := make([]uint, len(list))
	for i, sc := range list {
		ids[i] = sc.ModuleID
	}
	names := r.moduleNames(c, ids...)
	out := make([]scoreDTO, len(list))
	for i, sc := range list {
		out[i] = r.scoreDTO(sc, names)
	}
	query.SuccessResponse(c, nil, gin.H{"list": out, "total": total, "page": page, "pageSize": size})
}

// ListAdmissions serves the answers given to admission requests.
// @Summary Admission decisions
// @Tags Forecast
// @Router /api/v1/admissions [get]
func (r *AlgorithmCenterRouter) ListAdmissions(c *gin.Context) {
	page, size := pageParams(c)
	f := rdb.AdmissionFilter{ModuleID: uintQuery(c, "module_id"), Applied: boolQuery(c, "applied"), Keyword: c.Query("keyword"), Origin: c.Query("origin"),
		Since: timeQuery(c, "from"), Until: timeQuery(c, "to"), Sort: c.Query("sort_by"), Dir: c.Query("sort_order")}
	list, total, err := r.Predictions.SearchAdmissions(c.Request.Context(), f, page, size)
	if err != nil {
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, gin.H{"list": list, "total": total, "page": page, "pageSize": size})
}
