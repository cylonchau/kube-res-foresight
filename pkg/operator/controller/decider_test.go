package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// platformDecider is a decider that talks to the platform at url with the credential; no credential or no url is a decider that was not
// started with a platform.
func platformDecider(url, credential string) *PlatformDecider {
	return &PlatformDecider{Endpoint: &PlatformEndpoint{URL: url, Credential: credential}}
}

func TestPlatformDeciderSendsTheCredentialAndParsesTheDecision(t *testing.T) {
	valid := time.Now().Add(10 * time.Minute).UTC().Truncate(time.Second)
	var gotAuth, gotModule, gotPath, gotClient string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotModule, gotPath, gotClient = r.Header.Get("Authorization"), r.URL.Query().Get("module"), r.URL.Path, r.Header.Get("X-kube-resource-fcst-Client")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 10000, "msg": "success", "data": map[string]any{
			"available": true, "module": "api", "version": 12, "replicas": 7, "valid_until": valid, "source": "algorithm", "reason": "peak 650 rps"}})
	}))
	defer server.Close()

	d, err := platformDecider(server.URL+"/", "AK123:SK456").Decide(context.Background(), scaler())
	require.NoError(t, err)
	assert.Equal(t, "kube-resource-fcst-AK AK123:SK456", gotAuth)
	assert.Equal(t, "operator", gotClient, "the platform records who is calling")
	assert.Equal(t, "api", gotModule)
	assert.Equal(t, "/api/v1/operator/decisions", gotPath)
	assert.EqualValues(t, 7, d.Replicas)
	assert.EqualValues(t, 12, d.Version)
	assert.Equal(t, "platform:algorithm", d.Source)
	require.NotNil(t, d.ValidUntil)
	assert.True(t, d.ValidUntil.Equal(valid))
}

func TestPlatformDeciderTurnsEveryFailureIntoAnError(t *testing.T) {
	respond := func(status int, body string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
		}))
	}
	for name, c := range map[string]struct {
		status int
		body   string
	}{
		"no decision available": {200, `{"code":10000,"data":{"available":false,"reason":"module is in bypass"}}`},
		"http error":            {503, `down`},
		"api error code":        {200, `{"code":50116,"msg":"No permission"}`},
		"garbage":               {200, `<html>`},
		"negative replicas":     {200, `{"code":10000,"data":{"available":true,"replicas":-3}}`},
	} {
		server := respond(c.status, c.body)
		_, err := platformDecider(server.URL, "a:b").Decide(context.Background(), scaler())
		assert.Error(t, err, name)
		server.Close()
	}
	_, err := platformDecider("not a url", "a:b").Decide(context.Background(), scaler())
	assert.Error(t, err)
	_, err = platformDecider("ftp://platform", "a:b").Decide(context.Background(), scaler())
	assert.Error(t, err)
	_, err = platformDecider("http://127.0.0.1:1", "a:b").Decide(context.Background(), scaler())
	assert.Error(t, err)
	assert.True(t, IsUnreachable(err), "a platform that cannot be reached is told apart from one that answered no")
	_, err = (&PlatformDecider{}).Decide(context.Background(), scaler())
	assert.ErrorContains(t, err, "platform url", "an operator started without a platform has nothing to ask")
}

func TestPlatformDeciderReportsWithTheModuleName(t *testing.T) {
	var body Report
	var method string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method = r.Method
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = w.Write([]byte(`{"code":10000}`))
	}))
	defer server.Close()
	err := platformDecider(server.URL, "a:b").Report(context.Background(), scaler(), Report{Namespace: "prod", Scaler: "module-api", Desired: 8, Result: "applied"})
	require.NoError(t, err)
	assert.Equal(t, http.MethodPost, method)
	assert.Equal(t, "api", body.Module)
	assert.EqualValues(t, 8, body.Desired)
}
