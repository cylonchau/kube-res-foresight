package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/cylonchau/kubeforecast/pkg/operator/api/v1alpha1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func platformScaler(url string) *v1alpha1.PredictiveScaler {
	ps := scaler(v1alpha1.ModeAuto)
	ps.Spec.Decision = v1alpha1.Decision{Source: v1alpha1.SourcePlatformAPI, PlatformAPI: &v1alpha1.PlatformAPIDecision{
		URL: url, Module: "api", AccessKeySecretRef: v1alpha1.SecretKeyRef{Name: "platform", Key: "token"},
	}}
	return ps
}

func platformDecider(t *testing.T, secret string) *PlatformDecider {
	t.Helper()
	objs := []corev1.Secret{}
	if secret != "" {
		objs = append(objs, corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "platform", Namespace: "prod"}, Data: map[string][]byte{"token": []byte(secret)}})
	}
	b := fake.NewClientBuilder().WithScheme(testScheme(t))
	for i := range objs {
		b = b.WithObjects(&objs[i])
	}
	return &PlatformDecider{Reader: b.Build()}
}

func TestStaticDeciderRequiresItsConfiguration(t *testing.T) {
	d, err := StaticDecider{}.Decide(context.Background(), scaler(v1alpha1.ModeAuto))
	require.NoError(t, err)
	assert.EqualValues(t, 8, d.Replicas)
	assert.Nil(t, d.ValidUntil)
	ps := scaler(v1alpha1.ModeAuto)
	ps.Spec.Decision.Static = nil
	_, err = StaticDecider{}.Decide(context.Background(), ps)
	assert.Error(t, err)
}

func TestPlatformDeciderSendsTheCredentialAndParsesTheDecision(t *testing.T) {
	valid := time.Now().Add(10 * time.Minute).UTC().Truncate(time.Second)
	var gotAuth, gotModule, gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotModule, gotPath = r.Header.Get("Authorization"), r.URL.Query().Get("module"), r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 10000, "msg": "success", "data": map[string]any{
			"available": true, "module": "api", "version": 12, "replicas": 7, "valid_until": valid, "source": "algorithm", "reason": "peak 650 rps"}})
	}))
	defer server.Close()

	d, err := platformDecider(t, "AK123:SK456").Decide(context.Background(), platformScaler(server.URL+"/"))
	require.NoError(t, err)
	assert.Equal(t, "KubeForecast-AK AK123:SK456", gotAuth)
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
		secret string
	}{
		"no decision available": {200, `{"code":10000,"data":{"available":false,"reason":"module is in bypass"}}`, "a:b"},
		"http error":            {503, `down`, "a:b"},
		"api error code":        {200, `{"code":50116,"msg":"No permission"}`, "a:b"},
		"garbage":               {200, `<html>`, "a:b"},
		"negative replicas":     {200, `{"code":10000,"data":{"available":true,"replicas":-3}}`, "a:b"},
		"missing secret":        {200, `{}`, ""},
	} {
		server := respond(c.status, c.body)
		_, err := platformDecider(t, c.secret).Decide(context.Background(), platformScaler(server.URL))
		assert.Error(t, err, name)
		server.Close()
	}
	_, err := platformDecider(t, "a:b").Decide(context.Background(), platformScaler("not a url"))
	assert.Error(t, err)
	_, err = platformDecider(t, "a:b").Decide(context.Background(), platformScaler("ftp://platform"))
	assert.Error(t, err)
	unreachable := platformScaler("http://127.0.0.1:1")
	unreachable.Spec.Decision.PlatformAPI.TimeoutSeconds = 1
	_, err = platformDecider(t, "a:b").Decide(context.Background(), unreachable)
	assert.Error(t, err)
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
	err := platformDecider(t, "a:b").Report(context.Background(), platformScaler(server.URL), Report{Namespace: "prod", Scaler: "api", Desired: 8, Result: "applied"})
	require.NoError(t, err)
	assert.Equal(t, http.MethodPost, method)
	assert.Equal(t, "api", body.Module)
	assert.EqualValues(t, 8, body.Desired)
	assert.NoError(t, platformDecider(t, "a:b").Report(context.Background(), scaler(v1alpha1.ModeAuto), Report{}), "static scalers have nobody to report to")
}
