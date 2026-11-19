package steprun

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/forecastsvc"
	"github.com/cylonchau/kube-res-foresight/pkg/job/tasks/asyncrun"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeReporter struct {
	percent  []int
	stages   []string
	partials []string
}

func (f *fakeReporter) Progress(p int, stage string) {
	f.percent, f.stages = append(f.percent, p), append(f.stages, stage)
}
func (f *fakeReporter) Event(string, any) {}
func (f *fakeReporter) Partial(string)    {}
func (f *fakeReporter) PartialResult(v any) {
	raw, _ := json.Marshal(v)
	f.partials = append(f.partials, string(raw))
}

func TestWhatAStepReportsBecomesTheProgressOfItsTaskAndWhatItHasProducedSoFar(t *testing.T) {
	rep := &fakeReporter{}
	ctx := reporting(context.Background(), rep)
	// the step reports through the service: the same function a collect or a predict calls
	forecastsvc.ReportForTest(ctx, "collect: cpu", 1, 4, []string{"cpu"})
	forecastsvc.ReportForTest(ctx, "collect: memory", 4, 4, []string{"cpu", "memory"})
	assert.Equal(t, []int{5 + 22, 5 + 90}, rep.percent, "the fraction of the whole is mapped between 5 and 95")
	assert.Equal(t, []string{"collect: cpu", "collect: memory"}, rep.stages)
	require.Len(t, rep.partials, 2)
	assert.JSONEq(t, `{"stage":"collect: memory","done":4,"total":4,"result":["cpu","memory"]}`, rep.partials[1])
}

func TestAStepThatRunsOutOfTheLimitOfThePipelineIsNotTriedAgainAndOneThatTheOwnerCancelledIsLeftAlone(t *testing.T) {
	_, err := bounded(context.Background(), 20*time.Millisecond, func(ctx context.Context) (any, error) {
		<-ctx.Done()
		return "partial", ctx.Err()
	})
	require.Error(t, err)
	retryable, _ := asyncrun.Classify(err)
	assert.False(t, retryable, "the same work with the same data takes as long again")
	assert.Contains(t, err.Error(), "forecast.pipeline_timeout_seconds")
	assert.ErrorIs(t, err, context.DeadlineExceeded)

	parent, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = bounded(parent, time.Second, func(ctx context.Context) (any, error) { return nil, ctx.Err() })
	assert.ErrorIs(t, err, context.Canceled)
	retryable, _ = asyncrun.Classify(err)
	assert.False(t, retryable)

	// a data source that did not answer in time is a failure that waiting can mend: the step does not wrap it as the limit
	_, err = bounded(context.Background(), time.Second, func(ctx context.Context) (any, error) { return nil, errors.New("prometheus: connection refused") })
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "forecast.pipeline_timeout_seconds")
}

func TestATaskWhoseInputCannotBeReadFailsAtOnceAndForGood(t *testing.T) {
	for name, h := range map[string]asyncrun.Handler{"step": Handler(&forecastsvc.Service{}), "preview": PreviewHandler(&forecastsvc.Service{})} {
		_, err := h(context.Background(), &model.AsyncTask{Payload: `{"module_id":0}`}, &fakeReporter{})
		require.Error(t, err, name)
		retryable, _ := asyncrun.Classify(err)
		assert.False(t, retryable, name)
	}
	_, err := Handler(&forecastsvc.Service{})(context.Background(), &model.AsyncTask{Payload: `{"module_id":1,"step":"nonsense"}`}, &fakeReporter{})
	require.Error(t, err)
}

func TestEveryStepHasItsAttempts(t *testing.T) {
	for step := range Steps {
		assert.Contains(t, Attempts, step)
		assert.GreaterOrEqual(t, Attempts[step], 1, step)
	}
	assert.Greater(t, Attempts["collect"], Attempts["score"], "reading history again is cheap and a score that computes for minutes is not")
}
