package api

import (
	"context"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cylonchau/kube-res-foresight/pkg/cost"
	"github.com/cylonchau/kube-res-foresight/pkg/dao/rdb"
	"github.com/cylonchau/kube-res-foresight/pkg/forecastsvc"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/cylonchau/kube-res-foresight/pkg/promclient"
)

func idOf(v any) string { return strconv.Itoa(int(v.(float64))) }

type noCluster struct{}

func (noCluster) QueryVector(context.Context, string, time.Time) ([]promclient.VectorSample, error) {
	return nil, assert.AnError
}

func costTestRouter(t *testing.T) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db := apiTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.Datasource{}, &model.MetricMapping{}, &model.ForecastModule{PredictEnabled: true}, &model.AlgorithmConfig{},
		&model.ModulePeriod{}, &model.MetricSeries{}, &model.MetricSample{}, &model.Prediction{}, &model.BacktestRun{}, &model.Recommendation{},
		&model.PredictionEvaluation{}, &model.ScalingExecution{}, &model.MetricSource{}, &model.AlgorithmScore{}, &model.AdmissionDecision{}, &model.TrainingRun{},
		&model.ScaleOutcome{}, &model.ModuleRunState{}, &model.CostPriceRule{}, &model.SysConfig{}))
	require.NoError(t, db.Create(&model.Datasource{Name: "prom", Type: "prometheus", BaseURL: "http://prom:9090", AuthType: "none", TimeoutSeconds: 30, MaxConcurrency: 1, Enabled: true, Status: "unknown"}).Error)
	svc := forecastsvc.New(rdb.NewModuleDAO(db), rdb.NewMetricMappingDAO(db), rdb.NewDatasourceDAO(db), rdb.NewSampleDAO(db), rdb.NewPredictionDAO(db))
	svc.NewCostQuerier = func(*model.Datasource) (cost.Querier, error) { return noCluster{}, nil }
	h := &CostRouter{Prices: rdb.NewCostPriceDAO(db), Service: svc}
	r := gin.New()
	g := r.Group("/cost")
	g.GET("/prices", h.ListPrices)
	g.POST("/prices", h.CreatePrice)
	g.PUT("/prices/:id", h.UpdatePrice)
	g.DELETE("/prices/:id", h.DeletePrice)
	g.GET("/overview", h.Overview)
	g.GET("/modules", h.Modules)
	g.POST("/simulate", h.Simulate)
	return r
}

func TestPriceRulesAreValidatedStoredAndKeepOnlyTheFieldsOfTheirKind(t *testing.T) {
	r := costTestRouter(t)

	for name, body := range map[string]map[string]any{
		"no name":      {"match_kind": "all", "hourly_price": 1, "currency": "USD"},
		"no currency":  {"name": "x", "match_kind": "all", "hourly_price": 1},
		"negative":     {"name": "x", "match_kind": "all", "hourly_price": -1, "currency": "USD"},
		"label no key": {"name": "x", "match_kind": "label", "hourly_price": 1, "currency": "USD"},
		"bad pattern":  {"name": "x", "match_kind": "name", "name_pattern": "[", "hourly_price": 1, "currency": "USD"},
	} {
		assert.Equal(t, http.StatusBadRequest, performJSON(t, r, http.MethodPost, "/cost/prices", "", body).Code, name)
	}

	rec := performJSON(t, r, http.MethodPost, "/cost/prices", "", map[string]any{"name": "big", "match_kind": "label",
		"label_key": "node.kubernetes.io/instance-type", "label_value": "m5.xlarge", "name_pattern": "ignored", "hourly_price": 0.192, "currency": "usd", "priority": 5})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	got := decodeData(t, rec.Body.Bytes())
	assert.Equal(t, "USD", got["currency"])
	assert.Equal(t, "", got["name_pattern"], "a label rule keeps no name pattern")
	assert.Equal(t, true, got["enabled"], "a new rule prices nodes")
	id := got["id"]

	// the name is unique
	assert.Equal(t, http.StatusBadRequest, performJSON(t, r, http.MethodPost, "/cost/prices", "", map[string]any{"name": "big", "match_kind": "all", "hourly_price": 1, "currency": "USD"}).Code)

	// an update switches it off and to another kind; the old label does not linger
	rec = performJSON(t, r, http.MethodPut, "/cost/prices/"+idOf(id), "", map[string]any{"name": "big", "match_kind": "name", "name_pattern": "gpu-*",
		"label_key": "left over", "hourly_price": 3, "currency": "CNY", "enabled": false})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	updated := decodeData(t, rec.Body.Bytes())
	assert.Equal(t, "gpu-*", updated["name_pattern"])
	assert.Equal(t, "", updated["label_key"])
	assert.Equal(t, false, updated["enabled"])

	list := decodeData(t, performJSON(t, r, http.MethodGet, "/cost/prices", "", nil).Body.Bytes())
	assert.EqualValues(t, 1, list["total"])

	assert.Equal(t, http.StatusNotFound, performJSON(t, r, http.MethodPut, "/cost/prices/9999", "", map[string]any{"name": "x", "match_kind": "all", "currency": "USD"}).Code)
	assert.Equal(t, http.StatusOK, performJSON(t, r, http.MethodDelete, "/cost/prices/"+idOf(id), "", nil).Code)
	assert.Equal(t, http.StatusNotFound, performJSON(t, r, http.MethodDelete, "/cost/prices/"+idOf(id), "", nil).Code)
}

func TestTheCostCallsAnswerWhatIsMissingAsTheMessageNotAsAFailure(t *testing.T) {
	r := costTestRouter(t)
	// no data source chosen: the person is asked to choose
	assert.Equal(t, http.StatusBadRequest, performJSON(t, r, http.MethodGet, "/cost/overview", "", nil).Code)
	// a data source that cannot be read: a bad gateway, not a server error of ours
	assert.Equal(t, http.StatusBadGateway, performJSON(t, r, http.MethodGet, "/cost/overview?datasource_id=1", "", nil).Code)
	assert.Equal(t, http.StatusBadGateway, performJSON(t, r, http.MethodPost, "/cost/simulate", "", map[string]any{"datasource_id": 1, "level": "balanced"}).Code)
	assert.Equal(t, http.StatusBadRequest, performJSON(t, r, http.MethodPost, "/cost/simulate", "", map[string]any{"datasource_id": 1, "level": "reckless"}).Code)
	assert.Equal(t, http.StatusBadRequest, performJSON(t, r, http.MethodPost, "/cost/simulate", "", map[string]any{"datasource_id": 1, "level": "balanced", "window_days": 99}).Code)
	assert.Equal(t, http.StatusBadRequest, performJSON(t, r, http.MethodGet, "/cost/modules?window_days=99", "", nil).Code)

	// the waste of the modules needs no cluster: with none it is told without money
	rec := performJSON(t, r, http.MethodGet, "/cost/modules", "", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	got := decodeData(t, rec.Body.Bytes())
	assert.EqualValues(t, 7, got["window_days"])
	assert.Equal(t, "no_price_rules", got["money_note"])
	assert.Empty(t, got["modules"])
}
