package api

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/app/query"
	"github.com/cylonchau/kube-res-foresight/pkg/assistant"
	"github.com/cylonchau/kube-res-foresight/pkg/forecastsvc"
	"github.com/cylonchau/kube-res-foresight/pkg/inventory"
	"github.com/cylonchau/kube-res-foresight/pkg/inventorysvc"
	"github.com/gin-gonic/gin"
)

// InventoryRouter serves what the operator reports of the cluster and of the workloads of the modules (P10 A1), and the pages that show it. The operator
// reads the cluster read-only; the platform keeps what it was told, and asks Prometheus for what is used (never the cluster).
type InventoryRouter struct {
	Service  *inventorysvc.Service
	Forecast *forecastsvc.Service
}

// maxInventoryBody is the biggest report the platform takes: a report is a few hundred KiB for a cluster of hundreds of nodes and modules.
const maxInventoryBody = 8 << 20

// Targets serves GET /operator/inventory-targets: the modules the operator reads the workload of (every enabled module, whatever its mode).
// @Summary The workloads the operator reads
// @Tags Inventory
// @Router /api/v1/operator/inventory-targets [get]
func (r *InventoryRouter) Targets(c *gin.Context) {
	assistant.MarkOperatorSeen(time.Now())
	targets, err := r.Service.Targets(c.Request.Context())
	if err != nil {
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, gin.H{"targets": targets})
}

// Ingest serves POST /operator/inventory: one report of the operator. A report with a record id that was taken is a repeat and is answered as taken.
// @Summary Take a report of the cluster inventory
// @Tags Inventory
// @Router /api/v1/operator/inventory [post]
func (r *InventoryRouter) Ingest(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxInventoryBody)
	var snap inventory.Snapshot
	if err := c.ShouldBindJSON(&snap); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			query.ErrorResponse(c, http.StatusRequestEntityTooLarge, errors.New("the report is bigger than the platform takes"))
			return
		}
		query.BadRequest(c, err)
		return
	}
	res, err := r.Service.Ingest(c.Request.Context(), snap)
	if err != nil {
		if errors.Is(err, inventorysvc.ErrTooOld) || isInventoryInvalid(err) {
			query.BadRequest(c, err)
			return
		}
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, res)
}

// isInventoryInvalid is a report that does not pass its own checks (see inventory.Snapshot.Validate).
func isInventoryInvalid(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, inventory.ErrInvalid)
}

// Nodes serves GET /inventory/nodes: the cluster as the newest report of the operator saw it.
// @Summary The nodes of the cluster
// @Tags Inventory
// @Router /api/v1/inventory/nodes [get]
func (r *InventoryRouter) Nodes(c *gin.Context) {
	out, err := r.Service.Nodes(c.Request.Context())
	if err != nil {
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, out)
}

// NodeTrend serves GET /inventory/nodes/trend?hours=: the number of nodes and of those ready at every report.
// @Summary The trend of the nodes
// @Tags Inventory
// @Router /api/v1/inventory/nodes/trend [get]
func (r *InventoryRouter) NodeTrend(c *gin.Context) {
	hours, _ := strconv.Atoi(c.Query("hours"))
	out, err := r.Service.NodeTrend(c.Request.Context(), hours)
	if err != nil {
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, out)
}

// NodeUsage serves GET /inventory/nodes/usage: what each node uses and has been asked for right now, asked of the Prometheus data source (live, not stored).
// A data source that cannot be read is told in place, not as a failure of the page.
// @Summary Live figures of the nodes
// @Tags Inventory
// @Router /api/v1/inventory/nodes/usage [get]
func (r *InventoryRouter) NodeUsage(c *gin.Context) {
	out, err := r.Forecast.NodeUsageOf(c.Request.Context(), uintQuery(c, "datasource_id"))
	if err != nil {
		query.ErrorResponse(c, http.StatusBadGateway, err)
		return
	}
	query.SuccessResponse(c, nil, out)
}

// WorkloadSummary is a workload as the table of the dashboard shows it.
type WorkloadSummary struct {
	ModuleID  uint   `json:"module_id"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Desired   int    `json:"desired"`
	Ready     int    `json:"ready"`
	Missing   bool   `json:"missing"`
	Stale     bool   `json:"stale"`
	HPAKind   string `json:"hpa_kind"`
	HPAMin    int32  `json:"hpa_min"`
	HPAMax    int32  `json:"hpa_max"`
	Pods      int    `json:"pods"`
	Restarts  int    `json:"restarts"`
	CPUReq    int64  `json:"cpu_request_milli"`
	CPULimit  int64  `json:"cpu_limit_milli"`
	MemReq    int64  `json:"memory_request_bytes"`
	MemLimit  int64  `json:"memory_limit_bytes"`
	Reported  bool   `json:"reported"`
	Namespace string `json:"namespace"`
}

// Workloads serves GET /inventory/workloads: the workload of every module as the operator last reported it, one line each (requests and limits are the sums
// over the containers of one Pod).
// @Summary The workloads of the modules
// @Tags Inventory
// @Router /api/v1/inventory/workloads [get]
func (r *InventoryRouter) Workloads(c *gin.Context) {
	all, err := r.Service.Workloads(c.Request.Context())
	if err != nil {
		query.InternalError(c, err)
		return
	}
	out := make([]WorkloadSummary, 0, len(all))
	for _, w := range all {
		s := WorkloadSummary{ModuleID: w.ModuleID, Kind: w.Kind, Name: w.Name, Namespace: w.Namespace, Desired: w.Desired, Ready: w.Ready, Missing: w.Missing, Stale: w.Stale, Pods: len(w.Pods),
			Reported: true}
		for _, p := range w.Pods {
			s.Restarts += p.Restarts
		}
		for _, ct := range w.Containers {
			s.CPUReq += ct.Requests.CPUMilli
			s.CPULimit += ct.Limits.CPUMilli
			s.MemReq += ct.Requests.MemoryBytes
			s.MemLimit += ct.Limits.MemoryBytes
		}
		if w.HPA != nil {
			s.HPAKind, s.HPAMin, s.HPAMax = w.HPA.Kind, w.HPA.Min, w.HPA.Max
		}
		out = append(out, s)
	}
	query.SuccessResponse(c, nil, out)
}

// ModuleInventory serves GET /modules/:id/inventory?hours=: the workload of a module as the operator reported it (containers, Pods, HPA), the history of the
// changes of its configuration, and the trend of its replicas.
// @Summary The workload of a module as the operator reported it
// @Tags Inventory
// @Router /api/v1/modules/{id}/inventory [get]
func (r *InventoryRouter) ModuleInventory(c *gin.Context) {
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	if _, err := r.Forecast.Modules.Get(c.Request.Context(), id); err != nil {
		query.NotFound(c, err)
		return
	}
	hours, _ := strconv.Atoi(c.Query("hours"))
	out, err := r.Service.Workload(c.Request.Context(), id, hours)
	if err != nil {
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, out)
}
