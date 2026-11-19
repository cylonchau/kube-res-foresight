package forecastsvc

import "context"

// StepUpdate is how far a long step of a module is, as the step reports it (P11 B2): the stage it is in, how many of its parts are done, the share of the
// whole step that is (Fraction, 0 to 1) and what it has produced so far. A step run in the background hands it to its task, so the page shows the progress and
// the parts that are done while the rest still runs.
type StepUpdate struct {
	Stage    string  `json:"stage"`
	Done     int     `json:"done"`
	Total    int     `json:"total"`
	Fraction float64 `json:"fraction"`
	// Partial is what the step has produced so far; it is a value that marshals to JSON, not one that is kept or changed by the step afterwards.
	Partial any `json:"-"`
}

type (
	stepProgressKey struct{}
	stepPhaseKey    struct{}
)

type stepPhase struct{ index, count int }

// WithStepProgress makes a context in which the steps report to fn.
func WithStepProgress(ctx context.Context, fn func(StepUpdate)) context.Context {
	return context.WithValue(ctx, stepProgressKey{}, fn)
}

// withPhase says the work that follows is phase index of count phases of the step: its fraction is mapped into that part of the whole.
func withPhase(ctx context.Context, index, count int) context.Context {
	return context.WithValue(ctx, stepPhaseKey{}, stepPhase{index, count})
}

// stepReport tells the step's owner that done of total parts of the stage are done, with what has been produced.
func stepReport(ctx context.Context, stage string, done, total int, partial any) {
	fn, ok := ctx.Value(stepProgressKey{}).(func(StepUpdate))
	if !ok || fn == nil {
		return
	}
	share := 1.0
	if total > 0 {
		share = float64(done) / float64(total)
	}
	if ph, ok := ctx.Value(stepPhaseKey{}).(stepPhase); ok && ph.count > 0 {
		share = (float64(ph.index) + share) / float64(ph.count)
	}
	if share > 1 {
		share = 1
	}
	fn(StepUpdate{Stage: stage, Done: done, Total: total, Fraction: share, Partial: partial})
}

type backgroundKey struct{}

// Background says the work is not held by a request: a step run as a task has no gateway waiting for it and is bounded by its own parameter, not by the
// room under the gateway timeout.
func Background(ctx context.Context) context.Context {
	return context.WithValue(ctx, backgroundKey{}, true)
}

func isBackground(ctx context.Context) bool {
	v, _ := ctx.Value(backgroundKey{}).(bool)
	return v
}

// ReportForTest is stepReport for the tests of the packages that run the steps.
func ReportForTest(ctx context.Context, stage string, done, total int, partial any) {
	stepReport(ctx, stage, done, total, partial)
}
