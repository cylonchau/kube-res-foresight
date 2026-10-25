package middleware

import (
	"github.com/cylonchau/kubeforecast/pkg/app/query"
	"github.com/cylonchau/kubeforecast/pkg/app/utils"
	"github.com/cylonchau/kubeforecast/pkg/dao/rdb"
	"github.com/gin-gonic/gin"
	"net/http"
	"strings"
)

func extractDeveloperCredential(r *http.Request) (accessKey, accessSecret string, present bool) {
	accessKey = strings.TrimSpace(r.Header.Get("X-KubeForecast-AccessKey"))
	accessSecret = strings.TrimSpace(r.Header.Get("X-KubeForecast-AccessSecret"))
	if accessKey != "" || accessSecret != "" {
		return accessKey, accessSecret, true
	}

	authorization := strings.TrimSpace(r.Header.Get("Authorization"))
	if strings.HasPrefix(authorization, "KubeForecast-AK ") {
		parts := strings.SplitN(strings.TrimSpace(strings.TrimPrefix(authorization, "KubeForecast-AK ")), ":", 2)
		if len(parts) == 2 {
			return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), true
		}
		return "", "", true
	}

	queryValues := r.URL.Query()
	accessKey = strings.TrimSpace(queryValues.Get("__kubeforecast_ak"))
	accessSecret = strings.TrimSpace(queryValues.Get("__kubeforecast_sk"))
	if accessKey != "" || accessSecret != "" {
		return accessKey, accessSecret, true
	}
	return "", "", false
}

// WebSocketAuthSubprotocol marks the Sec-WebSocket-Protocol entry that precedes
// the access token. Browsers cannot set an Authorization header on a WebSocket
// handshake, and a token in the URL would leak into access logs, so the client
// offers ["kubeforecast.jwt", "<token>"] and the server accepts only "kubeforecast.jwt".
const WebSocketAuthSubprotocol = "kubeforecast.jwt"

func websocketBearer(r *http.Request) string {
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		return ""
	}
	var offered []string
	for _, value := range r.Header.Values("Sec-WebSocket-Protocol") {
		for _, item := range strings.Split(value, ",") {
			offered = append(offered, strings.TrimSpace(item))
		}
	}
	for i, item := range offered {
		if item == WebSocketAuthSubprotocol && i+1 < len(offered) && offered[i+1] != "" {
			return "Bearer " + offered[i+1]
		}
	}
	return ""
}

// JWTAuthMiddleware accepts either a user JWT or a scoped developer credential.
func JWTAuthMiddleware(refreshDAO *rdb.RefreshTokenSessionDAO, secretKeyDAO *rdb.SecretKeyDAO) func(c *gin.Context) {
	return func(c *gin.Context) {
		if accessKey, accessSecret, present := extractDeveloperCredential(c.Request); present {
			credential, err := secretKeyDAO.AuthenticateServerCredential(c.Request.Context(), accessKey, accessSecret)
			if err != nil {
				query.Unauthorized(c, query.ErrTokenExpired)
				c.Abort()
				return
			}
			c.Set("userId", uint(0))
			c.Set("username", "developer:"+accessKey)
			c.Set("roles", []string{"DEVELOPER_CREDENTIAL"})
			c.Set("permissions", []string{})
			c.Set("developerCredential", true)
			c.Set("developerPermissionIDs", credential.PermissionIDs)
			c.Next()
			return
		}

		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			authHeader = websocketBearer(c.Request)
		}
		if authHeader == "" {
			query.Unauthorized(c, query.ErrHeaderRequired)
			c.Abort()
			return
		}
		parts := strings.SplitN(authHeader, " ", 2)
		if !(len(parts) == 2 && parts[0] == "Bearer") {
			query.Unauthorized(c, query.ErrHeaderFormat)
			c.Abort()
			return
		}
		claims, err := utils.ParseToken(parts[1])
		if err != nil {
			query.Unauthorized(c, query.ErrTokenExpired)
			c.Abort()
			return
		}
		if claims.SessionID != "" {
			active, checkErr := refreshDAO.IsFamilyActive(c.Request.Context(), claims.UserID, claims.SessionID)
			if checkErr != nil {
				query.InternalError(c, checkErr)
				c.Abort()
				return
			}
			if !active {
				// Distinguish "you were kicked by a same-client-type login
				// elsewhere" from an ordinary expired/rotated session, so
				// the client can show the right message instead of a
				// generic "please log in again".
				errResponse := query.ErrTokenExpired
				if reason, reasonErr := refreshDAO.FamilyRevokedReason(c.Request.Context(), claims.UserID, claims.SessionID); reasonErr == nil && reason == "kicked" {
					errResponse = query.ErrSessionInvalid
				}
				query.Unauthorized(c, errResponse)
				c.Abort()
				return
			}
		}
		c.Set("userId", claims.UserID)
		c.Set("username", claims.Username)
		c.Set("roles", claims.Roles)
		c.Set("permissions", claims.Permissions)
		c.Next()
	}
}

func developerCredentialRouteDenied(path string) bool {
	for _, prefix := range []string{
		"/api/v1/auth", "/api/v1/users", "/api/v1/roles", "/api/v1/permissions",
		"/api/v1/configs", "/api/v1/secret-keys", "/api/v1/columns", "/api/v1/route",
		"/api/v1/ldap",
	} {
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}
	return false
}

// CasbinMiddleware verifies user policies or the explicitly assigned developer permissions.
func CasbinMiddleware(permissionDAO *rdb.PermissionDAO) func(c *gin.Context) {
	return func(c *gin.Context) {
		username, ok := c.Get("username")
		if !ok {
			query.Unauthorized(c, query.ErrUserContextMissing)
			c.Abort()
			return
		}
		method := c.Request.Method
		reqPath := c.Request.URL.Path
		fullPath := c.FullPath()

		if isDeveloper, _ := c.Get("developerCredential"); isDeveloper == true {
			if developerCredentialRouteDenied(reqPath) {
				query.Forbidden(c, query.ErrNoPermission)
				c.Abort()
				return
			}
			rawIDs, ok := c.Get("developerPermissionIDs")
			permissionIDs, idsOK := rawIDs.([]uint)
			if !ok || !idsOK {
				query.Forbidden(c, query.ErrNoPermission)
				c.Abort()
				return
			}
			allowed, err := permissionDAO.AllowsDeveloperCredential(c.Request.Context(), permissionIDs, method, reqPath)
			if err != nil {
				query.InternalError(c, err)
				c.Abort()
				return
			}
			if !allowed && fullPath != "" && fullPath != reqPath {
				allowed, _ = permissionDAO.AllowsDeveloperCredential(c.Request.Context(), permissionIDs, method, fullPath)
			}
			if !allowed {
				query.Forbidden(c, query.ErrNoPermission)
				c.Abort()
				return
			}
			c.Next()
			return
		}

		roles, ok := c.Get("roles")
		if ok {
			for _, role := range roles.([]string) {
				if role == "ADMIN" {
					c.Next()
					return
				}
			}
		}

		// Common self-service / UI preference routes accessible to all authenticated users
		if reqPath == "/api/v1/auth/info" ||
			reqPath == "/api/v1/auth/get-routers" ||
			reqPath == "/api/v1/auth/session-status" ||
			reqPath == "/api/v1/auth/password" ||
			reqPath == "/api/v1/auth/profile" ||
			strings.HasPrefix(reqPath, "/api/v1/auth/otp/") ||
			strings.HasPrefix(reqPath, "/api/v1/route/") ||
			strings.HasPrefix(reqPath, "/api/v1/columns/") ||
			(method == "GET" && strings.HasPrefix(reqPath, "/api/v1/configs/ui.")) ||
			(method == "GET" && strings.HasPrefix(reqPath, "/api/v1/configs/ui/")) {
			c.Next()
			return
		}

		if utils.Enforcer == nil {
			query.InternalError(c, query.ErrCasbinNotInit)
			c.Abort()
			return
		}

		allowed, err := utils.Enforcer.Enforce(username.(string), reqPath, method)
		if err != nil {
			query.InternalError(c, query.ErrEnforcerFailed)
			c.Abort()
			return
		}
		if !allowed && fullPath != "" && fullPath != reqPath {
			allowed, _ = utils.Enforcer.Enforce(username.(string), fullPath, method)
		}

		if !allowed && ok {
			if roleList, isSlice := roles.([]string); isSlice {
				for _, role := range roleList {
					if roleAllowed, _ := utils.Enforcer.Enforce(role, reqPath, method); roleAllowed {
						allowed = true
						break
					}
					if fullPath != "" && fullPath != reqPath {
						if roleAllowed, _ := utils.Enforcer.Enforce(role, fullPath, method); roleAllowed {
							allowed = true
							break
						}
					}
				}
			}
		}

		if !allowed {
			query.Forbidden(c, query.ErrNoPermission)
			c.Abort()
			return
		}
		c.Next()
	}
}
