package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/cylonchau/kube-resource-fcst/pkg/collector"
	"github.com/cylonchau/kube-resource-fcst/pkg/dao/rdb"
	"github.com/cylonchau/kube-resource-fcst/pkg/forecast"
	"github.com/cylonchau/kube-resource-fcst/pkg/forecastsvc"
	"github.com/cylonchau/kube-resource-fcst/pkg/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type flatQuerier struct{}

func (flatQuerier) QueryRange(_ context.Context, _ string, start, end time.Time, step time.Duration) ([]collector.Point, error) {
	var out []collector.Point
	for ts := start; !ts.After(end); ts = ts.Add(step) {
		out = append(out, collector.Point{Time: ts, Value: 2})
	}
	return out, nil
}

func forecastTestRouter(t *testing.T) (*gin.Engine, *model.MetricMapping) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db := apiTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.Datasource{}, &model.MetricMapping{}, &model.ForecastModule{}, &model.AlgorithmConfig{},
		&model.ModulePeriod{}, &model.MetricSeries{}, &model.MetricSample{}, &model.Prediction{}, &model.BacktestRun{}, &model.Recommendation{}, &model.PredictionEvaluation{}, &model.ScalingExecution{}, &model.MetricSource{}, &model.AlgorithmScore{}, &model.AdmissionDecision{}, &model.TrainingRun{}, &model.ScaleOutcome{}, &model.ModuleRunState{}, &model.SysConfig{}, &model.TaskDefinition{}, &model.Job{}))
	previous := model.DB
	model.DB = db
	t.Cleanup(func() { model.DB = previous })

	ds := &model.Datasource{Name: "prom", Type: "prometheus", BaseURL: "http://prom:9090", AuthType: "none", TimeoutSeconds: 30, MaxConcurrency: 1, Enabled: true, Status: "unknown"}
	require.NoError(t, db.Create(ds).Error)
	cpu := &model.MetricMapping{DatasourceID: ds.ID, Name: "cpu", Metric: "cpu", Level: "workload", Unit: "cores", StepSeconds: 60, Enabled: true,
		Query: `sum(rate(cpu{namespace="{{namespace}}",pod=~"{{workload}}-.*"}[{{range}}]))`}
	require.NoError(t, db.Create(cpu).Error)

	modules, algorithms, mappings, preds := rdb.NewModuleDAO(db), rdb.NewAlgorithmDAO(db), rdb.NewMetricMappingDAO(db), rdb.NewPredictionDAO(db)
	svc := forecastsvc.New(modules, mappings, rdb.NewDatasourceDAO(db), rdb.NewSampleDAO(db), preds)
	svc.NewQuerier = func(*model.Datasource) (collector.RangeQuerier, error) { return flatQuerier{}, nil }
	// every test starts with no algorithm enabled: the one it needs is turned on through the API
	require.NoError(t, svc.EnsureAlgorithms(context.Background()))
	require.NoError(t, db.Model(&model.AlgorithmConfig{}).Where("1 = 1").Update("enabled", false).Error)
	h := &ForecastRouter{Modules: modules, Configs: algorithms, Mappings: mappings, Predictions: preds, Service: svc, Config: rdb.NewSysConfigDAO(db)}
	center := &AlgorithmCenterRouter{Modules: modules, Algorithms: algorithms, Predictions: preds, Tasks: rdb.NewTaskDefinitionDAO(db), Jobs: rdb.NewJobDAO(db), RunStates: rdb.NewRunStateDAO(db), Service: svc}
	rt := &RuntimeRouter{Modules: modules, Predictions: preds, RunStates: rdb.NewRunStateDAO(db), Config: rdb.NewSysConfigDAO(db), Now: func() time.Time { return runtimeNow }}

	r := gin.New()
	r.GET("/algorithms", h.Algorithms)
	r.GET("/algorithms/:id", h.GetAlgorithm)
	r.PUT("/algorithms/:id", h.UpdateAlgorithm)
	r.GET("/training-runs", h.ListTrainings)
	r.GET("/training-runs/summary", h.TrainingSummary)
	r.GET("/training-runs/:id/data", h.TrainingData)
	r.GET("/training-runs/:id", h.GetTraining)
	r.POST("/algorithms", h.CreateAlgorithm)
	r.DELETE("/algorithms/:id", h.DeleteAlgorithm)
	r.POST("/algorithm-formulas/check", h.FormulaCheck)
	r.GET("/algorithm-formulas/functions", h.FormulaFunctions)
	r.GET("/algorithms/:id/schema", h.AlgorithmSchema)
	r.GET("/algorithms/:id/usage", center.Usage)
	r.GET("/algorithm-scores", center.ListScores)
	r.GET("/algorithm-scores/latest", center.LatestScores)
	r.GET("/algorithm-rankings/current", center.CurrentRankings)
	r.GET("/algorithm-rankings/history", center.HistoryRankings)
	r.GET("/modules/:id/metric-scores", center.MetricScores)
	r.GET("/admissions", center.ListAdmissions)
	r.GET("/modules", h.ListModules)
	r.POST("/modules", h.CreateModule)
	r.GET("/modules/:id", h.GetModule)
	r.PUT("/modules/:id", h.UpdateModule)
	r.DELETE("/modules/:id", h.DeleteModule)
	r.GET("/modules/:id/sources", h.ListSources)
	r.GET("/modules/:id/requirements", h.ModuleRequirements)
	r.GET("/dataset/catalog", h.DatasetCatalog)
	r.POST("/modules/:id/sources", h.CreateSource)
	r.PUT("/modules/:id/sources/:sid", h.UpdateSource)
	r.DELETE("/modules/:id/sources/:sid", h.DeleteSource)
	r.GET("/modules/:id/history", h.ModuleHistory)
	r.GET("/modules/:id/compare", h.ModuleCompare)
	r.POST("/modules/:id/collect", h.ModuleCollect)
	r.POST("/modules/:id/predict", h.ModulePredict)
	r.POST("/modules/:id/run", h.ModuleRun)
	r.POST("/modules/:id/backtest", h.ModuleBacktest)
	r.GET("/modules/:id/backtests", h.ModuleBacktests)
	r.POST("/modules/:id/evaluate", h.ModuleEvaluate)
	r.GET("/modules/:id/trace", center.Trace)
	r.GET("/modules/:id/overrides", h.ListPeriods)
	r.POST("/modules/:id/overrides", h.CreatePeriod)
	r.PUT("/modules/:id/overrides/:pid", h.UpdatePeriod)
	r.DELETE("/modules/:id/overrides/:pid", h.DeletePeriod)
	r.POST("/modules/:id/recommend", h.Recommend)
	r.POST("/modules/:id/score", center.RunScore)
	r.GET("/modules/:id/ranking", center.Ranking)
	r.GET("/modules/:id/leaderboard", h.Leaderboard)
	r.GET("/recommendations", h.ListRecommendations)
	r.GET("/snapshots", h.ListSnapshots)
	r.GET("/snapshots/:id", h.GetSnapshot)
	r.GET("/fallbacks", h.ListFallbacks)
	r.GET("/backtests", h.SearchBacktests)
	r.GET("/predictions", h.ListPredictions)
	r.GET("/evaluations", h.ListEvaluations)
	r.GET("/forecast-metrics/catalog", h.MetricCatalog)
	r.GET("/forecast-metrics/query", h.QueryMetrics)
	r.POST("/metric-queries/test", h.TestMetricQuery)
	r.POST("/records/batch-delete", h.BatchDeleteRecords)
	r.POST("/algorithms/batch", h.BatchAlgorithms)
	r.POST("/modules/batch", h.BatchModules)
	r.GET("/operator/decisions", rt.Decision)
	r.POST("/operator/reports", rt.Report)
	r.GET("/admission/recommendations", rt.Admission)
	r.GET("/scaling-decisions", rt.ListExecutions)
	r.POST("/controls/:id/bypass", h.Bypass)
	r.POST("/controls/:id/resume", h.Resume)
	return r, cpu
}

// createModule creates a module (a Kubernetes workload and its policy) and returns its id.
func createModule(t *testing.T, r *gin.Engine, body map[string]any) int {
	t.Helper()
	rec := performJSON(t, r, http.MethodPost, "/modules", "", body)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	return int(decodeData(t, rec.Body.Bytes())["id"].(float64))
}

// moduleWithCPU creates a module that reads the cpu mapping: the metric sources belong to the module.
func moduleWithCPU(t *testing.T, r *gin.Engine, cpu *model.MetricMapping, body map[string]any) int {
	t.Helper()
	req := map[string]any{"namespace": "prod", "workload": "api"}
	for k, v := range body {
		req[k] = v
	}
	if _, has := req["name"]; !has {
		req["name"] = req["workload"]
	}
	req["sources"] = []map[string]any{{"name": "cpu", "kind": "cpu", "mapping_id": cpu.ID}}
	return createModule(t, r, req)
}

// enableAlgorithm turns an algorithm on for every module through its platform-wide setting.
func enableAlgorithm(t *testing.T, r *gin.Engine, id string, params map[string]any) {
	t.Helper()
	body := map[string]any{"enabled": true}
	if params != nil {
		body["params"] = params
	}
	rec := performJSON(t, r, http.MethodPut, "/algorithms/"+id, "", body)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

// decodeList reads the list of a paged response.
func decodeList(t *testing.T, body []byte) []any {
	t.Helper()
	list, _ := decodeData(t, body)["list"].([]any)
	return list
}

func TestAlgorithmListIsPagedSearchedAndFiltered(t *testing.T) {
	r, _ := forecastTestRouter(t)
	rec := performJSON(t, r, http.MethodGet, "/algorithms?page=1&page_size=5", "", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	data := decodeData(t, rec.Body.Bytes())
	assert.Len(t, data["list"], 5)
	assert.Greater(t, data["total"].(float64), float64(5))
	first := data["list"].([]any)[0].(map[string]any)
	for _, key := range []string{"id", "category", "params", "enabled", "values", "weight"} {
		assert.Contains(t, first, key)
	}
	rec = performJSON(t, r, http.MethodGet, "/algorithms?keyword=seasonal", "", nil)
	assert.Contains(t, rec.Body.String(), `"season_length_steps"`, "the parameter schema comes with every algorithm")

	list := func(query string) []any {
		rec := performJSON(t, r, http.MethodGet, "/algorithms?"+query, "", nil)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		return decodeList(t, rec.Body.Bytes())
	}
	assert.Len(t, list("keyword=holt"), 1, "holt_winters")
	assert.Empty(t, list("keyword=prophet"))
	for _, a := range list("category=baseline&page_size=50") {
		assert.Equal(t, "baseline", a.(map[string]any)["category"])
	}
	enableAlgorithm(t, r, "ewma", nil)
	enabled := list("enabled=true&page_size=50")
	require.Len(t, enabled, 1)
	assert.Equal(t, "ewma", enabled[0].(map[string]any)["id"])
	assert.Greater(t, len(list("enabled=false&page_size=50")), 5)
}

func TestAlgorithmSettingIsValidatedAndRoundTrips(t *testing.T) {
	r, _ := forecastTestRouter(t)
	rec := performJSON(t, r, http.MethodGet, "/algorithms/ewma", "", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, 0.3, decodeData(t, rec.Body.Bytes())["values"].(map[string]any)["alpha"], "the defaults are shown as the current values")

	for name, body := range map[string]map[string]any{
		"unknown param": {"params": map[string]any{"beta": 0.1}},
		"out of range":  {"params": map[string]any{"alpha": 7}},
		"bad weight":    {"weight": -1},
		"bad budget":    {"train_budget_seconds": 99999},
		"bad promotion": {"promote_min_improvement": 2},
	} {
		assert.Equal(t, http.StatusBadRequest, performJSON(t, r, http.MethodPut, "/algorithms/ewma", "", body).Code, name)
	}
	assert.Equal(t, http.StatusBadRequest, performJSON(t, r, http.MethodPut, "/algorithms/ridge_ar", "", map[string]any{"params": map[string]any{"lags": 0}}).Code)
	assert.Equal(t, http.StatusNotFound, performJSON(t, r, http.MethodPut, "/algorithms/prophet", "", map[string]any{"enabled": true}).Code)
	assert.Equal(t, http.StatusNotFound, performJSON(t, r, http.MethodGet, "/algorithms/prophet", "", nil).Code)

	rec = performJSON(t, r, http.MethodPut, "/algorithms/ewma", "", map[string]any{"enabled": true, "params": map[string]any{"alpha": 0.7}, "weight": 1.5, "train_budget_seconds": 30})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	got := decodeData(t, rec.Body.Bytes())
	assert.Equal(t, true, got["enabled"])
	assert.Equal(t, 0.7, got["values"].(map[string]any)["alpha"])
	assert.EqualValues(t, 1.5, got["weight"])
	rec = performJSON(t, r, http.MethodGet, "/algorithms/ewma", "", nil)
	assert.Equal(t, 0.7, decodeData(t, rec.Body.Bytes())["values"].(map[string]any)["alpha"], "the setting is stored")
	rec = performJSON(t, r, http.MethodGet, "/algorithms/ewma/schema", "", nil)
	assert.Equal(t, "ewma", decodeData(t, rec.Body.Bytes())["id"])
	assert.Equal(t, http.StatusNotFound, performJSON(t, r, http.MethodGet, "/algorithms/prophet/schema", "", nil).Code)
}

func TestModuleIsAWorkloadWithItsOwnMetricsAndPolicy(t *testing.T) {
	r, cpu := forecastTestRouter(t)
	base := map[string]any{"name": "api", "namespace": "prod", "workload": "api"}
	id := createModule(t, r, base)

	rec := performJSON(t, r, http.MethodGet, fmt.Sprintf("/modules/%d", id), "", nil)
	got := decodeData(t, rec.Body.Bytes())
	assert.Equal(t, "Deployment", got["workload_kind"], "the kind defaults to Deployment")
	assert.EqualValues(t, 720, got["lookback_hours"], "the history window and the forecast period belong to the module")
	assert.EqualValues(t, 15, got["horizon_minutes"])
	assert.Empty(t, got["sources"])
	for _, gone := range []string{"cpu_mapping_id", "combine", "min_history_hours", "score_windows", "switch_margin", "promote_min_points"} {
		assert.NotContains(t, got, gone, "%s is not a setting of a module", gone)
	}

	assert.Equal(t, http.StatusBadRequest, performJSON(t, r, http.MethodPost, "/modules", "", base).Code, "duplicate name")
	other := map[string]any{"name": "api-copy", "namespace": "prod", "workload": "api"}
	assert.Equal(t, http.StatusBadRequest, performJSON(t, r, http.MethodPost, "/modules", "", other).Code, "one module per workload")
	other["workload_kind"] = "StatefulSet"
	assert.Equal(t, http.StatusOK, performJSON(t, r, http.MethodPost, "/modules", "", other).Code, "the same name of another kind is another workload")
	other["name"], other["workload_kind"], other["workload"] = "agent", "DaemonSet", "agent"
	assert.Equal(t, http.StatusOK, performJSON(t, r, http.MethodPost, "/modules", "", other).Code)

	for name, patch := range map[string]map[string]any{
		"admission not open": {"name": "m1", "mode": "admission"},
		"auto not open":      {"name": "m2", "mode": "auto"},
		"unknown mode":       {"name": "m3", "mode": "turbo"},
		"unknown kind":       {"name": "m4", "workload_kind": "CronJob"},
		"bad timezone":       {"name": "m5", "timezone": "Mars/Olympus"},
		"bad bounds":         {"name": "m6", "min_replicas": 5, "max_replicas": 2},
		"headroom below one": {"name": "m7", "headroom_ratio": 0.5},
		"bad horizon":        {"name": "m8", "horizon_minutes": 5000},
		"bad lookback":       {"name": "m9", "lookback_hours": 999999},
		"ghost mapping":      {"name": "m10", "sources": []map[string]any{{"name": "cpu", "kind": "cpu", "mapping_id": 9999}}},
		"memory on cpu":      {"name": "m11", "sources": []map[string]any{{"name": "m", "kind": "memory", "mapping_id": cpu.ID}}},
	} {
		body := map[string]any{"namespace": "prod", "workload": "w-" + name}
		for k, v := range patch {
			body[k] = v
		}
		rec := performJSON(t, r, http.MethodPost, "/modules", "", body)
		assert.Equal(t, http.StatusBadRequest, rec.Code, "%s: %s", name, rec.Body.String())
	}

	rec = performJSON(t, r, http.MethodPut, fmt.Sprintf("/modules/%d", id), "", map[string]any{"mode": "recommend", "headroom_ratio": 1.2, "description": "d", "horizon_minutes": 30})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	data := decodeData(t, rec.Body.Bytes())
	assert.Equal(t, "recommend", data["mode"])
	assert.Equal(t, 1.2, data["headroom_ratio"])
	assert.EqualValues(t, 30, data["horizon_minutes"])
	assert.Equal(t, "prod", data["namespace"], "untouched fields keep their value")

	rec = performJSON(t, r, http.MethodGet, "/modules?namespace=prod&mode=recommend&keyword=api", "", nil)
	assert.EqualValues(t, 1, decodeData(t, rec.Body.Bytes())["total"])

	assert.Equal(t, http.StatusBadRequest, performJSON(t, r, http.MethodDelete, fmt.Sprintf("/modules/%d", id), "", nil).Code, "an enabled module is not deleted")
	require.Equal(t, http.StatusOK, performJSON(t, r, http.MethodPut, fmt.Sprintf("/modules/%d", id), "", map[string]any{"enabled": false}).Code)
	assert.Equal(t, http.StatusOK, performJSON(t, r, http.MethodDelete, fmt.Sprintf("/modules/%d", id), "", nil).Code)
	assert.Equal(t, http.StatusNotFound, performJSON(t, r, http.MethodGet, fmt.Sprintf("/modules/%d", id), "", nil).Code)
}

func TestSourcesBelongToTheModule(t *testing.T) {
	r, cpu := forecastTestRouter(t)
	id := moduleWithCPU(t, r, cpu, map[string]any{"name": "src"})
	path := fmt.Sprintf("/modules/%d/sources", id)

	// a memory source cannot take a cpu mapping, and a mapping must exist
	rec := performJSON(t, r, http.MethodPost, path, "", map[string]any{"name": "mem", "kind": "memory", "mapping_id": cpu.ID})
	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	rec = performJSON(t, r, http.MethodPost, path, "", map[string]any{"name": "ghost", "kind": "cpu", "mapping_id": 9999})
	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Equal(t, http.StatusBadRequest, performJSON(t, r, http.MethodPost, path, "", map[string]any{"name": "cpu", "kind": "cpu", "mapping_id": cpu.ID}).Code, "the name is unique in the module")

	// a second source of the same kind gets its own key inside the module, with its own threshold
	rec = performJSON(t, r, http.MethodPost, path, "", map[string]any{"name": "cpu-2", "kind": "cpu", "mapping_id": cpu.ID, "threshold": 3})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	created := decodeData(t, rec.Body.Bytes())
	sid := int(created["id"].(float64))
	assert.Equal(t, "cpu_2", created["key"])
	assert.EqualValues(t, 3, created["threshold"])
	rec = performJSON(t, r, http.MethodGet, path, "", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"cpu_2"`)

	rec = performJSON(t, r, http.MethodPut, fmt.Sprintf("%s/%d", path, sid), "", map[string]any{"weight": 2, "enabled": false})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.EqualValues(t, 2, decodeData(t, rec.Body.Bytes())["weight"])
	assert.Equal(t, http.StatusOK, performJSON(t, r, http.MethodDelete, fmt.Sprintf("%s/%d", path, sid), "", nil).Code)
	assert.Equal(t, http.StatusNotFound, performJSON(t, r, http.MethodDelete, fmt.Sprintf("%s/%d", path, sid), "", nil).Code)

	// another module has sources of its own: the same source name is no conflict
	other := moduleWithCPU(t, r, cpu, map[string]any{"name": "src-2", "workload": "other"})
	assert.Equal(t, http.StatusOK, performJSON(t, r, http.MethodGet, fmt.Sprintf("/modules/%d/sources", other), "", nil).Code)
}

func TestFixedPeriodsAreValidated(t *testing.T) {
	r, _ := forecastTestRouter(t)
	id := createModule(t, r, map[string]any{"name": "api", "namespace": "prod", "workload": "api"})
	path := fmt.Sprintf("/modules/%d/overrides", id)
	good := map[string]any{"name": "sale", "weekdays": 62, "start_minute": 540, "end_minute": 720, "replicas": 8}
	rec := performJSON(t, r, http.MethodPost, path, "", good)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	pid := int(decodeData(t, rec.Body.Bytes())["id"].(float64))

	for name, body := range map[string]map[string]any{
		"duplicate name":   good,
		"no values":        {"name": "x", "weekdays": 1, "start_minute": 0, "end_minute": 60},
		"no weekdays":      {"name": "x", "start_minute": 0, "end_minute": 60, "replicas": 1},
		"weekday overflow": {"name": "x", "weekdays": 200, "start_minute": 0, "end_minute": 60, "replicas": 1},
		"empty window":     {"name": "x", "weekdays": 1, "start_minute": 60, "end_minute": 60, "replicas": 1},
		"half absolute":    {"name": "x", "abs_start": "2026-03-01T00:00:00Z", "replicas": 1},
	} {
		assert.Equal(t, http.StatusBadRequest, performJSON(t, r, http.MethodPost, path, "", body).Code, name)
	}
	abs := map[string]any{"name": "launch", "abs_start": "2026-03-01T00:00:00Z", "abs_end": "2026-03-02T00:00:00Z", "cpu_milli": 2000}
	assert.Equal(t, http.StatusOK, performJSON(t, r, http.MethodPost, path, "", abs).Code)

	good["replicas"] = 12
	rec = performJSON(t, r, http.MethodPut, fmt.Sprintf("%s/%d", path, pid), "", good)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.EqualValues(t, 12, decodeData(t, rec.Body.Bytes())["replicas"])
	assert.Equal(t, http.StatusOK, performJSON(t, r, http.MethodDelete, fmt.Sprintf("%s/%d", path, pid), "", nil).Code)
}

func TestPipelineFromTheModuleThroughTheEnabledAlgorithmsToTheRecommendation(t *testing.T) {
	r, cpu := forecastTestRouter(t)
	module := moduleWithCPU(t, r, cpu, map[string]any{"name": "api", "mode": "recommend", "lookback_hours": 6, "horizon_minutes": 10,
		"ttl_seconds": 3600, "headroom_ratio": 1.5, "fallback_cpu_milli": 400})
	base := fmt.Sprintf("/modules/%d", module)

	// no algorithm is enabled yet: nothing to forecast with, the module says so and falls back
	rec := performJSON(t, r, http.MethodPost, base+"/predict", "", nil)
	assert.Equal(t, http.StatusBadRequest, rec.Code, "no algorithm is enabled")
	rec = performJSON(t, r, http.MethodPost, base+"/recommend", "", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "fallback", decodeData(t, rec.Body.Bytes())["recommendation"].(map[string]any)["cpu_source"])

	// enabling an algorithm is all it takes: the module is associated with it automatically
	enableAlgorithm(t, r, "naive", nil)
	rec = performJSON(t, r, http.MethodPost, base+"/run", "", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rec = performJSON(t, r, http.MethodGet, base+"/history?metric=cpu", "", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	history := decodeData(t, rec.Body.Bytes())
	assert.NotEmpty(t, history["points"])
	assert.Equal(t, "cores", history["unit"])
	assert.Equal(t, http.StatusBadRequest, performJSON(t, r, http.MethodGet, base+"/history?metric=memory", "", nil).Code)
	rec = performJSON(t, r, http.MethodGet, fmt.Sprintf("/predictions?module_id=%d&metric=cpu&algorithm_id=naive", module), "", nil)
	assert.EqualValues(t, 10, decodeData(t, rec.Body.Bytes())["total"])

	rec = performJSON(t, r, http.MethodPost, base+"/backtest", "", map[string]any{"metric": "cpu", "cutoffs": 3})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rec = performJSON(t, r, http.MethodGet, base+"/backtests", "", nil)
	assert.EqualValues(t, 1, decodeData(t, rec.Body.Bytes())["total"])
	rec = performJSON(t, r, http.MethodGet, fmt.Sprintf("/backtests?module_id=%d", module), "", nil)
	assert.EqualValues(t, 1, decodeData(t, rec.Body.Bytes())["total"])

	rec = performJSON(t, r, http.MethodPost, base+"/recommend", "", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	recommendation := decodeData(t, rec.Body.Bytes())["recommendation"].(map[string]any)
	assert.EqualValues(t, 3000, recommendation["cpu_milli"], "peak 2 cores x headroom 1.5")
	assert.Equal(t, "algorithm", recommendation["cpu_source"])
	assert.Equal(t, "naive", recommendation["algorithm"])

	// another algorithm turned on later reaches the same module, and is ranked against the first
	enableAlgorithm(t, r, "ewma", map[string]any{"alpha": 0.5})
	require.Equal(t, http.StatusOK, performJSON(t, r, http.MethodPost, base+"/predict", "", nil).Code)
	rec = performJSON(t, r, http.MethodGet, fmt.Sprintf("/predictions?module_id=%d&metric=cpu&algorithm_id=ewma", module), "", nil)
	assert.EqualValues(t, 10, decodeData(t, rec.Body.Bytes())["total"])

	// bypass: the original is kept, resume restores model-driven values
	assert.Equal(t, http.StatusBadRequest, performJSON(t, r, http.MethodPost, fmt.Sprintf("/controls/%d/bypass", module), "", map[string]any{"reason": "  "}).Code)
	require.Equal(t, http.StatusOK, performJSON(t, r, http.MethodPost, fmt.Sprintf("/controls/%d/bypass", module), "", map[string]any{"reason": "incident"}).Code)
	rec = performJSON(t, r, http.MethodPost, base+"/recommend", "", nil)
	recommendation = decodeData(t, rec.Body.Bytes())["recommendation"].(map[string]any)
	assert.Equal(t, "bypass", recommendation["cpu_source"])
	assert.Equal(t, false, recommendation["cpu_set"])
	require.Equal(t, http.StatusOK, performJSON(t, r, http.MethodPost, fmt.Sprintf("/controls/%d/resume", module), "", nil).Code)
	rec = performJSON(t, r, http.MethodPost, base+"/recommend", "", nil)
	assert.Equal(t, "algorithm", decodeData(t, rec.Body.Bytes())["recommendation"].(map[string]any)["cpu_source"])

	// observe mode produces no recommendation
	require.Equal(t, http.StatusOK, performJSON(t, r, http.MethodPut, base, "", map[string]any{"mode": "observe"}).Code)
	assert.Equal(t, http.StatusConflict, performJSON(t, r, http.MethodPost, base+"/recommend", "", nil).Code)
}

func TestEvaluationEndpointsOnAFreshModule(t *testing.T) {
	r, cpu := forecastTestRouter(t)
	module := moduleWithCPU(t, r, cpu, map[string]any{"name": "fresh"})
	enableAlgorithm(t, r, "naive", nil)

	rec := performJSON(t, r, http.MethodPost, fmt.Sprintf("/modules/%d/evaluate", module), "", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rec = performJSON(t, r, http.MethodGet, fmt.Sprintf("/evaluations?module_id=%d&since_hours=24", module), "", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.EqualValues(t, 0, decodeData(t, rec.Body.Bytes())["total"])
	rec = performJSON(t, r, http.MethodGet, fmt.Sprintf("/modules/%d/leaderboard?kind=cpu", module), "", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, http.StatusBadRequest, performJSON(t, r, http.MethodGet, fmt.Sprintf("/modules/%d/leaderboard?kind=nope", module), "", nil).Code)
}

func TestModuleReplicaSolverSettingsAreValidatedAndRoundTrip(t *testing.T) {
	r, _ := forecastTestRouter(t)
	base := map[string]any{"name": "dyn", "namespace": "prod", "workload": "api"}
	with := func(extra map[string]any) map[string]any {
		out := map[string]any{}
		for k, v := range base {
			out[k] = v
		}
		for k, v := range extra {
			out[k] = v
		}
		return out
	}
	assert.Equal(t, http.StatusBadRequest, performJSON(t, r, http.MethodPost, "/modules", "", with(map[string]any{"replica_mode": "magic"})).Code)
	assert.Equal(t, http.StatusBadRequest, performJSON(t, r, http.MethodPost, "/modules", "", with(map[string]any{"target_utilization": 1.5})).Code)
	// estimating the capacity needs the replicas and rps sources, which are added after the module; the
	// recommendation explains what is missing, so the module itself is accepted
	rec := performJSON(t, r, http.MethodPost, "/modules", "", with(map[string]any{"name": "est", "workload": "est", "capacity_mode": "estimate", "pod_cpu_request_milli": 500}))
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	id := createModule(t, r, with(map[string]any{"replica_mode": "dynamic", "pod_cpu_request_milli": 500, "target_utilization": 0.7, "pod_capacity_rps": 120}))
	rec = performJSON(t, r, http.MethodGet, fmt.Sprintf("/modules/%d", id), "", nil)
	got := decodeData(t, rec.Body.Bytes())
	assert.Equal(t, "dynamic", got["replica_mode"])
	assert.EqualValues(t, 500, got["pod_cpu_request_milli"])
	assert.Equal(t, "manual", got["capacity_mode"])

	rec = performJSON(t, r, http.MethodPut, fmt.Sprintf("/modules/%d", id), "", map[string]any{"replica_mode": "fixed"})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "fixed", decodeData(t, rec.Body.Bytes())["replica_mode"])
}

func TestForecastMetricCatalogAndQueryEndpoints(t *testing.T) {
	r, cpu := forecastTestRouter(t)
	module := moduleWithCPU(t, r, cpu, map[string]any{"name": "metrics"})

	rec := performJSON(t, r, http.MethodGet, "/forecast-metrics/catalog", "", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"evaluation_wape"`)
	assert.Contains(t, rec.Body.String(), `"retention_days"`)
	assert.NotContains(t, rec.Body.String(), `"scope"`, "every metric belongs to a module")

	url := fmt.Sprintf("/forecast-metrics/query?name=usage_cpu_cores&module_id=%d", module)
	rec = performJSON(t, r, http.MethodGet, url, "", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "cores", decodeData(t, rec.Body.Bytes())["unit"], "nothing collected yet is an empty answer, not an error")
	rec = performJSON(t, r, http.MethodGet, fmt.Sprintf("/forecast-metrics/query?name=recommendation_replicas&module_id=%d", module), "", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	assert.Equal(t, http.StatusBadRequest, performJSON(t, r, http.MethodGet, "/forecast-metrics/query?name=usage_cpu_cores", "", nil).Code, "module_id is required")
	assert.Equal(t, http.StatusBadRequest, performJSON(t, r, http.MethodGet, fmt.Sprintf("/forecast-metrics/query?name=nope&module_id=%d", module), "", nil).Code)
	assert.Equal(t, http.StatusBadRequest, performJSON(t, r, http.MethodGet, url+"&max_points=99999", "", nil).Code)
	assert.Equal(t, http.StatusNotFound, performJSON(t, r, http.MethodGet, "/forecast-metrics/query?name=usage_cpu_cores&module_id=9999", "", nil).Code)
}

func TestCustomFormulaAlgorithmRunsLikeABuiltInOne(t *testing.T) {
	r, cpu := forecastTestRouter(t)
	t.Cleanup(func() { forecast.RemoveCustom("peak_margin") })
	body := map[string]any{"id": "peak_margin", "description": "peak of the recent window plus a margin", "formula": "smax(x, w) * margin", "min_samples": 5,
		"param_schema": []map[string]any{
			{"name": "w", "type": "int", "default": 6, "min": 2, "max": 500, "unit": "steps", "description": "window"},
			{"name": "margin", "type": "float", "default": 1.2, "min": 1, "max": 3, "description": "headroom"},
		}}

	// refused: built-in id, bad id, a formula that cannot compile or run, a parameter that is not in the schema
	assert.Equal(t, http.StatusBadRequest, performJSON(t, r, http.MethodPost, "/algorithms", "", map[string]any{"id": "ewma", "formula": "last(x)"}).Code)
	assert.Equal(t, http.StatusBadRequest, performJSON(t, r, http.MethodPost, "/algorithms", "", map[string]any{"id": "Bad Id", "formula": "last(x)"}).Code)
	assert.Equal(t, http.StatusBadRequest, performJSON(t, r, http.MethodPost, "/algorithms", "", map[string]any{"id": "broken_one", "formula": "last(y)"}).Code)
	assert.Equal(t, http.StatusBadRequest, performJSON(t, r, http.MethodPost, "/algorithms", "", map[string]any{"id": "broken_two", "formula": "1 / 0"}).Code)
	bad := map[string]any{}
	for k, v := range body {
		bad[k] = v
	}
	bad["params"] = map[string]any{"nope": 1}
	assert.Equal(t, http.StatusBadRequest, performJSON(t, r, http.MethodPost, "/algorithms", "", bad).Code)
	_, listed := forecast.Lookup("peak_margin")
	assert.False(t, listed, "a refused algorithm is not registered")

	// the editor can try a formula before saving
	rec := performJSON(t, r, http.MethodPost, "/algorithm-formulas/check", "", map[string]any{"formula": "last(y)"})
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, false, decodeData(t, rec.Body.Bytes())["ok"])
	rec = performJSON(t, r, http.MethodPost, "/algorithm-formulas/check", "", map[string]any{"formula": "mean(x, 12)", "min_samples": 12, "horizon": 10})
	check := decodeData(t, rec.Body.Bytes())
	assert.Equal(t, true, check["ok"])
	assert.Len(t, check["forecast"], 10)

	rec = performJSON(t, r, http.MethodPost, "/algorithms", "", body)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	created := decodeData(t, rec.Body.Bytes())
	assert.Equal(t, true, created["custom"])
	assert.Equal(t, "smax(x, w) * margin", created["formula"])
	assert.Equal(t, false, created["enabled"])
	assert.Equal(t, http.StatusConflict, performJSON(t, r, http.MethodPost, "/algorithms", "", body).Code, "the id is taken")
	rec = performJSON(t, r, http.MethodGet, "/algorithms?keyword=peak_margin", "", nil)
	assert.Len(t, decodeList(t, rec.Body.Bytes()), 1)

	// it runs for a module exactly like a built-in algorithm
	module := moduleWithCPU(t, r, cpu, map[string]any{"name": "api", "mode": "recommend", "lookback_hours": 6, "horizon_minutes": 10, "ttl_seconds": 3600, "headroom_ratio": 1.5})
	base := fmt.Sprintf("/modules/%d", module)
	enableAlgorithm(t, r, "peak_margin", map[string]any{"w": 4, "margin": 1.5})
	require.Equal(t, http.StatusOK, performJSON(t, r, http.MethodPost, base+"/run", "", nil).Code)
	rec = performJSON(t, r, http.MethodGet, fmt.Sprintf("/predictions?module_id=%d&metric=cpu&algorithm_id=peak_margin", module), "", nil)
	list := decodeList(t, rec.Body.Bytes())
	require.Len(t, list, 10)
	assert.InDelta(t, 3.0, list[0].(map[string]any)["value"], 1e-9, "flat 2 cores x margin 1.5")
	version, _ := list[0].(map[string]any)["model_version"].(string)

	// the run left a record: the window, the parameters and the formula it ran with
	rec = performJSON(t, r, http.MethodGet, fmt.Sprintf("/training-runs?module_id=%d&algorithm_id=peak_margin", module), "", nil)
	trainings := decodeList(t, rec.Body.Bytes())
	require.NotEmpty(t, trainings)
	training := trainings[0].(map[string]any)
	assert.Equal(t, "ok", training["status"])
	assert.Equal(t, "smax(x, w) * margin", training["formula"])
	assert.Equal(t, version, training["model_version"])
	assert.Contains(t, training["params"], "margin")
	assert.Greater(t, training["samples"], float64(0))
	assert.EqualValues(t, 10, training["points"])
	rec = performJSON(t, r, http.MethodGet, fmt.Sprintf("/training-runs/%v", training["id"]), "", nil)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, http.StatusNotFound, performJSON(t, r, http.MethodGet, "/training-runs/99999", "", nil).Code)

	// editing the formula makes a different model, and the stored values are checked against the new schema
	rec = performJSON(t, r, http.MethodPut, "/algorithms/peak_margin", "", map[string]any{"formula": "smax(x, w) * margin + 1"})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, http.StatusOK, performJSON(t, r, http.MethodPost, base+"/predict", "", nil).Code)
	rec = performJSON(t, r, http.MethodGet, fmt.Sprintf("/predictions?module_id=%d&metric=cpu&algorithm_id=peak_margin", module), "", nil)
	list = decodeList(t, rec.Body.Bytes())
	assert.InDelta(t, 4.0, list[0].(map[string]any)["value"], 1e-9)
	assert.NotEqual(t, version, list[0].(map[string]any)["model_version"])
	assert.Equal(t, http.StatusBadRequest, performJSON(t, r, http.MethodPut, "/algorithms/peak_margin", "", map[string]any{"formula": "last(nope)"}).Code)
	assert.Equal(t, http.StatusBadRequest, performJSON(t, r, http.MethodPut, "/algorithms/ewma", "", map[string]any{"formula": "last(x)"}).Code, "a built-in algorithm has no formula")

	// deleting: built-in no, enabled no, disabled custom yes
	assert.Equal(t, http.StatusConflict, performJSON(t, r, http.MethodDelete, "/algorithms/ewma", "", nil).Code)
	assert.Equal(t, http.StatusConflict, performJSON(t, r, http.MethodDelete, "/algorithms/peak_margin", "", nil).Code)
	require.Equal(t, http.StatusOK, performJSON(t, r, http.MethodPut, "/algorithms/peak_margin", "", map[string]any{"enabled": false}).Code)
	require.Equal(t, http.StatusOK, performJSON(t, r, http.MethodDelete, "/algorithms/peak_margin", "", nil).Code)
	assert.Equal(t, http.StatusNotFound, performJSON(t, r, http.MethodGet, "/algorithms/peak_margin", "", nil).Code)
}

func TestDatasetSourcesAreShapedHaveCustomPromQLAndAgreeWithTheModuleStep(t *testing.T) {
	r, cpu := forecastTestRouter(t)
	id := moduleWithCPU(t, r, cpu, map[string]any{"name": "ds", "lookback_hours": 48})
	path := fmt.Sprintf("/modules/%d/sources", id)

	// the dataset catalog groups the kinds into the five categories
	rec := performJSON(t, r, http.MethodGet, "/dataset/catalog", "", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	cats := decodeData(t, rec.Body.Bytes())["categories"].([]any)
	var ids []string
	for _, c := range cats {
		ids = append(ids, c.(map[string]any)["id"].(string))
	}
	assert.Equal(t, []string{"cpu", "memory", "gpu", "network", "other"}, ids)

	// the shaping of a source is validated
	for name, body := range map[string]map[string]any{
		"scale":       {"scale": -1},
		"precision":   {"precision": 20},
		"aggregation": {"aggregation": "median"},
		"missing":     {"missing": "guess"},
	} {
		b := map[string]any{"name": "bad-" + name, "kind": "cpu", "mapping_id": cpu.ID}
		for k, v := range body {
			b[k] = v
		}
		assert.Equal(t, http.StatusBadRequest, performJSON(t, r, http.MethodPost, path, "", b).Code, name)
	}

	// a PromQL written for the source creates its mapping; the list shows the query, the unit and the category
	datasource := cpu.DatasourceID
	rec = performJSON(t, r, http.MethodPost, path, "", map[string]any{"name": "gpu-util", "kind": "gpu_util", "scale": 0.01, "precision": 4, "aggregation": "max", "missing": "forward_fill",
		"custom": map[string]any{"datasource_id": datasource, "query": `avg(DCGM_FI_DEV_GPU_UTIL{namespace="{{namespace}}",pod=~"{{workload}}-.*"})`, "step_seconds": 60}})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	created := decodeData(t, rec.Body.Bytes())
	assert.EqualValues(t, 0.01, created["scale"])
	assert.Equal(t, "max", created["aggregation"])
	assert.Equal(t, "forward_fill", created["missing"])
	rec = performJSON(t, r, http.MethodGet, path, "", nil)
	var found map[string]any
	for _, row := range decodeData2(t, rec.Body.Bytes()) {
		if row.(map[string]any)["name"] == "gpu-util" {
			found = row.(map[string]any)
		}
	}
	require.NotNil(t, found)
	assert.Equal(t, "gpu", found["category"])
	assert.Equal(t, "ratio", found["unit"])
	assert.Contains(t, found["query"], "DCGM_FI_DEV_GPU_UTIL")

	// a wrong query is refused and leaves no mapping behind; the name of a mapping cannot be taken twice
	rec = performJSON(t, r, http.MethodPost, path, "", map[string]any{"name": "broken", "kind": "gpu_util",
		"custom": map[string]any{"datasource_id": datasource, "query": `avg(x{pod="{{nope}}"})`}})
	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Equal(t, http.StatusBadRequest, performJSON(t, r, http.MethodPost, path, "", map[string]any{"name": "gpu-util", "kind": "gpu_util",
		"custom": map[string]any{"datasource_id": datasource, "query": `avg(y{namespace="{{namespace}}",pod=~"{{workload}}-.*"})`}}).Code)
	assert.Equal(t, http.StatusBadRequest, performJSON(t, r, http.MethodPost, path, "", map[string]any{"name": "both", "kind": "cpu", "mapping_id": cpu.ID,
		"custom": map[string]any{"datasource_id": datasource, "query": "x"}}).Code, "mapping_id or custom, not both")

	// the module step must be a whole multiple of the step of every query
	mod := fmt.Sprintf("/modules/%d", id)
	assert.Equal(t, http.StatusOK, performJSON(t, r, http.MethodPut, mod, "", map[string]any{"step_seconds": 300, "min_points": 50, "run_interval_minutes": 10}).Code)
	rec = performJSON(t, r, http.MethodGet, mod, "", nil)
	assert.EqualValues(t, 300, decodeData(t, rec.Body.Bytes())["step_seconds"])
	assert.Equal(t, http.StatusBadRequest, performJSON(t, r, http.MethodPut, mod, "", map[string]any{"step_seconds": 90}).Code, "90s is not a multiple of the 60s query step")
	assert.Equal(t, http.StatusBadRequest, performJSON(t, r, http.MethodPut, mod, "", map[string]any{"step_seconds": 30}).Code, "finer than the query")
	assert.Equal(t, http.StatusBadRequest, performJSON(t, r, http.MethodPut, mod, "", map[string]any{"step_seconds": 3600, "lookback_hours": 24, "min_points": 50}).Code, "24 hourly points cannot hold 50")
	assert.Equal(t, http.StatusBadRequest, performJSON(t, r, http.MethodPut, mod, "", map[string]any{"step_seconds": 3600, "horizon_minutes": 30}).Code, "the horizon is shorter than a step")
	assert.Equal(t, http.StatusBadRequest, performJSON(t, r, http.MethodPut, mod, "", map[string]any{"run_interval_minutes": 2, "step_seconds": 300}).Code, "no new data between two runs")

	// what an enabled algorithm needs at that step, and whether the range holds it
	enableAlgorithm(t, r, "seasonal_naive", map[string]any{"season_length_steps": 288})
	rec = performJSON(t, r, http.MethodGet, mod+"/requirements", "", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var need map[string]any
	for _, row := range decodeData2(t, rec.Body.Bytes()) {
		m := row.(map[string]any)
		if m["algorithm_id"] == "seasonal_naive" && m["metric"] == "cpu" {
			need = m
		}
	}
	require.NotNil(t, need)
	assert.EqualValues(t, 300, need["step_seconds"])
	assert.EqualValues(t, 288, need["min_samples"], "one day of 5-minute steps")
	assert.EqualValues(t, 48*12, need["available_steps"])
	assert.Equal(t, true, need["ok"])
	// a season longer than the range holds is not refused: the range is extended to what the algorithm needs
	enableAlgorithm(t, r, "seasonal_naive", map[string]any{"season_length_steps": 600})
	rec = performJSON(t, r, http.MethodGet, mod+"/requirements", "", nil)
	for _, row := range decodeData2(t, rec.Body.Bytes()) {
		m := row.(map[string]any)
		if m["algorithm_id"] == "seasonal_naive" && m["metric"] == "cpu" {
			assert.Equal(t, true, m["ok"], m["reason"])
			assert.EqualValues(t, 75, m["effective_lookback_hours"], "600 steps of 5 minutes with the room kept over them")
		}
	}
	// one that needs more than the limit of the extension is reported with how to fix it
	enableAlgorithm(t, r, "seasonal_naive", map[string]any{"season_length_steps": 10000})
	rec = performJSON(t, r, http.MethodGet, mod+"/requirements", "", nil)
	for _, row := range decodeData2(t, rec.Body.Bytes()) {
		m := row.(map[string]any)
		if m["algorithm_id"] == "seasonal_naive" && m["metric"] == "cpu" {
			assert.Equal(t, false, m["ok"])
			assert.Contains(t, m["reason"], "lookback_hours")
		}
	}
}

// decodeData2 reads a response whose data is a list.
func decodeData2(t *testing.T, body []byte) []any {
	t.Helper()
	var envelope struct {
		Data []any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(body, &envelope), string(body))
	return envelope.Data
}

func TestVariantOfABuiltInAlgorithmKeepsItsOwnParametersAndIsDeletable(t *testing.T) {
	r, _ := forecastTestRouter(t)
	t.Cleanup(func() { forecast.RemoveCustom("ewma_fast") })

	// refused: a baseline, an algorithm without parameters, an unknown base, parameters the base does not have, a built-in id
	for name, body := range map[string]map[string]any{
		"required param": {"id": "season_copy", "base": "seasonal_naive"},
		"no parameters":  {"id": "naive_copy", "base": "naive"},
		"unknown base":   {"id": "prophet_two", "base": "prophet"},
		"foreign param":  {"id": "ewma_bad", "base": "ewma", "params": map[string]any{"lags": 3}},
		"out of range":   {"id": "ewma_wide", "base": "ewma", "params": map[string]any{"alpha": 7}},
		"neither":        {"id": "nothing_here"},
	} {
		assert.Equal(t, http.StatusBadRequest, performJSON(t, r, http.MethodPost, "/algorithms", "", body).Code, name)
	}
	assert.Equal(t, http.StatusConflict, performJSON(t, r, http.MethodPost, "/algorithms", "", map[string]any{"id": "ewma", "base": "ewma"}).Code, "a built-in id is taken")
	// a variant of a baseline is allowed, with its own period, and is not a baseline itself
	t.Cleanup(func() { forecast.RemoveCustom("weekly_naive") })
	rec := performJSON(t, r, http.MethodPost, "/algorithms", "", map[string]any{"id": "weekly_naive", "base": "seasonal_naive", "params": map[string]any{"season_length_steps": 672}})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	weekly := decodeData(t, rec.Body.Bytes())
	assert.Equal(t, "variant", weekly["origin"])
	assert.Equal(t, false, weekly["baseline"], "the baselines stay the reference")
	assert.EqualValues(t, 672, weekly["values"].(map[string]any)["season_length_steps"])
	_, registered := forecast.Lookup("ewma_bad")
	assert.False(t, registered, "a refused variant is not registered")

	rec = performJSON(t, r, http.MethodPost, "/algorithms", "", map[string]any{"id": "ewma_fast", "base": "ewma", "description": "reacts fast", "params": map[string]any{"alpha": 0.9}})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	created := decodeData(t, rec.Body.Bytes())
	assert.Equal(t, "variant", created["origin"])
	assert.Equal(t, "ewma", created["base"])
	assert.Equal(t, "statistical", created["category"], "a variant is in the category of its base")
	assert.Equal(t, 0.9, created["values"].(map[string]any)["alpha"])
	assert.NotEmpty(t, created["params"], "it shows the parameters of its base")
	assert.Equal(t, http.StatusConflict, performJSON(t, r, http.MethodPost, "/algorithms", "", map[string]any{"id": "ewma_fast", "base": "ewma"}).Code)

	// the base keeps its own values
	rec = performJSON(t, r, http.MethodGet, "/algorithms/ewma", "", nil)
	assert.NotEqual(t, 0.9, decodeData(t, rec.Body.Bytes())["values"].(map[string]any)["alpha"])

	// its parameters change like those of a built-in one; the formula fields are refused
	rec = performJSON(t, r, http.MethodPut, "/algorithms/ewma_fast", "", map[string]any{"params": map[string]any{"alpha": 0.5}, "description": "balanced"})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, 0.5, decodeData(t, rec.Body.Bytes())["values"].(map[string]any)["alpha"])
	assert.Equal(t, "balanced", decodeData(t, rec.Body.Bytes())["description"])
	assert.Equal(t, http.StatusBadRequest, performJSON(t, r, http.MethodPut, "/algorithms/ewma_fast", "", map[string]any{"formula": "last(x)"}).Code)

	// enabled ones are not deleted; a disabled one is, and its id is free again
	enableAlgorithm(t, r, "ewma_fast", nil)
	assert.Equal(t, http.StatusConflict, performJSON(t, r, http.MethodDelete, "/algorithms/ewma_fast", "", nil).Code)
	require.Equal(t, http.StatusOK, performJSON(t, r, http.MethodPut, "/algorithms/ewma_fast", "", map[string]any{"enabled": false}).Code)
	assert.Equal(t, http.StatusOK, performJSON(t, r, http.MethodDelete, "/algorithms/ewma_fast", "", nil).Code)
	assert.Equal(t, http.StatusNotFound, performJSON(t, r, http.MethodGet, "/algorithms/ewma_fast", "", nil).Code)
}

func TestTrainingSummaryCountsHowRunsEndedAndWhyTheyDidNotRun(t *testing.T) {
	r, cpu := forecastTestRouter(t)
	module := moduleWithCPU(t, r, cpu, map[string]any{"name": "ts"})
	at := time.Now()
	for _, run := range []model.TrainingRun{
		{ModuleID: uint(module), Metric: "cpu", AlgorithmID: "naive", Status: "ok", DurationMs: 10, PredictionTime: at},
		{ModuleID: uint(module), Metric: "cpu", AlgorithmID: "naive", Status: "ok", DurationMs: 30, PredictionTime: at.Add(-time.Hour)},
		{ModuleID: uint(module), Metric: "cpu", AlgorithmID: "lstm", Status: "skipped", Reason: "needs 96 samples, got 10", PredictionTime: at},
		{ModuleID: uint(module), Metric: "cpu", AlgorithmID: "lstm", Status: "skipped", Reason: "needs 96 samples, got 10", PredictionTime: at.Add(-time.Hour)},
		{ModuleID: uint(module), Metric: "cpu", AlgorithmID: "gbdt", Status: "failed", Reason: "budget exceeded", PredictionTime: at},
	} {
		require.NoError(t, model.DB.Create(&run).Error)
	}
	rec := performJSON(t, r, http.MethodGet, fmt.Sprintf("/training-runs/summary?module_id=%d", module), "", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	got := decodeData(t, rec.Body.Bytes())
	assert.EqualValues(t, 5, got["total"])
	assert.EqualValues(t, 2, got["ok"])
	assert.EqualValues(t, 2, got["skipped"])
	assert.EqualValues(t, 1, got["failed"])
	assert.InDelta(t, 8, got["avg_duration_ms"], 1e-9)
	algorithms := got["algorithms"].([]any)
	require.Len(t, algorithms, 3)
	assert.Equal(t, "gbdt", algorithms[0].(map[string]any)["algorithm_id"], "sorted by algorithm")
	reasons := got["reasons"].([]any)
	require.Len(t, reasons, 2)
	assert.Equal(t, "needs 96 samples, got 10", reasons[0].(map[string]any)["reason"], "the most common reason first")
	assert.EqualValues(t, 2, reasons[0].(map[string]any)["count"])

	// the filters of the list apply
	got = decodeData(t, performJSON(t, r, http.MethodGet, fmt.Sprintf("/training-runs/summary?module_id=%d&algorithm_id=naive", module), "", nil).Body.Bytes())
	assert.EqualValues(t, 2, got["total"])
	assert.Empty(t, got["reasons"])
	got = decodeData(t, performJSON(t, r, http.MethodGet, "/training-runs/summary?module_id=9999", "", nil).Body.Bytes())
	assert.EqualValues(t, 0, got["total"], "nothing recorded is an empty summary, not an error")
}

func TestCompareReadsTheActualHistoryFromPrometheusAndAlignsTheForecastsOfEveryAlgorithm(t *testing.T) {
	r, cpu := forecastTestRouter(t)
	module := moduleWithCPU(t, r, cpu, map[string]any{"name": "cmp", "lookback_hours": 6, "horizon_minutes": 10, "replica_mode": "dynamic", "capacity_mode": "manual",
		"pod_cpu_request_milli": 1000, "target_utilization": 0.5, "min_replicas": 1, "max_replicas": 20, "headroom_ratio": 1})
	base := fmt.Sprintf("/modules/%d", module)
	enableAlgorithm(t, r, "naive", nil)
	enableAlgorithm(t, r, "ewma", map[string]any{"alpha": 0.5})
	require.Equal(t, http.StatusOK, performJSON(t, r, http.MethodPost, base+"/run", "", nil).Code)

	// nothing was ever collected for this module, yet a window before it existed has actual values: they come from Prometheus
	from, to := time.Now().UTC().Add(-30*24*time.Hour), time.Now().UTC().Add(-29*24*time.Hour)
	rec := performJSON(t, r, http.MethodGet, fmt.Sprintf("%s/compare?metric=cpu&from=%s&to=%s&predictions=false", base, from.Format(time.RFC3339), to.Format(time.RFC3339)), "", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	got := decodeData(t, rec.Body.Bytes())
	actual := got["actual"].(map[string]any)
	assert.Equal(t, false, actual["missing"], "Prometheus has samples in the window")
	candles := actual["candles"].([]any)
	require.NotEmpty(t, candles)
	first := candles[0].(map[string]any)
	assert.EqualValues(t, 2, first["open"])
	assert.EqualValues(t, 2, first["high"])
	assert.EqualValues(t, 2, first["low"])
	assert.Empty(t, got["algorithms"], "no forecasts were asked for")

	// the window of the forecasts: every enabled algorithm with a forecast is in the answer, with its replicas
	now := time.Now().UTC()
	rec = performJSON(t, r, http.MethodGet, fmt.Sprintf("%s/compare?metric=cpu&from=%s&to=%s", base, now.Add(-time.Hour).Format(time.RFC3339), now.Add(time.Hour).Format(time.RFC3339)), "", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	got = decodeData(t, rec.Body.Bytes())
	algorithms := got["algorithms"].([]any)
	require.Len(t, algorithms, 2, "both enabled algorithms, not a fixed number")
	assert.Equal(t, "ewma", algorithms[0].(map[string]any)["algorithm_id"])
	point := algorithms[1].(map[string]any)["points"].([]any)[0].(map[string]any)
	assert.EqualValues(t, 2, point["value"])
	assert.EqualValues(t, 4, point["replicas"], "ceil(2 cores / (1 core x 50%))")
	assert.NotEmpty(t, got["replica_basis"])
	// a filter keeps only the algorithms asked for
	rec = performJSON(t, r, http.MethodGet, fmt.Sprintf("%s/compare?metric=cpu&algorithms=naive&from=%s&to=%s", base, now.Add(-time.Hour).Format(time.RFC3339), now.Add(time.Hour).Format(time.RFC3339)), "", nil)
	assert.Len(t, decodeData(t, rec.Body.Bytes())["algorithms"], 1)

	assert.Equal(t, http.StatusBadRequest, performJSON(t, r, http.MethodGet, base+"/compare?metric=memory", "", nil).Code)
	assert.Equal(t, http.StatusBadRequest, performJSON(t, r, http.MethodGet, fmt.Sprintf("%s/compare?metric=cpu&from=%s&to=%s", base, to.Format(time.RFC3339), from.Format(time.RFC3339)), "", nil).Code)
}

func TestMetricQueryTestSaysHowFarAQueryGotBeforeItIsSaved(t *testing.T) {
	r, cpu := forecastTestRouter(t)
	module := moduleWithCPU(t, r, cpu, map[string]any{"name": "qt"})
	call := func(body map[string]any) map[string]any {
		t.Helper()
		rec := performJSON(t, r, http.MethodPost, "/metric-queries/test", "", body)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		return decodeData(t, rec.Body.Bytes())
	}
	good := map[string]any{"datasource_id": cpu.DatasourceID, "metric": "cpu", "level": "workload", "module_id": module, "step_seconds": 60,
		"query": `sum(rate(cpu{namespace="{{namespace}}",pod=~"{{workload}}-.*"}[{{range}}]))`, "scale": 0.5, "precision": 2}
	got := call(good)
	assert.Equal(t, true, got["ok"])
	assert.Equal(t, "ok", got["stage"])
	assert.Equal(t, "cores", got["unit"])
	assert.Contains(t, got["rendered"], `namespace="prod"`, "the variables come from the workload of the module")
	assert.EqualValues(t, 1, got["mean"], "the values are shaped like a dataset: 2 x 0.5")
	assert.NotEmpty(t, got["points"])

	bad := map[string]any{}
	for k, v := range good {
		bad[k] = v
	}
	bad["query"] = `sum(up{job="{{nope}}"})`
	got = call(bad)
	assert.Equal(t, false, got["ok"])
	assert.Equal(t, "template", got["stage"])
	assert.Contains(t, got["error"], "unknown template variable")
	bad["query"], bad["datasource_id"] = good["query"], 9999
	assert.Equal(t, "datasource", call(bad)["stage"])
	bad["datasource_id"], bad["from"], bad["to"] = good["datasource_id"], "2026-01-02T00:00:00Z", "2026-01-01T00:00:00Z"
	assert.Equal(t, "template", call(bad)["stage"], "from after to")
}

func TestBatchOperationsReportWhatWasDoneAndWhatWasRefused(t *testing.T) {
	r, cpu := forecastTestRouter(t)
	t.Cleanup(func() { forecast.RemoveCustom("batch_ewma") })
	a := moduleWithCPU(t, r, cpu, map[string]any{"name": "b1", "workload": "b1"})
	b := moduleWithCPU(t, r, cpu, map[string]any{"name": "b2", "workload": "b2"})
	batch := func(path string, body map[string]any) map[string]any {
		t.Helper()
		rec := performJSON(t, r, http.MethodPost, path, "", body)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		return decodeData(t, rec.Body.Bytes())
	}

	// an enabled module is not deleted; after it is disabled it is, the other one is untouched
	got := batch("/modules/batch", map[string]any{"action": "delete", "ids": []int{a, b}})
	assert.EqualValues(t, 0, got["done"])
	assert.Len(t, got["failed"], 2)
	got = batch("/modules/batch", map[string]any{"action": "disable", "ids": []int{a}})
	assert.EqualValues(t, 1, got["done"])
	got = batch("/modules/batch", map[string]any{"action": "delete", "ids": []int{a, b}})
	assert.EqualValues(t, 1, got["done"], "only the disabled one")
	assert.Len(t, got["failed"], 1)
	assert.Equal(t, http.StatusNotFound, performJSON(t, r, http.MethodGet, fmt.Sprintf("/modules/%d", a), "", nil).Code)
	assert.Equal(t, http.StatusOK, performJSON(t, r, http.MethodGet, fmt.Sprintf("/modules/%d", b), "", nil).Code)
	assert.Equal(t, http.StatusBadRequest, performJSON(t, r, http.MethodPost, "/modules/batch", "", map[string]any{"action": "explode", "ids": []int{b}}).Code)

	// algorithms: a built-in one is disabled, never deleted; a derived one is deleted once it is disabled
	require.Equal(t, http.StatusOK, performJSON(t, r, http.MethodPost, "/algorithms", "", map[string]any{"id": "batch_ewma", "base": "ewma", "enabled": true}).Code)
	got = batch("/algorithms/batch", map[string]any{"action": "delete", "ids": []string{"batch_ewma", "naive", "ghost"}})
	assert.EqualValues(t, 0, got["done"])
	assert.Len(t, got["failed"], 3)
	got = batch("/algorithms/batch", map[string]any{"action": "enable", "ids": []string{"naive"}})
	assert.EqualValues(t, 1, got["done"])
	got = batch("/algorithms/batch", map[string]any{"action": "disable", "ids": []string{"batch_ewma", "naive"}})
	assert.EqualValues(t, 2, got["done"])
	got = batch("/algorithms/batch", map[string]any{"action": "delete", "ids": []string{"batch_ewma", "naive"}})
	assert.EqualValues(t, 1, got["done"])
	assert.Equal(t, http.StatusNotFound, performJSON(t, r, http.MethodGet, "/algorithms/batch_ewma", "", nil).Code)

	// records: only the lists that record what happened
	require.NoError(t, model.DB.Create(&model.TrainingRun{ModuleID: uint(b), Metric: "cpu", AlgorithmID: "naive", Status: "ok", PredictionTime: time.Now()}).Error)
	var run model.TrainingRun
	require.NoError(t, model.DB.First(&run).Error)
	got = batch("/records/batch-delete", map[string]any{"kind": "trainings", "ids": []uint{run.ID, 99999}})
	assert.EqualValues(t, 1, got["done"])
	assert.Equal(t, http.StatusBadRequest, performJSON(t, r, http.MethodPost, "/records/batch-delete", "", map[string]any{"kind": "recommendations", "ids": []uint{1}}).Code, "the snapshots are the audit trail")
}

func TestEveryEnabledAlgorithmIsListedWithItsStateAndAnAlgorithmWithoutRequiredParametersCannotBeEnabled(t *testing.T) {
	r, cpu := forecastTestRouter(t)
	module := moduleWithCPU(t, r, cpu, map[string]any{"name": "states", "lookback_hours": 6, "horizon_minutes": 10})
	base := fmt.Sprintf("/modules/%d", module)
	enableAlgorithm(t, r, "naive", nil)
	enableAlgorithm(t, r, "ewma", map[string]any{"alpha": 0.5})

	// random_forest needs "lags"; enabling it without would make every run fail, so it is refused with the reason
	rec := performJSON(t, r, http.MethodPut, "/algorithms/random_forest", "", map[string]any{"enabled": true})
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "lags")
	rec = performJSON(t, r, http.MethodPost, "/algorithms/batch", "", map[string]any{"action": "enable", "ids": []string{"random_forest", "knn"}})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "lags", "the batch names the one it refused and why")

	// before any run: both enabled algorithms are on the leaderboard, with a state instead of a rank
	rec = performJSON(t, r, http.MethodGet, base+"/leaderboard?kind=cpu", "", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var board struct {
		Data []map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &board))
	entries := board.Data
	require.Len(t, entries, 3, "naive, ewma and the knn the batch enabled; random_forest was refused")
	for _, e := range entries {
		assert.Equal(t, "not_run", e["status"], "an enabled algorithm that never ran is listed, not hidden")
		assert.NotEmpty(t, e["reason"])
		assert.EqualValues(t, 0, e["rank"])
	}

	// after a run each has forecasts but nothing to score yet: the state says so
	require.Equal(t, http.StatusOK, performJSON(t, r, http.MethodPost, base+"/run", "", nil).Code)
	from, to := time.Now().UTC().Add(-time.Hour), time.Now().UTC().Add(time.Hour)
	rec = performJSON(t, r, http.MethodGet, fmt.Sprintf("%s/compare?metric=cpu&from=%s&to=%s", base, from.Format(time.RFC3339), to.Format(time.RFC3339)), "", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	statuses := decodeData(t, rec.Body.Bytes())["statuses"].([]any)
	ids := map[string]string{}
	for _, s := range statuses {
		m := s.(map[string]any)
		ids[m["algorithm_id"].(string)] = m["status"].(string)
	}
	assert.Len(t, ids, 3, "every enabled algorithm is in the statuses of the comparison")
	assert.Contains(t, ids, "naive")
	assert.Contains(t, ids, "ewma")
}

func TestTrainingDataHasTheSamplesOfTheWindowAndTheForecastOfTheRun(t *testing.T) {
	r, cpu := forecastTestRouter(t)
	module := moduleWithCPU(t, r, cpu, map[string]any{"name": "td"})
	at := time.Now().UTC().Truncate(time.Second)
	start, end := at.Add(-10*time.Minute), at
	series := model.MetricSeries{ModuleID: uint(module), Metric: "cpu", StepSeconds: 60}
	require.NoError(t, model.DB.Create(&series).Error)
	for i := 0; i <= 10; i++ {
		require.NoError(t, model.DB.Create(&model.MetricSample{SeriesID: series.ID, TS: start.Add(time.Duration(i) * time.Minute).Unix(), Value: float64(i)}).Error)
	}
	run := model.TrainingRun{ModuleID: uint(module), Metric: "cpu", AlgorithmID: "naive", Status: "ok", PredictionTime: at, WindowStart: &start, WindowEnd: &end}
	require.NoError(t, model.DB.Create(&run).Error)
	for i := 1; i <= 3; i++ {
		require.NoError(t, model.DB.Create(&model.Prediction{ModuleID: uint(module), Metric: "cpu", AlgorithmID: "naive", PredictionTime: at, TargetTime: at.Add(time.Duration(i) * time.Minute), QuantilePermille: 500, Value: 5, Unit: "cores"}).Error)
	}
	rec := performJSON(t, r, http.MethodGet, fmt.Sprintf("/training-runs/%d/data", run.ID), "", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	got := decodeData(t, rec.Body.Bytes())
	assert.Len(t, got["input"], 11, "the stored samples of the window")
	assert.Len(t, got["forecast"], 3, "the forecast this run wrote")
	assert.Equal(t, http.StatusNotFound, performJSON(t, r, http.MethodGet, "/training-runs/999999/data", "", nil).Code)
}

// A new algorithm is only "in" the platform when it is registered, loads with its parameters, is enabled and has run: the list
// says which of these it has reached, so an algorithm that never produces a forecast is visible as such.
func TestAnAlgorithmShowsHowFarItHasReachedInThePipeline(t *testing.T) {
	r, cpu := forecastTestRouter(t)
	t.Cleanup(func() { forecast.RemoveCustom("reach_test") })
	runtimeOf := func(id string) map[string]any {
		rec := performJSON(t, r, http.MethodGet, "/algorithms/"+id, "", nil)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		return decodeData(t, rec.Body.Bytes())["runtime"].(map[string]any)
	}
	body := map[string]any{"id": "reach_test", "description": "last value", "formula": "last(x) * margin", "min_samples": 3,
		"param_schema": []map[string]any{{"name": "margin", "type": "float", "default": 1.1, "min": 1, "max": 3, "description": "headroom"}}}
	rec := performJSON(t, r, http.MethodPost, "/algorithms", "", body)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "disabled", decodeData(t, rec.Body.Bytes())["runtime"].(map[string]any)["state"], "registered, not enabled")

	module := moduleWithCPU(t, r, cpu, map[string]any{"name": "reach", "mode": "recommend", "lookback_hours": 6, "horizon_minutes": 10, "ttl_seconds": 3600, "headroom_ratio": 1.5})
	enableAlgorithm(t, r, "reach_test", map[string]any{"margin": 1.5})
	assert.Equal(t, "waiting", runtimeOf("reach_test")["state"], "enabled and loaded: the next cycle of the forecast task runs it")

	require.Equal(t, http.StatusOK, performJSON(t, r, http.MethodPost, fmt.Sprintf("/modules/%d/run", module), "", nil).Code)
	got := runtimeOf("reach_test")
	assert.Equal(t, "running", got["state"], "its latest run produced a forecast")
	assert.EqualValues(t, 1, got["runs"])
	assert.NotNil(t, got["last_run_at"])
	assert.Equal(t, "ok", got["last_status"])

	listed := decodeList(t, performJSON(t, r, http.MethodGet, "/algorithms?keyword=reach_test", "", nil).Body.Bytes())
	require.Len(t, listed, 1)
	assert.Equal(t, "running", listed[0].(map[string]any)["runtime"].(map[string]any)["state"], "the list carries it too")

	// an algorithm nobody enabled stays disabled
	assert.Equal(t, "disabled", runtimeOf("knn")["state"])
}
