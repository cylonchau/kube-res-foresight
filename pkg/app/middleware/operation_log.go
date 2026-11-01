package middleware

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/cylonchau/kube-resource-fcst/pkg/dao/rdb"
	"github.com/cylonchau/kube-resource-fcst/pkg/logger"
	"github.com/cylonchau/kube-resource-fcst/pkg/model"
	"github.com/gin-gonic/gin"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// maxOperationLogBody caps how much of a request body gets captured/stored —
// large payloads (file imports and bulk operations) are truncated rather
// than skipped outright, so the shape of the call is still visible.
const maxOperationLogBody = 32 * 1024

// loggedMethods is deliberately narrow: GET reads are noise for an
// "operation log" in the conventional admin-console sense (an audit trail
// of changes, not a full access log) and would dwarf the mutating requests
// that are actually interesting.
var loggedMethods = map[string]bool{
	http.MethodPost:   true,
	http.MethodPut:    true,
	http.MethodDelete: true,
	http.MethodPatch:  true,
}

// excludedPathPrefixes are mutating endpoints that save a per-user UI
// preference rather than perform an auditable business action — column
// widths/visibility being the prototypical case, saved automatically on
// nearly every resize/page load and therefore frequent enough to bury any
// real operation under a wall of "POST /api/v1/columns/config" rows if left
// in. These mirror the same "self-service" endpoints CasbinMiddleware
// already special-cases (see rbac.go).
var excludedPathPrefixes = []string{
	"/api/v1/columns/",
}

// excludedPathsExact is the same idea for endpoints matched by exact path
// rather than prefix.
var excludedPathsExact = map[string]bool{
	"/api/v1/auth/language": true,
}

func isExcludedFromOperationLog(path string) bool {
	if excludedPathsExact[path] {
		return true
	}
	for _, prefix := range excludedPathPrefixes {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

// secretFieldPattern matches JSON keys whose values should never be
// persisted verbatim — mirrors the same keyword heuristic the system-config
// UI already uses client-side (web/src/views/system/config/index.vue's
// isSecretKey) so a login/credential payload doesn't end up readable in the
// operation log.
var secretFieldPattern = regexp.MustCompile(`(?i)(password|secret|token|credential|access_key|private_key|api_hash|session|phone_code|verification_code|^code$)`)

// OperationLogMiddleware records every mutating (POST/PUT/DELETE/PATCH)
// request into operation_log for the "系统管理/操作日志" page. It must be
// registered AFTER JWTAuthMiddleware and CasbinMiddleware so it can read
// the userId/username/developerCredential values those set into the
// context, and it deliberately never aborts or alters the response — a
// logging failure must not affect the request it's describing.
func OperationLogMiddleware(dao *rdb.OperationLogDAO) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !loggedMethods[c.Request.Method] || isExcludedFromOperationLog(c.Request.URL.Path) {
			c.Next()
			return
		}

		body := captureBody(c)
		start := time.Now()
		c.Header(RequestIDHeader, requestIDOf(c))

		c.Next()

		entry := &model.OperationLog{
			CreatedAt:  start,
			RequestID:  requestIDOf(c),
			ClientType: resolveClientType(c),
			ClientName: truncate(strings.TrimSpace(c.GetHeader(ClientNameHeader)), 128),
			Method:     c.Request.Method,
			Path:       c.Request.URL.Path,
			Module:     resolveModule(c.Request.URL.Path),
			StatusCode: c.Writer.Status(),
			ClientIP:   c.ClientIP(),
			UserAgent:  c.Request.UserAgent(),
			Params:     truncate(c.Request.URL.RawQuery, maxOperationLogBody),
			Body:       body,
			DurationMs: time.Since(start).Milliseconds(),
		}
		if note, ok := c.Get(OperationLogNoteKey); ok {
			// a handler's own account of what the request did (an export's scope and row count)
			if text, ok := note.(string); ok && text != "" {
				entry.Body = truncate(text, maxOperationLogBody)
			}
		}
		if userID, ok := c.Get("userId"); ok {
			if uid, ok := userID.(uint); ok {
				entry.UserID = uid
			}
		}
		if username, ok := c.Get("username"); ok {
			if name, ok := username.(string); ok {
				entry.Username = name
			}
		}
		if len(c.Errors) > 0 {
			entry.ErrorMsg = truncate(c.Errors.String(), 500)
		}

		if err := dao.Create(c.Request.Context(), entry); err != nil {
			logger.Error("failed to write operation log", logger.Err(err), logger.String("path", entry.Path))
		}
	}
}

// resolveClientType maps the filterable sources onto the mechanisms that already distinguish them:
// developer-credential calls (access-key/secret, see extractDeveloperCredential) are "api" (or "cli" when the
// X-kube-resource-fcst-Client header says so); everything else is an interactive JWT session, "page".
func resolveClientType(c *gin.Context) string {
	if dev, _ := c.Get("developerCredential"); dev == true {
		// Command-line integrations use the same developer credential as
		// other API clients and identify their source with this header.
		switch strings.ToLower(strings.TrimSpace(c.GetHeader("X-kube-resource-fcst-Client"))) {
		case "cli":
			return "cli"
		case "operator":
			return "operator"
		}
		return "api"
	}
	return "page"
}

// resolveModule extracts the first path segment after /api/v1/ as a cheap,
// curation-free grouping key (e.g. "users", "modules", "tasks") for
// the filter UI.
func resolveModule(path string) string {
	trimmed := strings.TrimPrefix(path, "/api/v1/")
	if trimmed == path {
		trimmed = strings.TrimPrefix(path, "/")
	}
	if idx := strings.IndexByte(trimmed, '/'); idx >= 0 {
		return trimmed[:idx]
	}
	return trimmed
}

// captureBody buffers and redacts the request body ahead of the handler,
// restoring c.Request.Body so the real handler still sees the full,
// unredacted payload. Skipped for anything that isn't a reasonably small
// JSON/form body — file uploads are not
// captured at all rather than partially/uselessly truncated.
func captureBody(c *gin.Context) string {
	if c.Request.Body == nil {
		return ""
	}
	if bodyNotKept(c.Request.URL.Path) {
		return ""
	}
	contentType := c.Request.Header.Get("Content-Type")
	if strings.HasPrefix(contentType, "multipart/") {
		return ""
	}
	raw, err := io.ReadAll(io.LimitReader(c.Request.Body, maxOperationLogBody+1))
	if err != nil {
		return ""
	}
	c.Request.Body = io.NopCloser(bytes.NewReader(raw))
	if len(raw) == 0 {
		return ""
	}

	redacted := redactJSON(raw)
	return truncate(redacted, maxOperationLogBody)
}

// bodyNotKept says the body of a request is not written to the operation log at all. A system parameter that holds a credential arrives as
// {"config_value": "..."}: the field name says nothing, the key in the path does. The assistant's conversation is the user's questions
// and the platform's own state: the log says who asked and when, not what.
func bodyNotKept(path string) bool {
	if key, ok := strings.CutPrefix(path, "/api/v1/configs/"); ok {
		return model.IsSecretConfig(key)
	}
	return path == "/api/v1/assistant/chat"
}

// redactJSON walks a JSON object/array replacing any value whose key looks
// secret-shaped with "***". Non-JSON or unparsable bodies are returned
// as-is truncated — best-effort, never fatal to the request.
func redactJSON(raw []byte) string {
	var parsed any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return string(raw)
	}
	redactValue(parsed)
	out, err := json.Marshal(parsed)
	if err != nil {
		return string(raw)
	}
	return string(out)
}

func redactValue(v any) {
	switch typed := v.(type) {
	case map[string]any:
		for key, val := range typed {
			if secretFieldPattern.MatchString(key) {
				typed[key] = "***"
				continue
			}
			redactValue(val)
		}
	case []any:
		for _, item := range typed {
			redactValue(item)
		}
	}
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	// cut at a character boundary: half of a multi-byte character is invalid UTF-8 and reads as garbage
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return strings.ToValidUTF8(s[:cut], "")
}

// RequestIDHeader carries the identifier of an operation; ClientNameHeader names the caller (the operator's Pod name, say).
const (
	RequestIDHeader  = "X-Request-Id"
	ClientNameHeader = "X-kube-resource-fcst-Client-Name"
)

// requestIDOf is the caller's X-Request-Id when it is a sane value, else a generated one kept for the rest of the request.
func requestIDOf(c *gin.Context) string {
	if v, ok := c.Get("requestId"); ok {
		if id, ok := v.(string); ok && id != "" {
			return id
		}
	}
	id := strings.TrimSpace(c.GetHeader(RequestIDHeader))
	if id == "" || len(id) > 64 || !utf8.ValidString(id) {
		buf := make([]byte, 8)
		_, _ = rand.Read(buf)
		id = fmt.Sprintf("op-%s-%s", time.Now().UTC().Format("20060102T150405"), hex.EncodeToString(buf))
	}
	c.Set("requestId", id)
	return id
}

// OperationLogNoteKey is the context key a handler sets to put its own account of a request in the log.
const OperationLogNoteKey = "operationLogNote"
