package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cylonchau/kube-res-foresight/pkg/dao/rdb"
	"github.com/cylonchau/kube-res-foresight/pkg/forecastsvc"
	"github.com/cylonchau/kube-res-foresight/pkg/inventorysvc"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newGrafanaRig(t *testing.T) (*gin.Engine, *inventoryRig) {
	t.Helper()
	rig := newInventoryRig(t)
	modules := rdb.NewModuleDAO(rig.db)
	h := &GrafanaRouter{Inventory: &inventorysvc.Service{DAO: rdb.NewInventoryDAO(rig.db), Modules: modules, Config: rdb.NewSysConfigDAO(rig.db)},
		Forecast: forecastsvc.New(modules, rdb.NewMetricMappingDAO(rig.db), rdb.NewDatasourceDAO(rig.db), rdb.NewSampleDAO(rig.db), rdb.NewPredictionDAO(rig.db))}
	r := gin.New()
	r.GET("/grafana/settings", h.Settings)
	r.GET("/grafana/dashboard", h.Dashboard)
	return r, rig
}

func TestTheGrafanaSettingsHaveNoLinkUntilTheAddressIsSetAndThenTrimItsSlash(t *testing.T) {
	r, rig := newGrafanaRig(t)
	get := func() map[string]any {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/grafana/settings", nil))
		require.Equal(t, http.StatusOK, w.Code)
		var out struct {
			Data map[string]any `json:"data"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out))
		return out.Data
	}
	d := get()
	assert.Equal(t, false, d["configured"])
	assert.Equal(t, "kube-res-foresight-resources", d["dashboard_uid"])
	assert.Equal(t, "default", d["cluster"])

	require.NoError(t, rig.db.Create(&model.SysConfig{ConfigKey: inventorysvc.KeyGrafanaURL, ConfigValue: "https://grafana.example.com/", ConfigName: "grafana"}).Error)
	d = get()
	assert.Equal(t, true, d["configured"])
	assert.Equal(t, "https://grafana.example.com", d["base_url"])
}

func TestTheDashboardIsDownloadedAsJSONBuiltFromTheQueriesOfTheSystem(t *testing.T) {
	r, _ := newGrafanaRig(t)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/grafana/dashboard", nil))
	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Header().Get("Content-Disposition"), "kube-res-foresight-resources.json")
	var d struct {
		UID    string `json:"uid"`
		Panels []any  `json:"panels"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &d))
	assert.Equal(t, "kube-res-foresight-resources", d.UID)
	assert.Greater(t, len(d.Panels), 25)
}
