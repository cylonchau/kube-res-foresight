package middleware

import (
	"net/http"
	"strings"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/app/query"
	"github.com/cylonchau/kube-res-foresight/pkg/reqstat"
	"github.com/gin-gonic/gin"
)

// Measure records how long every request took, how it ended and how much of the time was in the database and in Prometheus (P11 B0). The statistics
// are what the diagnostics page reads to say which route is slow and in which layer. The context of the request carries a span the database callbacks
// and the Prometheus client add their time to. It sits before ReadDeadline: the context it looks at to see whether the other side gave up is the one the
// server made, not the one with the deadline the platform put on it, which is cancelled when the handler returns. The streams are left out.
func Measure(rec *reqstat.Recorder) gin.HandlerFunc {
	return func(c *gin.Context) {
		if rec == nil {
			c.Next()
			return
		}
		original := c.Request.Context()
		ctx, span := reqstat.WithSpan(original)
		c.Request = c.Request.WithContext(ctx)
		start := time.Now()
		c.Next()
		route := c.FullPath()
		if route == "" || strings.Contains(route, "/stream") {
			return
		}
		dbMs, dbCalls, promMs, promCalls := span.Totals()
		status := c.Writer.Status()
		rec.Observe(reqstat.Observation{
			Method: c.Request.Method, Route: route, Status: status, Took: time.Since(start),
			DBMs: dbMs, DBCalls: dbCalls, PromMs: promMs, PromCalls: promCalls,
			RequestID:       c.GetString("requestId"),
			Abandoned:       original.Err() != nil,
			PlatformTimeout: status == http.StatusGatewayTimeout && c.Writer.Header().Get(query.TimeoutSourceHeader) == "platform",
			At:              start,
		})
	}
}
