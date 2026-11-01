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

// A system parameter that holds a credential arrives as {"config_value": "..."}: the field name says nothing, so the key in the path decides.
// The assistant's conversation is not kept either.
func TestTheBodyOfACredentialParameterAndOfTheAssistantChatIsNotKept(t *testing.T) {
	for _, path := range []string{"/api/v1/configs/assistant.api_key", "/api/v1/configs/ldap.bind_password", "/api/v1/configs/ldap.ca_cert", "/api/v1/assistant/chat"} {
		if !bodyNotKept(path) {
			t.Errorf("the body of %s is kept", path)
		}
	}
	for _, path := range []string{"/api/v1/configs/assistant.model", "/api/v1/configs/forecast.snapshot_max_per_module", "/api/v1/assistant/export", "/api/v1/modules"} {
		if bodyNotKept(path) {
			t.Errorf("the body of %s is not kept", path)
		}
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
		{"the operator over a developer credential", true, "operator", "operator"},
		{"a page session cannot claim to be the operator", false, "operator", "page"},
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

func TestRequestIDIsTheCallersOrAGeneratedOneAndStaysTheSameForTheRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/api/v1/users", nil)
	c.Request.Header.Set(RequestIDHeader, "caller-123")
	if got := requestIDOf(c); got != "caller-123" {
		t.Fatalf("the caller's id is kept, got %q", got)
	}
	c2, _ := gin.CreateTestContext(httptest.NewRecorder())
	c2.Request = httptest.NewRequest("POST", "/api/v1/users", nil)
	first := requestIDOf(c2)
	if len(first) < 10 || first != requestIDOf(c2) {
		t.Fatalf("a generated id is stable within the request: %q", first)
	}
	c3, _ := gin.CreateTestContext(httptest.NewRecorder())
	c3.Request = httptest.NewRequest("POST", "/api/v1/users", nil)
	c3.Request.Header.Set(RequestIDHeader, string(make([]byte, 80)))
	if got := requestIDOf(c3); len(got) > 64 {
		t.Fatalf("an over-long id is replaced, got %d bytes", len(got))
	}
}
