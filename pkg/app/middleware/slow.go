package middleware

import (
	"github.com/cylonchau/kube-res-foresight/pkg/logger"
	"github.com/gin-gonic/gin"
	"strings"
	"time"
)

// SlowRequests logs the requests that take longer than a limit, with the route (not the query: it can hold keys), the
// status and the time, so the page that times out in a large installation can be named instead of guessed. The streams
// (WebSocket) are long by nature and are left out.
func SlowRequests(limit time.Duration) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		took := time.Since(start)
		path := c.FullPath()
		if took < limit || strings.Contains(path, "/stream") {
			return
		}
		logger.Warn("Slow request",
			logger.String("method", c.Request.Method), logger.String("route", path),
			logger.Int("status", c.Writer.Status()), logger.String("took", took.Round(time.Millisecond).String()))
	}
}
