package api

import (
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/cylonchau/kube-res-foresight/pkg/app/query"
	"github.com/cylonchau/kube-res-foresight/pkg/dao/rdb"
	"github.com/cylonchau/kube-res-foresight/pkg/scoring"
)

// ScoreRow is the newest grade of one algorithm of one module on one resource: the list of the algorithm scores has one such row per
// module, algorithm and resource, never a row per evaluation. The earlier evaluations are read for the algorithm and module (the
// history in its detail).
type ScoreRow struct {
	// Key is unique within the list.
	Key         string `json:"key"`
	ScoreID     uint   `json:"score_id"`
	ModuleID    uint   `json:"module_id"`
	ModuleName  string `json:"module_name"`
	Algorithm   string `json:"algorithm"`
	AlgorithmID string `json:"algorithm_id"`
	// Metric is the resource graded: cpu, memory, gpu, or overall (the combination the choice of the algorithm in use rests on).
	Metric   string  `json:"metric"`
	Score    float64 `json:"score"`
	Accuracy float64 `json:"accuracy"`
	// Rank is the place among the algorithms of the module on the same resource; Ranked how many were graded on it.
	Rank    int       `json:"rank"`
	Ranked  int       `json:"ranked"`
	Windows int       `json:"windows"`
	At      time.Time `json:"at"`
	// Selected is true for the algorithm the module uses now.
	Selected bool `json:"selected"`
	// Result is what the grade was computed from; Breakdown the parts of the combination.
	Result    *scoring.Result `json:"result,omitempty"`
	Breakdown []BreakdownRow  `json:"breakdown"`
}

// LatestScores lists the newest score of every algorithm of every module (or one), one row per resource.
// @Summary Newest algorithm scores
// @Tags Forecast
// @Router /api/v1/algorithm-scores/latest [get]
func (r *AlgorithmCenterRouter) LatestScores(c *gin.Context) {
	ctx := c.Request.Context()
	page, size := pageParams(c)
	wantModule := uintQuery(c, "module_id")
	wantMetric := strings.TrimSpace(c.Query("metric"))
	wantAlgorithm := strings.TrimSpace(c.Query("algorithm"))

	modules, _, err := r.Modules.List(ctx, rdb.ModuleFilter{}, 1, 1000)
	if err != nil {
		query.InternalError(c, err)
		return
	}
	var rows []ScoreRow
	for _, m := range modules {
		if wantModule != 0 && m.ID != wantModule {
			continue
		}
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
		byMetric := map[string][]ScoreRow{}
		for algorithm, sc := range scores {
			var res scoring.Result
			if sc.Detail == "" || json.Unmarshal([]byte(sc.Detail), &res) != nil {
				continue
			}
			base := ScoreRow{ScoreID: sc.ID, ModuleID: m.ID, ModuleName: m.Name, Algorithm: algorithm, AlgorithmID: sc.AlgorithmID,
				Windows: sc.Windows, At: sc.CreatedAt, Selected: algorithm == state.Selected, Result: &res}
			parts := breakdownOf(res, scoring.DefaultWeights())
			base.Breakdown = parts
			// the combination, for the algorithm in use: weights, OOM and the holes of the data are all in it
			overall := base
			overall.Metric, overall.Score = "overall", sc.Score
			overall.Accuracy = overallAccuracy(parts)
			byMetric["overall"] = append(byMetric["overall"], overall)
			for _, part := range parts {
				if part.Metric == "replicas" {
					continue // a module with no resource source: the replica count is all there is, and it is the overall grade
				}
				row := base
				row.Metric, row.Score, row.Accuracy = part.Metric, part.Score, part.Accuracy
				byMetric[part.Metric] = append(byMetric[part.Metric], row)
			}
		}
		for metric, list := range byMetric {
			sort.SliceStable(list, func(i, j int) bool {
				if list[i].Score != list[j].Score {
					return list[i].Score > list[j].Score
				}
				return list[i].Algorithm < list[j].Algorithm
			})
			for i := range list {
				list[i].Rank, list[i].Ranked = i+1, len(list)
				list[i].Key = m.Name + "/" + list[i].Algorithm + "/" + metric
				if wantMetric != "" && wantMetric != metric {
					continue
				}
				// without a chosen resource the list has the resources, not the combination twice over
				if wantMetric == "" && metric == "overall" {
					continue
				}
				if wantAlgorithm != "" && list[i].Algorithm != wantAlgorithm {
					continue
				}
				rows = append(rows, list[i])
			}
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if a.ModuleName != b.ModuleName {
			return a.ModuleName < b.ModuleName
		}
		if a.Metric != b.Metric {
			return a.Metric < b.Metric
		}
		return a.Rank < b.Rank
	})
	total := len(rows)
	from, to := (page-1)*size, page*size
	if from > total {
		from = total
	}
	if to > total {
		to = total
	}
	query.SuccessResponse(c, nil, gin.H{"list": rows[from:to], "total": total, "page": page, "pageSize": size})
}

// overallAccuracy is the accuracy of the combination: the parts weighted by their share.
func overallAccuracy(parts []BreakdownRow) float64 {
	var sum, weight float64
	for _, p := range parts {
		sum += p.Weight * p.Accuracy
		weight += p.Weight
	}
	if weight == 0 {
		return 0
	}
	return sum / weight
}
