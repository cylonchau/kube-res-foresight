package forecastsvc

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/decision"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
)

// TriggerState is what the platform saw of one metric trigger of the module in this decision: whether it is in force at this time, the value it compared, and whether it
// fired. It is kept in the detail of the recommendation, so a count that a trigger changed can be traced to the figure behind it.
type TriggerState struct {
	Name      string  `json:"name"`
	Enabled   bool    `json:"enabled"`
	Priority  int     `json:"priority"`
	Metric    string  `json:"metric"`
	Stat      string  `json:"stat"`
	Window    int     `json:"window_minutes"`
	Operator  string  `json:"operator"`
	Threshold float64 `json:"threshold"`
	Action    string  `json:"action"`
	Unit      string  `json:"unit"`
	Amount    float64 `json:"amount"`
	// Active says the trigger is in force at this time (its days and hours); Value is what the metric came to over the window, Samples the number of points behind it.
	Active  bool    `json:"active"`
	Value   float64 `json:"value"`
	Samples int     `json:"samples"`
	Fired   bool    `json:"fired"`
	// Reason says in words why it did not fire when it did not (off, outside its hours, no data) or what it saw when it did.
	Reason string `json:"reason"`
}

// compareTrigger applies the operator of a trigger.
func compareTrigger(op string, value, threshold float64) bool {
	switch op {
	case model.TriggerGTE:
		return value >= threshold
	case model.TriggerLT:
		return value < threshold
	case model.TriggerLTE:
		return value <= threshold
	}
	return value > threshold
}

// reduceTrigger is the value of a trigger over its window: the last sample, the mean, the maximum or the minimum.
func reduceTrigger(stat string, values []float64) float64 {
	switch stat {
	case model.TriggerStatLast:
		return values[len(values)-1]
	case model.TriggerStatMax:
		top := values[0]
		for _, v := range values {
			top = math.Max(top, v)
		}
		return top
	case model.TriggerStatMin:
		low := values[0]
		for _, v := range values {
			low = math.Min(low, v)
		}
		return low
	}
	sum := 0.0
	for _, v := range values {
		sum += v
	}
	return sum / float64(len(values))
}

// triggerSamples reads the samples a trigger compares: the stored series of a source of the module, or the heap used over the heap maximum for the JVM heap pressure.
func (s *Service) triggerSamples(ctx context.Context, m *model.ForecastModule, bs boundSet, t *model.ModuleTrigger, now time.Time) ([]float64, string, error) {
	from := now.Add(-time.Duration(t.WindowMinutes) * time.Minute)
	if t.Metric == model.TriggerJVMHeap {
		if len(bs.sourcesOfKind("jvm_heap_used")) == 0 || len(bs.sourcesOfKind("jvm_heap_max")) == 0 {
			return nil, "jvm_heap_used and jvm_heap_max are not both configured as metric sources of the module", nil
		}
		used, err := s.auxRange(ctx, bs, "jvm_heap_used", from, now)
		if err != nil {
			return nil, "", err
		}
		limit, err := s.auxRange(ctx, bs, "jvm_heap_max", from, now)
		if err != nil {
			return nil, "", err
		}
		if len(used) == 0 || len(limit) == 0 {
			return nil, fmt.Sprintf("no JVM heap samples in the last %d minutes", t.WindowMinutes), nil
		}
		maxAt := map[int64]float64{}
		for _, sm := range limit {
			maxAt[sm.TS] = sm.Value
		}
		lastMax := limit[len(limit)-1].Value
		var out []float64
		for _, sm := range used {
			cap := lastMax
			if v, ok := maxAt[sm.TS]; ok {
				cap = v
			}
			if cap > 0 {
				out = append(out, sm.Value/cap)
			}
		}
		if len(out) == 0 {
			return nil, "the heap maximum is not positive", nil
		}
		return out, "", nil
	}
	series, err := s.Samples.Series(ctx, m.ID, t.Metric)
	if err != nil {
		return nil, fmt.Sprintf("metric %q has no collected history", t.Metric), nil
	}
	rows, err := s.Samples.Range(ctx, series.ID, from, now)
	if err != nil {
		return nil, "", err
	}
	if len(rows) == 0 {
		return nil, fmt.Sprintf("no samples of %q in the last %d minutes", t.Metric, t.WindowMinutes), nil
	}
	out := make([]float64, len(rows))
	for i, r := range rows {
		out[i] = r.Value
	}
	return out, "", nil
}

// evaluateTriggers reads every trigger of the module: which are in force now, what each metric came to and which fired. A trigger that cannot be read (no data, a
// source that is gone) does not fire and says why; it never fails the recommendation.
func (s *Service) evaluateTriggers(ctx context.Context, m *model.ForecastModule, bs boundSet, loc *time.Location, now time.Time) ([]TriggerState, []decision.FiredTrigger, error) {
	triggers, err := s.Modules.Triggers(ctx, m.ID)
	if err != nil {
		return nil, nil, err
	}
	var states []TriggerState
	var fired []decision.FiredTrigger
	for _, t := range triggers {
		st := TriggerState{Name: t.Name, Enabled: t.Enabled, Priority: t.Priority, Metric: t.Metric, Stat: t.Stat, Window: t.WindowMinutes, Operator: t.Operator,
			Threshold: t.Threshold, Action: t.Action, Unit: t.Unit, Amount: t.Amount}
		switch {
		case !t.Enabled:
			st.Reason = "switched off"
		case !triggerInForce(t, now, loc):
			st.Reason = "outside the days and hours the trigger is in force"
		default:
			st.Active = true
			values, why, err := s.triggerSamples(ctx, m, bs, t, now)
			if err != nil {
				return nil, nil, err
			}
			if why != "" {
				st.Reason = why
				break
			}
			st.Samples, st.Value = len(values), reduceTrigger(t.Stat, values)
			cmp := map[string]string{model.TriggerGT: ">", model.TriggerGTE: ">=", model.TriggerLT: "<", model.TriggerLTE: "<="}[t.Operator]
			if compareTrigger(t.Operator, st.Value, t.Threshold) {
				st.Fired = true
				st.Reason = fmt.Sprintf("%s of %s over %d minutes is %.4g %s %.4g", t.Stat, t.Metric, t.WindowMinutes, st.Value, cmp, t.Threshold)
				fired = append(fired, decision.FiredTrigger{Name: t.Name, Priority: t.Priority, Action: t.Action, Unit: t.Unit, Amount: t.Amount, Metric: t.Metric, Value: st.Value, Why: st.Reason})
			} else {
				st.Reason = fmt.Sprintf("%s of %s over %d minutes is %.4g, not %s %.4g", t.Stat, t.Metric, t.WindowMinutes, st.Value, cmp, t.Threshold)
			}
		}
		states = append(states, st)
	}
	return states, fired, nil
}

// triggerInForce reports whether the days and hours of a trigger include now (no days: always).
func triggerInForce(t *model.ModuleTrigger, now time.Time, loc *time.Location) bool {
	if t.Weekdays == 0 {
		return true
	}
	return decision.FixedPeriod{Weekdays: uint8(t.Weekdays), StartMinute: t.StartMinute, EndMinute: t.EndMinute}.Active(now, loc)
}

// measuredReplicas is the newest stored sample of the replicas metric of the module (0 when it has none, or it is older than the report of the operator may be): the
// replicas that run, for a module whose algorithms do not run and for the triggers, which are relative to what runs.
func (s *Service) measuredReplicas(ctx context.Context, m *model.ForecastModule, now time.Time) int {
	sources, err := s.Modules.Sources(ctx, m.ID)
	if err != nil {
		return 0
	}
	for _, src := range sources {
		if src.Kind != model.MetricReplicas || !src.Enabled || src.IsDisplay() {
			continue
		}
		series, err := s.Samples.Series(ctx, m.ID, src.Key)
		if err != nil {
			continue
		}
		at, ok, err := s.Samples.Latest(ctx, series.ID)
		if err != nil || !ok || now.Sub(at) > k8sReportMaxAge {
			continue
		}
		if v, ok := s.latestValue(ctx, m.Data(), src.Key); ok && v >= 1 {
			return int(math.Round(v))
		}
	}
	return 0
}
