package forecastsvc

import (
	"context"
	"encoding/json"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/cylonchau/kube-resource-fcst/pkg/dao/rdb"
	"github.com/cylonchau/kube-resource-fcst/pkg/decision"
	"github.com/cylonchau/kube-resource-fcst/pkg/model"
	"github.com/cylonchau/kube-resource-fcst/pkg/timing"
)

// The effect report gathers what the platform already measured into one answer per module: is the forecast accurate, are the scale-ups in
// time, is capacity wasted, are the replicas stable, who is doing the work. It reads stored facts only; it changes no decision and collects
// nothing new.
const (
	// MaxEffectHours is the longest window a report may ask for.
	MaxEffectHours = 24 * 30
	// MinEffectSamples is how many facts a section needs before it says a figure instead of "not enough samples".
	MinEffectSamples = 5
	// ReversalWindow is how soon after a change the opposite change counts as a reversal.
	ReversalWindow = 30 * time.Minute

	actionScaleUp   = "ScaleUp"
	actionScaleDown = "ScaleDown"
)

// EffectReport is the answer for one module over a window. Every section carries its sample count and whether it is enough to read.
type EffectReport struct {
	ModuleID uint      `json:"module_id"`
	Since    time.Time `json:"since"`
	Hours    int       `json:"hours"`
	// Algorithm is the algorithm in use now, whose evaluated runs the accuracy is made of.
	Algorithm string `json:"algorithm,omitempty"`

	Accuracy  []AccuracyRow   `json:"accuracy"`
	Delay     *DelayReport    `json:"delay"`
	Waste     WasteSection    `json:"waste"`
	Capacity  CapacitySection `json:"capacity"`
	Stability StabilityPart   `json:"stability"`
	Adoption  AdoptionPart    `json:"adoption"`
	Tuning    *TuningSection  `json:"tuning,omitempty"`
}

// AccuracyRow is the back-filled accuracy of one metric of the algorithm in use: the runs whose actuals have arrived.
type AccuracyRow struct {
	Metric string `json:"metric"`
	// Runs is how many evaluated forecast runs the figures are made of and Points how many forecast steps had an actual.
	Runs   int  `json:"runs"`
	Points int  `json:"points"`
	Enough bool `json:"enough"`
	// WAPE, PeakUnder (mean relative shortfall of the predicted peak), Bias (positive = over-prediction) and Coverage are weighted by the
	// steps each run had an actual for. They are zero, and meaningless, when Enough is false.
	WAPE      float64 `json:"wape"`
	PeakUnder float64 `json:"peak_under"`
	Bias      float64 `json:"bias"`
	Coverage  float64 `json:"coverage"`
}

// WasteSection compares the replicas the platform asked for with what the measured load needed.
type WasteSection struct {
	decision.Waste
	Enough bool `json:"enough"`
	// OverShare and UnderShare are the shares of the judged requests that were over and under.
	OverShare  float64 `json:"over_share"`
	UnderShare float64 `json:"under_share"`
	// Note says why nothing could be measured, or how the load was turned into replicas.
	Note string `json:"note,omitempty"`
}

// StabilityPart is how often the replicas moved, and how often the recommendation had to fall back.
type StabilityPart struct {
	decision.Churn
	// Recommendations is how many were made in the window and Degraded how many of them fell back for some field.
	Recommendations int            `json:"recommendations"`
	Degraded        int            `json:"degraded"`
	Reasons         map[string]int `json:"reasons"`
	Enough          bool           `json:"enough"`
}

// AdoptionRow is the share of the recommendations an algorithm decided.
type AdoptionRow struct {
	Algorithm string  `json:"algorithm"`
	Count     int     `json:"count"`
	Share     float64 `json:"share"`
}

// AdoptionPart says who decided the recommendations of the window.
type AdoptionPart struct {
	Total  int           `json:"total"`
	Rows   []AdoptionRow `json:"rows"`
	Enough bool          `json:"enough"`
}

// TuningSection is the state of the fine-tuned copy a model service keeps for the module.
type TuningSection struct {
	Algorithm string `json:"algorithm"`
	// Tuned and Failed count the fine-tunes of the window.
	Tuned  int `json:"tuned"`
	Failed int `json:"failed"`
	// LastTunedAt and AgeHours say when the newest successful fine-tune ran and how old it is; nil when there was none in the window.
	LastTunedAt *time.Time `json:"last_tuned_at,omitempty"`
	AgeHours    float64    `json:"age_hours,omitempty"`
	// LastFailedAt and LastFailure are the newest failure.
	LastFailedAt *time.Time `json:"last_failed_at,omitempty"`
	LastFailure  string     `json:"last_failure,omitempty"`
}

// EffectReport builds the report of a module over the last hours (DefaultDelayHours when 0).
func (s *Service) EffectReport(ctx context.Context, moduleID uint, hours int) (*EffectReport, error) {
	m, err := s.Modules.Get(ctx, moduleID)
	if err != nil {
		return nil, err
	}
	if hours <= 0 {
		hours = DefaultDelayHours
	}
	if hours > MaxEffectHours {
		hours = MaxEffectHours
	}
	now := s.now()
	since := now.Add(-time.Duration(hours) * time.Hour)
	rep := &EffectReport{ModuleID: moduleID, Since: since, Hours: hours, Accuracy: []AccuracyRow{}}

	if rep.Delay, err = s.DelayReport(ctx, moduleID, hours); err != nil {
		return nil, err
	}
	rep.Algorithm = rep.Delay.Algorithm

	if rep.Algorithm != "" {
		evals, err := s.Predictions.AllEvaluations(ctx, rdb.EvaluationFilter{ModuleID: moduleID, AlgorithmID: rep.Algorithm, Since: &since})
		if err != nil {
			return nil, err
		}
		rep.Accuracy = accuracyOf(evals)
	}

	recs, err := s.Predictions.RecommendationsSince(ctx, moduleID, since)
	if err != nil {
		return nil, err
	}
	rep.Waste = s.wasteOf(ctx, m, recs, since, now)
	rep.Capacity = s.capacityOf(ctx, m, since, now, hours)
	rep.Adoption = adoptionOf(recs)

	execs, err := s.Predictions.AppliedExecutionsSince(ctx, moduleID, since)
	if err != nil {
		return nil, err
	}
	rep.Stability = stabilityOf(recs, execs)

	if rep.Tuning, err = s.tuningOf(ctx, moduleID, since, now); err != nil {
		return nil, err
	}
	return rep, nil
}

func accuracyOf(evals []model.PredictionEvaluation) []AccuracyRow {
	type acc struct {
		row                         AccuracyRow
		wape, under, bias, coverage float64
	}
	byMetric := map[string]*acc{}
	for _, e := range evals {
		if e.Points <= 0 {
			continue
		}
		a := byMetric[e.Metric]
		if a == nil {
			a = &acc{row: AccuracyRow{Metric: e.Metric}}
			byMetric[e.Metric] = a
		}
		w := float64(e.Points)
		a.row.Runs++
		a.row.Points += e.Points
		a.wape += e.WAPE * w
		a.under += e.PeakUnder * w
		a.bias += e.Bias * w
		a.coverage += e.Coverage * w
	}
	rows := make([]AccuracyRow, 0, len(byMetric))
	for _, a := range byMetric {
		a.row.Enough = a.row.Runs >= MinEffectSamples
		if a.row.Enough {
			w := float64(a.row.Points)
			a.row.WAPE, a.row.PeakUnder, a.row.Bias, a.row.Coverage = a.wape/w, a.under/w, a.bias/w, a.coverage/w
		}
		rows = append(rows, a.row)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Metric < rows[j].Metric })
	return rows
}

// wasteOf turns the measured load into the replicas it needed (the same way the scale-ups are judged) and compares each replica request
// of the window with it. The load can be turned into replicas only when the module knows what one Pod carries.
func (s *Service) wasteOf(ctx context.Context, m *model.ForecastModule, recs []*model.Recommendation, since, now time.Time) WasteSection {
	var out WasteSection
	var held []decision.HeldReplicas
	for i, r := range recs {
		if !r.ReplicasSet || r.Replicas <= 0 {
			continue
		}
		to := now
		if !r.ValidUntil.IsZero() && r.ValidUntil.Before(to) {
			to = r.ValidUntil
		}
		for _, next := range recs[i+1:] {
			if next.ReplicasSet && next.Replicas > 0 {
				if next.CreatedAt.Before(to) {
					to = next.CreatedAt
				}
				break
			}
		}
		held = append(held, decision.HeldReplicas{From: r.CreatedAt, To: to, Replicas: r.Replicas})
	}
	if len(held) == 0 {
		out.Note = "no recommendation of the window asked for a replica count"
		return out
	}

	needs, _, note := s.measuredNeeds(ctx, m, since, now)
	if note != "" {
		out.Note = note
		return out
	}
	out.Waste = decision.WasteOf(held, needs)
	out.Enough = out.Judged >= MinEffectSamples
	if out.Judged > 0 {
		out.OverShare = float64(out.Over) / float64(out.Judged)
		out.UnderShare = float64(out.Under) / float64(out.Judged)
	}
	if !out.Enough {
		out.Note = "too few replica requests had a measured load to compare with"
	}
	return out
}

func stabilityOf(recs []*model.Recommendation, execs []*model.ScalingExecution) StabilityPart {
	out := StabilityPart{Recommendations: len(recs), Reasons: map[string]int{}}
	events := make([]decision.ScaleEvent, 0, len(execs))
	for _, e := range execs {
		switch e.Action {
		case actionScaleUp:
			events = append(events, decision.ScaleEvent{At: e.CreatedAt, Up: true})
		case actionScaleDown:
			events = append(events, decision.ScaleEvent{At: e.CreatedAt})
		}
	}
	out.Churn = decision.ChurnOf(events, ReversalWindow)
	for _, r := range recs {
		if !r.Degraded {
			continue
		}
		out.Degraded++
		var reasons map[string]string
		if json.Unmarshal([]byte(r.DegradeReasons), &reasons) == nil {
			seen := map[string]bool{}
			for _, code := range reasons {
				if !seen[code] {
					seen[code] = true
					out.Reasons[code]++
				}
			}
		}
	}
	out.Enough = len(recs) >= MinEffectSamples
	return out
}

func adoptionOf(recs []*model.Recommendation) AdoptionPart {
	count := map[string]int{}
	total := 0
	for _, r := range recs {
		if r.Algorithm == "" {
			continue
		}
		count[r.Algorithm]++
		total++
	}
	out := AdoptionPart{Total: total, Rows: []AdoptionRow{}, Enough: total >= MinEffectSamples}
	for a, n := range count {
		out.Rows = append(out.Rows, AdoptionRow{Algorithm: a, Count: n, Share: float64(n) / float64(total)})
	}
	sort.Slice(out.Rows, func(i, j int) bool {
		if out.Rows[i].Count != out.Rows[j].Count {
			return out.Rows[i].Count > out.Rows[j].Count
		}
		return out.Rows[i].Algorithm < out.Rows[j].Algorithm
	})
	return out
}

// tuningOf reads the fine-tune records of the model service of the module; nil when the module has none.
func (s *Service) tuningOf(ctx context.Context, moduleID uint, since, now time.Time) (*TuningSection, error) {
	runs, err := s.Predictions.TrainingsSince(ctx, moduleID, "model_service", since)
	if err != nil {
		return nil, err
	}
	var out *TuningSection
	for _, r := range runs {
		switch {
		case r.Status == StatusOK && strings.HasPrefix(r.Reason, "fine-tuned"):
			if out == nil {
				out = &TuningSection{Algorithm: "model_service"}
			}
			out.Tuned++
			if out.LastTunedAt == nil {
				at := r.CreatedAt
				out.LastTunedAt, out.AgeHours = &at, now.Sub(at).Hours()
			}
		case r.Status == StatusFailed && strings.HasPrefix(r.Reason, "fine-tune"):
			if out == nil {
				out = &TuningSection{Algorithm: "model_service"}
			}
			out.Failed++
			if out.LastFailedAt == nil {
				at := r.CreatedAt
				out.LastFailedAt, out.LastFailure = &at, clipText(r.Reason, 200)
			}
		}
	}
	return out, nil
}

func clipText(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// measuredNeeds is the replicas the measured load of the module needed at each moment of [since, now]: the largest of what its CPU, memory
// and request rate ask for at the target utilization, with no safety margin (the way the scale-ups are judged). When it cannot be made,
// note says why: it needs a workload-level series and what one Pod carries.
func (s *Service) measuredNeeds(ctx context.Context, m *model.ForecastModule, since, now time.Time) ([]decision.NeedPoint, boundSet, string) {
	cfg := decision.SolverConfig{PodCPUCores: float64(m.PodCPURequestMilli) / 1000, PodMemoryBytes: float64(m.PodMemoryRequestBytes), TargetUtilization: m.TargetUtilization}
	if m.CapacityMode != model.CapacityModeEstimate {
		cfg.CapacityRPS = m.PodCapacityRPS
	}
	bs, err := s.boundSet(ctx, m)
	if err != nil {
		return nil, nil, "the sources of the module could not be read: " + err.Error()
	}
	if len(s.demandSources(ctx, bs, cfg)) == 0 {
		return nil, bs, "the load cannot be turned into replicas: no workload-level CPU, memory or request-rate series with a per-Pod request or capacity"
	}
	return s.demandNeeds(ctx, bs, cfg, since, now), bs, ""
}

// MinCapacityPoints is how many measured moments the Pod-hours and the SLO need before they are read, and MinCapacityCoverage the share of
// the window those moments have to stand for.
const (
	MinCapacityPoints   = 12
	MinCapacityCoverage = 0.5
)

// CapacitySection is what ran against what was needed: Pod-hours and the capacity SLO of the window, from the measured Ready replicas and
// the measured load. It is a measurement of the execution, not of the recommendations (those are the waste section).
type CapacitySection struct {
	decision.Capacity
	// Coverage is the share of the window the compared moments stand for.
	Coverage float64 `json:"coverage"`
	Enough   bool    `json:"enough"`
	// Target is the capacity SLO (a share) and Verdict pass, fail or insufficient against it.
	Target  float64 `json:"target"`
	Verdict string  `json:"verdict"`
	Note    string  `json:"note,omitempty"`
}

func (s *Service) capacityOf(ctx context.Context, m *model.ForecastModule, since, now time.Time, hours int) CapacitySection {
	out := CapacitySection{Verdict: decision.IndicatorInsufficient}
	out.Target = float64(s.targetPercent(ctx, timing.CapacitySLOTarget)) / 100
	needs, bs, note := s.measuredNeeds(ctx, m, since, now)
	if note != "" {
		out.Note = note
		return out
	}
	ref := bs.firstOfKind("replicas")
	if ref == nil {
		out.Note = "the module has no ready-replicas series, so what ran cannot be measured"
		return out
	}
	series, err := s.Samples.Series(ctx, ref.Inst.ID, ref.Src.Key)
	if err != nil {
		out.Note = "the ready-replicas series has no samples yet"
		return out
	}
	rows, err := s.Samples.Range(ctx, series.ID, since, now)
	if err != nil {
		out.Note = "the ready-replicas series could not be read: " + err.Error()
		return out
	}
	replicas := make([]decision.ReplicaSample, 0, len(rows))
	for _, r := range rows {
		if r.Value >= 0 && !math.IsNaN(r.Value) {
			replicas = append(replicas, decision.ReplicaSample{At: time.Unix(r.TS, 0).UTC(), Replicas: int(math.Round(r.Value))})
		}
	}
	out.Capacity = decision.CapacityOf(replicas, needs)
	if hours > 0 {
		out.Coverage = out.CoveredHours / float64(hours)
	}
	out.Enough = out.Points >= MinCapacityPoints && out.Coverage >= MinCapacityCoverage
	switch {
	case !out.Enough:
		out.Note = "too few measured moments (or too little of the window) to read the Pod-hours and the SLO"
	case out.Attainment >= out.Target:
		out.Verdict = decision.IndicatorPass
	default:
		out.Verdict = decision.IndicatorFail
	}
	return out
}

// targetPercent reads a percent parameter, kept in its range.
func (s *Service) targetPercent(ctx context.Context, p timing.Param) int {
	if s.Config == nil {
		return p.Default
	}
	raw, err := s.Config.GetValueOrDefault(ctx, p.Key, "")
	if err != nil {
		return p.Default
	}
	return p.Parse(raw)
}
