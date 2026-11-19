// Package steprun runs the long steps of a module (collect, predict, run, backtest, evaluate, score) as async tasks (P11 B1). The same service
// calls answer the synchronous endpoints; here they are not held to the gateway timeout, only to the system parameter of the step
// (forecast.pipeline_timeout_seconds), because no request waits for them: the page reads the task.
package steprun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/cylonchau/kube-res-foresight/pkg/apis"
	"github.com/cylonchau/kube-res-foresight/pkg/forecastsvc"
	"github.com/cylonchau/kube-res-foresight/pkg/job/tasks/asyncrun"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/cylonchau/kube-res-foresight/pkg/timing"
)

// Kind is the kind of the async task that runs one step of a module.
const Kind = "forecast.step"

// Steps are the steps that can be run in the background.
var Steps = map[string]bool{"collect": true, "predict": true, "run": true, "backtest": true, "evaluate": true, "score": true}

// Input is what a step task is given.
type Input struct {
	ModuleID uint            `json:"module_id"`
	Step     string          `json:"step"`
	Body     json.RawMessage `json:"body,omitempty"`
}

// Handler runs the step of a task with the service of the platform.
func Handler(svc *forecastsvc.Service) asyncrun.Handler {
	return func(ctx context.Context, t *model.AsyncTask, rep asyncrun.Reporter) (any, error) {
		var in Input
		if err := json.Unmarshal([]byte(t.Payload), &in); err != nil || in.ModuleID == 0 || !Steps[in.Step] {
			return nil, errors.New("the step of the task cannot be read")
		}
		ctx, cancel := context.WithTimeout(ctx, svc.Timing(ctx, timing.PipelineTimeout))
		defer cancel()
		rep.Progress(5, in.Step)
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
				return nil, errors.New("the backtest needs a metric")
			}
			return svc.Backtest(ctx, in.ModuleID, req.Metric, req.Cutoffs)
		}
		return nil, fmt.Errorf("unknown step %q", in.Step)
	}
}
