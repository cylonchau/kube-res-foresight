package api

import (
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cylonchau/kube-resource-fcst/pkg/dao/rdb"
	"github.com/cylonchau/kube-resource-fcst/pkg/model"
)

func presetTestRouter(t *testing.T) (*gin.Engine, *model.Datasource) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db := apiTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.Datasource{}, &model.MetricMapping{}, &model.ForecastModule{PredictEnabled: true}, &model.AlgorithmConfig{},
		&model.ModulePeriod{}, &model.MetricSeries{}, &model.MetricSample{}, &model.Prediction{}, &model.BacktestRun{}, &model.Recommendation{},
		&model.PredictionEvaluation{}, &model.ScalingExecution{}, &model.MetricSource{}, &model.AlgorithmScore{}, &model.AdmissionDecision{}, &model.TrainingRun{}, &model.ScaleOutcome{}, &model.ModuleRunState{}))
	ds := &model.Datasource{Name: "prom", Type: "prometheus", BaseURL: "http://prom:9090", AuthType: "none", TimeoutSeconds: 30, MaxConcurrency: 1, Enabled: true, Status: "unknown"}
	require.NoError(t, db.Create(ds).Error)
	h := &PresetRouter{Datasources: rdb.NewDatasourceDAO(db), Mappings: rdb.NewMetricMappingDAO(db)}
	r := gin.New()
	r.GET("/metric-presets", h.List)
	r.POST("/metric-presets/:id/apply", h.Apply)
	return r, ds
}

func TestPresetListFiltersAndCarriesSourceAndLimits(t *testing.T) {
	r, _ := presetTestRouter(t)
	rec := performJSON(t, r, http.MethodGet, "/metric-presets?metric=rps", "", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	data := decodeData(t, rec.Body.Bytes())
	list := data["list"].([]any)
	require.NotEmpty(t, list)
	for _, item := range list {
		p := item.(map[string]any)
		assert.Equal(t, "rps", p["metric"])
		assert.NotEmpty(t, p["source"].(map[string]any)["component"])
		assert.NotEmpty(t, p["limitations"].(map[string]any)["en"])
		assert.Equal(t, false, p["verified"])
	}
	all := decodeData(t, performJSON(t, r, http.MethodGet, "/metric-presets", "", nil).Body.Bytes())["total"].(float64)
	assert.Greater(t, all, float64(len(list)))
}

func TestApplyPresetCreatesAnEditableMappingThatRemembersItsPreset(t *testing.T) {
	r, ds := presetTestRouter(t)
	rec := performJSON(t, r, http.MethodPost, "/metric-presets/cadvisor.cpu.workload/apply", "", map[string]any{"datasource_id": ds.ID, "step_seconds": 120})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	got := decodeData(t, rec.Body.Bytes())
	assert.Equal(t, "cadvisor.cpu.workload", got["preset_id"])
	assert.Equal(t, "cpu", got["metric"])
	assert.EqualValues(t, 120, got["step_seconds"])

	// the same name on the same datasource is refused; another name works
	assert.Equal(t, http.StatusBadRequest, performJSON(t, r, http.MethodPost, "/metric-presets/cadvisor.cpu.workload/apply", "", map[string]any{"datasource_id": ds.ID}).Code)
	assert.Equal(t, http.StatusOK, performJSON(t, r, http.MethodPost, "/metric-presets/cadvisor.cpu.workload/apply", "", map[string]any{"datasource_id": ds.ID, "name": "cpu-2"}).Code)

	// the PromQL the template generates can be edited on the way in; it is validated like any mapping
	edited := `sum(rate(container_cpu_usage_seconds_total{namespace="{{namespace}}",pod=~"{{workload}}-.*"}[{{range}}]))`
	rec = performJSON(t, r, http.MethodPost, "/metric-presets/cadvisor.cpu.workload/apply", "", map[string]any{"datasource_id": ds.ID, "name": "cpu-edited", "query": edited})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, edited, decodeData(t, rec.Body.Bytes())["query"])
	assert.Equal(t, http.StatusBadRequest, performJSON(t, r, http.MethodPost, "/metric-presets/cadvisor.cpu.workload/apply", "", map[string]any{"datasource_id": ds.ID, "name": "cpu-bad", "query": "up{x=\"{{nope}}\"}"}).Code)
	assert.Equal(t, http.StatusNotFound, performJSON(t, r, http.MethodPost, "/metric-presets/nope/apply", "", map[string]any{"datasource_id": ds.ID}).Code)
	assert.Equal(t, http.StatusBadRequest, performJSON(t, r, http.MethodPost, "/metric-presets/cadvisor.cpu.workload/apply", "", map[string]any{"datasource_id": 9999}).Code)
}
