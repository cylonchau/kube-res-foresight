package api

import (
	"github.com/cylonchau/kube-res-foresight/pkg/app/query"
	"github.com/cylonchau/kube-res-foresight/pkg/forecastsvc"
	"github.com/cylonchau/kube-res-foresight/pkg/grafana"
	"github.com/cylonchau/kube-res-foresight/pkg/inventorysvc"
	"github.com/gin-gonic/gin"
)

// GrafanaRouter serves what the resource pages need to link to Grafana, and the dashboard itself (P10 A4).
type GrafanaRouter struct {
	Inventory *inventorysvc.Service
	Forecast  *forecastsvc.Service
}

// Settings serves GET /grafana/settings: the address of the Grafana, the uid of the dashboard and the name of the cluster, which the pages build the "open in
// Grafana" links with; they have no link while the address is empty.
// @Summary Grafana settings for the links of the pages
// @Tags Grafana
// @Router /api/v1/grafana/settings [get]
func (r *GrafanaRouter) Settings(c *gin.Context) {
	query.SuccessResponse(c, nil, r.Inventory.Grafana(c.Request.Context()))
}

// Dashboard serves GET /grafana/dashboard: the resource dashboard as JSON to import into Grafana, built from the queries of the system parameters (query.*), the
// same the pages ask, with the cluster name and the links back to the platform when its address is set.
// @Summary The resource dashboard of Grafana
// @Tags Grafana
// @Router /api/v1/grafana/dashboard [get]
func (r *GrafanaRouter) Dashboard(c *gin.Context) {
	ctx := c.Request.Context()
	cfg := r.Inventory.Grafana(ctx)
	raw, err := grafana.JSON(grafana.Options{UID: cfg.UID, Queries: r.Forecast.Queries(ctx), Cluster: cfg.Cluster, PlatformURL: cfg.PlatformURL})
	if err != nil {
		query.InternalError(c, err)
		return
	}
	c.Header("Content-Disposition", `attachment; filename="`+grafana.ShippedName+`"`)
	c.Data(200, "application/json; charset=utf-8", raw)
}
