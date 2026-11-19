package middleware

import (
	"strings"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/logger"
	"github.com/gin-gonic/gin"
)

// SlowRequests logs the requests that take longer than the limit and the ones the client (or the gateway in front) gave up on before they
// were answered, with the route (not the query: it can hold keys), the status, the time and the request id, so the page behind a 502 or a
// 504 can be named instead of guessed. A request that is abandoned has a cancelled context: that is what a browser time-out or a gateway
// read time-out looks like from here, and the time it took is then the time the other side waited. The limit is read on every request, so a
// change of the system parameter is seen at once; 0 or less turns the slow log off but not the abandoned one. The streams (WebSocket) are
// long by nature and are left out. It also gives every response its request id.
func SlowRequests(limit func() time.Duration) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := requestIDOf(c)
		c.Writer.Header().Set(RequestIDHeader, id)
		c.Set("requestId", id)
		// the context the server made: the one a handler sees may be given a deadline by a middleware that cancels it when it returns, which is not the other side giving up
		original := c.Request.Context()
		start := time.Now()
		c.Next()
		took := time.Since(start)
		path := c.FullPath()
		if strings.Contains(path, "/stream") {
			return
		}
		abandoned := original.Err() != nil
		threshold := time.Duration(0)
		if limit != nil {
			threshold = limit()
		}
		slow := threshold > 0 && took >= threshold
		if !slow && !abandoned {
			return
		}
		message := "Slow request"
		if abandoned {
			message = "Request abandoned before it was answered (the client or the gateway closed the connection)"
		}
		logger.Warn(message,
			logger.String("method", c.Request.Method), logger.String("route", path), logger.String("request_id", id),
			logger.Int("status", c.Writer.Status()), logger.String("took", took.Round(time.Millisecond).String()),
			logger.String("client_ip", c.ClientIP()))
	}
}
