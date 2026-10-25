package middleware

import (
	"github.com/gin-gonic/gin"
	"net/http"
)

// CORS returns a middleware that allows cross-origin requests (with
// credentials) from exactly the origins in allowedOrigins. If allowedOrigins
// is empty, it does nothing at all — no CORS headers are sent, matching the
// long-standing default where this binary serves both the API and the built
// SPA from a single origin and no browser needs cross-origin access. Only a
// literal, case-sensitive match against the request's Origin header is
// allowed (no wildcard "*": that's incompatible with
// Access-Control-Allow-Credentials, which every authenticated request here
// needs).
func CORS(allowedOrigins []string) gin.HandlerFunc {
	if len(allowedOrigins) == 0 {
		return func(c *gin.Context) { c.Next() }
	}
	allowed := make(map[string]bool, len(allowedOrigins))
	for _, origin := range allowedOrigins {
		allowed[origin] = true
	}
	return func(c *gin.Context) {
		origin := c.Request.Header.Get("Origin")
		if origin != "" && allowed[origin] {
			c.Header("Access-Control-Allow-Origin", origin)
			c.Header("Access-Control-Allow-Credentials", "true")
			c.Header("Vary", "Origin")
			c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
			c.Header("Access-Control-Allow-Headers", "Content-Type, Authorization, X-kube-resource-fcst-AccessKey, X-kube-resource-fcst-AccessSecret")
		}
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}
