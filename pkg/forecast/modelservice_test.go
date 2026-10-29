package forecast

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func history(n int) Input {
	v := make([]float64, n)
	for i := range v {
		v[i] = 10 + float64(i%5)
	}
	return Input{Values: v, Step: time.Minute, End: time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC), Series: "m1/cpu/model_service"}
}

func serviceOn(t *testing.T, srv *httptest.Server, extra map[string]any) Model {
	t.Helper()
	params := map[string]any{"endpoint": srv.URL}
	for k, v := range extra {
		params[k] = v
	}
	m, err := Build("model_service", params)
	require.NoError(t, err)
	return m
}

// The finetune protocol sends the history and the series name; a quantile is read from the answer; a negative value is
// clamped to zero like every other algorithm of the platform.
func TestTheFinetuneProtocolSendsTheHistoryAndTheSeriesAndReadsTheAnswer(t *testing.T) {
	var got map[string]any
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/predict", r.URL.Path)
		auth = r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		require.NoError(t, json.Unmarshal(raw, &got))
		_, _ = w.Write([]byte(`{"mean":[1,2,-3],"quantiles":{"0.9":[4,5,6]}}`))
	}))
	defer srv.Close()
	t.Setenv("MODEL_KEY_FOR_TEST", "secret")

	m := serviceOn(t, srv, map[string]any{"api_key_env": "MODEL_KEY_FOR_TEST"})
	out, err := m.Forecast(context.Background(), history(40), 3)
	require.NoError(t, err)
	assert.Equal(t, []float64{1, 2, 0}, out, "the mean forecast, a negative step clamped to zero")
	assert.Equal(t, "Bearer secret", auth, "the key comes from the environment variable named in the parameters")
	assert.Equal(t, "m1/cpu/model_service", got["model_key"])
	assert.Len(t, got["values"], 40)

	q := serviceOn(t, srv, map[string]any{"service_quantile": 0.9})
	out, err = q.Forecast(context.Background(), history(40), 3)
	require.NoError(t, err)
	assert.Equal(t, []float64{4, 5, 6}, out)
	level, on := q.(Quantiled).QuantileLevel()
	assert.True(t, on)
	assert.Equal(t, 0.9, level)
}

// The ready-made unitshub containers have their own request shapes.
func TestTheUnitsHubProtocolsUseTheirOwnRequestShapes(t *testing.T) {
	for protocol, want := range map[string]struct{ task, field string }{
		ProtocolUnitsChronos: {"forecast_quantile", "series"},
		ProtocolUnitsTimesFM: {"forecast_point", "history"},
	} {
		t.Run(protocol, func(t *testing.T) {
			var got struct {
				Task  string         `json:"task"`
				Input map[string]any `json:"input"`
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "/models/current/invoke", r.URL.Path)
				raw, _ := io.ReadAll(r.Body)
				require.NoError(t, json.Unmarshal(raw, &got))
				_, _ = w.Write([]byte(`{"output":{"forecasts":[{"mean":[7,8],"quantiles":{}}]}}`))
			}))
			defer srv.Close()
			m := serviceOn(t, srv, map[string]any{"protocol": protocol})
			out, err := m.Forecast(context.Background(), history(40), 2)
			require.NoError(t, err)
			assert.Equal(t, []float64{7, 8}, out)
			assert.Equal(t, want.task, got.Task)
			assert.Contains(t, got.Input, want.field)
		})
	}
}

// A service that fails or answers wrongly fails the forecast; it never produces made-up numbers.
func TestAServiceThatAnswersBadlyFailsTheForecast(t *testing.T) {
	for name, body := range map[string]string{
		"wrong length": `{"mean":[1,2],"quantiles":{}}`,
		"not a number": `{"mean":[1,"x",3],"quantiles":{}}`,
		"missing":      `{}`,
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
			defer srv.Close()
			_, err := serviceOn(t, srv, nil).Forecast(context.Background(), history(40), 3)
			assert.Error(t, err)
		})
	}
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "boom", http.StatusInternalServerError) }))
	_, err := serviceOn(t, down, nil).Forecast(context.Background(), history(40), 3)
	down.Close()
	assert.ErrorContains(t, err, "500")
	_, err = serviceOn(t, down, nil).Forecast(context.Background(), history(40), 3)
	assert.Error(t, err, "a service that is gone")

	_, err = serviceOn(t, down, nil).Forecast(context.Background(), history(5), 3)
	assert.ErrorIs(t, err, ErrInsufficientHistory)
}

// The fine-tuning parameters are the platform's: what is saved in the algorithm is what the service is sent, and the
// service is asked when its copy was made.
func TestTheFineTuningParametersAreTheOnesTheAdministratorSaved(t *testing.T) {
	var fit map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/fit":
			raw, _ := io.ReadAll(r.Body)
			require.NoError(t, json.Unmarshal(raw, &fit))
			_, _ = w.Write([]byte(`{"steps":50,"points":120,"seconds":12.5}`))
		case "/v1/tuned":
			if r.URL.Query().Get("key") != "m1/cpu/model_service" {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write([]byte(`{"fitted_at":"2026-10-29T10:00:00Z"}`))
		default:
			t.Errorf("unexpected %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	m := serviceOn(t, srv, map[string]any{"finetune_interval_hours": 6, "finetune_mode": "full", "finetune_steps": 50,
		"finetune_learning_rate": 0.0002, "finetune_batch_size": 8, "finetune_context_length": 128, "finetune_min_points": 100})
	tun, ok := TunableOf(m)
	require.True(t, ok)
	assert.True(t, tun.Tuning().Enabled())

	res, err := tun.Tune(context.Background(), "m1/cpu/model_service", make([]float64, 120), 10)
	require.NoError(t, err)
	assert.Equal(t, TuneResult{Steps: 50, Points: 120, Seconds: 12.5}, res)
	assert.Equal(t, "full", fit["mode"])
	assert.EqualValues(t, 50, fit["steps"])
	assert.EqualValues(t, 0.0002, fit["learning_rate"])
	assert.EqualValues(t, 8, fit["batch_size"])
	assert.EqualValues(t, 128, fit["context_length"])
	assert.EqualValues(t, 10, fit["horizon"])
	assert.Equal(t, "m1/cpu/model_service", fit["model_key"])

	at, held, err := tun.TunedAt(context.Background(), "m1/cpu/model_service")
	require.NoError(t, err)
	assert.True(t, held)
	assert.Equal(t, time.Date(2026, 10, 29, 10, 0, 0, 0, time.UTC), at)
	_, held, err = tun.TunedAt(context.Background(), "other")
	require.NoError(t, err)
	assert.False(t, held, "404 = the service holds no copy")
}

// Parameters that are each valid alone but do not work together are refused when they are saved.
func TestModelServiceParametersThatContradictEachOtherAreRefused(t *testing.T) {
	_, err := Normalize("model_service", map[string]any{"endpoint": "chronos:8000"})
	assert.ErrorContains(t, err, "http")
	_, err = Normalize("model_service", map[string]any{"endpoint": "http://x", "service_quantile": 0.3})
	assert.ErrorContains(t, err, "service_quantile")
	_, err = Normalize("model_service", map[string]any{"endpoint": "http://x", "protocol": "unitshub_chronos", "finetune_interval_hours": 6})
	assert.ErrorContains(t, err, "cannot be fine-tuned")
	_, err = Normalize("model_service", map[string]any{})
	assert.ErrorContains(t, err, "endpoint", "the address is required")

	got, err := Normalize("model_service", map[string]any{"endpoint": "http://x"})
	require.NoError(t, err)
	assert.EqualValues(t, 0, got["finetune_interval_hours"], "fine-tuning is off unless asked for")
	assert.Equal(t, "finetune", got["protocol"])
}

// The fine-tunable model is found through the corrections that wrap it, so a saved quantile or regime reset does not hide it.
func TestTheFineTunableModelIsFoundInsideItsWrappers(t *testing.T) {
	m, err := Build("model_service", map[string]any{"endpoint": "http://x", "quantile_level": 0.9, "reset_on_change": true, "finetune_interval_hours": 1})
	require.NoError(t, err)
	_, direct := m.(Tunable)
	assert.False(t, direct, "wrapped by the quantile calibration and the regime reset")
	_, found := TunableOf(m)
	assert.True(t, found)
	_, found = TunableOf(Naive{})
	assert.False(t, found)
}
