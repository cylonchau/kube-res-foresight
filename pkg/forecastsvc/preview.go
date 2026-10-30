package forecastsvc

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/cylonchau/kube-resource-fcst/pkg/forecast"
	"github.com/cylonchau/kube-resource-fcst/pkg/model"
	"github.com/cylonchau/kube-resource-fcst/pkg/multimetric"
	"github.com/cylonchau/kube-resource-fcst/pkg/timing"
)

// The change preview answers "what would the replicas have been if this setting had been in force": it replays the recent past twice, with
// the saved setting and with the change, from the same stored history, and sets the two timelines beside each other. It is read-only: it
// writes no prediction, recommendation, training record or score, and it never asks a model service to fine-tune (a model service answers
// from its pretrained model, as in a backtest).
const (
	// DefaultPreviewHours and MaxPreviewHours bound the window that is replayed.
	DefaultPreviewHours = 24 * 7
	MaxPreviewHours     = 24 * 14
	// MaxPreviewWindows bounds how many origins are replayed; a longer window is refused, not thinned out.
	MaxPreviewWindows = 400
	// previewUnderWeight is how much worse a replica too few is than one too many in the risk error, as in the evaluation of the algorithms.
	previewUnderWeight = 3.0
)

// ErrPreviewInvalid is a change the preview cannot replay: nothing to compare, a value out of range, or parameters the algorithm refuses.
var ErrPreviewInvalid = errors.New("invalid change")

// ErrPreviewTooLong means the replay would pass its limits (windows or time). Nothing is returned rather than a partial answer.
var ErrPreviewTooLong = errors.New("the preview is too long")

// PreviewChange is the setting to try. A field that is left out keeps the saved value.
type PreviewChange struct {
	// Algorithm is the algorithm to try (empty = the one in use); Params replace the saved parameters of that algorithm.
	Algorithm string         `json:"algorithm"`
	Params    map[string]any `json:"params"`
	// Module settings that decide the replica count from the forecast.
	HeadroomRatio  *float64 `json:"headroom_ratio"`
	PodCapacityRPS *float64 `json:"pod_capacity_rps"`
	MinReplicas    *int     `json:"min_replicas"`
	MaxReplicas    *int     `json:"max_replicas"`
	// Short-term correction of the algorithms that remember long (0 recent minutes = off).
	ShortTermRecentMinutes *float64 `json:"short_term_recent_minutes"`
	ShortTermTrigger       *float64 `json:"short_term_trigger"`
	ShortTermFadeMinutes   *float64 `json:"short_term_fade_minutes"`
	ShortTermDownShare     *float64 `json:"short_term_down_share"`
}

func (c PreviewChange) empty() bool {
	return c.Algorithm == "" && c.Params == nil && c.HeadroomRatio == nil && c.PodCapacityRPS == nil && c.MinReplicas == nil && c.MaxReplicas == nil &&
		c.ShortTermRecentMinutes == nil && c.ShortTermTrigger == nil && c.ShortTermFadeMinutes == nil && c.ShortTermDownShare == nil
}

// PreviewPoint is one replayed origin: the replicas each setting asked for and the replicas the measured load needed over the window after it.
type PreviewPoint struct {
	At       time.Time `json:"at"`
	Current  int       `json:"current"`
	Proposed int       `json:"proposed"`
	Need     int       `json:"need"`
}

// PreviewSide is the figures of one setting over the replayed windows.
type PreviewSide struct {
	Algorithm string `json:"algorithm"`
	// Under and Over count the windows that asked for fewer or more replicas than the load needed; UnderReplicas and OverReplicas are the
	// mean gap of those windows.
	Under         int     `json:"under"`
	Over          int     `json:"over"`
	UnderReplicas float64 `json:"under_replicas"`
	OverReplicas  float64 `json:"over_replicas"`
	// RiskError is (3 x replicas short + replicas surplus) / replicas needed, over all windows: a shortfall costs three times a surplus.
	RiskError float64 `json:"risk_error"`
	// ReplicaHours is the replicas asked for, held for the length of each window.
	ReplicaHours float64 `json:"replica_hours"`
	// Changes is how many times the replica count differs from the window before.
	Changes int `json:"changes"`
}

// PreviewReport is the answer of a preview.
type PreviewReport struct {
	ModuleID uint      `json:"module_id"`
	Since    time.Time `json:"since"`
	Until    time.Time `json:"until"`
	Hours    int       `json:"hours"`
	// WindowMinutes is the length of one replayed window; Windows how many were compared, Skipped how many had to be left out because one
	// of the settings could not forecast it or the load of it is incomplete.
	WindowMinutes int `json:"window_minutes"`
	Windows       int `json:"windows"`
	Skipped       int `json:"skipped"`

	Current  PreviewSide `json:"current"`
	Proposed PreviewSide `json:"proposed"`
	// Points is the timeline of both settings and the need; Largest the windows where they differ most.
	Points  []PreviewPoint `json:"points"`
	Largest []PreviewPoint `json:"largest"`
	Notes   []string       `json:"notes"`
	// ElapsedMs is how long the replay took.
	ElapsedMs int64 `json:"elapsed_ms"`
}

// PreviewChange replays the last hours (DefaultPreviewHours when 0) with the saved setting and with the change.
func (s *Service) PreviewChange(ctx context.Context, moduleID uint, hours int, ch PreviewChange) (*PreviewReport, error) {
	started := time.Now()
	m, err := s.Modules.Get(ctx, moduleID)
	if err != nil {
		return nil, err
	}
	if ch.empty() {
		return nil, fmt.Errorf("%w: nothing to compare, the change is empty", ErrPreviewInvalid)
	}
	if hours <= 0 {
		hours = DefaultPreviewHours
	}
	if hours > MaxPreviewHours {
		return nil, fmt.Errorf("%w: at most %d hours can be replayed", ErrPreviewInvalid, MaxPreviewHours)
	}

	runs, err := s.runsOf(ctx, m)
	if err != nil {
		return nil, err
	}
	if len(runs) == 0 {
		return nil, fmt.Errorf("%w: no algorithm is enabled", ErrNothingToScore)
	}
	cur, err := s.runInUse(ctx, m, runs)
	if err != nil {
		return nil, err
	}
	proposedModule, proposedRun, err := s.applyChange(ctx, m, cur, runs, ch)
	if err != nil {
		return nil, err
	}

	budget := s.Timing(ctx, timing.PreviewBudget)
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()

	bs := boundSet(runs)
	replicaGrid := s.signalGrid(ctx, bs, "replicas")
	currentReplicas := func(at time.Time) int {
		if replicaGrid.ok {
			if idx, ok := gridIndex(replicaGrid.in, at); ok {
				return int(math.Round(replicaGrid.in.Values[idx]))
			}
		}
		switch {
		case m.FallbackReplicas > 0:
			return m.FallbackReplicas
		case m.MinReplicas > 0:
			return m.MinReplicas
		}
		return 1
	}

	curGrids, curWhy := s.gridsOf(ctx, cur)
	if len(curGrids) == 0 {
		return nil, fmt.Errorf("%w: no source has usable history", ErrNothingToScore)
	}
	propGrids, propWhy := s.gridsOf(ctx, proposedRun)
	end := time.Time{}
	for _, in := range curGrids {
		if end.IsZero() || in.End.Before(end) {
			end = in.End
		}
	}
	window := time.Duration(cur.CoverMinutes()) * time.Minute
	n := int(time.Duration(hours) * time.Hour / window)
	if n < 1 {
		n = 1
	}
	if n > MaxPreviewWindows {
		return nil, fmt.Errorf("%w: %d hours in windows of %d minutes is %d windows, at most %d are replayed; ask for a shorter window", ErrPreviewTooLong,
			hours, int(window.Minutes()), n, MaxPreviewWindows)
	}
	specs := make([]replayWindow, n)
	for k := 0; k < n; k++ {
		specs[k] = replayWindow{start: end.Add(-time.Duration(n-k) * window), end: end.Add(-time.Duration(n-k-1) * window)}
	}

	// what the load needed: the replicas the measured values of each window ask for under the saved bounds and no safety margin, so that a
	// setting that adds margin shows as surplus and one that takes it away as shortfall
	truthModule := *m
	truthModule.HeadroomRatio = 1
	need := s.replayNeed(&truthModule, cur, curGrids, curWhy, specs, currentReplicas)

	curPred, err := s.replaySide(ctx, budget, m, cur, curGrids, curWhy, specs, currentReplicas)
	if err != nil {
		return nil, err
	}
	propPred, err := s.replaySide(ctx, budget, proposedModule, proposedRun, propGrids, propWhy, specs, currentReplicas)
	if err != nil {
		return nil, err
	}

	rep := &PreviewReport{ModuleID: moduleID, Since: specs[0].start, Until: end, Hours: hours, WindowMinutes: int(window.Minutes()), Points: []PreviewPoint{},
		Current: PreviewSide{Algorithm: cur.Key()}, Proposed: PreviewSide{Algorithm: proposedRun.Key()}, Notes: []string{}}
	for i, w := range specs {
		if need[i] <= 0 || curPred[i] <= 0 || propPred[i] <= 0 {
			rep.Skipped++
			continue
		}
		rep.Points = append(rep.Points, PreviewPoint{At: w.start, Current: curPred[i], Proposed: propPred[i], Need: need[i]})
	}
	rep.Windows = len(rep.Points)
	if rep.Windows == 0 {
		return nil, fmt.Errorf("%w: no window of the last %d hours could be replayed with both settings; the history is too short or incomplete", ErrNothingToScore, hours)
	}
	hoursPerWindow := window.Hours()
	tally(&rep.Current, rep.Points, func(p PreviewPoint) int { return p.Current }, hoursPerWindow)
	tally(&rep.Proposed, rep.Points, func(p PreviewPoint) int { return p.Proposed }, hoursPerWindow)

	largest := append([]PreviewPoint(nil), rep.Points...)
	sort.SliceStable(largest, func(i, j int) bool {
		return absInt(largest[i].Proposed-largest[i].Current) > absInt(largest[j].Proposed-largest[j].Current)
	})
	for _, p := range largest {
		if p.Proposed == p.Current || len(rep.Largest) == 5 {
			break
		}
		rep.Largest = append(rep.Largest, p)
	}
	if rep.Largest == nil {
		rep.Largest = []PreviewPoint{}
	}

	rep.Notes = append(rep.Notes,
		"The need is the replicas the measured load asked for in each window with no safety margin and the saved replica bounds; it is replayed history, not a promise about the future.",
		"Each window is forecast only from the data before it. The platform decision after the forecast (cooldowns, steps, the look-ahead, protections) is not replayed.")
	if cur.AlgorithmID == "model_service" || proposedRun.AlgorithmID == "model_service" {
		rep.Notes = append(rep.Notes, "A model service answers from its pretrained model in a preview: it is not fine-tuned and no fine-tuned copy is used.")
	}
	if rep.Skipped > 0 {
		rep.Notes = append(rep.Notes, fmt.Sprintf("%d of %d windows were left out because one of the settings could not forecast them or their load is incomplete.", rep.Skipped, n))
	}
	rep.ElapsedMs = time.Since(started).Milliseconds()
	return rep, nil
}

type replayWindow struct{ start, end time.Time }

// runInUse is the algorithm whose replicas are in force: the one the last evaluation selected, else the module's fixed one, else the first.
func (s *Service) runInUse(ctx context.Context, m *model.ForecastModule, runs []*model.AlgorithmRun) (*model.AlgorithmRun, error) {
	want := ""
	if m.PredictMode == model.PredictFixed {
		want = m.PredictAlgorithm
	} else if st, err := s.RunStates.RunState(ctx, m.ID); err == nil && st != nil {
		want = st.Selected
	}
	for _, r := range runs {
		if r.Key() == want {
			return r, nil
		}
	}
	return runs[0], nil
}

// applyChange builds the module and the run the change describes, on copies. The saved ones are never touched.
func (s *Service) applyChange(ctx context.Context, m *model.ForecastModule, cur *model.AlgorithmRun, runs []*model.AlgorithmRun, ch PreviewChange) (*model.ForecastModule, *model.AlgorithmRun, error) {
	pm := *m
	if ch.HeadroomRatio != nil {
		pm.HeadroomRatio = *ch.HeadroomRatio
	}
	if ch.PodCapacityRPS != nil {
		pm.PodCapacityRPS = *ch.PodCapacityRPS
	}
	if ch.MinReplicas != nil {
		pm.MinReplicas = *ch.MinReplicas
	}
	if ch.MaxReplicas != nil {
		pm.MaxReplicas = *ch.MaxReplicas
	}
	if err := pm.Validate(); err != nil {
		return nil, nil, fmt.Errorf("%w: %v", ErrPreviewInvalid, err)
	}

	run := *cur
	if ch.Algorithm != "" && ch.Algorithm != cur.Key() {
		var found *model.AlgorithmRun
		for _, r := range runs {
			if r.Key() == ch.Algorithm {
				found = r
				break
			}
		}
		if found != nil {
			run = *found
		} else {
			c, err := s.Algorithms.Get(ctx, ch.Algorithm)
			if err != nil {
				return nil, nil, fmt.Errorf("%w: unknown algorithm %q", ErrPreviewInvalid, ch.Algorithm)
			}
			run = *model.NewRun(m, c)
			run.LeadCoverMinutes = cur.LeadCoverMinutes
			run.ShortTermRecentMinutes, run.ShortTermTrigger, run.ShortTermFadeMinutes, run.ShortTermDownShare =
				cur.ShortTermRecentMinutes, cur.ShortTermTrigger, cur.ShortTermFadeMinutes, cur.ShortTermDownShare
		}
	}
	if ch.Params != nil {
		run.Params = marshal(ch.Params)
	}
	for _, f := range []struct {
		to *float64
		v  *float64
	}{{&run.ShortTermRecentMinutes, ch.ShortTermRecentMinutes}, {&run.ShortTermTrigger, ch.ShortTermTrigger},
		{&run.ShortTermFadeMinutes, ch.ShortTermFadeMinutes}, {&run.ShortTermDownShare, ch.ShortTermDownShare}} {
		if f.v == nil {
			continue
		}
		if *f.v < 0 || math.IsNaN(*f.v) || math.IsInf(*f.v, 0) {
			return nil, nil, fmt.Errorf("%w: the short-term correction values must be zero or more", ErrPreviewInvalid)
		}
		*f.to = *f.v
	}
	if _, err := bind(&run); err != nil { // the parameters are checked the way a saved setting is
		return nil, nil, fmt.Errorf("%w: %v", ErrPreviewInvalid, err)
	}
	// the module copy carries the sources the run was built from
	run.Sources = m.Sources
	pm.Sources = m.Sources
	return &pm, &run, nil
}

// gridsOf loads the measured history of every source of a run as a gap-free grid.
func (s *Service) gridsOf(ctx context.Context, run *model.AlgorithmRun) (map[string]forecast.Input, map[string]string) {
	grids, why := map[string]forecast.Input{}, map[string]string{}
	for _, src := range run.ActiveSources() {
		in, _, err := s.loadTarget(ctx, run, src.Key)
		if err != nil {
			why[src.Key] = err.Error()
			continue
		}
		grids[src.Key] = in
	}
	return grids, why
}

// replayInputs are the values the sources contribute to a window: the measured values of a forecast source, the history before the window
// for a source read from history.
func replayInputs(run *model.AlgorithmRun, grids map[string]forecast.Input, w replayWindow, forecastOf func(src *model.MetricSource) ([]float64, bool)) (map[string][]float64, bool) {
	values := map[string][]float64{}
	for _, src := range run.ActiveSources() {
		in, ok := grids[src.Key]
		if src.Basis == model.BasisHistory {
			if !ok {
				continue
			}
			hours := src.WindowHours
			if hours <= 0 {
				hours = 24
			}
			idx, found := gridIndex(in, w.start)
			if !found {
				continue
			}
			from := idx - int(time.Duration(hours)*time.Hour/in.Step)
			if from < 0 {
				from = 0
			}
			values[src.Key] = in.Values[from : idx+1]
			continue
		}
		v, ok := forecastOf(src)
		if !ok {
			return nil, false
		}
		values[src.Key] = v
	}
	return values, len(values) > 0
}

// replayNeed is the replicas the measured values of each window asked for; 0 where the window is incomplete.
func (s *Service) replayNeed(m *model.ForecastModule, run *model.AlgorithmRun, grids map[string]forecast.Input, why map[string]string, specs []replayWindow, current func(time.Time) int) []int {
	out := make([]int, len(specs))
	for i, w := range specs {
		values, ok := replayInputs(run, grids, w, func(src *model.MetricSource) ([]float64, bool) {
			in, ok := grids[src.Key]
			if !ok {
				return nil, false
			}
			i0, ok0 := gridIndex(in, w.start)
			i1, ok1 := gridIndex(in, w.end)
			if !ok0 || !ok1 || i1 <= i0 {
				return nil, false
			}
			return in.Values[i0+1 : i1+1], true
		})
		if !ok {
			continue
		}
		res := multimetric.Compute(multimetric.Config{}, calcInputs(run, values, why))
		out[i] = outcomeOf(m, res, current(w.start)).Replicas
	}
	return out
}

// replaySide is the replicas a setting asked for at each window, forecast from the data before the window; 0 where it could not forecast it.
func (s *Service) replaySide(ctx context.Context, budget time.Duration, m *model.ForecastModule, run *model.AlgorithmRun, grids map[string]forecast.Input, why map[string]string, specs []replayWindow, current func(time.Time) int) ([]int, error) {
	bm, err := bind(run)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrPreviewInvalid, err)
	}
	out := make([]int, len(specs))
	for i, w := range specs {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("%w: it did not finish in %s", ErrPreviewTooLong, budget)
		}
		values, ok := replayInputs(run, grids, w, func(src *model.MetricSource) ([]float64, bool) {
			return s.replayForecast(ctx, run, bm, src, grids, w.start, w.end)
		})
		if !ok {
			continue
		}
		res := multimetric.Compute(multimetric.Config{}, calcInputs(run, values, why))
		out[i] = outcomeOf(m, res, current(w.start)).Replicas
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("%w: it did not finish in %s", ErrPreviewTooLong, budget)
	}
	return out, nil
}

func tally(side *PreviewSide, points []PreviewPoint, pick func(PreviewPoint) int, hoursPerWindow float64) {
	var under, over, needed float64
	prev := 0
	for i, p := range points {
		got := pick(p)
		switch {
		case got < p.Need:
			side.Under++
			under += float64(p.Need - got)
		case got > p.Need:
			side.Over++
			over += float64(got - p.Need)
		}
		needed += float64(p.Need)
		side.ReplicaHours += float64(got) * hoursPerWindow
		if i > 0 && got != prev {
			side.Changes++
		}
		prev = got
	}
	if side.Under > 0 {
		side.UnderReplicas = under / float64(side.Under)
	}
	if side.Over > 0 {
		side.OverReplicas = over / float64(side.Over)
	}
	if needed > 0 {
		side.RiskError = (previewUnderWeight*under + over) / needed
	}
}

func absInt(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
