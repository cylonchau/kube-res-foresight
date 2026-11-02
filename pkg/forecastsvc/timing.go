package forecastsvc

import (
	"context"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/cylonchau/kube-res-foresight/pkg/timing"
)

// Timing is the time limit a system parameter sets (see package timing): what is stored, kept in the range of the parameter, or its
// default when nothing usable is stored or the database cannot be read. The parameters are read through the short cache of the system
// parameter DAO, so a change is in force within seconds, with no restart.
func (s *Service) Timing(ctx context.Context, p timing.Param) time.Duration {
	if s.Config == nil {
		return timing.Seconds(p.Default)
	}
	raw, err := s.Config.GetValueOrDefault(ctx, p.Key, "")
	if err != nil {
		return timing.Seconds(p.Default)
	}
	return p.Seconds(raw)
}

// Percentile is a percentile parameter (see timing.LeadPercentile) as a level in (0, 1): 90 is 0.9. A missing or unreadable value is the
// default; one outside 1-99 is brought to the nearest end.
func (s *Service) Percentile(ctx context.Context, p timing.Param) float64 {
	if s.Config == nil {
		return float64(p.Default) / 100
	}
	raw, err := s.Config.GetValueOrDefault(ctx, p.Key, "")
	if err != nil {
		return float64(p.Default) / 100
	}
	return float64(p.Parse(raw)) / 100
}

// RefreshWait is how long a request of the operator waits for a refresh of the recommendation. It is the parameter, but always shorter
// than the time the operator waits for its own request (the platform asks the same parameters the operator is given): an answer that
// would come after the operator hung up is worth nothing.
func (s *Service) RefreshWait(ctx context.Context) time.Duration {
	wait, requestTimeout := s.Timing(ctx, timing.DecisionRefreshWait), s.Timing(ctx, timing.OperatorRequestTimeout)
	if limit := requestTimeout - time.Second; wait > limit {
		wait = limit
	}
	if wait < time.Second {
		wait = time.Second
	}
	return wait
}

// budgetFor is the time one forecast of the algorithm may take: its own budget, else the system parameter.
func (s *Service) budgetFor(ctx context.Context, b *model.AlgorithmRun) time.Duration {
	if b.TrainBudgetSeconds > 0 {
		return time.Duration(b.TrainBudgetSeconds) * time.Second
	}
	return s.Timing(ctx, timing.TrainBudget)
}
