package agentauth

import (
	apputils "github.com/cylonchau/kubeforecast/pkg/app/utils"
	"github.com/stretchr/testify/assert"
	"net/http/httptest"
	"testing"
)

func TestBearerAndTokenChecks(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer token")
	assert.Equal(t, "token", BearerToken(req))
	assert.True(t, TokensMatch(apputils.HashToken("token"), "token"))
	assert.False(t, TokensMatch("", "token"))
	assert.False(t, TokensMatch(apputils.HashToken("token"), "wrong"))
	t.Setenv("KUBEFORECAST_AGENT_BOOTSTRAP_TOKEN", "bootstrap")
	assert.True(t, BootstrapTokenOK("bootstrap"))
	assert.False(t, BootstrapTokenOK("wrong"))
}
