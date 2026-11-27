package api

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cylonchau/kube-res-foresight/pkg/dao/rdb"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
)

func nodePoolEngine() *gin.Engine {
	np := &NodePoolRouter{DAO: rdb.NewNodePoolDAO(model.DB)}
	rt := &RuntimeRouter{Modules: rdb.NewModuleDAO(model.DB), Predictions: rdb.NewPredictionDAO(model.DB), NodePools: rdb.NewNodePoolDAO(model.DB), Config: rdb.NewSysConfigDAO(model.DB),
		Now: func() time.Time { return runtimeNow }}
	e := gin.New()
	e.GET("/node-pools", np.List)
	e.POST("/node-pools", np.Create)
	e.GET("/node-pools/:id", np.Get)
	e.PUT("/node-pools/:id", np.Update)
	e.DELETE("/node-pools/:id", np.Delete)
	e.GET("/admission/recommendations", rt.Admission)
	e.GET("/operator/decisions", rt.Decision)
	return e
}

// A node pool is made, listed and changed; a module that uses pools has the Node Selector of the recommended pool in the admission answer and the Pod spec in the decision of the operator.
func TestNodePoolsAreManagedAndSteerAdmissionAndTheOperator(t *testing.T) {
	_, cpu := forecastTestRouter(t)
	e := nodePoolEngine()

	rec := performJSON(t, e, http.MethodPost, "/node-pools", "", map[string]any{"name": "mem-4c32g", "kind": "memory", "node_selector": map[string]string{"pool": "mem"},
		"node_cpu_milli": 4000, "node_memory_bytes": 32 << 30})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	id := int(answer(t, rec.Body.Bytes())["id"].(float64))
	assert.Equal(t, http.StatusBadRequest, performJSON(t, e, http.MethodPost, "/node-pools", "", map[string]any{"name": "mem-4c32g", "kind": "memory",
		"node_selector": map[string]string{"pool": "x"}, "node_cpu_milli": 4000, "node_memory_bytes": 32 << 30}).Code, "the name is taken in the cluster")
	assert.Equal(t, http.StatusBadRequest, performJSON(t, e, http.MethodPost, "/node-pools", "", map[string]any{"name": "no-labels", "kind": "cpu"}).Code, "the labels are required")
	list := answer(t, performJSON(t, e, http.MethodGet, "/node-pools?kind=memory", "", nil).Body.Bytes())
	assert.EqualValues(t, 1, list["total"])

	m := seedModule(t, cpu.ID, model.ModeAuto, func(x *model.ForecastModule) { x.NodePoolEnabled, x.LimitMemoryRatio = true, 1.5 })
	seedRecommendation(t, m, func(r *model.Recommendation) { r.NodePoolID, r.NodePool = uint(id), "mem-4c32g" })

	data := answer(t, performJSON(t, e, http.MethodGet, "/admission/recommendations?namespace=prod&workload=api&container=app&request_uid="+nextUID(), "", nil).Body.Bytes())
	assert.Equal(t, true, data["apply"])
	assert.Equal(t, "mem-4c32g", data["node_pool"])
	assert.Equal(t, map[string]any{"pool": "mem"}, data["node_selector"])

	decision := answer(t, performJSON(t, e, http.MethodGet, "/operator/decisions?module="+m.Name, "", nil).Body.Bytes())
	require.Equal(t, true, decision["available"], fmt.Sprint(decision["reason"]))
	spec, ok := decision["pod_spec"].(map[string]any)
	require.True(t, ok, "an auto module's decision carries the Pod spec")
	assert.EqualValues(t, 1500, spec["cpu_milli"])
	assert.EqualValues(t, 512<<20, spec["memory_bytes"])
	assert.EqualValues(t, 768<<20, spec["limit_memory_bytes"], "the Limit the module manages: the request times its ratio")
	assert.Equal(t, map[string]any{"pool": "mem"}, spec["node_selector"])

	// switched off, the pool is not recommended any more: the Node Selector goes from both answers
	require.Equal(t, http.StatusOK, performJSON(t, e, http.MethodPut, fmt.Sprintf("/node-pools/%d", id), "", map[string]any{"enabled": false}).Code)
	data = answer(t, performJSON(t, e, http.MethodGet, "/admission/recommendations?namespace=prod&workload=api&container=app&request_uid="+nextUID(), "", nil).Body.Bytes())
	assert.Nil(t, data["node_selector"])
	assert.Equal(t, http.StatusOK, performJSON(t, e, http.MethodDelete, fmt.Sprintf("/node-pools/%d", id), "", nil).Code)
}
