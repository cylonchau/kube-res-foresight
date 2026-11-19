package middleware

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/app/query"
	"github.com/gin-gonic/gin"
)

// ReadDeadline gives the read requests (GET) a time limit: the context of the request ends after the budget, so the database statements it sent
// are cancelled and the handler answers 504 with the reason instead of the gateway cutting a request that is still working. The budget is read on
// every request (a change of the system parameter is seen at once) and is kept under the gateway timeout by the caller. The streams, the
// requests that ask for a long wait (the header X-Long-Request) and every method that is not GET are left alone: a write is not cut half way,
// and the long steps have their own limits.
func ReadDeadline(budget func() time.Duration) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Method != "GET" || c.GetHeader("X-Long-Request") != "" || strings.Contains(c.Request.URL.Path, "/stream") || budget == nil {
			c.Next()
			return
		}
		limit := budget()
		if limit <= 0 {
			c.Next()
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), limit)
		defer cancel()
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}

// PageDepth refuses a list request that pages deeper than the limit (the rows skipped before the page): the database reads and drops every row
// it skips, so page 4000 of a table that grew is a long query that no index makes short. The answer says to narrow the filter instead.
func PageDepth(limit int) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Method != "GET" {
			c.Next()
			return
		}
		page, _ := strconv.Atoi(c.Query("page"))
		size, _ := strconv.Atoi(c.Query("page_size"))
		if page > 1 {
			if size < 1 || size > 200 {
				size = 20
			}
			if (page-1)*size > limit {
				query.BadRequest(c, fmt.Errorf("page %d is too deep: the rows before it are read and skipped, which takes longer than a request may. Narrow the filter (a time range, a module) instead of paging on", page))
				c.Abort()
				return
			}
		}
		c.Next()
	}
}
