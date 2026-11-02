package forecastsvc

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeModelService stands in for a model service of the finetune protocol: it records every request and keeps the time
// of the fit it was asked to make.
type fakeModelService struct {
	srv *httptest.Server

	mu       sync.Mutex
	fits     []map[string]any
	predicts []map[string]any
	tunedAt  map[string]time.Time
	failFit  bool
	now      func() time.Time
}

func newFakeModelService(t *testing.T, now func() time.Time) *fakeModelService {
	t.Helper()
	f := &fakeModelService{tunedAt: map[string]time.Time{}, now: now}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		var body map[string]any
		if raw, _ := io.ReadAll(r.Body); len(raw) > 0 {
			_ = json.Unmarshal(raw, &body)
		}
		switch r.URL.Path {
		case "/v1/predict":
			f.predicts = append(f.predicts, body)
			h := int(body["horizon"].(float64))
			mean := make([]float64, h)
			for i := range mean {
				mean[i] = 0.5
			}
			raw, _ := json.Marshal(map[string]any{"mean": mean, "quantiles": map[string][]float64{"0.5": mean}})
			_, _ = w.Write(raw)
		case "/v1/fit":
			f.fits = append(f.fits, body)
			if f.failFit {
				http.Error(w, "out of memory", http.StatusInternalServerError)
				return
			}
			f.tunedAt[body["model_key"].(string)] = f.now()
			_, _ = w.Write([]byte(`{"steps":1,"points":1,"seconds":2}`))
		case "/v1/tuned":
			at, ok := f.tunedAt[r.URL.Query().Get("key")]
			if !ok {
				http.NotFound(w, r)
				return
			}
			raw, _ := json.Marshal(map[string]any{"fitted_at": at})
			_, _ = w.Write(raw)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeModelService) counts() (fits, predicts int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.fits), len(f.predicts)
}

func tuningFixture(t *testing.T, extra map[string]any) (*fixture, *fakeModelService) {
	t.Helper()
	f := newFixture(t)
	f.svc.TuneInline = true
	svc := newFakeModelService(t, func() time.Time { return *f.clock })
	params := map[string]any{"endpoint": svc.srv.URL, "finetune_interval_hours": 6, "finetune_steps": 7, "finetune_learning_rate": 0.0003,
		"finetune_batch_size": 4, "finetune_context_length": 64, "finetune_min_points": 60}
	for k, v := range extra {
		params[k] = v
	}
	f.bind(t, "cpu", "model_service", params, 0)
	_, err := f.collect(context.Background())
	require.NoError(t, err)
	return f, svc
}

// The first forecast starts a fine-tune with the parameters the administrator saved and asks the service to answer from
// the copy of this series; a copy that is fresh is not made again, an old one is.
func TestAFineTunedModelServiceIsTunedWithTheSavedParametersAndOnlyWhenTheCopyIsOld(t *testing.T) {
	f, svc := tuningFixture(t, nil)
	ctx := context.Background()

	outcomes, err := f.predict(ctx)
	require.NoError(t, err)
	assert.Equal(t, map[string]bool{"model_service": true}, algorithmsPredicted(outcomes))

	fits, predicts := svc.counts()
	require.Equal(t, 1, fits, "no copy yet: the series is submitted")
	require.Equal(t, 1, predicts)
	fit := svc.fits[0]
	assert.EqualValues(t, 7, fit["steps"])
	assert.EqualValues(t, 0.0003, fit["learning_rate"])
	assert.EqualValues(t, 4, fit["batch_size"])
	assert.EqualValues(t, 64, fit["context_length"])
	assert.Equal(t, "full", fit["mode"])
	assert.True(t, strings.HasPrefix(fit["model_key"].(string), "m"), "the series is named by module, source and algorithm: %v", fit["model_key"])
	assert.Equal(t, fit["model_key"], svc.predicts[0]["model_key"], "the forecast asks for the copy of the same series")

	_, err = f.predict(ctx)
	require.NoError(t, err)
	fits, _ = svc.counts()
	assert.Equal(t, 1, fits, "the copy is fresh: not made again")

	*f.clock = f.clock.Add(7 * time.Hour)
	_, err = f.collect(ctx)
	require.NoError(t, err)
	_, err = f.predict(ctx)
	require.NoError(t, err)
	fits, _ = svc.counts()
	assert.Equal(t, 2, fits, "older than the 6 hour interval: submitted again")

	var runs []model.TrainingRun
	require.NoError(t, f.db.Where("algorithm_id = ?", "model_service").Find(&runs).Error)
	var tuned int
	for _, r := range runs {
		if strings.HasPrefix(r.Reason, "fine-tuned (full, 7 steps, learning rate 0.0003, batch 4, context 64)") {
			tuned++
		}
	}
	assert.Equal(t, 2, tuned, "each fine-tune is on record with the parameters it used")
}

// A fine-tune that fails never fails the forecast: the service still answers (from the pretrained model), and the failure
// is on record.
func TestAFailedFineTuneLeavesTheForecastAndIsRecorded(t *testing.T) {
	f, svc := tuningFixture(t, nil)
	svc.failFit = true

	outcomes, err := f.predict(context.Background())
	require.NoError(t, err)
	assert.Equal(t, map[string]bool{"model_service": true}, algorithmsPredicted(outcomes))

	var runs []model.TrainingRun
	require.NoError(t, f.db.Where("algorithm_id = ? AND status = ?", "model_service", StatusFailed).Find(&runs).Error)
	require.Len(t, runs, 1)
	assert.Contains(t, runs[0].Reason, "fine-tune failed")
	assert.Contains(t, runs[0].Reason, "out of memory")

	svc.failFit = false
	_, err = f.predict(context.Background())
	require.NoError(t, err)
	fits, _ := svc.counts()
	assert.Equal(t, 2, fits, "the next forecast tries again")
}

// With the interval at 0 the platform never tunes and never names a series, so a copy left from an earlier setting is not used.
func TestWithFineTuningOffTheServiceIsOnlyAskedToPredict(t *testing.T) {
	f, svc := tuningFixture(t, map[string]any{"finetune_interval_hours": 0})
	_, err := f.predict(context.Background())
	require.NoError(t, err)
	fits, predicts := svc.counts()
	assert.Zero(t, fits)
	assert.Equal(t, 1, predicts)
	assert.NotContains(t, svc.predicts[0], "model_key", "an empty series name: the pretrained model answers")
}

// A series shorter than finetune_min_points is not worth tuning on.
func TestAShortSeriesIsNotSubmittedForFineTuning(t *testing.T) {
	f, svc := tuningFixture(t, map[string]any{"finetune_min_points": 100000})
	_, err := f.predict(context.Background())
	require.NoError(t, err)
	fits, predicts := svc.counts()
	assert.Zero(t, fits)
	assert.Equal(t, 1, predicts)
}

// A backtest uses the pretrained model: it never names a series, so it cannot read a copy tuned on data after its cutoff.
func TestABacktestNeverUsesATunedCopy(t *testing.T) {
	f, svc := tuningFixture(t, nil)
	_, err := f.backtest(context.Background(), "cpu", 3)
	require.NoError(t, err)
	fits, predicts := svc.counts()
	assert.Zero(t, fits, "a backtest does not tune")
	require.Positive(t, predicts)
	for _, p := range svc.predicts {
		assert.NotContains(t, p, "model_key", "a backtest window is cut from the history and carries no series name")
	}
}
