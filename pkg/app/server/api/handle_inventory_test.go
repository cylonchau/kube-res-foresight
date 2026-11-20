package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/dao/rdb"
	"github.com/cylonchau/kube-res-foresight/pkg/forecastsvc"
	"github.com/cylonchau/kube-res-foresight/pkg/inventory"
	"github.com/cylonchau/kube-res-foresight/pkg/inventorysvc"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var invSeq int32

type inventoryRig struct {
	r      *gin.Engine
	db     *gorm.DB
	module *model.ForecastModule
}

func newInventoryRig(t *testing.T) *inventoryRig {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:invapi%d?mode=memory&cache=shared", atomic.AddInt32(&invSeq, 1))), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(model.Models...))
	m := &model.ForecastModule{Name: "web", Namespace: "prod", Workload: "web", WorkloadKind: "Deployment", Enabled: true, Mode: model.ModeObserve, Timezone: "UTC", LookbackHours: 72,
		HorizonMinutes: 15, MaxReplicas: 10, MinReplicas: 1, HeadroomRatio: 1.1, PodCapacityRPS: 10, TTLSeconds: 900, MaxFillSteps: 3}
	require.NoError(t, m.Validate())
	require.NoError(t, db.Create(m).Error)
	modules := rdb.NewModuleDAO(db)
	h := &InventoryRouter{Service: &inventorysvc.Service{DAO: rdb.NewInventoryDAO(db), Modules: modules, Config: rdb.NewSysConfigDAO(db)},
		Forecast: forecastsvc.New(modules, rdb.NewMetricMappingDAO(db), rdb.NewDatasourceDAO(db), rdb.NewSampleDAO(db), rdb.NewPredictionDAO(db))}
	r := gin.New()
	r.GET("/operator/inventory-targets", h.Targets)
	r.POST("/operator/inventory", h.Ingest)
	r.GET("/inventory/nodes", h.Nodes)
	r.GET("/inventory/nodes/trend", h.NodeTrend)
	r.GET("/inventory/nodes/usage", h.NodeUsage)
	r.GET("/inventory/workloads", h.Workloads)
	r.GET("/modules/:id/inventory", h.ModuleInventory)
	return &inventoryRig{r: r, db: db, module: m}
}

func (i *inventoryRig) call(t *testing.T, method, path string, body any) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		require.NoError(t, err)
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	i.r.ServeHTTP(w, req)
	var out struct {
		Data map[string]any `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w, out.Data
}

func report(id string, at time.Time) inventory.Snapshot {
	return inventory.Snapshot{RecordID: id, Operator: "op-1", ObservedAt: at,
		Nodes: []inventory.Node{{Name: "n1", Ready: true, Provider: "aws", Zone: "us-east-1a", Capacity: inventory.Capacity{CPUMilli: 4000, MemoryBytes: 16 << 30, Pods: 110},
			Allocatable: inventory.Capacity{CPUMilli: 3900, MemoryBytes: 15 << 30, Pods: 110}, Pods: 2}},
		Workloads: []inventory.Workload{{Module: "web", Namespace: "prod", Kind: "Deployment", Name: "web", Desired: 3, Ready: 3, Available: 3,
			Containers: []inventory.Container{{Name: "app", Requests: inventory.Resources{CPUMilli: 500, MemoryBytes: 512 << 20}, Limits: inventory.Resources{CPUMilli: 1000, MemoryBytes: 1 << 30}}},
			Pods:       []inventory.Pod{{Name: "web-0", Node: "n1", Phase: "Running", Ready: true, Restarts: 2}},
			HPA:        &inventory.HPA{Kind: "hpa", Name: "web", Min: 2, Max: 10, Current: 3, Desired: 3}}}}
}

func TestTheOperatorIsToldWhichWorkloadsToReadAndReportsThemOnce(t *testing.T) {
	rig := newInventoryRig(t)
	w, data := rig.call(t, http.MethodGet, "/operator/inventory-targets", nil)
	require.Equal(t, http.StatusOK, w.Code)
	targets := data["targets"].([]any)
	require.Len(t, targets, 1)
	assert.Equal(t, "web", targets[0].(map[string]any)["name"])
	assert.Equal(t, "Deployment", targets[0].(map[string]any)["workload_kind"])

	now := time.Now().UTC()
	w, data = rig.call(t, http.MethodPost, "/operator/inventory", report("rec-1", now))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Equal(t, false, data["duplicate"])
	assert.EqualValues(t, 1, data["nodes"])
	assert.EqualValues(t, 1, data["workloads"])
	assert.EqualValues(t, 1, data["changes"], "the first reading is the first entry of the history")

	w, data = rig.call(t, http.MethodPost, "/operator/inventory", report("rec-1", now))
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, true, data["duplicate"], "the answer was lost and the operator sent it again: it is acknowledged, not stored twice")
	var n int64
	require.NoError(t, rig.db.Model(&model.ClusterNodeSnapshot{}).Count(&n).Error)
	assert.EqualValues(t, 1, n)
}

func TestAReportThatIsNotValidOrToBigIsRefusedForGood(t *testing.T) {
	rig := newInventoryRig(t)
	w, _ := rig.call(t, http.MethodPost, "/operator/inventory", inventory.Snapshot{ObservedAt: time.Now()})
	assert.Equal(t, http.StatusBadRequest, w.Code, "no record id: sending it again cannot mend it")
	tooMany := report("big", time.Now())
	tooMany.Nodes = make([]inventory.Node, inventory.MaxNodes+1)
	w, _ = rig.call(t, http.MethodPost, "/operator/inventory", tooMany)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	old := report("old", time.Now().AddDate(0, 0, -90))
	w, _ = rig.call(t, http.MethodPost, "/operator/inventory", old)
	assert.Equal(t, http.StatusBadRequest, w.Code, "from before the snapshots are kept")

	huge := strings.NewReader(`{"record_id":"x","nodes":[{"name":"` + strings.Repeat("a", maxInventoryBody) + `"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/operator/inventory", huge)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	rig.r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)

	req = httptest.NewRequest(http.MethodPost, "/operator/inventory", strings.NewReader("{not json"))
	rec = httptest.NewRecorder()
	rig.r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestThePagesReadTheNodesTheTrendTheWorkloadsAndTheWorkloadOfAModule(t *testing.T) {
	rig := newInventoryRig(t)
	_, empty := rig.call(t, http.MethodGet, "/inventory/nodes", nil)
	assert.Equal(t, false, empty["reported"], "nothing is reported yet")
	assert.NotNil(t, empty["nodes"], "a list, never null")

	now := time.Now().UTC()
	rig.call(t, http.MethodPost, "/operator/inventory", report("a", now.Add(-2*time.Hour)))
	changed := report("b", now.Add(-time.Hour))
	changed.Workloads[0].Desired = 5
	changed.Nodes[0].Ready = false
	rig.call(t, http.MethodPost, "/operator/inventory", changed)

	_, nodes := rig.call(t, http.MethodGet, "/inventory/nodes", nil)
	assert.Equal(t, true, nodes["reported"])
	totals := nodes["totals"].(map[string]any)
	assert.EqualValues(t, 1, totals["nodes"])
	assert.EqualValues(t, 0, totals["ready"], "the newest report says n1 is not ready")
	assert.Equal(t, "default", nodes["cluster"], "the cluster name is a system parameter with a default")

	w, trend := rig.call(t, http.MethodGet, "/inventory/nodes/trend?hours=24", nil)
	require.Equal(t, http.StatusOK, w.Code)
	var points []map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &struct {
		Data *[]map[string]any `json:"data"`
	}{&points}))
	_ = trend
	require.Len(t, points, 2)
	assert.EqualValues(t, 1, points[0]["ready"])
	assert.EqualValues(t, 0, points[1]["ready"])

	w, _ = rig.call(t, http.MethodGet, "/inventory/workloads", nil)
	var list []map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &struct {
		Data *[]map[string]any `json:"data"`
	}{&list}))
	require.Len(t, list, 1)
	assert.EqualValues(t, 5, list[0]["desired"])
	assert.Equal(t, "hpa", list[0]["hpa_kind"])
	assert.EqualValues(t, 10, list[0]["hpa_max"])
	assert.EqualValues(t, 500, list[0]["cpu_request_milli"])
	assert.EqualValues(t, 2, list[0]["restarts"])

	w, mod := rig.call(t, http.MethodGet, fmt.Sprintf("/modules/%d/inventory?hours=24", rig.module.ID), nil)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, true, mod["reported"])
	assert.EqualValues(t, 5, mod["desired"])
	assert.Len(t, mod["containers"], 1)
	assert.Len(t, mod["pods"], 1)
	assert.Len(t, mod["trend"], 2)
	changes := mod["changes"].([]any)
	require.Len(t, changes, 2)
	assert.Contains(t, changes[0].(map[string]any)["summary"], "replicas 3 → 5")

	w, _ = rig.call(t, http.MethodGet, "/modules/9999/inventory", nil)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestTheLiveFiguresOfTheNodesSayWhenThereIsNoDataSourceToAskInsteadOfFailingThePage(t *testing.T) {
	rig := newInventoryRig(t)
	w, body := rig.call(t, http.MethodGet, "/inventory/nodes/usage", nil)
	assert.Equal(t, http.StatusOK, w.Code, "no data source: an answer with the reason, so the page shows it where the figures would be")
	assert.Equal(t, "no_datasource", body["reason"])
	assert.Contains(t, body["message"], "Metric Templates")
}
