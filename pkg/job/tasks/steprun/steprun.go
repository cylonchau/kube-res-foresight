// Package steprun runs the long steps of a module (collect, predict, run, backtest, evaluate, score) and the change preview as async tasks (P11 B1, B2). The
// same service calls answer the synchronous endpoints; here they are not held to the gateway timeout, only to the system parameter of the step
// (forecast.pipeline_timeout_seconds), because no request waits for them: the page reads the task. The steps report how far they are and what they have
// produced so far, which the page shows while the rest runs and keeps if the step fails.
package steprun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/apis"
	"github.com/cylonchau/kube-res-foresight/pkg/forecastsvc"
	"github.com/cylonchau/kube-res-foresight/pkg/job/tasks/asyncrun"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/cylonchau/kube-res-foresight/pkg/timing"
)

// Kind is the kind of the async task that runs one step of a module.
const Kind = "forecast.step"

// PreviewKind is the kind of the async task that replays a change of a module.
const PreviewKind = "forecast.preview"

// Steps are the steps that can be run in the background.
var Steps = map[string]bool{"collect": true, "predict": true, "run": true, "backtest": true, "evaluate": true, "score": true}

// Attempts is how many times each step is run for a failure that can be tried again (a data source that could not be reached): a step that only reads
// and stores history again is cheap to repeat, one that computes for minutes is not.
var Attempts = map[string]int{"collect": 3, "predict": 2, "run": 2, "evaluate": 2, "backtest": 1, "score": 1}

// PreviewAttempts is the same for the change preview.
const PreviewAttempts = 2

// Input is what a step task is given.
type Input struct {
	ModuleID uint            `json:"module_id"`
	Step     string          `json:"step"`
	Body     json.RawMessage `json:"body,omitempty"`
}

// PreviewInput is what a preview task is given.
type PreviewInput struct {
	ModuleID   uint                      `json:"module_id"`
	SinceHours int                       `json:"since_hours"`
	Change     forecastsvc.PreviewChange `json:"change"`
}

// reporting hands what a step reports to its task: the share of the whole as the progress, and what it has produced so far.
func reporting(ctx context.Context, rep asyncrun.Reporter) context.Context {
	return forecastsvc.WithStepProgress(forecastsvc.Background(ctx), func(u forecastsvc.StepUpdate) {
		rep.Progress(5+int(u.Fraction*90), u.Stage)
		if u.Partial != nil {
			rep.PartialResult(map[string]any{"stage": u.Stage, "done": u.Done, "total": u.Total, "result": u.Partial})
		}
	})
}

// bounded runs fn with the time limit of the pipeline. A step that is still running at the limit did not fail for a reason that waiting mends: it is the
// same work, with the same amount of data, and it would take as long again, so it is not tried again.
func bounded(ctx context.Context, limit time.Duration, fn func(context.Context) (any, error)) (any, error) {
	inner, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	out, err := fn(inner)
	if err != nil && ctx.Err() == nil && errors.Is(inner.Err(), context.DeadlineExceeded) {
		return out, asyncrun.Permanent(fmt.Errorf("the step did not finish within %s (forecast.pipeline_timeout_seconds): %w", limit.Round(time.Second), err))
	}
	return out, err
}

// Handler runs the step of a task with the service of the platform.
func Handler(svc *forecastsvc.Service) asyncrun.Handler {
	return func(ctx context.Context, t *model.AsyncTask, rep asyncrun.Reporter) (any, error) {
		var in Input
		if err := json.Unmarshal([]byte(t.Payload), &in); err != nil || in.ModuleID == 0 || !Steps[in.Step] {
			return nil, asyncrun.Permanent(errors.New("the step of the task cannot be read"))
		}
		rep.Progress(5, in.Step)
		ctx = reporting(ctx, rep)
		return bounded(ctx, svc.Timing(ctx, timing.PipelineTimeout), func(ctx context.Context) (any, error) {
			switch in.Step {
			case "collect":
				return svc.Collect(ctx, in.ModuleID)
			case "predict":
				return svc.Predict(ctx, in.ModuleID)
			case "run":
				return svc.RunData(ctx, in.ModuleID, nil)
			case "evaluate":
				return svc.Evaluate(ctx, in.ModuleID)
			case "score":
				return svc.ScoreModule(ctx, in.ModuleID)
			case "backtest":
				var req apis.BacktestReq
				if err := json.Unmarshal(in.Body, &req); err != nil || req.Metric == "" {
					return nil, asyncrun.Permanent(errors.New("the backtest needs a metric"))
				}
				return svc.Backtest(ctx, in.ModuleID, req.Metric, req.Cutoffs)
			}
			return nil, asyncrun.Permanent(fmt.Errorf("unknown step %q", in.Step))
		})
	}
}

// PreviewHandler replays a change of a module as a task: the same replay as the synchronous preview, held to the pipeline limit and not to the room under the
// gateway timeout. A request that is not valid (an empty change, too many hours) is not tried again.
func PreviewHandler(svc *forecastsvc.Service) asyncrun.Handler {
	return func(ctx context.Context, t *model.AsyncTask, rep asyncrun.Reporter) (any, error) {
		var in PreviewInput
		if err := json.Unmarshal([]byte(t.Payload), &in); err != nil || in.ModuleID == 0 {
			return nil, asyncrun.Permanent(errors.New("the preview of the task cannot be read"))
		}
		rep.Progress(5, "preview")
		ctx = reporting(ctx, rep)
		out, err := bounded(ctx, svc.Timing(ctx, timing.PipelineTimeout), func(ctx context.Context) (any, error) {
			rep, _, err := svc.PreviewChangeCached(ctx, in.ModuleID, in.SinceHours, in.Change)
			return rep, err
		})
		if err != nil && (errors.Is(err, forecastsvc.ErrPreviewInvalid) || errors.Is(err, forecastsvc.ErrNothingToScore) || errors.Is(err, forecastsvc.ErrPreviewTooLong)) {
			return nil, asyncrun.Permanent(err)
		}
		return out, err
	}
}
