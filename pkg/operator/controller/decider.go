package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/timing"
)

// Decider reads the desired replicas of a scaler.
type Decider interface {
	Decide(ctx context.Context, s *Scaler) (*Decision, error)
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
	// RecordID identifies this record for good: the operator makes it when the report is queued (see Outbox) and the platform keeps one
	// row per id, so a report that is sent twice is stored once. OccurredAt is when the operator took the action, which is not when the
	// platform hears of it after an outage. DecisionSource says what the replicas followed (decision, cache, fallback or frozen), State
	// the state of the module in an outage of the platform (normal, cached, holding, degraded, recovering) and Operator the Pod.
	RecordID       string    `json:"record_id,omitempty"`
	OccurredAt     time.Time `json:"occurred_at"`
	DecisionSource string    `json:"decision_source,omitempty"`
	State          string    `json:"state,omitempty"`
	Operator       string    `json:"operator,omitempty"`
}

// Reporter delivers a Report; failures are logged by the caller and never affect scaling.
type Reporter interface {
	Report(ctx context.Context, s *Scaler, r Report) error
}

// PlatformEndpoint is the platform the operator was started with (--platform-url and its credential).
type PlatformEndpoint struct {
	URL string
	// Credential is "<access key>:<access secret>".
	Credential string
	// Client is used as is; nil builds a default one.
	Client *http.Client
}

// PlatformDecider asks the kube-res-foresight platform for the replicas of a module. It also implements Reporter.
type PlatformDecider struct {
	Metrics *Metrics
	// Endpoint is the platform every module is read from.
	Endpoint *PlatformEndpoint
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

func (d *PlatformDecider) do(ctx context.Context, method, path string, query url.Values, body io.Reader) ([]byte, error) {
	started := time.Now()
	data, err := d.request(ctx, method, path, query, body)
	d.Metrics.recordPlatform(err, time.Since(started))
	return data, err
}

func (d *PlatformDecider) request(ctx context.Context, method, path string, query url.Values, body io.Reader) ([]byte, error) {
	if d.Endpoint == nil {
		return nil, errors.New("the operator was not started with a platform url and credential")
	}
	httpClient := d.Endpoint.Client
	if httpClient == nil {
		httpClient = &http.Client{Timeout: timing.Seconds(timing.OperatorRequestTimeout.Max)}
	}
	if d.Settings != nil {
		// how long one answer may take is a setting of the platform, read again from time to time: it bounds this request, not
		// the client, which only carries a ceiling
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, d.Settings.RequestTimeout())
		defer cancel()
	}
	base, err := url.Parse(strings.TrimRight(d.Endpoint.URL, "/"))
	if err != nil || base.Host == "" || (base.Scheme != "http" && base.Scheme != "https") {
		return nil, fmt.Errorf("invalid platform url %q", d.Endpoint.URL)
	}
	target := base.JoinPath(path)
	target.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, method, target.String(), body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "kube-res-foresight-AK "+d.Endpoint.Credential)
	// who is calling: the platform records it in the operation log (source "operator", the Pod as the client name)
	req.Header.Set("X-kube-res-foresight-Client", "operator")
	if host, err := os.Hostname(); err == nil && host != "" {
		req.Header.Set("X-kube-res-foresight-Client-Name", host)
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
		return nil, unreachable(&statusError{Code: resp.StatusCode})
	}
	return data, nil
}

// statusError is the HTTP status of an answer that was not a 200.
type statusError struct{ Code int }

func (e *statusError) Error() string { return fmt.Sprintf("platform answered %d", e.Code) }

// Decide implements Decider.
func (d *PlatformDecider) Decide(ctx context.Context, s *Scaler) (*Decision, error) {
	data, err := d.do(ctx, http.MethodGet, "/api/v1/operator/decisions", url.Values{"module": {s.Module}}, nil)
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
func (d *PlatformDecider) Report(ctx context.Context, s *Scaler, r Report) error {
	r.Module = s.Module
	payload, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = d.do(ctx, http.MethodPost, "/api/v1/operator/reports", url.Values{}, strings.NewReader(string(payload)))
	return err
}
