package forecastsvc

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/forecast"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/cylonchau/kube-res-foresight/pkg/multimetric"
	"github.com/cylonchau/kube-res-foresight/pkg/scoring"
)

// ErrNothingToScore means no algorithm instance is bound to the module or no usable history exists to replay.
var ErrNothingToScore = fmt.Errorf("%w: nothing to score", ErrNotConfigured)

// calcInputs turns the values of each source into the inputs of the multi-metric calculation. A source with no
// entry in values is missing, and why[key] says why.
func calcInputs(a *model.AlgorithmRun, values map[string][]float64, why map[string]string) []multimetric.Input {
	sources := a.ActiveSources()
	out := make([]multimetric.Input, 0, len(sources))
	for _, src := range sources {
		in := multimetric.Input{Source: src.Spec()}
		if v, ok := values[src.Key]; ok && len(v) > 0 {
			in.Values = v
		} else {
			in.Missing, in.Reason = true, why[src.Key]
			if in.Reason == "" {
				in.Reason = "no values"
			}
		}
		out = append(out, in)
	}
	return out
}

// outcomeOf turns a calculation into what it recommends: the resources with the module headroom applied, and the
// replica count from the pressure and the throughput constraint, inside the module bounds.
func outcomeOf(m *model.ForecastModule, res multimetric.Result, current int) scoring.Outcome {
	headroom := m.HeadroomRatio
	if headroom < 1 {
		headroom = 1
	}
	var o scoring.Outcome
	if v, ok := res.Resources[multimetric.ResourceCPU]; ok {
		o.CPU, o.HasCPU = v.Value*headroom, true
	}
	if v, ok := res.Resources[multimetric.ResourceMemory]; ok {
		o.Memory, o.HasMemory = v.Value*headroom, true
	}
	if v, ok := res.Resources[multimetric.ResourceGPU]; ok {
		o.GPU, o.HasGPU = math.Ceil(v.Value*headroom), true
	}
	n, _ := res.ReplicasFor(current)
	if v, ok := res.Resources[multimetric.ResourceReplicas]; ok && m.PodCapacityRPS > 0 {
		if tp := int(math.Ceil(v.Value * headroom / m.PodCapacityRPS)); tp > n {
			n = tp
		}
	}
	if n == 0 {
		n = current
	}
	if m.MinReplicas > 0 && n < m.MinReplicas {
		n = m.MinReplicas
	}
	ceiling := m.MaxReplicas
	if m.MaxReplicasMode == model.MaxReplicasDynamic && m.HardMaxReplicas > 0 {
		ceiling = m.HardMaxReplicas
	}
	if ceiling > 0 && n > ceiling {
		n = ceiling
	}
	o.Replicas = n
	return o
}

// gridIndex is the position of the grid step at or before t.
func gridIndex(in forecast.Input, t time.Time) (int, bool) {
	if t.Before(in.TimeAt(0)) {
		return 0, false
	}
	i := int(t.Sub(in.TimeAt(0)) / in.Step)
	if i >= len(in.Values) {
		return len(in.Values) - 1, true
	}
	return i, true
}

// ScoreModule replays the last windows of every algorithm instance bound to the module: the instance forecasts each
// window from the data before it, the multi-metric calculation turns the forecast into a recommendation (replicas,
// CPU, memory, GPU) under the module's policy, and the same calculation on what the instance measured in the window
// says what was needed. The grade (see pkg/scoring) is stored with a rank; ranking multiplies the score by the
// weight of the algorithm.
func (s *Service) ScoreModule(ctx context.Context, moduleID uint) ([]*model.AlgorithmScore, error) {
	m, err := s.Modules.Get(ctx, moduleID)
	if err != nil {
		return nil, err
	}
	bs, err := s.boundSet(ctx, m)
	if err != nil {
		return nil, err
	}
	if len(bs) == 0 {
		return nil, fmt.Errorf("%w: no algorithm is enabled", ErrNothingToScore)
	}
	// the module-wide signals of the windows: the replica count that was running and the OOM kills
	oomGrid, replicaGrid := s.signalGrid(ctx, bs, "oom"), s.signalGrid(ctx, bs, "replicas")
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

	var rows []*model.AlgorithmScore
	weight := map[string]float64{}
	for _, b := range bs {
		row, err := s.scoreInstance(ctx, m, b, currentReplicas, oomGrid)
		if err != nil {
			continue // an instance with nothing to replay has no score yet; the others are still ranked
		}
		rows = append(rows, row)
		w := b.Weight
		if w <= 0 {
			w = 1
		}
		weight[row.Algorithm] = w
	}
	if len(rows) == 0 {
		return nil, ErrNothingToScore
	}
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i].Score*weight[rows[i].Algorithm], rows[j].Score*weight[rows[j].Algorithm]
		if a != b {
			return a > b
		}
		return rows[i].Algorithm < rows[j].Algorithm
	})
	state, err := s.RunStates.RunState(ctx, m.ID)
	if err != nil {
		return nil, err
	}
	for i, r := range rows {
		r.Rank = i + 1
		r.Selected = r.Algorithm == state.Selected
	}
	if err := s.Predictions.SaveScores(ctx, rows); err != nil {
		return nil, err
	}
	// the records beyond the cap of the system parameter are not worth keeping
	if _, err := s.Predictions.PruneScores(ctx, m.ID, s.RetentionPolicy(ctx).Scores); err != nil {
		return rows, err
	}
	return rows, nil
}

// signalGrid is the measured history of the first source of a kind across the bound instances.
type signalGrid struct {
	in forecast.Input
	ok bool
}

func (s *Service) signalGrid(ctx context.Context, bs boundSet, kind string) signalGrid {
	ref := bs.firstOfKind(kind)
	if ref == nil {
		return signalGrid{}
	}
	in, _, err := s.loadTarget(ctx, ref.Inst, ref.Src.Key)
	if err != nil {
		return signalGrid{}
	}
	return signalGrid{in: in, ok: true}
}

// scoreInstance replays the recent windows of one bound instance on the sources it owns.
func (s *Service) scoreInstance(ctx context.Context, m *model.ForecastModule, inst *model.AlgorithmRun, currentReplicas func(time.Time) int, oomGrid signalGrid) (*model.AlgorithmScore, error) {
	bm, err := bind(inst)
	if err != nil {
		return nil, err
	}
	sources := inst.ActiveSources()
	if len(sources) == 0 {
		return nil, ErrNothingToScore
	}
	// the measured history of every source, as a gap-free grid
	grids := map[string]forecast.Input{}
	why := map[string]string{}
	for _, src := range sources {
		in, _, err := s.loadTarget(ctx, inst, src.Key)
		if err != nil {
			why[src.Key] = err.Error()
			continue
		}
		grids[src.Key] = in
	}
	if len(grids) == 0 {
		return nil, fmt.Errorf("%w: no source has usable history", ErrNothingToScore)
	}
	// the windows end at the newest sample every source has
	end := time.Time{}
	for _, in := range grids {
		if end.IsZero() || in.End.Before(end) {
			end = in.End
		}
	}
	horizon := time.Duration(inst.CoverMinutes()) * time.Minute // the stretch the algorithm forecasts is the stretch it is graded on

	var specs []windowSpec
	for k := model.DefaultScoreWindows; k >= 1; k-- {
		specs = append(specs, windowSpec{start: end.Add(-time.Duration(k) * horizon), end: end.Add(-time.Duration(k-1) * horizon)})
	}
	recent := len(specs)
	// The newest windows are the calm of the last hour or two, where "the load stays as it is" is right and every algorithm looks alike. The
	// windows in which the load rose most in the history are graded with them: whether an algorithm saw a peak coming is what its score is for.
	if ref, ok := grids[referenceSource(sources)]; ok {
		notBefore := ref.TimeAt(0).Add(time.Duration(stepsNeeded(inst, ref.Step)) * ref.Step)
		specs = append(specs, eventWindows(ref, end, horizon, recent, model.DefaultEventWindows, model.EventMinRise, notBefore)...)
	}

	// actual: what the measured values of a window required
	history := func(src *model.MetricSource, at time.Time) []float64 {
		in, ok := grids[src.Key]
		if !ok {
			return nil
		}
		hours := src.WindowHours
		if hours <= 0 {
			hours = 24
		}
		idx, ok := gridIndex(in, at)
		if !ok {
			return nil
		}
		from := idx - int(time.Duration(hours)*time.Hour/in.Step)
		if from < 0 {
			from = 0
		}
		return in.Values[from : idx+1]
	}
	windowOf := func(in forecast.Input, w windowSpec) ([]float64, bool) {
		i0, ok0 := gridIndex(in, w.start)
		i1, ok1 := gridIndex(in, w.end)
		if !ok0 || !ok1 || i1 <= i0 {
			return nil, false
		}
		return in.Values[i0+1 : i1+1], true
	}
	windowValues := func(src *model.MetricSource, w windowSpec) ([]float64, bool) {
		in, ok := grids[src.Key]
		if !ok {
			return nil, false
		}
		return windowOf(in, w)
	}

	type actualWindow struct {
		outcome  scoring.Outcome
		oom      int
		complete bool
		ok       bool
	}
	actuals := make([]actualWindow, len(specs))
	cfg := multimetric.Config{}
	for i, w := range specs {
		values := map[string][]float64{}
		complete := true
		for _, src := range sources {
			if src.Basis == model.BasisHistory {
				if v := history(src, w.start); len(v) > 0 {
					values[src.Key] = v
				}
				continue
			}
			v, ok := windowValues(src, w)
			if !ok {
				complete = false
				continue
			}
			values[src.Key] = v
		}
		if len(values) == 0 {
			continue
		}
		res := multimetric.Compute(cfg, calcInputs(inst, values, why))
		a := actualWindow{outcome: outcomeOf(m, res, currentReplicas(w.start)), complete: complete, ok: true}
		if oomGrid.ok {
			if v, ok := windowOf(oomGrid.in, w); ok {
				for _, x := range v {
					if x > 0 {
						a.oom += int(math.Round(x))
					}
				}
			}
		}
		actuals[i] = a
	}

	var wins []scoring.Window
	for i, w := range specs {
		if !actuals[i].ok {
			continue
		}
		predicted := map[string][]float64{}
		missing := false
		for _, src := range sources {
			if src.Basis == model.BasisHistory {
				if v := history(src, w.start); len(v) > 0 {
					predicted[src.Key] = v
				}
				continue
			}
			vals, ok := s.replayForecast(ctx, inst, bm, src, grids, w.start, w.end)
			if !ok {
				missing = true
				continue
			}
			predicted[src.Key] = vals
		}
		// a window in which the algorithm could not forecast a source it owns is no evidence about it: scoring the rest (a replica
		// count read from history, say) would give every algorithm that failed the same high score for forecasting nothing
		if missing || len(predicted) == 0 {
			continue
		}
		res := multimetric.Compute(cfg, calcInputs(inst, predicted, why))
		wins = append(wins, scoring.Window{Start: w.start, End: w.end, Predicted: outcomeOf(m, res, currentReplicas(w.start)),
			Actual: actuals[i].outcome, OOM: actuals[i].oom, Complete: actuals[i].complete && !missing, Event: w.event, Rise: w.rise})
	}
	result := scoring.Score(wins, scoring.WeightsWithOOM(m.OOMScoreWeight()))
	// which sources each resource was read from: the basis a person checks the figures against
	result.Sources = map[string][]string{}
	for _, src := range sources {
		if kind := src.Kind; kind == "cpu" || kind == "memory" || kind == "gpu" {
			result.Sources[kind] = append(result.Sources[kind], src.Key)
		}
	}
	if result.Windows == 0 {
		// nothing could be replayed (too little history for the algorithm, or no window with actual values): a score of 0
		// over no window would rank the algorithm on no evidence at all, so there is no score
		return nil, fmt.Errorf("%w: no window of the recent history could be replayed with %s", ErrNothingToScore, inst.Key())
	}
	return &model.AlgorithmScore{ModuleID: m.ID, Algorithm: inst.Key(), AlgorithmID: inst.AlgorithmID, Score: result.Score,
		Windows: result.Windows, Detail: marshal(result), WindowStart: specs[0].start, WindowEnd: specs[recent-1].end}, nil
}

// replayForecast forecasts the window [start, end] of a source with the instance from the data before start.
func (s *Service) replayForecast(ctx context.Context, b *model.AlgorithmRun, bm *boundModel, src *model.MetricSource,
	grids map[string]forecast.Input, start, end time.Time) ([]float64, bool) {
	full, ok := grids[src.Key]
	if !ok {
		return nil, false
	}
	i0, ok := gridIndex(full, start)
	if !ok {
		return nil, false
	}
	train := forecast.Input{Values: full.Values[:i0+1], Step: full.Step, End: full.TimeAt(i0)}
	steps := int((end.Sub(start) + train.Step - 1) / train.Step)
	if steps < 1 {
		steps = 1
	}
	if bm.def.Covariates {
		for _, key := range coverOf(b, src.Key, bm.def) {
			cov, ok := grids[key]
			if !ok {
				continue
			}
			// the covariate on the grid of the target, cut at the same step: nothing after the cut is visible
			aligned := make([]float64, len(train.Values))
			complete := true
			for j := range aligned {
				idx, found := gridIndex(cov, train.TimeAt(j))
				if !found {
					complete = false
					break
				}
				aligned[j] = cov.Values[idx]
			}
			if complete {
				if train.Covariates == nil {
					train.Covariates = map[string][]float64{}
				}
				train.Covariates[key] = aligned
			}
		}
	}
	rctx, cancel := context.WithTimeout(ctx, s.budgetFor(ctx, b))
	defer cancel()
	values, err := bm.model.Forecast(rctx, train, steps)
	if err != nil {
		return nil, false
	}
	return values, true
}
