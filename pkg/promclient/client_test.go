package promclient

import (
	"context"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const vectorOne = `{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1700000000,"1"]}]}}`

func fakePrometheus(t *testing.T, check func(*http.Request) bool) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/query", func(w http.ResponseWriter, r *http.Request) {
		if check != nil && !check(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(vectorOne))
	})
	mux.HandleFunc("/api/v1/status/buildinfo", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success","data":{"version":"2.53.0"}}`))
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func TestNormalizeBaseURL(t *testing.T) {
	got, err := NormalizeBaseURL(" http://prom:9090/prefix/ ")
	require.NoError(t, err)
	assert.Equal(t, "http://prom:9090/prefix", got)
	for _, bad := range []string{"", "prom:9090", "ftp://prom", "http://user:pw@prom", "http://prom?x=1", "http://prom#a", "http://"} {
		_, err := NormalizeBaseURL(bad)
		assert.Error(t, err, bad)
	}
}

func TestTestProbesQueryAndBuildInfo(t *testing.T) {
	server := fakePrometheus(t, nil)
	client, err := New(Config{BaseURL: server.URL, Timeout: 5 * time.Second})
	require.NoError(t, err)
	result, err := client.Test(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "2.53.0", result.Version)
}

func TestBasicAndBearerAuthAreSent(t *testing.T) {
	basic := fakePrometheus(t, func(r *http.Request) bool {
		user, pass, ok := r.BasicAuth()
		return ok && user == "u" && pass == "p" && r.UserAgent() != ""
	})
	client, err := New(Config{BaseURL: basic.URL, AuthType: AuthBasic, Username: "u", Password: "p"})
	require.NoError(t, err)
	_, err = client.Test(context.Background())
	require.NoError(t, err)

	bearer := fakePrometheus(t, func(r *http.Request) bool { return r.Header.Get("Authorization") == "Bearer tok" })
	client, err = New(Config{BaseURL: bearer.URL, AuthType: AuthBearer, BearerToken: "tok"})
	require.NoError(t, err)
	_, err = client.Test(context.Background())
	require.NoError(t, err)

	client, err = New(Config{BaseURL: bearer.URL, AuthType: AuthBearer, BearerToken: "wrong"})
	require.NoError(t, err)
	_, err = client.Test(context.Background())
	assert.Error(t, err)
}

func TestTestFailsOnNonPrometheusEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL})
	require.NoError(t, err)
	_, err = client.Test(context.Background())
	assert.Error(t, err)
}

func TestNewRejectsBadConfig(t *testing.T) {
	cases := map[string]Config{
		"basic without username": {BaseURL: "http://p", AuthType: AuthBasic},
		"bearer without token":   {BaseURL: "http://p", AuthType: AuthBearer},
		"unknown auth":           {BaseURL: "http://p", AuthType: "digest"},
		"bad ca":                 {BaseURL: "https://p", CACertPEM: "not a pem"},
		"bad url":                {BaseURL: "p"},
	}
	for name, cfg := range cases {
		_, err := New(cfg)
		assert.Error(t, err, name)
	}
}

func matrixServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server
}

func TestQueryRangeReturnsTheSingleSeries(t *testing.T) {
	server := matrixServer(t, `{"status":"success","data":{"resultType":"matrix","result":[{"metric":{},"values":[[1700000000,"1.5"],[1700000060,"NaN"],[1700000120,"3"]]}]}}`)
	client, err := New(Config{BaseURL: server.URL})
	require.NoError(t, err)
	samples, err := client.QueryRange(context.Background(), "up", time.Unix(1700000000, 0), time.Unix(1700000120, 0), time.Minute)
	require.NoError(t, err)
	require.Len(t, samples, 3)
	assert.Equal(t, 1.5, samples[0].Value)
	assert.True(t, math.IsNaN(samples[1].Value))
	assert.Equal(t, time.Unix(1700000120, 0).UTC(), samples[2].Time.UTC())
}

func TestQueryRangeEmptyAndAmbiguousResults(t *testing.T) {
	empty := matrixServer(t, `{"status":"success","data":{"resultType":"matrix","result":[]}}`)
	client, err := New(Config{BaseURL: empty.URL})
	require.NoError(t, err)
	samples, err := client.QueryRange(context.Background(), "up", time.Unix(0, 0), time.Unix(60, 0), time.Minute)
	require.NoError(t, err)
	assert.Empty(t, samples)

	two := matrixServer(t, `{"status":"success","data":{"resultType":"matrix","result":[{"metric":{"a":"1"},"values":[[1,"1"]]},{"metric":{"a":"2"},"values":[[1,"2"]]}]}}`)
	client, err = New(Config{BaseURL: two.URL})
	require.NoError(t, err)
	_, err = client.QueryRange(context.Background(), "up", time.Unix(0, 0), time.Unix(60, 0), time.Minute)
	assert.ErrorIs(t, err, ErrAmbiguousSeries)
	assert.True(t, IsPermanent(err))
}

func TestQueryRangeBadQueryIsPermanent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"status":"error","errorType":"bad_data","error":"parse error"}`))
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL})
	require.NoError(t, err)
	_, err = client.QueryRange(context.Background(), "sum(", time.Unix(0, 0), time.Unix(60, 0), time.Minute)
	require.Error(t, err)
	assert.True(t, IsPermanent(err))
	assert.False(t, IsPermanent(errors.New("connection refused")))
}
