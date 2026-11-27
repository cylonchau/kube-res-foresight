package forecastsvc

import (
	"context"

	"github.com/cylonchau/kube-res-foresight/pkg/model"
)

// boundSet is what a module works with: one run for each enabled algorithm, in algorithm order, each reading the
// module's own metric sources.
type boundSet []*model.AlgorithmRun

// dataSet is what the metrics around a decision (the JVM heap, the OOM kills, the replicas that run, the metric triggers) are read through: the runs of the algorithms when the
// module predicts, else the module itself as the owner of its metric sources (a module on a strategy, or on the recommendation mode, has no algorithm run, and its sources are as much
// its own).
func dataSet(m *model.ForecastModule, bs boundSet) boundSet {
	if len(bs) > 0 {
		return bs
	}
	return boundSet{m.Data()}
}

// sourceRef is a metric source with the algorithm run that forecasts it. A source is the module's, but each algorithm
// forecasts it separately, so a forecast is found through both.
type sourceRef struct {
	Inst *model.AlgorithmRun
	Src  *model.MetricSource
}

// boundSet loads the runs of a module.
func (s *Service) boundSet(ctx context.Context, m *model.ForecastModule) (boundSet, error) {
	runs, err := s.runsOf(ctx, m)
	return boundSet(runs), err
}

// sourcesOfKind are the module's enabled sources of a kind (each once, whatever the number of algorithms).
func (bs boundSet) sourcesOfKind(kind string) []sourceRef {
	var out []sourceRef
	if len(bs) == 0 {
		return out
	}
	// the metrics around the forecast (OOM, JVM, Requests, Limits) are read from every source of the module, the display ones too; only the forecast of a resource
	// (forecastOfResource) is limited to the algorithm sources
	all := *bs[0]
	all.AllSources = true
	for _, src := range all.ActiveSources() {
		if src.Kind == kind {
			out = append(out, sourceRef{bs[0], src})
		}
	}
	return out
}

// firstOfKind is the first enabled source of a kind (by source priority), or nil.
func (bs boundSet) firstOfKind(kind string) *sourceRef {
	if refs := bs.sourcesOfKind(kind); len(refs) > 0 {
		return &refs[0]
	}
	return nil
}

// forecastOfResource are the enabled, forecast-basis sources that feed a resource, once for every algorithm that
// forecasts them.
func (bs boundSet) forecastOfResource(resource string) []sourceRef {
	var out []sourceRef
	for _, b := range bs {
		for _, src := range b.OfResource(resource) {
			if src.Basis == model.BasisForecast {
				out = append(out, sourceRef{b, src})
			}
		}
	}
	return out
}

// pairKey names a source forecast by an algorithm in maps that span algorithms.
func pairKey(algorithm, source string) string { return algorithm + "/" + source }
