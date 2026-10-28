package middleware

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"

	"github.com/cylonchau/kube-resource-fcst/pkg/app/query"
)

// maxUTF8CheckBody is how much of a request body is read to check its encoding; a larger body is not inspected.
const maxUTF8CheckBody = 4 << 20

// UTF8BodyMiddleware refuses a JSON or form body that is not valid UTF-8. A client on a GBK console (a PowerShell or curl call
// with Chinese text) sends bytes the JSON decoder would silently replace with U+FFFD, and the remark or name would be stored
// as unrecoverable garbage; answering 400 makes the caller fix its encoding instead.
func UTF8BodyMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		switch c.Request.Method {
		case "POST", "PUT", "PATCH":
		default:
			c.Next()
			return
		}
		contentType := strings.ToLower(c.Request.Header.Get("Content-Type"))
		if c.Request.Body == nil || strings.HasPrefix(contentType, "multipart/") ||
			(!strings.Contains(contentType, "json") && !strings.Contains(contentType, "x-www-form-urlencoded")) {
			c.Next()
			return
		}
		raw, err := io.ReadAll(io.LimitReader(c.Request.Body, maxUTF8CheckBody+1))
		if err != nil {
			c.Next()
			return
		}
		if len(raw) > maxUTF8CheckBody {
			// too large to inspect: hand over the whole body, read part by part
			c.Request.Body = io.NopCloser(io.MultiReader(bytes.NewReader(raw), c.Request.Body))
			c.Next()
			return
		}
		if !utf8.Valid(raw) {
			query.BadRequest(c, errors.New("the request body is not valid UTF-8: send it encoded as UTF-8 (a Chinese text sent from a GBK console arrives as garbage)"))
			c.Abort()
			return
		}
		c.Request.Body = io.NopCloser(bytes.NewReader(raw))
		c.Next()
	}
}
