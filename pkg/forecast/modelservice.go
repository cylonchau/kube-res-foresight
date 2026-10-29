package forecast

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// Protocols a model service can speak. unitshub_* are the ready-made read-only containers (kingfs/unitshub: predict
// only, zero-shot); finetune is the service of scripts/tsfm-eval/finetune, which also fine-tunes on a series the
// platform submits.
const (
	ProtocolUnitsChronos = "unitshub_chronos"
	ProtocolUnitsTimesFM = "unitshub_timesfm"
	ProtocolFinetune     = "finetune"
)

// TuneSettings are the fine-tuning parameters the platform defines and hands to the model service with every fit
// request. The service decides nothing about them: what the administrator saved is what it trains with.
type TuneSettings struct {
	// IntervalHours is how old the tuned copy may get before the platform submits the series again; 0 = never tune and
	// always predict with the pretrained model.
	IntervalHours int
	Mode          string // full | lora
	Steps         int
	LearningRate  float64
	BatchSize     int
	ContextLength int // newest steps a training window may read
	MinPoints     int // shortest series worth fine-tuning on
}

// Enabled says whether the platform keeps a fine-tuned copy at the service.
func (t TuneSettings) Enabled() bool { return t.IntervalHours > 0 }

// TuneResult is what the service reports about a fit.
type TuneResult struct {
	Steps   int
	Points  int
	Seconds float64
}

// Tunable is a model whose provider can be fine-tuned on the series it forecasts. The platform submits the series
// (Tune), asks how old the tuned copy is (TunedAt) and the forecast then names the series (Input.Series) so the
// service answers from that copy.
type Tunable interface {
	Model
	Tuning() TuneSettings
	// TunedAt is when the service finished the fit behind key, or false when it holds none.
	TunedAt(ctx context.Context, key string) (time.Time, bool, error)
	// Tune submits the history and the fine-tuning parameters for the given forecast horizon (in steps).
	Tune(ctx context.Context, key string, values []float64, horizon int) (TuneResult, error)
}

// unwrapper is implemented by the models that wrap another one (short-term correction, regime reset, conformal).
type unwrapper interface{ Unwrap() Model }

// TunableOf finds the fine-tunable model inside the wrappers of m.
func TunableOf(m Model) (Tunable, bool) {
	for m != nil {
		if t, ok := m.(Tunable); ok {
			return t, true
		}
		u, ok := m.(unwrapper)
		if !ok {
			return nil, false
		}
		m = u.Unwrap()
	}
	return nil, false
}

// Unwrap returns the model the correction wraps.
func (m ShortTerm) Unwrap() Model { return m.Inner }

// Unwrap returns the model the reset wraps.
func (m RegimeReset) Unwrap() Model { return m.Base }

// Unwrap returns the model the quantile calibration wraps.
func (m Conformal) Unwrap() Model { return m.Base }

// ModelService forecasts by asking a model that runs as a separate service. The history before the forecast is sent
// with every request, so the service keeps nothing about the series between calls except the fine-tuned copy it was
// asked to make.
type ModelService struct {
	Protocol   string
	Endpoint   string
	APIKeyEnv  string // name of the environment variable that holds the key; the key itself is never stored
	Timeout    time.Duration
	FitTimeout time.Duration
	Quantile   float64 // 0 = the mean forecast, else a quantile the service reports
	Settings   TuneSettings
	Client     *http.Client
}

// ID is the algorithm id.
func (m ModelService) ID() string { return "model_service" }

// MinSamples is the shortest history sent to the service; the service has its own limits.
func (m ModelService) MinSamples() int { return 30 }

// QuantileLevel makes a quantile forecast visible to the backtest and to the stored predictions.
func (m ModelService) QuantileLevel() (float64, bool) { return m.Quantile, m.Quantile > 0 }

// Tuning returns the fine-tuning parameters.
func (m ModelService) Tuning() TuneSettings { return m.Settings }

func (m ModelService) client() *http.Client {
	if m.Client != nil {
		return m.Client
	}
	return http.DefaultClient
}

func (m ModelService) call(ctx context.Context, timeout time.Duration, method, path string, body, out any) (int, error) {
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(m.Endpoint, "/")+path, rd)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if m.APIKeyEnv != "" {
		if key := os.Getenv(m.APIKeyEnv); key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
	}
	resp, err := m.client().Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, fmt.Errorf("model service %s: %d %.200s", path, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return resp.StatusCode, fmt.Errorf("model service %s: unreadable answer: %w", path, err)
		}
	}
	return resp.StatusCode, nil
}

// Forecast sends the history and returns the next horizon steps. A resource amount is never negative, so the answer is
// clamped at zero like the platform's own algorithms.
func (m ModelService) Forecast(ctx context.Context, in Input, horizon int) ([]float64, error) {
	if err := checkInput(m, in, horizon); err != nil {
		return nil, err
	}
	level := m.Quantile
	if level == 0 {
		level = 0.5
	}
	var mean []float64
	var quantiles map[string][]float64
	switch m.Protocol {
	case ProtocolUnitsChronos, ProtocolUnitsTimesFM:
		task, input := "forecast_quantile", map[string]any{"series": in.Values, "horizon": horizon, "quantiles": []float64{level}}
		if m.Protocol == ProtocolUnitsTimesFM {
			task, input = "forecast_point", map[string]any{"history": in.Values, "horizon": horizon}
		}
		var out struct {
			Output struct {
				Forecasts []struct {
					Mean      []float64            `json:"mean"`
					Quantiles map[string][]float64 `json:"quantiles"`
				} `json:"forecasts"`
			} `json:"output"`
		}
		if _, err := m.call(ctx, m.Timeout, http.MethodPost, "/models/current/invoke", map[string]any{"task": task, "input": input}, &out); err != nil {
			return nil, err
		}
		if len(out.Output.Forecasts) != 1 {
			return nil, fmt.Errorf("model service: expected one forecast, got %d", len(out.Output.Forecasts))
		}
		mean, quantiles = out.Output.Forecasts[0].Mean, out.Output.Forecasts[0].Quantiles
	case ProtocolFinetune:
		var out struct {
			Mean      []float64            `json:"mean"`
			Quantiles map[string][]float64 `json:"quantiles"`
		}
		body := map[string]any{"values": in.Values, "horizon": horizon, "quantiles": []float64{level}}
		if in.Series != "" {
			body["model_key"] = in.Series // without it the service answers from the pretrained model
		}
		if _, err := m.call(ctx, m.Timeout, http.MethodPost, "/v1/predict", body, &out); err != nil {
			return nil, err
		}
		mean, quantiles = out.Mean, out.Quantiles
	default:
		return nil, fmt.Errorf("model service: unknown protocol %q", m.Protocol)
	}
	values := mean
	if m.Quantile > 0 {
		var ok bool
		if values, ok = quantiles[fmt.Sprintf("%g", m.Quantile)]; !ok {
			return nil, fmt.Errorf("model service: no %g quantile in the answer", m.Quantile)
		}
	}
	if len(values) != horizon {
		return nil, fmt.Errorf("model service: asked for %d steps, got %d", horizon, len(values))
	}
	out := make([]float64, horizon)
	for i, v := range values {
		if v != v || v > 1e300 || v < -1e300 { // NaN or infinite
			return nil, fmt.Errorf("model service: step %d is not a finite number", i+1)
		}
		if v < 0 {
			v = 0
		}
		out[i] = v
	}
	return out, nil
}

// TunedAt asks the service when its fine-tuned copy for key was made.
func (m ModelService) TunedAt(ctx context.Context, key string) (time.Time, bool, error) {
	if m.Protocol != ProtocolFinetune {
		return time.Time{}, false, nil
	}
	var out struct {
		FittedAt time.Time `json:"fitted_at"`
	}
	code, err := m.call(ctx, m.Timeout, http.MethodGet, "/v1/tuned?key="+url.QueryEscape(key), nil, &out)
	if code == http.StatusNotFound {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, err
	}
	return out.FittedAt, true, nil
}

// Tune submits the series with the platform's fine-tuning parameters and waits for the service to finish.
func (m ModelService) Tune(ctx context.Context, key string, values []float64, horizon int) (TuneResult, error) {
	if m.Protocol != ProtocolFinetune {
		return TuneResult{}, fmt.Errorf("model service: protocol %q cannot be fine-tuned", m.Protocol)
	}
	t := m.Settings
	body := map[string]any{"model_key": key, "values": values, "horizon": horizon, "mode": t.Mode, "steps": t.Steps,
		"learning_rate": t.LearningRate, "batch_size": t.BatchSize, "context_length": t.ContextLength}
	var out struct {
		Steps   int     `json:"steps"`
		Points  int     `json:"points"`
		Seconds float64 `json:"seconds"`
	}
	if _, err := m.call(ctx, m.FitTimeout, http.MethodPost, "/v1/fit", body, &out); err != nil {
		return TuneResult{}, err
	}
	return TuneResult{Steps: out.Steps, Points: out.Points, Seconds: out.Seconds}, nil
}
