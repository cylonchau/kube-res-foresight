package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

// An upgraded platform still understands what the earlier releases send: the project was kubeforecast, then kube-resource-fcst.
func TestTheNamesOfTheEarlierReleasesAreStillUnderstood(t *testing.T) {
	newReq := func(mutate func(*http.Request)) *http.Request {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/operator/decisions", nil)
		mutate(req)
		return req
	}
	cases := map[string]func(*http.Request){
		"current headers": func(r *http.Request) {
			r.Header.Set("X-kube-res-foresight-AccessKey", "ak")
			r.Header.Set("X-kube-res-foresight-AccessSecret", "sk")
		},
		"kube-resource-fcst headers": func(r *http.Request) {
			r.Header.Set("X-kube-resource-fcst-AccessKey", "ak")
			r.Header.Set("X-kube-resource-fcst-AccessSecret", "sk")
		},
		"KubeForecast headers": func(r *http.Request) {
			r.Header.Set("X-KubeForecast-AccessKey", "ak")
			r.Header.Set("X-KubeForecast-AccessSecret", "sk")
		},
		"current scheme":            func(r *http.Request) { r.Header.Set("Authorization", "kube-res-foresight-AK ak:sk") },
		"kube-resource-fcst scheme": func(r *http.Request) { r.Header.Set("Authorization", "kube-resource-fcst-AK ak:sk") },
		"KubeForecast scheme":       func(r *http.Request) { r.Header.Set("Authorization", "KubeForecast-AK ak:sk") },
		"scheme in another case":    func(r *http.Request) { r.Header.Set("Authorization", "KUBE-RESOURCE-FCST-ak ak:sk") },
		"current parameters":        func(r *http.Request) { r.URL.RawQuery = "__kube_res_foresight_ak=ak&__kube_res_foresight_sk=sk" },
		"kube-resource-fcst params": func(r *http.Request) { r.URL.RawQuery = "__kube_resource_fcst_ak=ak&__kube_resource_fcst_sk=sk" },
		"kubeforecast parameters":   func(r *http.Request) { r.URL.RawQuery = "__kubeforecast_ak=ak&__kubeforecast_sk=sk" },
	}
	for name, mutate := range cases {
		key, secret, present := extractDeveloperCredential(newReq(mutate))
		assert.True(t, present, name)
		assert.Equal(t, "ak", key, name)
		assert.Equal(t, "sk", secret, name)
	}
	// a bearer token of a user is not a developer credential
	_, _, present := extractDeveloperCredential(newReq(func(r *http.Request) { r.Header.Set("Authorization", "Bearer abc") }))
	assert.False(t, present)
	// the current name wins when both are sent
	key, _, _ := extractDeveloperCredential(newReq(func(r *http.Request) {
		r.Header.Set("X-kube-res-foresight-AccessKey", "new")
		r.Header.Set("X-KubeForecast-AccessKey", "old")
	}))
	assert.Equal(t, "new", key)
}

func TestTheSubprotocolOfAnEarlierReleaseCarriesTheToken(t *testing.T) {
	for _, name := range []string{"kube-res-foresight.jwt", "kube-resource-fcst.jwt", "kubeforecast.jwt"} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/stream", nil)
		req.Header.Set("Upgrade", "websocket")
		req.Header.Set("Sec-WebSocket-Protocol", name+", tok.en")
		assert.Equal(t, "Bearer tok.en", websocketBearer(req), name)
		assert.Contains(t, WebSocketAuthSubprotocols, name, "the server offers it, so the handshake answers with it")
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/stream", nil)
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Sec-WebSocket-Protocol", "something-else, tok.en")
	assert.Empty(t, websocketBearer(req))
}

func TestTheClientHeadersOfEarlierReleasesStillSayWhoCalls(t *testing.T) {
	for _, header := range []string{"X-kube-res-foresight-Client", "X-kube-resource-fcst-Client", "X-KubeForecast-Client"} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set(header, "operator")
		assert.Equal(t, "operator", firstHeader(req, clientHeaders), header)
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-kube-resource-fcst-Client-Name", "op-0")
	assert.Equal(t, "op-0", firstHeader(req, clientNameHeaders))
}
