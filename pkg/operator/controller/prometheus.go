package controller

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/cylonchau/kubeforecast/pkg/operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// maxPrometheusReplicas guards against a query that returns a counter or a byte count by mistake.
const maxPrometheusReplicas = 100000

const defaultMaxStaleness = 120 * time.Second

// PrometheusDecider reads the desired replicas from one Prometheus series. It never guesses: a query with no
// series, several series, a NaN or negative value, a stale sample or a refused request is an error, and the
// reconciler then freezes the replicas or uses the fallback, as it does for any unavailable decision.
type PrometheusDecider struct {
	Reader client.Reader
	// HTTPClient overrides the transport (tests); nil builds one per request from the CA secret.
	HTTPClient *http.Client
	Metrics    *Metrics
	Now        func() time.Time
}

type promResponse struct {
	Status    string `json:"status"`
	ErrorType string `json:"errorType"`
	Error     string `json:"error"`
	Data      struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Metric map[string]string `json:"metric"`
			Value  []any             `json:"value"`
		} `json:"result"`
	} `json:"data"`
}

func (d *PrometheusDecider) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

func (d *PrometheusDecider) secret(ctx context.Context, namespace string, ref v1alpha1.SecretKeyRef, defaultKey string) (string, error) {
	var s corev1.Secret
	if err := d.Reader.Get(ctx, types.NamespacedName{Namespace: namespace, Name: ref.Name}, &s); err != nil {
		return "", fmt.Errorf("secret %s/%s: %w", namespace, ref.Name, err)
	}
	key := ref.Key
	if key == "" {
		key = defaultKey
	}
	v, ok := s.Data[key]
	if !ok || len(v) == 0 {
		return "", fmt.Errorf("secret %s/%s has no key %q", namespace, ref.Name, key)
	}
	return strings.TrimSpace(string(v)), nil
}

func (d *PrometheusDecider) client(ctx context.Context, ps *v1alpha1.PredictiveScaler) (*http.Client, error) {
	cfg := ps.Spec.Decision.Prometheus
	if d.HTTPClient != nil {
		return d.HTTPClient, nil
	}
	timeout := time.Duration(cfg.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if cfg.CABundleSecretRef != nil {
		pem, err := d.secret(ctx, ps.Namespace, *cfg.CABundleSecretRef, "ca.crt")
		if err != nil {
			return nil, err
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM([]byte(pem)) {
			return nil, errors.New("the CA bundle secret holds no valid PEM certificate")
		}
		transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool}
	}
	return &http.Client{Timeout: timeout, Transport: transport}, nil
}

// Decide implements Decider.
func (d *PrometheusDecider) Decide(ctx context.Context, ps *v1alpha1.PredictiveScaler) (*Decision, error) {
	started := time.Now()
	decision, err := d.decide(ctx, ps)
	d.Metrics.recordPrometheus(err, time.Since(started))
	return decision, err
}

func (d *PrometheusDecider) decide(ctx context.Context, ps *v1alpha1.PredictiveScaler) (*Decision, error) {
	cfg := ps.Spec.Decision.Prometheus
	if cfg == nil {
		return nil, errors.New("spec.decision.prometheus is required for the Prometheus source")
	}
	base, err := url.Parse(strings.TrimRight(cfg.URL, "/"))
	if err != nil || base.Host == "" || (base.Scheme != "http" && base.Scheme != "https") {
		return nil, fmt.Errorf("invalid prometheus url %q", cfg.URL)
	}
	httpClient, err := d.client(ctx, ps)
	if err != nil {
		return nil, err
	}
	target := base.JoinPath("/api/v1/query")
	target.RawQuery = url.Values{"query": {cfg.Query}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if cfg.BearerTokenSecretRef != nil {
		token, err := d.secret(ctx, ps.Namespace, *cfg.BearerTokenSecretRef, "token")
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("prometheus request failed: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, fmt.Errorf("prometheus refused the request (%d): check the bearer token", resp.StatusCode)
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("prometheus answered %d", resp.StatusCode)
	}
	var out promResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("unreadable prometheus response: %w", err)
	}
	if out.Status != "success" {
		return nil, fmt.Errorf("prometheus query failed: %s %s", out.ErrorType, out.Error)
	}
	if out.Data.ResultType != "vector" {
		return nil, fmt.Errorf("the query returned a %s; it must return an instant vector of one series", out.Data.ResultType)
	}
	switch n := len(out.Data.Result); {
	case n == 0:
		return nil, errors.New("the query returned no series")
	case n > 1:
		return nil, fmt.Errorf("the query returned %d series; it must return exactly one (aggregate it)", n)
	}
	sample := out.Data.Result[0].Value
	if len(sample) != 2 {
		return nil, errors.New("malformed sample in the prometheus response")
	}
	tsFloat, ok := sample[0].(float64)
	raw, okValue := sample[1].(string)
	if !ok || !okValue {
		return nil, errors.New("malformed sample in the prometheus response")
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
		return nil, fmt.Errorf("the series value %q is not a finite number", raw)
	}
	if value < 0 {
		return nil, fmt.Errorf("the series value %s is negative", raw)
	}
	if value > maxPrometheusReplicas {
		return nil, fmt.Errorf("the series value %s is implausible as a replica count", raw)
	}
	maxStale := time.Duration(cfg.MaxStalenessSeconds) * time.Second
	if maxStale <= 0 {
		maxStale = defaultMaxStaleness
	}
	sampleTime := time.Unix(int64(tsFloat), int64((tsFloat-math.Floor(tsFloat))*1e9)).UTC()
	if age := d.now().Sub(sampleTime); age > maxStale {
		return nil, fmt.Errorf("the sample is %s old, older than the allowed %s", age.Round(time.Second), maxStale)
	}
	validUntil := sampleTime.Add(maxStale)
	replicas := int32(math.Ceil(value))
	return &Decision{Replicas: replicas, Source: "prometheus", ValidUntil: &validUntil, CreatedAt: &sampleTime,
		Reason: fmt.Sprintf("prometheus query returned %s (rounded up to %d)", raw, replicas)}, nil
}
