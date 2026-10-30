package controller

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/cylonchau/kube-resource-fcst/pkg/operator/api/v1alpha1"
	"github.com/cylonchau/kube-resource-fcst/pkg/timing"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Decider reads the desired replicas for a PredictiveScaler.
type Decider interface {
	Decide(ctx context.Context, ps *v1alpha1.PredictiveScaler) (*Decision, error)
}

// Report is what the operator tells the platform after each evaluation, for audit and accuracy evaluation.
type Report struct {
	Module          string `json:"module"`
	Namespace       string `json:"namespace"`
	Scaler          string `json:"scaler"`
	TargetKind      string `json:"target_kind"`
	TargetName      string `json:"target_name"`
	Current         int32  `json:"current"`
	Ready           int32  `json:"ready"`
	Desired         int32  `json:"desired"`
	Action          string `json:"action"`
	Result          string `json:"result"` // applied, observed, held, conflict, error
	Reason          string `json:"reason"`
	DecisionVersion int64  `json:"decision_version"`
}

// Reporter delivers a Report; failures are logged by the caller and never affect scaling.
type Reporter interface {
	Report(ctx context.Context, ps *v1alpha1.PredictiveScaler, r Report) error
}

// StaticDecider returns spec.decision.static.
type StaticDecider struct{}

func (StaticDecider) Decide(_ context.Context, ps *v1alpha1.PredictiveScaler) (*Decision, error) {
	if ps.Spec.Decision.Static == nil {
		return nil, errors.New("spec.decision.static is required for the Static source")
	}
	return &Decision{Replicas: ps.Spec.Decision.Static.Replicas, Source: "static", Reason: "fixed replicas"}, nil
}

// PlatformEndpoint is a platform the operator was started with (flags), as opposed to one a PredictiveScaler names in its
// spec: the scalers built from platform modules have no spec to carry a URL and a Secret reference.
type PlatformEndpoint struct {
	URL string
	// Credential is "<access key>:<access secret>".
	Credential string
	// Client is used as is; nil builds a default one.
	Client *http.Client
}

// PlatformDecider asks the kube-resource-fcst platform for the replicas of a module. It also implements Reporter.
type PlatformDecider struct {
	Reader  client.Reader
	Metrics *Metrics
	// Endpoint, when set, is the platform for every scaler built from a platform module; scalers of the CRD still use
	// the URL and credential of their own spec.
	Endpoint *PlatformEndpoint
	// HTTPClient overrides the transport (tests); nil builds one per request from the CA secret.
	HTTPClient *http.Client
	// Settings gives the time limits the platform sets (how long one request may take); nil takes the defaults.
	Settings *SettingsStore
}

type platformEnvelope struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		Available       bool       `json:"available"`
		Module          string     `json:"module"`
		Version         int64      `json:"version"`
		Replicas        int32      `json:"replicas"`
		ValidUntil      *time.Time `json:"valid_until"`
		CreatedAt       *time.Time `json:"created_at"`
		Source          string     `json:"source"`
		Reason          string     `json:"reason"`
		Algorithm       string     `json:"algorithm"`
		Score           float64    `json:"score"`
		MinReplicas     int32      `json:"min_replicas"`
		HardMaxReplicas int32      `json:"hard_max_replicas"`
		Urgent          bool       `json:"urgent"`
	} `json:"data"`
}

func (d *PlatformDecider) secret(ctx context.Context, namespace string, ref v1alpha1.SecretKeyRef) (string, error) {
	var s corev1.Secret
	if err := d.Reader.Get(ctx, types.NamespacedName{Namespace: namespace, Name: ref.Name}, &s); err != nil {
		return "", fmt.Errorf("secret %s/%s: %w", namespace, ref.Name, err)
	}
	key := ref.Key
	if key == "" {
		key = "token"
	}
	v, ok := s.Data[key]
	if !ok || len(v) == 0 {
		return "", fmt.Errorf("secret %s/%s has no key %q", namespace, ref.Name, key)
	}
	return strings.TrimSpace(string(v)), nil
}

func (d *PlatformDecider) httpClient(ctx context.Context, ps *v1alpha1.PredictiveScaler) (*http.Client, error) {
	cfg := ps.Spec.Decision.PlatformAPI
	timeout := time.Duration(cfg.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		// the scaler sets none: the platform's setting, else the default
		timeout = timing.Seconds(timing.OperatorRequestTimeout.Default)
		if d.Settings != nil {
			timeout = d.Settings.RequestTimeout()
		}
	}
	if d.HTTPClient != nil {
		return d.HTTPClient, nil
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if cfg.CABundleSecretRef != nil {
		pem, err := d.secret(ctx, ps.Namespace, *cfg.CABundleSecretRef)
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

func (d *PlatformDecider) do(ctx context.Context, ps *v1alpha1.PredictiveScaler, method, path string, query url.Values, body io.Reader) ([]byte, error) {
	started := time.Now()
	data, err := d.request(ctx, ps, method, path, query, body)
	d.Metrics.recordPlatform(err, time.Since(started))
	return data, err
}

func (d *PlatformDecider) request(ctx context.Context, ps *v1alpha1.PredictiveScaler, method, path string, query url.Values, body io.Reader) ([]byte, error) {
	cfg := ps.Spec.Decision.PlatformAPI
	if cfg == nil {
		return nil, errors.New("spec.decision.platformAPI is required for the PlatformAPI source")
	}
	rawURL, credential, httpClient := cfg.URL, "", (*http.Client)(nil)
	var err error
	if IsModuleDriven(ps) {
		if d.Endpoint == nil {
			return nil, errors.New("the operator was not started with a platform url and credential")
		}
		rawURL, credential, httpClient = d.Endpoint.URL, d.Endpoint.Credential, d.Endpoint.Client
		if d.Settings != nil {
			// how long one answer may take is a setting of the platform, read again from time to time: it bounds this request, not
			// the client, which only carries a ceiling
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, d.Settings.RequestTimeout())
			defer cancel()
		}
	} else {
		if credential, err = d.secret(ctx, ps.Namespace, cfg.AccessKeySecretRef); err != nil {
			return nil, err
		}
		if httpClient, err = d.httpClient(ctx, ps); err != nil {
			return nil, err
		}
	}
	base, err := url.Parse(strings.TrimRight(rawURL, "/"))
	if err != nil || base.Host == "" || (base.Scheme != "http" && base.Scheme != "https") {
		return nil, fmt.Errorf("invalid platform url %q", rawURL)
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 5 * time.Second}
	}
	target := base.JoinPath(path)
	target.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, method, target.String(), body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "kube-resource-fcst-AK "+credential)
	// who is calling: the platform records it in the operation log (source "operator", the Pod as the client name)
	req.Header.Set("X-kube-resource-fcst-Client", "operator")
	if host, err := os.Hostname(); err == nil && host != "" {
		req.Header.Set("X-kube-resource-fcst-Client-Name", host)
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, unreachable(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, unreachable(err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, unreachable(fmt.Errorf("platform answered %d", resp.StatusCode))
	}
	return data, nil
}

// Decide implements Decider.
func (d *PlatformDecider) Decide(ctx context.Context, ps *v1alpha1.PredictiveScaler) (*Decision, error) {
	if ps.Spec.Decision.PlatformAPI == nil {
		return nil, errors.New("spec.decision.platformAPI is required for the PlatformAPI source")
	}
	data, err := d.do(ctx, ps, http.MethodGet, "/api/v1/operator/decisions", url.Values{"module": {ps.Spec.Decision.PlatformAPI.Module}}, nil)
	if err != nil {
		return nil, err
	}
	var env platformEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, unreachable(fmt.Errorf("unreadable platform response: %w", err))
	}
	if env.Code != 10000 {
		return nil, unreachable(fmt.Errorf("platform error %d: %s", env.Code, env.Msg))
	}
	if !env.Data.Available {
		return nil, fmt.Errorf("platform has no decision: %s", env.Data.Reason)
	}
	if env.Data.Replicas < 0 {
		return nil, errors.New("platform returned a negative replica count")
	}
	return &Decision{Replicas: env.Data.Replicas, Source: "platform:" + env.Data.Source, Version: env.Data.Version, ValidUntil: env.Data.ValidUntil, CreatedAt: env.Data.CreatedAt, Reason: env.Data.Reason,
		Algorithm: env.Data.Algorithm, Score: env.Data.Score, MinReplicas: env.Data.MinReplicas, HardMaxReplicas: env.Data.HardMaxReplicas, Urgent: env.Data.Urgent}, nil
}

// Report implements Reporter.
func (d *PlatformDecider) Report(ctx context.Context, ps *v1alpha1.PredictiveScaler, r Report) error {
	if ps.Spec.Decision.PlatformAPI == nil {
		return nil
	}
	r.Module = ps.Spec.Decision.PlatformAPI.Module
	payload, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = d.do(ctx, ps, http.MethodPost, "/api/v1/operator/reports", url.Values{}, strings.NewReader(string(payload)))
	return err
}
