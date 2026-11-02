package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cylonchau/kube-res-foresight/pkg/dao/rdb"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func datasourceTestRouter(t *testing.T) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db := apiTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.Datasource{}, &model.MetricMapping{}, &model.MetricSource{}))
	previous := model.DB
	model.DB = db
	t.Cleanup(func() { model.DB = previous })
	router := gin.New()
	datasources := &DatasourceRouter{DAO: rdb.NewDatasourceDAO(db)}
	mappings := &MetricMappingRouter{DAO: rdb.NewMetricMappingDAO(db)}
	router.GET("/datasources", datasources.List)
	router.GET("/datasources/options", datasources.Options)
	router.POST("/datasources", datasources.Create)
	router.POST("/datasources/batch-delete", datasources.BatchDelete)
	router.GET("/datasources/:id", datasources.Get)
	router.PUT("/datasources/:id", datasources.Update)
	router.DELETE("/datasources/:id", datasources.Delete)
	router.POST("/datasources/:id/test", datasources.Test)
	router.GET("/metrics/catalog", mappings.Catalog)
	router.GET("/metric-mappings", mappings.List)
	router.POST("/metric-mappings", mappings.Create)
	router.POST("/metric-mappings/batch-delete", mappings.BatchDelete)
	router.GET("/metric-mappings/:id", mappings.Get)
	router.PUT("/metric-mappings/:id", mappings.Update)
	router.DELETE("/metric-mappings/:id", mappings.Delete)
	return router
}

func fakePromServer(t *testing.T, wantToken string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/query", func(w http.ResponseWriter, r *http.Request) {
		if wantToken != "" && r.Header.Get("Authorization") != "Bearer "+wantToken {
			http.Error(w, "denied", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1700000000,"1"]}]}}`))
	})
	mux.HandleFunc("/api/v1/status/buildinfo", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success","data":{"version":"2.53.0"}}`))
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func decodeData(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(body, &envelope), string(body))
	return envelope.Data
}

func TestDatasourceLifecycleHidesSecretsAndTestsConnection(t *testing.T) {
	router := datasourceTestRouter(t)
	prom := fakePromServer(t, "s3cret")

	rec := performJSON(t, router, http.MethodPost, "/datasources", "", map[string]any{
		"name": "prod", "base_url": prom.URL + "/", "auth_type": "bearer", "bearer_token": "s3cret",
	})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.NotContains(t, rec.Body.String(), "s3cret")
	created := decodeData(t, rec.Body.Bytes())
	id := int(created["id"].(float64))
	assert.Equal(t, true, created["has_bearer_token"])
	assert.Equal(t, "unknown", created["status"])
	assert.Equal(t, prom.URL, created["base_url"])

	var stored model.Datasource
	require.NoError(t, model.DB.First(&stored, id).Error)
	assert.NotEmpty(t, stored.BearerTokenEncrypted)
	assert.NotContains(t, stored.BearerTokenEncrypted, "s3cret")

	rec = performJSON(t, router, http.MethodPost, fmt.Sprintf("/datasources/%d/test", id), "", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	result := decodeData(t, rec.Body.Bytes())
	assert.Equal(t, "ok", result["status"], rec.Body.String())
	assert.Equal(t, "2.53.0", result["server_version"])

	rec = performJSON(t, router, http.MethodGet, fmt.Sprintf("/datasources/%d", id), "", nil)
	got := decodeData(t, rec.Body.Bytes())
	assert.Equal(t, "ok", got["status"])
	assert.NotNil(t, got["last_checked_at"])

	// Wrong token: the probe is reported as an error result, not an HTTP failure.
	rec = performJSON(t, router, http.MethodPut, fmt.Sprintf("/datasources/%d", id), "", map[string]any{"bearer_token": "wrong"})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "unknown", decodeData(t, rec.Body.Bytes())["status"], "changing credentials resets the probe result")
	rec = performJSON(t, router, http.MethodPost, fmt.Sprintf("/datasources/%d/test", id), "", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "error", decodeData(t, rec.Body.Bytes())["status"])
	assert.NotContains(t, rec.Body.String(), "wrong")

	// Blank secret keeps the stored one; unrelated edits keep the probe result.
	rec = performJSON(t, router, http.MethodPut, fmt.Sprintf("/datasources/%d", id), "", map[string]any{"bearer_token": "", "remark": "edited"})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "error", decodeData(t, rec.Body.Bytes())["status"])
	assert.Equal(t, true, decodeData(t, rec.Body.Bytes())["has_bearer_token"])
}

func TestDatasourceValidationAndUniqueness(t *testing.T) {
	router := datasourceTestRouter(t)
	bad := []map[string]any{
		{"name": "a", "base_url": "not-a-url"},
		{"name": "a", "base_url": "http://user:pw@prom:9090"},
		{"name": "a", "base_url": "http://prom:9090", "auth_type": "basic", "username": "u"},
		{"name": "a", "base_url": "http://prom:9090", "auth_type": "bearer"},
		{"name": "a", "base_url": "http://prom:9090", "auth_type": "digest"},
		{"name": "a", "base_url": "http://prom:9090", "type": "influx"},
		{"name": "a", "base_url": "http://prom:9090", "timeout_seconds": 9999},
		{"name": "a", "base_url": "https://prom:9090", "ca_cert": "garbage"},
		{"name": "  ", "base_url": "http://prom:9090"},
	}
	for _, body := range bad {
		rec := performJSON(t, router, http.MethodPost, "/datasources", "", body)
		assert.Equal(t, http.StatusBadRequest, rec.Code, fmt.Sprintf("%v -> %s", body, rec.Body.String()))
	}
	ok := map[string]any{"name": "dup", "base_url": "http://prom:9090"}
	assert.Equal(t, http.StatusOK, performJSON(t, router, http.MethodPost, "/datasources", "", ok).Code)
	assert.Equal(t, http.StatusBadRequest, performJSON(t, router, http.MethodPost, "/datasources", "", ok).Code)
}

// The number of concurrent requests has no ceiling of the platform's own: 16 was an arbitrary limit and the field accepts
// whatever the operator knows the Prometheus can take. Zero means the default and a negative number is refused.
func TestDatasourceConcurrencyHasNoUpperLimit(t *testing.T) {
	router := datasourceTestRouter(t)
	for i, n := range []int{1, 16, 17, 64, 500} {
		body := map[string]any{"name": fmt.Sprintf("c%d", i), "base_url": "http://prom:9090", "max_concurrency": n}
		rec := performJSON(t, router, http.MethodPost, "/datasources", "", body)
		require.Equal(t, http.StatusOK, rec.Code, "%d: %s", n, rec.Body.String())
		assert.EqualValues(t, n, decodeData(t, rec.Body.Bytes())["max_concurrency"])
	}
	rec := performJSON(t, router, http.MethodPost, "/datasources", "", map[string]any{"name": "neg", "base_url": "http://prom:9090", "max_concurrency": -3})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	rec = performJSON(t, router, http.MethodPost, "/datasources", "", map[string]any{"name": "zero", "base_url": "http://prom:9090"})
	require.Equal(t, http.StatusOK, rec.Code)
	assert.EqualValues(t, 1, decodeData(t, rec.Body.Bytes())["max_concurrency"], "unset is the default of one")
	id := int(decodeData(t, performJSON(t, router, http.MethodPost, "/datasources", "", map[string]any{"name": "up", "base_url": "http://prom:9090", "max_concurrency": 2}).Body.Bytes())["id"].(float64))
	rec = performJSON(t, router, http.MethodPut, fmt.Sprintf("/datasources/%d", id), "", map[string]any{"max_concurrency": 40})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.EqualValues(t, 40, decodeData(t, rec.Body.Bytes())["max_concurrency"], "an update above 16 is saved too")
}

func TestDatasourceAuthSwitchClearsSecretsAndRequiresNewOnes(t *testing.T) {
	router := datasourceTestRouter(t)
	rec := performJSON(t, router, http.MethodPost, "/datasources", "", map[string]any{
		"name": "ds", "base_url": "http://prom:9090", "auth_type": "basic", "username": "u", "password": "p",
	})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	id := int(decodeData(t, rec.Body.Bytes())["id"].(float64))

	rec = performJSON(t, router, http.MethodPut, fmt.Sprintf("/datasources/%d", id), "", map[string]any{"auth_type": "bearer"})
	assert.Equal(t, http.StatusBadRequest, rec.Code, "switching to bearer needs a token")

	rec = performJSON(t, router, http.MethodPut, fmt.Sprintf("/datasources/%d", id), "", map[string]any{"auth_type": "none"})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	data := decodeData(t, rec.Body.Bytes())
	assert.Equal(t, false, data["has_password"])
	assert.Equal(t, "", data["username"])
}

func TestMetricMappingCRUDAndDatasourceInUse(t *testing.T) {
	router := datasourceTestRouter(t)
	rec := performJSON(t, router, http.MethodPost, "/datasources", "", map[string]any{"name": "prod", "base_url": "http://prom:9090"})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	dsID := int(decodeData(t, rec.Body.Bytes())["id"].(float64))

	mapping := map[string]any{
		"datasource_id": dsID, "name": "cpu-workload", "metric": "cpu", "level": "workload",
		"query": `sum(rate(container_cpu_usage_seconds_total{namespace="{{namespace}}",pod=~"{{workload}}-.*"}[{{range}}]))`,
	}
	rec = performJSON(t, router, http.MethodPost, "/metric-mappings", "", mapping)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	created := decodeData(t, rec.Body.Bytes())
	mappingID := int(created["id"].(float64))
	assert.Equal(t, "cores", created["unit"])
	assert.Equal(t, "prod", created["datasource_name"])
	assert.ElementsMatch(t, []any{"namespace", "range", "workload"}, created["variables"])

	assert.Equal(t, http.StatusBadRequest, performJSON(t, router, http.MethodPost, "/metric-mappings", "", mapping).Code, "duplicate name in datasource")
	missingDS := map[string]any{"datasource_id": 9999, "name": "x", "metric": "cpu", "level": "cluster", "query": "sum(up)"}
	assert.Equal(t, http.StatusNotFound, performJSON(t, router, http.MethodPost, "/metric-mappings", "", missingDS).Code)
	invalid := map[string]any{"datasource_id": dsID, "name": "bad", "metric": "cpu", "level": "workload", "query": "sum(up)"}
	assert.Equal(t, http.StatusBadRequest, performJSON(t, router, http.MethodPost, "/metric-mappings", "", invalid).Code)

	rec = performJSON(t, router, http.MethodGet, fmt.Sprintf("/metric-mappings?datasource_id=%d&metric=cpu&enabled=true&keyword=cpu", dsID), "", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.EqualValues(t, 1, decodeData(t, rec.Body.Bytes())["total"])

	rec = performJSON(t, router, http.MethodPut, fmt.Sprintf("/metric-mappings/%d", mappingID), "", map[string]any{"metric": "memory", "level": "cluster", "query": "sum(container_memory_working_set_bytes)"})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "bytes", decodeData(t, rec.Body.Bytes())["unit"], "unit follows the new metric")

	rec = performJSON(t, router, http.MethodGet, "/datasources", "", nil)
	list := decodeData(t, rec.Body.Bytes())["list"].([]any)
	assert.EqualValues(t, 1, list[0].(map[string]any)["mapping_count"])

	rec = performJSON(t, router, http.MethodDelete, fmt.Sprintf("/datasources/%d", dsID), "", nil)
	assert.Equal(t, http.StatusBadRequest, rec.Code, "datasource with mappings cannot be deleted")

	// a template a module metric still uses is refused with 409, whether it is deleted alone or in a batch, and goes once free
	source := &model.MetricSource{ModuleID: 1, Key: "memory", Name: "memory", Kind: "memory", Resource: "memory", MappingID: uint(mappingID), Stat: "p95", Enabled: true}
	require.NoError(t, model.DB.Create(source).Error)
	rec = performJSON(t, router, http.MethodDelete, fmt.Sprintf("/metric-mappings/%d", mappingID), "", nil)
	assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	rec = performJSON(t, router, http.MethodPost, "/metric-mappings/batch-delete", "", map[string]any{"ids": []int{mappingID}})
	assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	require.NoError(t, model.DB.Delete(source).Error)

	rec = performJSON(t, router, http.MethodPost, "/metric-mappings/batch-delete", "", map[string]any{"ids": []int{mappingID}})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rec = performJSON(t, router, http.MethodDelete, fmt.Sprintf("/datasources/%d", dsID), "", nil)
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, http.StatusNotFound, performJSON(t, router, http.MethodGet, fmt.Sprintf("/datasources/%d", dsID), "", nil).Code)
}

func TestMetricCatalogAndOptions(t *testing.T) {
	router := datasourceTestRouter(t)
	rec := performJSON(t, router, http.MethodGet, "/metrics/catalog", "", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	data := decodeData(t, rec.Body.Bytes())
	assert.Len(t, data["metrics"], len(model.MetricKinds))
	assert.Len(t, data["levels"], len(model.MetricLevels))

	require.Equal(t, http.StatusOK, performJSON(t, router, http.MethodPost, "/datasources", "", map[string]any{"name": "off", "base_url": "http://p:9090", "enabled": false}).Code)
	require.Equal(t, http.StatusOK, performJSON(t, router, http.MethodPost, "/datasources", "", map[string]any{"name": "on", "base_url": "http://p:9090"}).Code)
	rec = performJSON(t, router, http.MethodGet, "/datasources/options", "", nil)
	var envelope struct {
		Data []map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope))
	require.Len(t, envelope.Data, 1)
	assert.Equal(t, "on", envelope.Data[0]["name"])
}
