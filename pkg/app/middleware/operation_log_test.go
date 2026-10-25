package middleware

import (
	"encoding/json"
	"github.com/gin-gonic/gin"
	"net/http/httptest"
	"testing"
)

func TestIsExcludedFromOperationLog(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"/api/v1/columns/config", true},
		{"/api/v1/columns/meta", true},
		{"/api/v1/modules", false},
		{"/api/v1/auth/language", true},
		{"/api/v1/users", false},
		{"/api/v1/records/1", false},
		{"/api/v1/auth/password", false},
	}
	for _, c := range cases {
		if got := isExcludedFromOperationLog(c.path); got != c.want {
			t.Errorf("isExcludedFromOperationLog(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}

func TestRedactJSONSecretFields(t *testing.T) {
	raw := []byte(`{"name":"reader","api_hash":"hash-value","code":"12345","password":"2fa-value","nested":{"session":"session-value"},"status_code":200}`)

	redacted := redactJSON(raw)
	var got map[string]any
	if err := json.Unmarshal([]byte(redacted), &got); err != nil {
		t.Fatalf("redactJSON returned invalid JSON: %v", err)
	}

	for _, key := range []string{"api_hash", "code", "password"} {
		if got[key] != "***" {
			t.Errorf("%s was not redacted: %#v", key, got[key])
		}
	}
	nested, ok := got["nested"].(map[string]any)
	if !ok || nested["session"] != "***" {
		t.Errorf("nested session was not redacted: %#v", got["nested"])
	}
	if got["status_code"] != float64(200) {
		t.Errorf("unrelated status_code should be preserved: %#v", got["status_code"])
	}
}

func TestResolveClientType(t *testing.T) {
	gin.SetMode(gin.TestMode)

	newCtx := func(developerCredential bool, xKubeResourceFcstClient string) *gin.Context {
		req := httptest.NewRequest("POST", "/api/v1/users", nil)
		if xKubeResourceFcstClient != "" {
			req.Header.Set("X-kube-resource-fcst-Client", xKubeResourceFcstClient)
		}
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = req
		if developerCredential {
			c.Set("developerCredential", true)
		}
		return c
	}

	cases := []struct {
		name                    string
		developerCredential     bool
		xKubeResourceFcstClient string
		want                    string
	}{
		{"kube-resource-fcstctl CLI over a developer credential", true, "cli", "cli"},
		{"a different integration over the same developer credential", true, "", "api"},
		{"a developer credential with an unrelated X-kube-resource-fcst-Client value", true, "something-else", "api"},
		{"web admin session (no header, no developer credential)", false, "", "page"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveClientType(newCtx(tc.developerCredential, tc.xKubeResourceFcstClient)); got != tc.want {
				t.Errorf("resolveClientType() = %q, want %q", got, tc.want)
			}
		})
	}
}
