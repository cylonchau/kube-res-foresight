package forecastsvc

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"sort"
	"sync/atomic"
	"time"

	"context"
	"github.com/cylonchau/kube-res-foresight/pkg/forecast"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/cylonchau/kube-res-foresight/pkg/timing"
)

// A forecast is a function of what the algorithm is given, so one that would be given the same as the last run is not computed or stored again (P11 B3): the
// key is the module, the source, the algorithm instance, the model version (the hash of the implementation and its parameters), the end of the data and the data
// itself, and a forecast stands for the same request for forecast.reuse_minutes. When the history changes in any point (a new sample, a hole that was filled
// again), the key changes and the forecast is made.

// ReuseStats counts, for this process, the forecasts that were made and the ones that a forecast already made stood for.
type ReuseStats struct {
	Computed int64 `json:"computed"`
	Reused   int64 `json:"reused"`
}

var computed, reused atomic.Int64

// Reuse reads the counters.
func Reuse() ReuseStats { return ReuseStats{Computed: computed.Load(), Reused: reused.Load()} }

// inputHash is the hash of everything a forecast was made from.
func inputHash(bm *boundModel, in *forecast.Input, horizon int) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s|%s|%s|%d|%d|%d|%d|", bm.def.ID, bm.version, bm.params, in.End.Unix(), int64(in.Step/time.Second), horizon, len(in.Values))
	writeFloats := func(values []float64) {
		var buf [8]byte
		for _, v := range values {
			binary.LittleEndian.PutUint64(buf[:], math.Float64bits(v))
			h.Write(buf[:])
		}
	}
	writeFloats(in.Values)
	names := make([]string, 0, len(in.Covariates))
	for name := range in.Covariates {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		fmt.Fprintf(h, "|%s|", name)
		writeFloats(in.Covariates[name])
	}
	return hex.EncodeToString(h.Sum(nil))[:40]
}

// reusable says whether the forecast of the last run of this algorithm on this source stands for the one that is about to be made: it was made from the same
// inputs, not more than the reuse window ago (counted from the run whose rows it is), and the algorithm answers the same for the same inputs. It returns the
// time of the run whose rows are the forecast and the number of points it has.
func (s *Service) reusable(ctx context.Context, b *model.AlgorithmRun, bm *boundModel, key, hash string, at time.Time) (from time.Time, points int, ok bool) {
	window := time.Duration(s.Count(ctx, timing.ForecastReuse)) * time.Minute
	if window <= 0 || bm.def.Category == forecast.CategoryModelService {
		return time.Time{}, 0, false
	}
	if t, tunable := forecast.TunableOf(bm.model); tunable && t.Tuning().Enabled() {
		return time.Time{}, 0, false // a model that is tuned on the series changes under the same inputs
	}
	prev, err := s.Predictions.LatestOKTraining(ctx, b.ID, key, b.Key())
	if err != nil || prev == nil || prev.InputHash == "" || prev.InputHash != hash {
		return time.Time{}, 0, false
	}
	source := prev.PredictionTime
	if prev.ReusedFrom != nil {
		source = *prev.ReusedFrom
	}
	if at.Sub(source) >= window || at.Before(source) {
		return time.Time{}, 0, false
	}
	return source, prev.Points, true
}
