// Package promclient is a thin wrapper over the official Prometheus Go API client.
// It adds auth, TLS and a request timeout, and never logs or returns credentials.
package promclient

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/cylonchau/kube-resource-fcst/pkg/version"
	"github.com/prometheus/client_golang/api"
	promv1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/common/model"
)

const (
	AuthNone   = "none"
	AuthBasic  = "basic"
	AuthBearer = "bearer"

	DefaultTimeout = 30 * time.Second
	MaxTimeout     = 300 * time.Second
)

// Config describes one Prometheus-compatible endpoint.
type Config struct {
	BaseURL       string
	AuthType      string
	Username      string
	Password      string
	BearerToken   string
	TLSSkipVerify bool
	CACertPEM     string
	Timeout       time.Duration
}

// Client queries a single endpoint.
type Client struct {
	api     promv1.API
	timeout time.Duration
}

// TestResult is the outcome of a connectivity probe.
type TestResult struct {
	Version   string
	LatencyMs int
}

// NormalizeBaseURL validates and canonicalizes an endpoint URL: http(s), a host,
// no embedded credentials, query or fragment. A path prefix is kept.
func NormalizeBaseURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", errors.New("base_url must be an absolute http(s) URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", errors.New("base_url scheme must be http or https")
	}
	if u.User != nil {
		return "", errors.New("base_url must not contain credentials; use auth_type instead")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("base_url must not contain a query or fragment")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = ""
	return u.String(), nil
}

type authRoundTripper struct {
	next http.RoundTripper
	cfg  Config
}

func (rt authRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("User-Agent", "kube-resource-fcst/"+version.Version)
	switch rt.cfg.AuthType {
	case AuthBasic:
		req.SetBasicAuth(rt.cfg.Username, rt.cfg.Password)
	case AuthBearer:
		req.Header.Set("Authorization", "Bearer "+rt.cfg.BearerToken)
	}
	return rt.next.RoundTrip(req)
}

// New builds a client. The transport is not shared between datasources.
func New(cfg Config) (*Client, error) {
	base, err := NormalizeBaseURL(cfg.BaseURL)
	if err != nil {
		return nil, err
	}
	switch cfg.AuthType {
	case "", AuthNone:
		cfg.AuthType = AuthNone
	case AuthBasic:
		if cfg.Username == "" {
			return nil, errors.New("username is required for basic auth")
		}
	case AuthBearer:
		if cfg.BearerToken == "" {
			return nil, errors.New("bearer token is required for bearer auth")
		}
	default:
		return nil, fmt.Errorf("unsupported auth_type %q", cfg.AuthType)
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
	}
	if cfg.Timeout > MaxTimeout {
		cfg.Timeout = MaxTimeout
	}
	// Skipping verification is an explicit per-datasource admin opt-in.
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: cfg.TLSSkipVerify} //nolint:gosec
	if strings.TrimSpace(cfg.CACertPEM) != "" {
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM([]byte(cfg.CACertPEM)) {
			return nil, errors.New("ca_cert contains no valid PEM certificate")
		}
		tlsConfig.RootCAs = pool
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = tlsConfig
	client, err := api.NewClient(api.Config{Address: base, RoundTripper: authRoundTripper{next: transport, cfg: cfg}})
	if err != nil {
		return nil, err
	}
	return &Client{api: promv1.NewAPI(client), timeout: cfg.Timeout}, nil
}

// Test probes the endpoint with a constant instant query and, when supported, reads build info.
func (c *Client) Test(ctx context.Context) (*TestResult, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	started := time.Now()
	value, _, err := c.api.Query(ctx, "vector(1)", time.Now())
	if err != nil {
		return nil, err
	}
	vector, ok := value.(model.Vector)
	if !ok || len(vector) != 1 || float64(vector[0].Value) != 1 {
		return nil, errors.New("unexpected response to vector(1): endpoint is not a Prometheus-compatible query API")
	}
	result := &TestResult{LatencyMs: int(time.Since(started).Milliseconds())}
	// Build info is optional: older Prometheus and some compatible stores do not expose it.
	if info, err := c.api.Buildinfo(ctx); err == nil {
		result.Version = info.Version
	}
	return result, nil
}

// Sample is one point of a series. Value is NaN where Prometheus returned NaN.
type Sample struct {
	Time  time.Time
	Value float64
}

// ErrAmbiguousSeries is returned when a range query yields more than one series. A mapping must aggregate to
// one series, so series of different workloads are never merged or silently picked from.
var ErrAmbiguousSeries = errors.New("query returned more than one series; aggregate it to a single series")

// IsPermanent reports whether retrying the same query cannot succeed: a malformed query or an ambiguous result.
func IsPermanent(err error) bool {
	if errors.Is(err, ErrAmbiguousSeries) {
		return true
	}
	var apiErr *promv1.Error
	return errors.As(err, &apiErr) && apiErr.Type == promv1.ErrBadData
}

// QueryRange runs one query_range and returns its single series ordered by time. An empty result is a nil
// slice, not an error: the window simply has no data.
func (c *Client) QueryRange(ctx context.Context, query string, start, end time.Time, step time.Duration) ([]Sample, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	value, _, err := c.api.QueryRange(ctx, query, promv1.Range{Start: start, End: end, Step: step})
	if err != nil {
		return nil, err
	}
	matrix, ok := value.(model.Matrix)
	if !ok {
		return nil, fmt.Errorf("unexpected result type %s for a range query", value.Type())
	}
	switch len(matrix) {
	case 0:
		return nil, nil
	case 1:
	default:
		return nil, fmt.Errorf("%w (%d series)", ErrAmbiguousSeries, len(matrix))
	}
	out := make([]Sample, 0, len(matrix[0].Values))
	for _, p := range matrix[0].Values {
		out = append(out, Sample{Time: p.Timestamp.Time(), Value: float64(p.Value)})
	}
	return out, nil
}
