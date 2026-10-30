package forecastsvc

import (
	"context"
	"encoding/json"
	"sort"
	"time"

	"github.com/cylonchau/kube-resource-fcst/pkg/model"
)

// The home page answers two questions at a glance: is the platform doing its work, and which modules need someone to look. It reads
// stored facts only (modules, the newest recommendation and the run state of each, what the operator applied) with a fixed number of
// queries, whatever the number of modules, and changes nothing.

// Flags of a module that needs a look, in the order they are worth looking at.
const (
	// FlagFailing: the scheduled runs of the module have failed MinFailuresToFlag times in a row.
	FlagFailing = "failing"
	// FlagDegraded: the newest recommendation fell back to the module fallback or the original value for some field.
	FlagDegraded = "degraded"
	// FlagStale: the newest recommendation is past its validity: nothing has refreshed it.
	FlagStale = "stale"
	// FlagNoRecommendation: the module forecasts and is not in observe mode, but has never made a recommendation.
	FlagNoRecommendation = "no_recommendation"
	// FlagBypass: an administrator switched the module to its default configuration.
	FlagBypass = "bypass"

	// MinFailuresToFlag is how many runs in a row must fail before a module is flagged: one failure is often a hiccup of the datasource.
	MinFailuresToFlag = 3
)

var flagOrder = map[string]int{FlagFailing: 0, FlagDegraded: 1, FlagStale: 2, FlagNoRecommendation: 3, FlagBypass: 4}

// DashboardCounts are the modules by what they are.
type DashboardCounts struct {
	Total     int `json:"total"`
	Enabled   int `json:"enabled"`
	Auto      int `json:"auto"`
	Admission int `json:"admission"`
	Recommend int `json:"recommend"`
	Observe   int `json:"observe"`
	// Predicting are the enabled modules that forecast; Bypass the enabled ones on their default configuration.
	Predicting int `json:"predicting"`
	Bypass     int `json:"bypass"`
	// Attention are the enabled modules with at least one flag.
	Attention int `json:"attention"`
}

// DashboardTotals are what happened in the window.
type DashboardTotals struct {
	// ScaleUps and ScaleDowns are the replica changes the operator applied; FailedRuns the forecast runs that failed.
	ScaleUps   int   `json:"scale_ups"`
	ScaleDowns int   `json:"scale_downs"`
	FailedRuns int64 `json:"failed_runs"`
}

// DashboardModule is one module that needs a look, with what makes it so.
type DashboardModule struct {
	ID        uint   `json:"id"`
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Workload  string `json:"workload"`
	Mode      string `json:"mode"`
	// Algorithm is the algorithm in use; Replicas and RecommendedAt the newest recommendation (0 and nil when there is none).
	Algorithm     string     `json:"algorithm,omitempty"`
	Replicas      int        `json:"replicas"`
	RecommendedAt *time.Time `json:"recommended_at,omitempty"`
	ValidUntil    *time.Time `json:"valid_until,omitempty"`
	LastRunAt     *time.Time `json:"last_run_at,omitempty"`
	// LastError and ConsecutiveFailures are the newest failure of the scheduled run; DegradedFields the fields that fell back.
	LastError           string   `json:"last_error,omitempty"`
	ConsecutiveFailures int      `json:"consecutive_failures"`
	DegradedFields      []string `json:"degraded_fields"`
	Flags               []string `json:"flags"`
}

// DashboardBucket is what the operator applied in one stretch of the window.
type DashboardBucket struct {
	At    time.Time `json:"at"`
	Ups   int       `json:"ups"`
	Downs int       `json:"downs"`
}

// Dashboard is the answer for the home page.
type Dashboard struct {
	At    time.Time `json:"at"`
	Hours int       `json:"hours"`
	// BucketHours is the width of one bucket of Activity.
	BucketHours int               `json:"bucket_hours"`
	Modules     DashboardCounts   `json:"modules"`
	Totals      DashboardTotals   `json:"totals"`
	Attention   []DashboardModule `json:"attention"`
	Activity    []DashboardBucket `json:"activity"`
}

// Dashboard builds the home page of the last hours (DefaultDelayHours when 0, at most MaxEffectHours).
func (s *Service) Dashboard(ctx context.Context, hours int) (*Dashboard, error) {
	if hours <= 0 {
		hours = DefaultDelayHours
	}
	if hours > MaxEffectHours {
		hours = MaxEffectHours
	}
	now := s.now()
	since := now.Add(-time.Duration(hours) * time.Hour)
	out := &Dashboard{At: now, Hours: hours, Attention: []DashboardModule{}}

	modules, err := s.Modules.AllModules(ctx)
	if err != nil {
		return nil, err
	}
	states, err := s.RunStates.AllRunStates(ctx)
	if err != nil {
		return nil, err
	}
	recs, err := s.Predictions.LatestRecommendations(ctx)
	if err != nil {
		return nil, err
	}
	out.Modules.Total = len(modules)
	for _, m := range modules {
		if !m.Enabled {
			continue
		}
		out.Modules.Enabled++
		switch m.Mode {
		case model.ModeAuto:
			out.Modules.Auto++
		case model.ModeAdmission:
			out.Modules.Admission++
		case model.ModeRecommend:
			out.Modules.Recommend++
		case model.ModeObserve:
			out.Modules.Observe++
		}
		if m.Predicts() {
			out.Modules.Predicting++
		}
		if m.Bypass {
			out.Modules.Bypass++
		}
		if row := moduleHealth(m, states[m.ID], recs[m.ID], now); len(row.Flags) > 0 {
			out.Attention = append(out.Attention, row)
		}
	}
	out.Modules.Attention = len(out.Attention)
	sort.SliceStable(out.Attention, func(i, j int) bool {
		a, b := flagOrder[out.Attention[i].Flags[0]], flagOrder[out.Attention[j].Flags[0]]
		if a != b {
			return a < b
		}
		return out.Attention[i].Name < out.Attention[j].Name
	})

	execs, err := s.Predictions.AppliedExecutionsInWindow(ctx, since)
	if err != nil {
		return nil, err
	}
	out.BucketHours = bucketHours(hours)
	out.Activity = activityOf(execs, since, now, time.Duration(out.BucketHours)*time.Hour)
	for _, b := range out.Activity {
		out.Totals.ScaleUps += b.Ups
		out.Totals.ScaleDowns += b.Downs
	}
	if out.Totals.FailedRuns, err = s.Predictions.CountFailedTrainings(ctx, since); err != nil {
		return nil, err
	}
	return out, nil
}

// moduleHealth is the state of one enabled module and the flags it earns. A module that is not asked to forecast, or that only observes,
// makes no recommendation by design: it is not flagged for having none.
func moduleHealth(m *model.ForecastModule, st *model.ModuleRunState, rec *model.Recommendation, now time.Time) DashboardModule {
	row := DashboardModule{ID: m.ID, Name: m.Name, Namespace: m.Namespace, Workload: m.Workload, Mode: m.Mode, DegradedFields: []string{}, Flags: []string{}}
	if st != nil {
		row.Algorithm, row.LastRunAt, row.LastError, row.ConsecutiveFailures = st.Selected, st.LastRunAt, st.LastError, st.ConsecutiveFailures
		var fields map[string]string
		if st.DegradedFields != "" && json.Unmarshal([]byte(st.DegradedFields), &fields) == nil {
			for field := range fields {
				row.DegradedFields = append(row.DegradedFields, field)
			}
			sort.Strings(row.DegradedFields)
		}
	}
	if rec != nil {
		at, until := rec.CreatedAt, rec.ValidUntil
		row.Replicas, row.RecommendedAt, row.ValidUntil = rec.Replicas, &at, &until
		if row.Algorithm == "" {
			row.Algorithm = rec.Algorithm
		}
	}
	predicts := m.Predicts() && m.Mode != model.ModeObserve
	switch {
	case row.ConsecutiveFailures >= MinFailuresToFlag:
		row.Flags = append(row.Flags, FlagFailing)
	}
	if predicts && rec != nil && rec.Degraded {
		row.Flags = append(row.Flags, FlagDegraded)
	}
	if predicts && rec != nil && !rec.ValidUntil.IsZero() && now.After(rec.ValidUntil) {
		row.Flags = append(row.Flags, FlagStale)
	}
	if predicts && rec == nil {
		row.Flags = append(row.Flags, FlagNoRecommendation)
	}
	if m.Bypass {
		row.Flags = append(row.Flags, FlagBypass)
	}
	sort.SliceStable(row.Flags, func(i, j int) bool { return flagOrder[row.Flags[i]] < flagOrder[row.Flags[j]] })
	return row
}

// bucketHours is the width of one bar of the activity chart: an hour up to two days, six hours up to a week, a day beyond.
func bucketHours(hours int) int {
	switch {
	case hours <= 48:
		return 1
	case hours <= 24*7:
		return 6
	}
	return 24
}

// activityOf counts the replica changes the operator applied in buckets from since to now, every bucket present even when nothing happened
// in it (a chart with holes would read as missing data).
func activityOf(execs []*model.ScalingExecution, since, now time.Time, width time.Duration) []DashboardBucket {
	start := since.UTC().Truncate(width)
	n := int(now.Sub(start)/width) + 1
	out := make([]DashboardBucket, n)
	for i := range out {
		out[i].At = start.Add(time.Duration(i) * width)
	}
	for _, e := range execs {
		i := int(e.CreatedAt.UTC().Sub(start) / width)
		if i < 0 || i >= n {
			continue
		}
		switch e.Action {
		case actionScaleUp:
			out[i].Ups++
		case actionScaleDown:
			out[i].Downs++
		}
	}
	return out
}
