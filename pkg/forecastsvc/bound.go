package forecastsvc

import (
	"context"

	"github.com/cylonchau/kube-res-foresight/pkg/model"
)

// boundSet is what a module works with: one run for each enabled algorithm, in algorithm order, each reading the
// module's own metric sources.
type boundSet []*model.AlgorithmRun

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
	for _, src := range bs[0].ActiveSources() {
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
