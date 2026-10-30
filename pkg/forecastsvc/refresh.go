package forecastsvc

import (
	"context"
	"strconv"
	"sync"
	"time"

	"github.com/cylonchau/kube-resource-fcst/pkg/model"
	"github.com/cylonchau/kube-resource-fcst/pkg/timing"
)

// ConfigDecisionMaxAge is the system parameter that says how old the recommendation the operator reads may be, in seconds: 0 turns
// the refresh off and the operator reads what the scheduled task stored.
const ConfigDecisionMaxAge = "forecast.decision_max_age_seconds"

// DefaultDecisionMaxAge is how old a recommendation may be when the operator asks for it, without the platform making it again first.
// The scheduled task runs every few minutes; a scaler that reads every 30 seconds would otherwise act on a decision made before
// the load changed.
const DefaultDecisionMaxAge = 60 * time.Second

// DecisionMaxAge is the age in force: the system parameter, or the default when it is missing or not a number of seconds.
func (s *Service) DecisionMaxAge(ctx context.Context) time.Duration {
	if s.Config == nil {
		return DefaultDecisionMaxAge
	}
	v, err := s.Config.GetValueOrDefault(ctx, ConfigDecisionMaxAge, "")
	if err != nil || v == "" {
		return DefaultDecisionMaxAge
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 || n > 86400 {
		return DefaultDecisionMaxAge
	}
	return time.Duration(n) * time.Second
}

// refreshState serialises the refreshes of one module and remembers when the last one was tried.
type refreshState struct {
	mu   sync.Mutex
	last time.Time
}

// pipelineLock serialises the pipeline work of one module (collect, predict, recommend): the scheduled task and a refresh that
// the operator asked for must not write the same module's forecasts and recommendation versions at the same time.
func (s *Service) pipelineLock(moduleID uint) *sync.Mutex {
	v, _ := s.pipelines.LoadOrStore(moduleID, &sync.Mutex{})
	return v.(*sync.Mutex)
}

// FreshRecommendation is the newest recommendation of a module, made again first when it is older than maxAge: the platform
// decides before the operator acts, on the load as it is now, not on the one of the last scheduled run. refreshed says that a new
// version was made.
//
// The refresh is the same pipeline the scheduled task runs (collect the new samples, forecast with every algorithm, decide), once at
// a time per module. A module that runs on its own interval (run_interval_minutes) is left to it. A refresh is not tried again
// sooner than maxAge after the last one, whatever came of it, so a module whose algorithms are slow is not recomputed at every poll
// of the scaler; and a refresh that fails leaves the stored recommendation to be read, with the error.
func (s *Service) FreshRecommendation(ctx context.Context, moduleID uint, maxAge time.Duration) (rec *model.Recommendation, refreshed bool, err error) {
	rec, err = s.Predictions.LatestRecommendation(ctx, moduleID)
	if err != nil {
		return nil, false, err
	}
	fresh := func(r *model.Recommendation) bool { return r != nil && s.now().Sub(r.CreatedAt) <= maxAge }
	if maxAge <= 0 || fresh(rec) {
		return rec, false, nil
	}

	// The refresh runs on its own context, not the request's: the operator hangs up after its own timeout, and a refresh cut off half way
	// leaves forecasts without their training records. The request waits as long as its context allows; when that runs out the stored
	// recommendation is read and the refresh goes on, so the next poll finds its result.
	flight := s.refreshFlightOf(ctx, moduleID, maxAge)
	select {
	case <-flight.done:
		if flight.err != nil {
			return rec, false, flight.err
		}
		if flight.rec == nil {
			return rec, false, nil
		}
		return flight.rec, flight.refreshed, nil
	case <-ctx.Done():
		return rec, false, nil
	}
}

// refreshFlight is a refresh of one module that is under way, shared by every request that asks for the module meanwhile.
type refreshFlight struct {
	done      chan struct{}
	rec       *model.Recommendation
	refreshed bool
	err       error
}

func (s *Service) refreshFlightOf(ctx context.Context, moduleID uint, maxAge time.Duration) *refreshFlight {
	flight := &refreshFlight{done: make(chan struct{})}
	if v, loaded := s.flights.LoadOrStore(moduleID, flight); loaded {
		return v.(*refreshFlight)
	}
	work, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.Timing(ctx, timing.RefreshBudget))
	go func() {
		defer cancel()
		defer close(flight.done)
		defer s.flights.Delete(moduleID)
		flight.rec, flight.refreshed, flight.err = s.refreshNow(work, moduleID, maxAge)
	}()
	return flight
}

// refreshNow makes the recommendation again, once at a time per module.
func (s *Service) refreshNow(ctx context.Context, moduleID uint, maxAge time.Duration) (rec *model.Recommendation, refreshed bool, err error) {
	rec, err = s.Predictions.LatestRecommendation(ctx, moduleID)
	if err != nil {
		return nil, false, err
	}
	fresh := func(r *model.Recommendation) bool { return r != nil && s.now().Sub(r.CreatedAt) <= maxAge }
	v, _ := s.refreshes.LoadOrStore(moduleID, &refreshState{})
	state := v.(*refreshState)
	state.mu.Lock()
	defer state.mu.Unlock()
	// another request may have made it again while this one waited
	if rec, err = s.Predictions.LatestRecommendation(ctx, moduleID); err != nil || fresh(rec) {
		return rec, false, err
	}
	if !state.last.IsZero() && s.now().Sub(state.last) < maxAge {
		return rec, false, nil
	}
	state.last = s.now()

	m, err := s.Modules.Get(ctx, moduleID)
	if err != nil {
		return rec, false, err
	}
	if !m.Enabled || m.Mode == model.ModeObserve {
		return rec, false, nil
	}
	if due, err := s.Due(ctx, m); err != nil || !due {
		return rec, false, err
	}
	failures := map[string]string{}
	data, err := s.RunData(ctx, moduleID, failures)
	if err != nil {
		return rec, false, err
	}
	if _, err := s.RunModule(ctx, moduleID, failures, data.Errors); err != nil {
		return rec, false, err
	}
	newest, err := s.Predictions.LatestRecommendation(ctx, moduleID)
	if err != nil {
		return rec, false, err
	}
	return newest, newest != nil && (rec == nil || newest.Version != rec.Version), nil
}
