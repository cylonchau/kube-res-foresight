package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/clusteragent"
	"github.com/cylonchau/kube-res-foresight/pkg/clustersvc"
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

var clusterSeq int32

type clusterRig struct {
	r   *gin.Engine
	db  *gorm.DB
	svc *clustersvc.Service
}

// newClusterRig is the platform with one default cluster ("main"), the routes of the cluster page and the operator, and a stand-in for the credential middleware that
// says which cluster the access key is bound to (the header X-Test-Bound).
func newClusterRig(t *testing.T) *clusterRig {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:clapi%d?mode=memory&cache=shared", atomic.AddInt32(&clusterSeq, 1))), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(model.Models...))
	clusters := rdb.NewClusterDAO(db)
	_, err = clusters.EnsureDefault(context.Background(), "main")
	require.NoError(t, err)

	svc := &clustersvc.Service{DAO: clusters, Config: rdb.NewSysConfigDAO(db)}
	gate := &ClusterGate{Service: svc}
	modules := rdb.NewModuleDAO(db)
	ch := &ClusterRouter{Service: svc, Datasources: rdb.NewDatasourceDAO(db), Gate: gate}
	ih := &InventoryRouter{Service: &inventorysvc.Service{DAO: rdb.NewInventoryDAO(db), Modules: modules, Config: rdb.NewSysConfigDAO(db)},
		Forecast: forecastsvc.New(modules, rdb.NewMetricMappingDAO(db), rdb.NewDatasourceDAO(db), rdb.NewSampleDAO(db), rdb.NewPredictionDAO(db)), Gate: gate}
	rt := &RuntimeRouter{Modules: modules, Predictions: rdb.NewPredictionDAO(db), Config: rdb.NewSysConfigDAO(db), Gate: gate,
		Now: func() time.Time { return runtimeNow }}

	r := gin.New()
	r.Use(func(c *gin.Context) {
		if v := c.GetHeader("X-Test-Bound"); v != "" {
			c.Set("developerCluster", v)
		}
	})
	r.GET("/clusters", ch.List)
	r.POST("/clusters", ch.Create)
	r.GET("/cluster-keys", ch.Keys)
	r.PUT("/clusters/:id", ch.Update)
	r.DELETE("/clusters/:id", ch.Delete)
	r.GET("/clusters/:id/events", ch.Events)
	r.GET("/operator/cluster-settings", ch.OperatorSettings)
	r.POST("/operator/events", ch.OperatorEvents)
	r.POST("/operator/changes", ch.OperatorChanges)
	r.GET("/clusters/:id/changes", ch.Changes)
	r.GET("/clusters/:id/changes/:change", ch.Change)
	r.GET("/operator/modules", rt.OperatorModules)
	r.GET("/operator/decisions", rt.Decision)
	r.POST("/operator/reports", rt.Report)
	r.GET("/operator/inventory-targets", ih.Targets)
	r.POST("/operator/inventory", ih.Ingest)
	r.GET("/inventory/nodes", ih.Nodes)
	return &clusterRig{r: r, db: db, svc: svc}
}

// call sends a request; headers are name, value pairs.
func (c *clusterRig) call(t *testing.T, method, path string, body any, headers ...string) (int, map[string]any) {
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
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	w := httptest.NewRecorder()
	c.r.ServeHTTP(w, req)
	var out struct {
		Data map[string]any `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out.Data
}

func (c *clusterRig) autoModule(t *testing.T, name, cluster string) *model.ForecastModule {
	t.Helper()
	require.NoError(t, c.db.Where("config_key = ?", ConfigAllowAutoMode).Attrs(model.SysConfig{ConfigValue: "true", ConfigName: "x"}).FirstOrCreate(&model.SysConfig{ConfigKey: ConfigAllowAutoMode}).Error)
	m := &model.ForecastModule{Name: name, Cluster: cluster, Enabled: true, Mode: model.ModeAuto, Namespace: "prod", Workload: name, WorkloadKind: "Deployment",
		TTLSeconds: 900, HeadroomRatio: 1, Timezone: "UTC", MinReplicas: 1, MaxReplicas: 10, PredictEnabled: true,
		MinCPUMilli: 100, MaxCPUMilli: 4000, MinMemoryBytes: 64 << 20, MaxMemoryBytes: 4 << 30}
	require.NoError(t, c.db.Create(m).Error)
	return m
}

func (c *clusterRig) key(t *testing.T, name string) *model.SecretKey {
	t.Helper()
	k := &model.SecretKey{Name: name, KeyType: model.SecretKeyTypeDeveloperAccess, EncryptedValue: "x", MaskedValue: name}
	require.NoError(t, c.db.Create(k).Error)
	return k
}

func clusterID64(d map[string]any) float64 { return d["id"].(float64) }

func TestTheClusterPageRegistersChangesAndDeletesClusters(t *testing.T) {
	c := newClusterRig(t)
	k := c.key(t, "east-key")

	code, d := c.call(t, http.MethodPost, "/clusters", map[string]any{"name": "east", "description": "the east cluster", "namespace_include": []string{"Shop*"},
		"namespace_exclude": []string{"shop-test"}, "key_ids": []int64{k.ID}})
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, "east", d["name"])
	assert.Equal(t, []any{"shop*"}, d["namespace_include"])
	assert.Equal(t, []any{float64(k.ID)}, d["key_ids"])
	assert.Equal(t, false, d["is_default"])
	assert.Equal(t, true, d["enabled"], "a cluster is on unless it says otherwise")
	id := clusterID64(d)

	code, _ = c.call(t, http.MethodPost, "/clusters", map[string]any{"name": "east"})
	assert.Equal(t, http.StatusBadRequest, code, "the name is taken")
	code, _ = c.call(t, http.MethodPost, "/clusters", map[string]any{"name": "Bad Name"})
	assert.Equal(t, http.StatusBadRequest, code)
	code, _ = c.call(t, http.MethodPost, "/clusters", map[string]any{"name": "ds", "datasource_id": 99})
	assert.Equal(t, http.StatusBadRequest, code, "a data source that does not exist")

	code, d = c.call(t, http.MethodGet, "/clusters", nil)
	require.Equal(t, http.StatusOK, code)
	list := d["list"].([]any)
	require.Len(t, list, 2)
	assert.Equal(t, "main", list[0].(map[string]any)["name"], "the default cluster is first")
	assert.Equal(t, true, list[0].(map[string]any)["is_default"])

	// the namespaces and the keys are changed after the operator has connected
	code, d = c.call(t, http.MethodPut, fmt.Sprintf("/clusters/%d", int(id)), map[string]any{"name": "east", "enabled": true, "namespace_exclude": []string{"kube-system"}, "key_ids": []int64{}})
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, "east", d["name"])
	assert.Equal(t, []any{"kube-system"}, d["namespace_exclude"])
	assert.Empty(t, d["namespace_include"])
	assert.Empty(t, d["key_ids"])

	code, _ = c.call(t, http.MethodPut, fmt.Sprintf("/clusters/%d", int(id)), map[string]any{"namespace_include": []string{"has space"}})
	assert.Equal(t, http.StatusBadRequest, code)

	_, main := c.call(t, http.MethodGet, "/clusters", nil)
	mainID := main["list"].([]any)[0].(map[string]any)["id"].(float64)
	code, _ = c.call(t, http.MethodDelete, fmt.Sprintf("/clusters/%d", int(mainID)), nil)
	assert.Equal(t, http.StatusBadRequest, code, "the default cluster stays")

	c.autoModule(t, "api", "east")
	code, _ = c.call(t, http.MethodDelete, fmt.Sprintf("/clusters/%d", int(id)), nil)
	assert.Equal(t, http.StatusBadRequest, code, "a cluster with a module stays")
	require.NoError(t, c.db.Where("name = ?", "api").Delete(&model.ForecastModule{}).Error)
	code, _ = c.call(t, http.MethodDelete, fmt.Sprintf("/clusters/%d", int(id)), nil)
	assert.Equal(t, http.StatusOK, code)

	code, d = c.call(t, http.MethodGet, "/cluster-keys", nil)
	require.Equal(t, http.StatusOK, code)
	assert.Len(t, d["list"], 1)
}

func TestAnOperatorIsToldTheNamespacesTheUserSetAfterItConnected(t *testing.T) {
	c := newClusterRig(t)
	k := c.key(t, "east-key")
	_, d := c.call(t, http.MethodPost, "/clusters", map[string]any{"name": "east", "key_ids": []int64{k.ID}})
	id := int(clusterID64(d))
	bound := []string{"X-Test-Bound", "east", clusteragent.HeaderCluster, "east"}

	// connected, nothing set: it collects every namespace
	code, s := c.call(t, http.MethodGet, "/operator/cluster-settings", nil, bound...)
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, "east", s["cluster"])
	assert.Equal(t, true, s["collect_events"])
	ns := s["namespaces"].(map[string]any)
	assert.Empty(t, ns["include"])
	assert.Empty(t, ns["exclude"])

	_, list := c.call(t, http.MethodGet, "/clusters", nil)
	var row map[string]any
	for _, x := range list["list"].([]any) {
		if x.(map[string]any)["name"] == "east" {
			row = x.(map[string]any)
		}
	}
	assert.Equal(t, true, row["connected"], "the operator asked: the cluster shows as connected")
	assert.NotNil(t, row["last_seen_at"])

	// the user sets which namespaces after it is connected, and the next answer has them
	code, _ = c.call(t, http.MethodPut, fmt.Sprintf("/clusters/%d", id), map[string]any{"enabled": true, "namespace_include": []string{"shop", "billing*"}, "namespace_exclude": []string{"billing-test"}})
	require.Equal(t, http.StatusOK, code)
	_, s = c.call(t, http.MethodGet, "/operator/cluster-settings", nil, bound...)
	ns = s["namespaces"].(map[string]any)
	assert.Equal(t, []any{"billing*", "shop"}, ns["include"])
	assert.Equal(t, []any{"billing-test"}, ns["exclude"])
}

func TestACredentialIsOnlyGoodForItsOwnCluster(t *testing.T) {
	c := newClusterRig(t)
	_, _ = c.call(t, http.MethodPost, "/clusters", map[string]any{"name": "east"})

	// an operator of a platform that was upgraded: no cluster named, a key that is not bound -> the default cluster, as before
	code, s := c.call(t, http.MethodGet, "/operator/cluster-settings", nil)
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, "main", s["cluster"])

	code, _ = c.call(t, http.MethodGet, "/operator/cluster-settings", nil, clusteragent.HeaderCluster, "east")
	assert.Equal(t, http.StatusForbidden, code, "an unbound key cannot act for a cluster other than the default")
	code, _ = c.call(t, http.MethodGet, "/operator/cluster-settings", nil, "X-Test-Bound", "east", clusteragent.HeaderCluster, "main")
	assert.Equal(t, http.StatusForbidden, code, "a bound key cannot act for another cluster")
	code, _ = c.call(t, http.MethodGet, "/operator/cluster-settings", nil, "X-Test-Bound", "west")
	assert.Equal(t, http.StatusNotFound, code, "bound to a cluster that is not registered")

	var east model.Cluster
	require.NoError(t, c.db.Where("name = ?", "east").First(&east).Error)
	code, _ = c.call(t, http.MethodPut, fmt.Sprintf("/clusters/%d", east.ID), map[string]any{"enabled": false})
	require.Equal(t, http.StatusOK, code)
	code, _ = c.call(t, http.MethodGet, "/operator/cluster-settings", nil, "X-Test-Bound", "east")
	assert.Equal(t, http.StatusForbidden, code, "a cluster that is switched off")
}

func TestAnOperatorReadsAndScalesOnlyTheModulesOfItsOwnCluster(t *testing.T) {
	c := newClusterRig(t)
	_, _ = c.call(t, http.MethodPost, "/clusters", map[string]any{"name": "east"})
	c.autoModule(t, "old-module", "") // written before clusters: the default cluster's
	c.autoModule(t, "main-module", "main")
	c.autoModule(t, "east-module", "east")
	east := []string{"X-Test-Bound", "east"}

	names := func(path string, key string, headers ...string) []string {
		code, d := c.call(t, http.MethodGet, path, nil, headers...)
		require.Equal(t, http.StatusOK, code)
		var out []string
		for _, x := range d[key].([]any) {
			m := x.(map[string]any)
			if n, ok := m["name"]; ok {
				out = append(out, n.(string))
			}
		}
		return out
	}
	assert.ElementsMatch(t, []string{"old-module", "main-module"}, names("/operator/modules", "modules"), "an operator that names no cluster keeps scaling what it scaled")
	assert.Equal(t, []string{"east-module"}, names("/operator/modules", "modules", east...))
	assert.ElementsMatch(t, []string{"old-module", "main-module"}, names("/operator/inventory-targets", "targets"))
	assert.Equal(t, []string{"east-module"}, names("/operator/inventory-targets", "targets", east...))

	code, _ := c.call(t, http.MethodGet, "/operator/decisions?module=main-module", nil, east...)
	assert.Equal(t, http.StatusForbidden, code, "a decision of a module of another cluster is not given")
	code, _ = c.call(t, http.MethodGet, "/operator/decisions?module=east-module", nil, east...)
	assert.Equal(t, http.StatusOK, code)
	code, _ = c.call(t, http.MethodGet, "/operator/decisions?module=old-module", nil)
	assert.Equal(t, http.StatusOK, code, "the operator that names no cluster still gets the decisions of the module that names none")

	report := map[string]any{"module": "main-module", "namespace": "prod", "scaler": "op", "target_kind": "Deployment", "target_name": "main-module", "action": "none", "result": "observed"}
	code, _ = c.call(t, http.MethodPost, "/operator/reports", report, east...)
	assert.Equal(t, http.StatusForbidden, code, "a report about a module of another cluster is refused")
	code, _ = c.call(t, http.MethodPost, "/operator/reports", report)
	assert.Equal(t, http.StatusOK, code)
	var count int64
	require.NoError(t, c.db.Model(&model.ScalingExecution{}).Count(&count).Error)
	assert.EqualValues(t, 1, count, "only the report of the right cluster was stored")
}

func inventoryReportOf(id, node, module string, at time.Time) inventory.Snapshot {
	return inventory.Snapshot{RecordID: id, Operator: "op", ObservedAt: at,
		Nodes: []inventory.Node{{Name: node, Ready: true, Capacity: inventory.Capacity{CPUMilli: 4000, MemoryBytes: 16 << 30, Pods: 110}, Allocatable: inventory.Capacity{CPUMilli: 3900, MemoryBytes: 15 << 30, Pods: 110}}},
		Workloads: []inventory.Workload{{Module: module, Namespace: "prod", Kind: "Deployment", Name: module, Desired: 2, Ready: 2, Available: 2,
			Containers: []inventory.Container{{Name: "app"}}, Pods: []inventory.Pod{{Name: module + "-0", Node: node, Phase: "Running", Ready: true}}}}}
}

func TestTheNodesOfOneClusterAreNotTheNodesOfAnother(t *testing.T) {
	c := newClusterRig(t)
	_, _ = c.call(t, http.MethodPost, "/clusters", map[string]any{"name": "east"})
	c.autoModule(t, "main-module", "")
	c.autoModule(t, "east-module", "east")
	east := []string{"X-Test-Bound", "east"}
	at := time.Now().UTC().Add(-time.Minute)

	code, res := c.call(t, http.MethodPost, "/operator/inventory", inventoryReportOf("r-main", "node-main", "main-module", at))
	require.Equal(t, http.StatusOK, code)
	assert.EqualValues(t, 1, res["workloads"])
	code, res = c.call(t, http.MethodPost, "/operator/inventory", inventoryReportOf("r-east", "node-east", "east-module", at), east...)
	require.Equal(t, http.StatusOK, code)
	assert.EqualValues(t, 1, res["workloads"])

	// an operator that reports a workload of a module of another cluster is not believed
	code, res = c.call(t, http.MethodPost, "/operator/inventory", inventoryReportOf("r-mixed", "node-east", "main-module", at.Add(time.Second)), east...)
	require.Equal(t, http.StatusOK, code)
	assert.EqualValues(t, 0, res["workloads"])
	assert.Equal(t, []any{"main-module"}, res["unknown"])

	nodeNames := func(path string) []string {
		code, d := c.call(t, http.MethodGet, path, nil)
		require.Equal(t, http.StatusOK, code)
		out := []string{}
		for _, n := range d["nodes"].([]any) {
			out = append(out, n.(map[string]any)["node"].(string))
		}
		return out
	}
	assert.Equal(t, []string{"node-main"}, nodeNames("/inventory/nodes"), "the page asks about the default cluster unless it names one")
	assert.Equal(t, []string{"node-east"}, nodeNames("/inventory/nodes?cluster=east"))
	code, _ = c.call(t, http.MethodGet, "/inventory/nodes?cluster=nowhere", nil)
	assert.Equal(t, http.StatusNotFound, code)
}

func TestEventsAreStoredReadAndKeptPerClusterAndNotSentToAnotherClusterPage(t *testing.T) {
	c := newClusterRig(t)
	_, d := c.call(t, http.MethodPost, "/clusters", map[string]any{"name": "east"})
	eastID := int(clusterID64(d))
	var def model.Cluster
	require.NoError(t, c.db.Where("is_default = ?", true).First(&def).Error)
	east := []string{"X-Test-Bound", "east"}
	now := time.Now().UTC()
	ev := func(uid, reason, component string, count int) clusteragent.Event {
		return clusteragent.Event{UID: uid, Type: "Normal", Reason: reason, Message: reason + " said", Component: component, Count: count, Kind: "Node", Name: "n1",
			FirstAt: now.Add(-2 * time.Minute), LastAt: now.Add(-time.Minute)}
	}

	code, res := c.call(t, http.MethodPost, "/operator/events", clusteragent.Batch{Operator: "op-east-1", Events: []clusteragent.Event{
		ev("e1", "ScaleDown", "cluster-autoscaler", 1), ev("e2", "NodeNotReady", "node-controller", 2), ev("e3", "Odd", "something-new", 1)}}, east...)
	require.Equal(t, http.StatusOK, code)
	assert.EqualValues(t, 3, res["added"])
	code, res = c.call(t, http.MethodPost, "/operator/events", clusteragent.Batch{Events: []clusteragent.Event{ev("e1", "Other", "kubelet", 1)}})
	require.Equal(t, http.StatusOK, code)
	assert.EqualValues(t, 1, res["added"], "the main cluster's event of the same uid is another event")

	code, d = c.call(t, http.MethodGet, fmt.Sprintf("/clusters/%d/events", eastID), nil)
	require.Equal(t, http.StatusOK, code)
	assert.EqualValues(t, 3, d["total"])
	assert.EqualValues(t, 3, d["retention_days"], "the events of 3 days")
	byUID := map[string]map[string]any{}
	for _, x := range d["list"].([]any) {
		m := x.(map[string]any)
		byUID[m["uid"].(string)] = m
	}
	assert.Equal(t, "auto", byUID["e1"]["origin"])
	assert.Equal(t, "autoscaler", byUID["e1"]["category"])
	assert.Equal(t, "ScaleDown", byUID["e1"]["reason"])
	assert.Equal(t, "unclassified", byUID["e3"]["origin"])

	code, d = c.call(t, http.MethodGet, fmt.Sprintf("/clusters/%d/events?origin=unclassified", eastID), nil)
	require.Equal(t, http.StatusOK, code)
	assert.EqualValues(t, 1, d["total"])
	code, d = c.call(t, http.MethodGet, fmt.Sprintf("/clusters/%d/events", def.ID), nil)
	require.Equal(t, http.StatusOK, code)
	assert.EqualValues(t, 1, d["total"], "the default cluster's page shows its own events only")

	// refused: no uid; too many
	code, _ = c.call(t, http.MethodPost, "/operator/events", clusteragent.Batch{Events: []clusteragent.Event{{UID: "", LastAt: now}}}, east...)
	assert.Equal(t, http.StatusBadRequest, code)
	big := clusteragent.Batch{}
	for i := 0; i <= clusteragent.MaxBatchEvents; i++ {
		big.Events = append(big.Events, ev(fmt.Sprintf("b%d", i), "A", "kubelet", 1))
	}
	code, _ = c.call(t, http.MethodPost, "/operator/events", big, east...)
	assert.Equal(t, http.StatusBadRequest, code)

	// a key bound to the other cluster cannot send events for this one
	code, _ = c.call(t, http.MethodPost, "/operator/events", clusteragent.Batch{Events: []clusteragent.Event{ev("x", "A", "kubelet", 1)}}, "X-Test-Bound", "main", clusteragent.HeaderCluster, "east")
	assert.Equal(t, http.StatusForbidden, code)
}

func TestAModuleCanOnlyBeInARegisteredCluster(t *testing.T) {
	c := newClusterRig(t)
	_, _ = c.call(t, http.MethodPost, "/clusters", map[string]any{"name": "east"})
	h := &ForecastRouter{Clusters: rdb.NewClusterDAO(c.db)}
	got, err := h.checkCluster(context.Background(), "")
	assert.NoError(t, err, "empty is the default cluster")
	assert.Equal(t, "", got)
	got, err = h.checkCluster(context.Background(), "main")
	assert.NoError(t, err)
	assert.Equal(t, "", got, "naming the default cluster is the same as naming none")
	got, err = h.checkCluster(context.Background(), " east ")
	assert.NoError(t, err)
	assert.Equal(t, "east", got)
	_, err = h.checkCluster(context.Background(), "west")
	assert.ErrorContains(t, err, "not registered")
	got, err = (&ForecastRouter{}).checkCluster(context.Background(), "anything")
	assert.NoError(t, err, "a process that registers no clusters does not check")
	assert.Equal(t, "anything", got)
}

func TestTheSameWorkloadCanBeAModuleInTwoClustersButNotTwiceInOne(t *testing.T) {
	c := newClusterRig(t)
	_, _ = c.call(t, http.MethodPost, "/clusters", map[string]any{"name": "east"})
	modules := rdb.NewModuleDAO(c.db)
	mk := func(name, cluster string) error {
		m := &model.ForecastModule{Name: name, Cluster: cluster, Enabled: true, Mode: model.ModeObserve, Namespace: "prod", Workload: "api", WorkloadKind: "Deployment",
			Timezone: "UTC", LookbackHours: 72, HorizonMinutes: 15, MaxReplicas: 10, MinReplicas: 1, HeadroomRatio: 1.1, PodCapacityRPS: 10, TTLSeconds: 900, MaxFillSteps: 3}
		require.NoError(t, m.Validate())
		return modules.Create(context.Background(), m)
	}
	require.NoError(t, mk("api-main", ""))
	require.NoError(t, mk("api-east", "east"), "the same namespace, kind and workload name in another cluster")
	assert.ErrorIs(t, mk("api-east-2", "east"), rdb.ErrWorkloadTaken, "but not twice in one cluster")
	assert.ErrorIs(t, mk("api-main-2", ""), rdb.ErrWorkloadTaken, "nor twice in the default cluster")
	assert.ErrorIs(t, mk("api-main-3", "main"), rdb.ErrWorkloadTaken, "naming the default cluster is the same as naming none")
}

func TestChangesAreTakenFromTheBoundOperatorListedPerClusterAndExplained(t *testing.T) {
	c := newClusterRig(t)
	_, d := c.call(t, http.MethodPost, "/clusters", map[string]any{"name": "east"})
	eastID := int(clusterID64(d))
	var def model.Cluster
	require.NoError(t, c.db.Where("is_default = ?", true).First(&def).Error)
	east := []string{"X-Test-Bound", "east"}
	at := time.Now().UTC().Add(-20 * time.Minute)

	node := clusteragent.Change{RecordID: "n1-ready", Kind: "Node", Name: "ip-10-0-1-9", ObjectUID: "uid-n", Type: "ready", From: "True", To: "False", OccurredAt: at, ObservedAt: at,
		Reasons:  []clusteragent.Reason{{Source: "node_condition", Reason: "KubeletNotReady", Message: "runtime down", Component: "Ready"}},
		Snapshot: &clusteragent.Snapshot{PodCount: 1, Capacity: &clusteragent.Res{CPUMilli: 4000}}}
	pod := clusteragent.Change{RecordID: "p-evicted", Kind: "Pod", Namespace: "shop", Name: "web-1", Type: "status", To: "Evicted", OccurredAt: at.Add(time.Minute), ObservedAt: at.Add(time.Minute),
		Node: "ip-10-0-1-9", Workload: "web", Reasons: []clusteragent.Reason{{Source: "pod_status", Reason: "Evicted", Message: "The node ip-10-0-1-9 was low on resource: memory."}}}
	code, res := c.call(t, http.MethodPost, "/operator/changes", clusteragent.ChangeBatch{Operator: "op-east-1", Changes: []clusteragent.Change{node, pod}}, east...)
	require.Equal(t, http.StatusOK, code)
	assert.EqualValues(t, 2, res["added"])
	code, res = c.call(t, http.MethodPost, "/operator/changes", clusteragent.ChangeBatch{Changes: []clusteragent.Change{node}}, east...)
	require.Equal(t, http.StatusOK, code)
	assert.EqualValues(t, 0, res["added"], "a retry is the same change")

	code, d = c.call(t, http.MethodGet, fmt.Sprintf("/clusters/%d/changes", eastID), nil)
	require.Equal(t, http.StatusOK, code)
	assert.EqualValues(t, 2, d["total"])
	list := d["list"].([]any)
	byName := map[string]map[string]any{}
	for _, x := range list {
		m := x.(map[string]any)
		byName[m["name"].(string)] = m
	}
	assert.Equal(t, "False", byName["ip-10-0-1-9"]["to"])
	assert.Equal(t, "KubeletNotReady", byName["ip-10-0-1-9"]["reasons"].([]any)[0].(map[string]any)["reason"])
	assert.Equal(t, "pending", byName["ip-10-0-1-9"]["usage_status"])
	assert.EqualValues(t, 3, d["retention_days"])

	code, d = c.call(t, http.MethodGet, fmt.Sprintf("/clusters/%d/changes?kind=Pod", eastID), nil)
	require.Equal(t, http.StatusOK, code)
	assert.EqualValues(t, 1, d["total"])
	code, d = c.call(t, http.MethodGet, fmt.Sprintf("/clusters/%d/changes?search=KubeletNotReady", eastID), nil)
	require.Equal(t, http.StatusOK, code)
	assert.EqualValues(t, 1, d["total"], "the reasons are searched")
	code, d = c.call(t, http.MethodGet, fmt.Sprintf("/clusters/%d/changes", def.ID), nil)
	require.Equal(t, http.StatusOK, code)
	assert.EqualValues(t, 0, d["total"], "the default cluster does not see the changes of another")

	// explained: the Pod's reason names the node, so the node change is its cause
	podID := byName["web-1"]["id"].(float64)
	code, d = c.call(t, http.MethodGet, fmt.Sprintf("/clusters/%d/changes/%d", eastID, int(podID)), nil)
	require.Equal(t, http.StatusOK, code)
	causes := d["causes"].([]any)
	require.Len(t, causes, 1)
	link := causes[0].(map[string]any)
	assert.Equal(t, "caused_by", link["relation"])
	assert.Equal(t, "cause", link["direction"])
	assert.Contains(t, link["evidence"], "ip-10-0-1-9")
	assert.Equal(t, "ip-10-0-1-9", link["change"].(map[string]any)["name"])

	// another cluster's change is not found under this one
	nodeID := byName["ip-10-0-1-9"]["id"].(float64)
	code, _ = c.call(t, http.MethodGet, fmt.Sprintf("/clusters/%d/changes/%d", def.ID, int(nodeID)), nil)
	assert.Equal(t, http.StatusNotFound, code)
	code, _ = c.call(t, http.MethodGet, fmt.Sprintf("/clusters/%d/changes/abc", eastID), nil)
	assert.Equal(t, http.StatusBadRequest, code)

	// refused: a kind that is not watched; a key bound to another cluster
	code, _ = c.call(t, http.MethodPost, "/operator/changes", clusteragent.ChangeBatch{Changes: []clusteragent.Change{{RecordID: "s", Kind: "Secret", Name: "s", Type: "t", OccurredAt: at}}}, east...)
	assert.Equal(t, http.StatusBadRequest, code)
	code, _ = c.call(t, http.MethodPost, "/operator/changes", clusteragent.ChangeBatch{Changes: []clusteragent.Change{node}}, "X-Test-Bound", "main", clusteragent.HeaderCluster, "east")
	assert.Equal(t, http.StatusForbidden, code)

	// the operator is told to collect changes unless the platform says otherwise
	code, s := c.call(t, http.MethodGet, "/operator/cluster-settings", nil, east...)
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, true, s["collect_changes"])
}

func TestAClusterCanBeRenamedTheDefaultOneToo(t *testing.T) {
	c := newClusterRig(t)
	k := c.key(t, "east-key")
	_, d := c.call(t, http.MethodPost, "/clusters", map[string]any{"name": "east", "key_ids": []int64{k.ID}})
	id := int(clusterID64(d))
	c.autoModule(t, "east-module", "east")
	code, _ := c.call(t, http.MethodPost, "/operator/events", clusteragent.Batch{Events: []clusteragent.Event{{UID: "e1", Reason: "R", Component: "kubelet", Kind: "Node", Name: "n", LastAt: time.Now().UTC().Add(-time.Minute), Count: 1}}}, "X-Test-Bound", "east")
	require.Equal(t, http.StatusOK, code)

	code, d = c.call(t, http.MethodPut, fmt.Sprintf("/clusters/%d", id), map[string]any{"name": "prod-east", "enabled": true})
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, "prod-east", d["name"])
	assert.Equal(t, []any{float64(k.ID)}, d["key_ids"], "the key follows the cluster")
	assert.EqualValues(t, 1, d["modules"], "so does the module")
	assert.EqualValues(t, 1, d["events"], "and the events")

	// an operator started with the new name works with the key; one started with the old name is told the cluster is not there
	code, s := c.call(t, http.MethodGet, "/operator/cluster-settings", nil, "X-Test-Bound", "prod-east", clusteragent.HeaderCluster, "prod-east")
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, "prod-east", s["cluster"])
	code, _ = c.call(t, http.MethodGet, "/operator/cluster-settings", nil, "X-Test-Bound", "prod-east", clusteragent.HeaderCluster, "east")
	assert.Equal(t, http.StatusForbidden, code, "the key is bound to the new name")
	code, _ = c.call(t, http.MethodGet, "/operator/modules", nil, "X-Test-Bound", "prod-east")
	require.Equal(t, http.StatusOK, code)

	// the default cluster: renamed, and what names none is still its
	c.autoModule(t, "old-module", "")
	var def model.Cluster
	require.NoError(t, c.db.Where("is_default = ?", true).First(&def).Error)
	code, d = c.call(t, http.MethodPut, fmt.Sprintf("/clusters/%d", def.ID), map[string]any{"name": "prod-main", "enabled": true})
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, "prod-main", d["name"])
	assert.Equal(t, true, d["is_default"])
	code, s = c.call(t, http.MethodGet, "/operator/cluster-settings", nil)
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, "prod-main", s["cluster"], "an operator that names no cluster is the default one, whatever it is called")
	code, _ = c.call(t, http.MethodGet, "/operator/cluster-settings", nil, clusteragent.HeaderCluster, "main")
	assert.Equal(t, http.StatusForbidden, code, "the old name is not it any more")
	code, _ = c.call(t, http.MethodGet, "/operator/cluster-settings", nil, clusteragent.HeaderCluster, "prod-main")
	assert.Equal(t, http.StatusOK, code)
	code, d = c.call(t, http.MethodGet, "/operator/modules", nil)
	require.Equal(t, http.StatusOK, code)
	assert.Len(t, d["modules"], 1, "the module that names no cluster is still the default cluster's")

	// refused: a name in use, a name that is not one
	code, _ = c.call(t, http.MethodPut, fmt.Sprintf("/clusters/%d", id), map[string]any{"name": "prod-main", "enabled": true})
	assert.Equal(t, http.StatusBadRequest, code)
	code, _ = c.call(t, http.MethodPut, fmt.Sprintf("/clusters/%d", id), map[string]any{"name": "Bad Name", "enabled": true})
	assert.Equal(t, http.StatusBadRequest, code)
	// the platform's own name for its cluster follows the default one
	var cfg model.SysConfig
	if err := c.db.Where("config_key = ?", "cluster.name").First(&cfg).Error; err == nil {
		assert.Equal(t, "prod-main", cfg.ConfigValue)
	}
}
