// Package agentauth holds low-level bearer-token primitives shared by
// generic agent check-in protocols: extracting the bearer token from a request,
// constant-time comparing it against a stored hash, and checking it against
// the operator-configured bootstrap token. The higher-level register/
// heartbeat state machines built on top of these differ enough per agent
// kind (different agents may use different identifier representations;
// the persisted fields differ) that they're intentionally not unified here.
package agentauth

import (
	"crypto/subtle"
	apputils "github.com/cylonchau/kubeforecast/pkg/app/utils"
	"net/http"
	"os"
	"strings"
)

// BearerToken extracts the raw token from a request's Authorization header
// ("Bearer <token>"), or "" if absent.
func BearerToken(r *http.Request) string {
	return strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
}

// TokensMatch reports whether token hashes (via apputils.HashToken) to the
// given stored hash, compared in constant time.
func TokensMatch(hash, token string) bool {
	if hash == "" || token == "" {
		return false
	}
	expected := apputils.HashToken(token)
	return subtle.ConstantTimeCompare([]byte(hash), []byte(expected)) == 1
}

// BootstrapTokenOK reports whether provided matches the operator-configured
// KUBEFORECAST_AGENT_BOOTSTRAP_TOKEN environment variable (compared in constant
// time). Returns false if the env var isn't set.
func BootstrapTokenOK(provided string) bool {
	bootstrap := strings.TrimSpace(os.Getenv("KUBEFORECAST_AGENT_BOOTSTRAP_TOKEN"))
	return bootstrap != "" && subtle.ConstantTimeCompare([]byte(provided), []byte(bootstrap)) == 1
}
