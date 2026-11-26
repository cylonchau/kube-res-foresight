package api

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/cylonchau/kube-res-foresight/pkg/dao/rdb"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func registeredModule(t *testing.T, name string) uint {
	t.Helper()
	m := &model.ForecastModule{Name: name, Namespace: "shop", WorkloadKind: "Deployment", Workload: name, Mode: model.ModeObserve, AutoRegistered: true}
	require.NoError(t, rdb.NewModuleDAO(model.DB).Create(context.Background(), m))
	return m.ID
}

// a module the platform registered by itself is switched on only when it has the maximum of its replicas and a source; the form, the batch and a copy all follow the same rule
func TestARegisteredModuleCannotBeSwitchedOnUntilItIsComplete(t *testing.T) {
	r, cpu := forecastTestRouter(t)
	id := registeredModule(t, "auto-a")
	url := fmt.Sprintf("/modules/%d", id)

	got := decodeData(t, performJSON(t, r, http.MethodGet, url, "", nil).Body.Bytes())
	assert.Equal(t, true, got["auto_registered"])
	assert.ElementsMatch(t, []any{"max_replicas", "metric_sources"}, got["enable_blockers"].([]any))

	rec := performJSON(t, r, http.MethodPut, url, "", map[string]any{"enabled": true})
	require.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "maximum of replicas")
	assert.Contains(t, rec.Body.String(), "metric source")
	rec = performJSON(t, r, http.MethodPut, url, "", map[string]any{"enabled": true, "max_replicas": 6})
	require.Equal(t, http.StatusBadRequest, rec.Code, "the bound alone is not enough")
	assert.NotContains(t, rec.Body.String(), "maximum of replicas")
	still := decodeData(t, performJSON(t, r, http.MethodGet, url, "", nil).Body.Bytes())
	assert.Equal(t, false, still["enabled"])
	assert.EqualValues(t, 0, still["max_replicas"], "a refused update keeps nothing of it")

	// the batch follows the same rule
	batch := performJSON(t, r, http.MethodPost, "/modules/batch", "", map[string]any{"action": "enable", "ids": []uint{id}})
	require.Equal(t, http.StatusOK, batch.Code)
	assert.EqualValues(t, 0, decodeData(t, batch.Body.Bytes())["done"])

	// a module that is set up: its parameters and its CPU source are copied, and then it may be switched on
	ref := moduleWithCPU(t, r, cpu, map[string]any{"name": "ref", "workload": "ref", "min_replicas": 2, "max_replicas": 9, "scale_up_max_step": 2})
	cp := performJSON(t, r, http.MethodPost, url+"/copy-config", "", map[string]any{"from_id": ref})
	require.Equal(t, http.StatusOK, cp.Code, cp.Body.String())
	done := decodeData(t, performJSON(t, r, http.MethodGet, url, "", nil).Body.Bytes())
	assert.EqualValues(t, 9, done["max_replicas"])
	assert.Equal(t, "shop", done["namespace"], "who the module is stays")
	assert.Equal(t, false, done["enabled"], "copying does not switch it on")
	assert.Empty(t, done["enable_blockers"])
	assert.Equal(t, http.StatusOK, performJSON(t, r, http.MethodPut, url, "", map[string]any{"enabled": true}).Code)

	// now it is on: nothing is copied onto it, and a module made by hand was never held to the rule
	assert.Equal(t, http.StatusBadRequest, performJSON(t, r, http.MethodPost, url+"/copy-config", "", map[string]any{"from_id": ref}).Code)
	assert.Equal(t, http.StatusNotFound, performJSON(t, r, http.MethodPost, fmt.Sprintf("/modules/%d/copy-config", registeredModule(t, "auto-b")), "", map[string]any{"from_id": 9999}).Code)
}
