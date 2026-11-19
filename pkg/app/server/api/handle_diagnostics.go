package api

import (
	"github.com/cylonchau/kube-res-foresight/pkg/app/query"
	"github.com/cylonchau/kube-res-foresight/pkg/dao/rdb"
	"github.com/gin-gonic/gin"
)

// DiagnosticsRouter serves what an administrator needs to tell why a page is slow or a request ends in a 502 or 504: the pool of database
// connections of this process, the size of the tables that grow, the use of their indexes, the statements the database is running and which module
// writes the most. It reads the statistics the database keeps, never the rows, and holds nothing secret.
type DiagnosticsRouter struct {
	Diagnostics *rdb.DiagnosticsDAO
}

// Database serves GET /diagnostics/database.
// @Summary Database diagnostics
// @Tags Diagnostics
// @Router /api/v1/diagnostics/database [get]
func (r *DiagnosticsRouter) Database(c *gin.Context) {
	report, err := r.Diagnostics.Report(c.Request.Context())
	if err != nil {
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, report)
}
