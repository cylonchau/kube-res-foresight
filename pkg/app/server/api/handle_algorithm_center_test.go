package api

import (
	"fmt"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cylonchau/kube-resource-fcst/pkg/model"
)

func itoa(n int) string { return strconv.Itoa(n) }

func TestAlgorithmUsageListsTheModulesTheAlgorithmRunsFor(t *testing.T) {
	r, cpu := forecastTestRouter(t)
	moduleWithCPU(t, r, cpu, map[string]any{"name": "one", "workload": "api"})
	moduleWithCPU(t, r, cpu, map[string]any{"name": "two", "workload": "web"})
	enableAlgorithm(t, r, "ewma", map[string]any{"alpha": 0.8})

	rec := performJSON(t, r, http.MethodGet, "/algorithms/ewma/usage", "", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	data := decodeData(t, rec.Body.Bytes())
	assert.Equal(t, true, data["enabled"])
	modules := data["modules"].([]any)
	require.Len(t, modules, 2, "an enabled algorithm applies to every module, whatever was created before or after it")
	names := map[string]bool{}
	for _, m := range modules {
		item := m.(map[string]any)
		names[item["module_name"].(string)] = true
		assert.Len(t, item["source_usage"], 1)
	}
	assert.Equal(t, map[string]bool{"one": true, "two": true}, names)
	assert.Equal(t, http.StatusNotFound, performJSON(t, r, http.MethodGet, "/algorithms/prophet/usage", "", nil).Code)
}

func TestTraceFollowsTaskPredictionAndEvaluationOfAnAlgorithmOnAModule(t *testing.T) {
	r, cpu := forecastTestRouter(t)
	module := moduleWithCPU(t, r, cpu, map[string]any{"name": "traced"})
	enableAlgorithm(t, r, "naive", nil)
	base := fmt.Sprintf("/modules/%d", module)

	assert.Equal(t, http.StatusBadRequest, performJSON(t, r, http.MethodGet, base+"/trace", "", nil).Code, "the algorithm is named")
	rec := performJSON(t, r, http.MethodGet, base+"/trace?algorithm=naive", "", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	trace := decodeData(t, rec.Body.Bytes())
	task := trace["task"].(map[string]any)
	assert.Equal(t, "forecast.run", task["task_type"])
	assert.Equal(t, false, task["defined"], "no seeded task in this test database")
	assert.Equal(t, false, trace["prediction"].(map[string]any)["present"], "nothing predicted yet")
	assert.NotContains(t, trace, "evaluation")
	assert.Equal(t, http.StatusNotFound, performJSON(t, r, http.MethodGet, fmt.Sprintf("/modules/%d/trace?algorithm=naive", module+1), "", nil).Code)

	require.Equal(t, http.StatusOK, performJSON(t, r, http.MethodPost, base+"/collect", "", nil).Code)
	require.Equal(t, http.StatusOK, performJSON(t, r, http.MethodPost, base+"/predict", "", nil).Code)
	trace = decodeData(t, performJSON(t, r, http.MethodGet, base+"/trace?algorithm=naive", "", nil).Body.Bytes())
	prediction := trace["prediction"].(map[string]any)
	assert.Equal(t, true, prediction["present"])
	assert.Equal(t, true, prediction["version_current"], "the stored prediction was made by the model the algorithm setting describes")

	// changing the parameters of the algorithm makes the stored prediction come from another model
	enableAlgorithm(t, r, "ewma", map[string]any{"alpha": 0.5})
	require.Equal(t, http.StatusOK, performJSON(t, r, http.MethodPost, base+"/predict", "", nil).Code)
	enableAlgorithm(t, r, "ewma", map[string]any{"alpha": 0.9})
	trace = decodeData(t, performJSON(t, r, http.MethodGet, base+"/trace?algorithm=ewma", "", nil).Body.Bytes())
	assert.Equal(t, false, trace["prediction"].(map[string]any)["version_current"])
}

func TestScoringRanksTheEnabledAlgorithmsOfAModuleAndTheRankingEndpointReadsThemBack(t *testing.T) {
	r, cpu := forecastTestRouter(t)
	module := moduleWithCPU(t, r, cpu, map[string]any{"name": "m", "lookback_hours": 24, "horizon_minutes": 10})
	enableAlgorithm(t, r, "naive", nil)
	enableAlgorithm(t, r, "ewma", map[string]any{"alpha": 0.5})
	base := fmt.Sprintf("/modules/%d", module)
	require.Equal(t, http.StatusOK, performJSON(t, r, http.MethodPost, base+"/collect", "", nil).Code)

	rec := performJSON(t, r, http.MethodPost, base+"/score", "", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	rec = performJSON(t, r, http.MethodGet, base+"/ranking", "", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	list := decodeData(t, rec.Body.Bytes())["list"].([]any)
	require.Len(t, list, 2, "only the enabled algorithms are ranked")
	for _, row := range list {
		assert.NotNil(t, row.(map[string]any)["score"], "every algorithm was scored on the flat series")
	}

	for _, row := range list {
		assert.Nil(t, row.(map[string]any)["previous_rank"], "scored once: nothing to compare with")
	}
	require.Equal(t, http.StatusOK, performJSON(t, r, http.MethodPost, base+"/score", "", nil).Code)
	rec = performJSON(t, r, http.MethodGet, base+"/ranking", "", nil)
	for _, row := range decodeData(t, rec.Body.Bytes())["list"].([]any) {
		assert.NotNil(t, row.(map[string]any)["previous_rank"], "scored twice: the ranking shows where it was")
		assert.NotNil(t, row.(map[string]any)["previous_score"])
	}

	rec = performJSON(t, r, http.MethodGet, fmt.Sprintf("/algorithm-scores?module_id=%d&sort_by=score&sort_order=desc", module), "", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.EqualValues(t, 4, decodeData(t, rec.Body.Bytes())["total"], "two evaluations of two algorithms")
	assert.Equal(t, http.StatusOK, performJSON(t, r, http.MethodGet, "/algorithm-scores?sort_by=nope;drop", "", nil).Code, "an unknown sort falls back instead of reaching SQL")

	// every record carries its parts: accuracy, error, the weight the part had and what it contributed
	first := decodeList(t, rec.Body.Bytes())[0].(map[string]any)
	assert.Equal(t, "m", first["module_name"])
	parts := first["breakdown"].([]any)
	require.NotEmpty(t, parts)
	var weights, contributions float64
	for _, p := range parts {
		part := p.(map[string]any)
		assert.InDelta(t, 1, part["accuracy"].(float64)+part["error"].(float64), 1e-9)
		weights += part["weight"].(float64)
		contributions += part["contribution"].(float64)
	}
	assert.InDelta(t, 1, weights, 1e-9, "the shares of the parts add up to one")
	assert.InDelta(t, first["score"].(float64), contributions, 1e-6, "the contributions add up to the score")

	// the current ranking of the module, and the history of the evaluations
	rec = performJSON(t, r, http.MethodGet, fmt.Sprintf("/algorithm-rankings/current?module_id=%d", module), "", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	current := decodeList(t, rec.Body.Bytes())
	require.Len(t, current, 1)
	entry := current[0].(map[string]any)
	assert.Len(t, entry["rows"], 2)
	assert.NotEmpty(t, entry["best"])
	rec = performJSON(t, r, http.MethodGet, "/algorithm-rankings/history?page_size=5", "", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	history := decodeList(t, rec.Body.Bytes())
	require.Len(t, history, 2, "two evaluations so far")
	assert.Len(t, history[0].(map[string]any)["rows"], 2)
	assert.Equal(t, "m", history[0].(map[string]any)["module_name"])
	assert.Equal(t, http.StatusOK, performJSON(t, r, http.MethodGet, "/algorithm-rankings/history?algorithm=nope", "", nil).Code)
}

func TestMetricScoresCompareEveryAlgorithmOnTheSameSourcesAgainstTheActuals(t *testing.T) {
	r, cpu := forecastTestRouter(t)
	module := moduleWithCPU(t, r, cpu, map[string]any{"name": "ms", "lookback_hours": 24})
	enableAlgorithm(t, r, "naive", nil)
	enableAlgorithm(t, r, "ewma", map[string]any{"alpha": 0.5})
	base := fmt.Sprintf("/modules/%d", module)
	// a second source, so the composite has two weighted parts
	rec := performJSON(t, r, http.MethodPost, base+"/sources", "", map[string]any{"name": "cpu-b", "kind": "cpu", "mapping_id": cpu.ID, "weight": 3})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	eval := func(algorithm, metric string, wape float64) {
		require.NoError(t, model.DB.Create(&model.PredictionEvaluation{ModuleID: uint(module), Metric: metric, AlgorithmID: algorithm, ModelVersion: "v",
			PredictionTime: time.Now().UTC(), QuantilePermille: 500, Points: 10, Expected: 10, MAE: wape, RMSE: wape * 2, WAPE: wape}).Error)
	}
	eval("naive", "cpu", 0.10)
	eval("naive", "cpu_2", 0.30)
	eval("ewma", "cpu", 0.20)
	eval("ewma", "cpu_2", 0.10)

	rec = performJSON(t, r, http.MethodGet, base+"/metric-scores", "", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	res := decodeData(t, rec.Body.Bytes())
	assert.Equal(t, true, res["comparable"])
	algorithms := res["algorithms"].([]any)
	require.Len(t, algorithms, 2)
	// weights 1 and 3: naive 0.25*0.9 + 0.75*0.7 = 0.75; ewma 0.25*0.8 + 0.75*0.9 = 0.875
	best := algorithms[0].(map[string]any)
	assert.Equal(t, "ewma", best["algorithm_id"])
	assert.InDelta(t, 87.5, best["score"], 1e-6)
	assert.EqualValues(t, 1, best["rank"])
	assert.Equal(t, "ewma", res["best"])
	assert.InDelta(t, 75, algorithms[1].(map[string]any)["score"], 1e-6)

	var cpuB map[string]any
	for _, m := range res["metrics"].([]any) {
		row := m.(map[string]any)
		if row["algorithm_id"] == "naive" && row["metric"] == "cpu_2" {
			cpuB = row
		}
	}
	require.NotNil(t, cpuB)
	assert.InDelta(t, 0.3, cpuB["error"], 1e-9)
	assert.InDelta(t, 0.7, cpuB["accuracy"], 1e-9)
	assert.InDelta(t, 0.75, cpuB["weight"], 1e-9, "the weight of the source among the sources of the composite")
	assert.InDelta(t, 0.6, cpuB["rmse"], 1e-9)
	assert.Equal(t, "cpu", cpuB["category"])
}

func TestAdmissionRecordsAreListedAndFiltered(t *testing.T) {
	r, _ := forecastTestRouter(t)
	module := createModule(t, r, map[string]any{"name": "m", "namespace": "prod", "workload": "api"})
	for i, applied := range []bool{true, false} {
		require.NoError(t, model.DB.Create(&model.AdmissionDecision{ModuleID: uint(module), Version: i + 1, Applied: applied, Reason: "r"}).Error)
	}
	rec := performJSON(t, r, http.MethodGet, "/admissions?module_id="+itoa(module), "", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.EqualValues(t, 2, decodeData(t, rec.Body.Bytes())["total"])
	rec = performJSON(t, r, http.MethodGet, "/admissions?applied=true", "", nil)
	assert.EqualValues(t, 1, decodeData(t, rec.Body.Bytes())["total"])
}
