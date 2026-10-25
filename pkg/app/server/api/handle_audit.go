package api

import (
	"encoding/csv"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/cylonchau/kube-resource-fcst/pkg/app/middleware"
	"github.com/cylonchau/kube-resource-fcst/pkg/app/query"
	"github.com/cylonchau/kube-resource-fcst/pkg/dao/rdb"
	"github.com/cylonchau/kube-resource-fcst/pkg/model"
	"github.com/gin-gonic/gin"
)

// maxExportRows bounds one export; narrower filters or time ranges export the rest.
const maxExportRows = 50000

// AuditRouter exports the forecasting audit trail as CSV.
type AuditRouter struct {
	Predictions *rdb.PredictionDAO
}

// csvCell neutralizes spreadsheet formula injection: a cell starting with = + - @ or a control character is
// prefixed with a quote, so opening the file in a spreadsheet never executes anything.
func csvCell(s string) string {
	if s != "" && strings.ContainsAny(s[:1], "=+-@\t\r") {
		return "'" + s
	}
	return s
}

func tstr(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// Export serves GET /audit/export?kind=recommendations|executions|evaluations|predictions&module_id=&since_hours=.
func (r *AuditRouter) Export(c *gin.Context) {
	kind := c.Query("kind")
	moduleID, _ := strconv.ParseUint(c.Query("module_id"), 10, 64)
	since := sinceHours(c)
	ctx := c.Request.Context()

	var header []string
	var rows [][]string
	switch kind {
	case "recommendations", "fallbacks":
		var list []*model.Recommendation
		var err error
		if kind == "fallbacks" {
			list, _, err = r.Predictions.SearchFallbacks(ctx, rdb.FallbackFilter{ModuleID: uint(moduleID), Reason: c.Query("reason"), Since: since, Until: timeQuery(c, "to")}, 1, maxExportRows)
		} else {
			f := recommendationFilter(c)
			f.Since = since
			if from := timeQuery(c, "from"); from != nil {
				f.Since = from
			}
			list, _, err = r.Predictions.SearchRecommendations(ctx, f, 1, maxExportRows)
		}
		if err != nil {
			query.InternalError(c, err)
			return
		}
		header = []string{"id", "module_id", "version", "mode", "created_at", "valid_until", "cpu_milli", "cpu_source", "memory_bytes", "memory_source", "replicas", "replicas_source", "degraded", "degrade_reasons", "oom_floor", "detail"}
		for _, x := range list {
			if since != nil && x.CreatedAt.Before(*since) {
				continue
			}
			cpu, mem, rep := "", "", ""
			if x.CPUSet {
				cpu = strconv.FormatInt(x.CPUMilli, 10)
			}
			if x.MemorySet {
				mem = strconv.FormatInt(x.MemoryBytes, 10)
			}
			if x.ReplicasSet {
				rep = strconv.Itoa(x.Replicas)
			}
			rows = append(rows, []string{fmt.Sprint(x.ID), fmt.Sprint(x.ModuleID), fmt.Sprint(x.Version), x.Mode, tstr(x.CreatedAt), tstr(x.ValidUntil),
				cpu, x.CPUSource, mem, x.MemorySource, rep, x.ReplicasSource, fmt.Sprint(x.Degraded), x.DegradeReasons, fmt.Sprint(x.OOMFloor), x.Detail})
		}
	case "executions":
		f := executionFilter(c)
		f.Since = since
		if from := timeQuery(c, "from"); from != nil {
			f.Since = from
		}
		list, _, err := r.Predictions.SearchExecutions(ctx, f, 1, maxExportRows)
		if err != nil {
			query.InternalError(c, err)
			return
		}
		header = []string{"id", "module_id", "created_at", "namespace", "scaler", "target_kind", "target_name", "current", "ready", "desired", "action", "result", "reason", "decision_version"}
		for _, x := range list {
			if since != nil && x.CreatedAt.Before(*since) {
				continue
			}
			rows = append(rows, []string{fmt.Sprint(x.ID), fmt.Sprint(x.ModuleID), tstr(x.CreatedAt), x.Namespace, x.Scaler, x.TargetKind, x.TargetName,
				fmt.Sprint(x.Current), fmt.Sprint(x.Ready), fmt.Sprint(x.Desired), x.Action, x.Result, x.Reason, fmt.Sprint(x.DecisionVersion)})
		}
	case "evaluations":
		list, _, err := r.Predictions.ListEvaluations(ctx, rdb.EvaluationFilter{ModuleID: uintQuery(c, "module_id"), Metric: c.Query("metric"), AlgorithmID: c.Query("algorithm_id"), Since: since}, 1, maxExportRows)
		if err != nil {
			query.InternalError(c, err)
			return
		}
		header = []string{"id", "module_id", "metric", "algorithm_id", "model_version", "prediction_time", "points", "expected", "mae", "rmse", "wape", "bias", "peak_under", "coverage"}
		for _, x := range list {
			rows = append(rows, []string{fmt.Sprint(x.ID), fmt.Sprint(x.ModuleID), x.Metric, x.AlgorithmID, x.ModelVersion, tstr(x.PredictionTime),
				fmt.Sprint(x.Points), fmt.Sprint(x.Expected), fmt.Sprint(x.MAE), fmt.Sprint(x.RMSE), fmt.Sprint(x.WAPE), fmt.Sprint(x.Bias), fmt.Sprint(x.PeakUnder), fmt.Sprint(x.Coverage)})
		}
	case "predictions":
		list, _, err := r.Predictions.ListPredictions(ctx, rdb.PredictionFilter{ModuleID: uintQuery(c, "module_id"), Metric: c.Query("metric"), AlgorithmID: c.Query("algorithm_id"), From: timeQuery(c, "from"), To: timeQuery(c, "to")}, 1, maxExportRows)
		if err != nil {
			query.InternalError(c, err)
			return
		}
		header = []string{"id", "module_id", "metric", "algorithm_id", "model_version", "prediction_time", "target_time", "quantile_permille", "value", "unit"}
		for _, x := range list {
			rows = append(rows, []string{fmt.Sprint(x.ID), fmt.Sprint(x.ModuleID), x.Metric, x.AlgorithmID, x.ModelVersion, tstr(x.PredictionTime), tstr(x.TargetTime),
				fmt.Sprint(x.QuantilePermille), strconv.FormatFloat(x.Value, 'g', -1, 64), x.Unit})
		}
	default:
		query.BadRequest(c, fmt.Errorf("kind must be one of recommendations, fallbacks, executions, evaluations, predictions"))
		return
	}

	// the export is a read that hands out records in bulk: it is entered in the operation log with its scope
	c.Set(middleware.OperationLogNoteKey, fmt.Sprintf("export kind=%s module_id=%d rows=%d", kind, moduleID, len(rows)))
	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="kube-resource-fcst-%s-%s.csv"`, kind, time.Now().UTC().Format("20060102T150405Z")))
	c.Status(http.StatusOK)
	w := csv.NewWriter(c.Writer)
	_ = w.Write(header)
	for _, row := range rows {
		for i := range row {
			row[i] = csvCell(row[i])
		}
		_ = w.Write(row)
	}
	w.Flush()
}
