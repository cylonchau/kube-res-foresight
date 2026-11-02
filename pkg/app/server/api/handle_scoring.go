package api

import (
	"encoding/json"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/cylonchau/kube-res-foresight/pkg/app/query"
	"github.com/cylonchau/kube-res-foresight/pkg/dao/rdb"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/cylonchau/kube-res-foresight/pkg/scoring"
)

// BreakdownRow is one part of a score: how accurate the algorithm was on a resource, the error, the weight the part
// has in the composite and what it contributed to it. A part with no data in any window is left out of the composite
// and the other weights are renormalised, so Weight is the share actually used.
type BreakdownRow struct {
	Metric       string  `json:"metric"` // cpu, memory or gpu; replicas only for a module with no resource source
	Accuracy     float64 `json:"accuracy"`
	Error        float64 `json:"error"`
	Weight       float64 `json:"weight"`
	Contribution float64 `json:"contribution"` // points of the 0-100 score
	// Score is the grade of this resource by itself (0-100): what ranks the algorithms for CPU, for memory and for GPU.
	Score float64 `json:"score"`
}

// breakdownOf lists the parts of a score, using the weights the score was made with. The accuracy is that of the resources;
// the replica count follows from them and is a part only when there is no resource to grade.
func breakdownOf(d scoring.Result, wt scoring.Weights) []BreakdownRow {
	parts := []struct {
		name     string
		accuracy float64
		weight   float64
	}{{"cpu", d.CPU, wt.CPU}, {"memory", d.Memory, wt.Memory}, {"gpu", d.GPU, wt.GPU}}
	hasResource := false
	for _, p := range parts {
		hasResource = hasResource || p.accuracy >= 0
	}
	if !hasResource {
		parts = parts[:0]
		parts = append(parts, struct {
			name     string
			accuracy float64
			weight   float64
		}{"replicas", d.Replicas, wt.Replicas})
	}
	var total float64
	for _, p := range parts {
		if p.accuracy >= 0 {
			total += p.weight
		}
	}
	holes := 0.7 + 0.3*d.Completeness
	rows := []BreakdownRow{}
	for _, p := range parts {
		if p.accuracy < 0 || total == 0 {
			continue
		}
		share := p.weight / total
		counted := p.accuracy
		if p.name == "memory" {
			counted *= 1 - d.OOMPenalty // an OOM kill is a memory event: it lowers the memory part and no other
		}
		rows = append(rows, BreakdownRow{Metric: p.name, Accuracy: p.accuracy, Error: 1 - p.accuracy, Weight: share,
			Contribution: 100 * share * counted * holes, Score: 100 * counted * holes})
	}
	return rows
}

// scoreDTO is a stored score with its parts.
type scoreDTO struct {
	*model.AlgorithmScore
	ModuleName string          `json:"module_name"`
	Result     *scoring.Result `json:"result,omitempty"`
	Breakdown  []BreakdownRow  `json:"breakdown"`
}

func (r *AlgorithmCenterRouter) scoreDTO(s *model.AlgorithmScore, names map[uint]string) scoreDTO {
	d := scoreDTO{AlgorithmScore: s, ModuleName: names[s.ModuleID], Breakdown: []BreakdownRow{}}
	var res scoring.Result
	if json.Unmarshal([]byte(s.Detail), &res) == nil && s.Detail != "" {
		d.Result = &res
		d.Breakdown = breakdownOf(res, scoring.DefaultWeights())
	}
	return d
}

// moduleNames reads the names of modules.
func (r *AlgorithmCenterRouter) moduleNames(c *gin.Context, ids ...uint) map[uint]string {
	names := map[uint]string{}
	for _, id := range ids {
		if _, seen := names[id]; seen {
			continue
		}
		if m, err := r.Modules.Get(c.Request.Context(), id); err == nil {
			names[id] = m.Name
		}
	}
	return names
}

// HistoryRankings serves the ranking of every past evaluation: for each module and time, how the algorithms were
// ordered and which one was in use, so the change of the ranking over time and between modules can be followed.
// @Summary Ranking history
// @Tags Forecast
// @Router /api/v1/algorithm-rankings/history [get]
func (r *AlgorithmCenterRouter) HistoryRankings(c *gin.Context) {
	page, size := pageParams(c)
	f := rdb.ScoreFilter{ModuleID: uintQuery(c, "module_id"), Algorithm: c.Query("algorithm"), Since: timeQuery(c, "from"), Until: timeQuery(c, "to")}
	runs, total, err := r.Predictions.ScoreRuns(c.Request.Context(), f, page, size)
	if err != nil {
		query.InternalError(c, err)
		return
	}
	var ids []uint
	for _, run := range runs {
		ids = append(ids, run.ModuleID)
	}
	names := r.moduleNames(c, ids...)
	type row struct {
		Algorithm string         `json:"algorithm"`
		Score     float64        `json:"score"`
		Rank      int            `json:"rank"`
		Windows   int            `json:"windows"`
		Selected  bool           `json:"selected"`
		Breakdown []BreakdownRow `json:"breakdown"`
	}
	type entry struct {
		ModuleID    uint      `json:"module_id"`
		ModuleName  string    `json:"module_name"`
		At          time.Time `json:"at"`
		WindowStart time.Time `json:"window_start"`
		WindowEnd   time.Time `json:"window_end"`
		Best        string    `json:"best"`
		Selected    string    `json:"selected"`
		Rows        []row     `json:"rows"`
	}
	list := make([]entry, 0, len(runs))
	for _, run := range runs {
		e := entry{ModuleID: run.ModuleID, ModuleName: names[run.ModuleID], At: run.At, WindowStart: run.WindowStart, WindowEnd: run.WindowEnd, Rows: []row{}}
		for _, s := range run.Scores {
			d := r.scoreDTO(s, names)
			e.Rows = append(e.Rows, row{Algorithm: s.Algorithm, Score: s.Score, Rank: s.Rank, Windows: s.Windows, Selected: s.Selected, Breakdown: d.Breakdown})
			if s.Rank == 1 || e.Best == "" {
				e.Best = s.Algorithm
			}
			if s.Selected {
				e.Selected = s.Algorithm
			}
		}
		list = append(list, e)
	}
	query.SuccessResponse(c, nil, gin.H{"list": list, "total": total, "page": page, "pageSize": size})
}

// CurrentRankings serves the newest ranking of each module (or of one): the score, the accuracy and the error of each
// algorithm with their parts, the best algorithm and the one in use.
// @Summary Current rankings
// @Tags Forecast
// @Router /api/v1/algorithm-rankings/current [get]
func (r *AlgorithmCenterRouter) CurrentRankings(c *gin.Context) {
	page, size := pageParams(c)
	ctx := c.Request.Context()
	modules, total, err := r.Modules.List(ctx, rdb.ModuleFilter{Keyword: c.Query("keyword")}, page, size)
	if err != nil {
		query.InternalError(c, err)
		return
	}
	if id := uintQuery(c, "module_id"); id != 0 {
		m, err := r.Modules.Get(ctx, id)
		if err != nil {
			query.NotFound(c, query.ErrRecordNotFound)
			return
		}
		modules, total = []*model.ForecastModule{m}, 1
	}
	type row struct {
		Algorithm string          `json:"algorithm"`
		Score     float64         `json:"score"`
		Rank      int             `json:"rank"`
		Windows   int             `json:"windows"`
		Selected  bool            `json:"selected"`
		At        time.Time       `json:"at"`
		Result    *scoring.Result `json:"result,omitempty"`
		Breakdown []BreakdownRow  `json:"breakdown"`
		Weight    float64         `json:"weight"`
	}
	type entry struct {
		ModuleID   uint   `json:"module_id"`
		ModuleName string `json:"module_name"`
		Namespace  string `json:"namespace"`
		Workload   string `json:"workload"`
		Best       string `json:"best"`
		Selected   string `json:"selected"`
		Rows       []row  `json:"rows"`
	}
	if err := r.Service.EnsureAlgorithms(ctx); err != nil {
		query.InternalError(c, err)
		return
	}
	configs, err := r.Algorithms.All(ctx)
	if err != nil {
		query.InternalError(c, err)
		return
	}
	weights := map[string]float64{}
	for _, cfg := range configs {
		weights[cfg.AlgorithmID] = cfg.Weight
	}
	list := make([]entry, 0, len(modules))
	for _, m := range modules {
		scores, err := r.Predictions.LatestScores(ctx, m.ID)
		if err != nil {
			query.InternalError(c, err)
			return
		}
		state, err := r.RunStates.RunState(ctx, m.ID)
		if err != nil {
			query.InternalError(c, err)
			return
		}
		e := entry{ModuleID: m.ID, ModuleName: m.Name, Namespace: m.Namespace, Workload: m.Workload, Selected: state.Selected, Rows: []row{}}
		for _, s := range scores {
			d := r.scoreDTO(s, map[uint]string{m.ID: m.Name})
			e.Rows = append(e.Rows, row{Algorithm: s.Algorithm, Score: s.Score, Rank: s.Rank, Windows: s.Windows, Selected: s.Algorithm == state.Selected,
				At: s.CreatedAt, Result: d.Result, Breakdown: d.Breakdown, Weight: weights[s.Algorithm]})
		}
		sortRows(e.Rows, func(a, b row) bool {
			if a.Rank != b.Rank && a.Rank > 0 && b.Rank > 0 {
				return a.Rank < b.Rank
			}
			if a.Score != b.Score {
				return a.Score > b.Score
			}
			return a.Algorithm < b.Algorithm
		})
		if len(e.Rows) > 0 {
			e.Best = e.Rows[0].Algorithm
		}
		list = append(list, e)
	}
	query.SuccessResponse(c, nil, gin.H{"list": list, "total": total, "page": page, "pageSize": size})
}

func sortRows[T any](rows []T, less func(a, b T) bool) {
	for i := 1; i < len(rows); i++ {
		for j := i; j > 0 && less(rows[j], rows[j-1]); j-- {
			rows[j], rows[j-1] = rows[j-1], rows[j]
		}
	}
}

// MetricScores serves how each algorithm forecast each source of a module against the actuals Prometheus reported:
// the error, accuracy and weight of every source and the composite per algorithm, on one standard for all.
// @Summary Metric scores of a module
// @Tags Forecast
// @Router /api/v1/modules/{id}/metric-scores [get]
func (r *AlgorithmCenterRouter) MetricScores(c *gin.Context) {
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	res, err := r.Service.MetricScores(c.Request.Context(), id, sinceHoursOrFrom(c), timeQuery(c, "to"))
	if err != nil {
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, res)
}

// sinceHoursOrFrom reads the start of a window from since_hours or from.
func sinceHoursOrFrom(c *gin.Context) *time.Time {
	if t := sinceHours(c); t != nil {
		return t
	}
	return timeQuery(c, "from")
}
