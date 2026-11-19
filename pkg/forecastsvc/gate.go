package forecastsvc

import (
	"context"
	"sync"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/collector"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/cylonchau/kube-res-foresight/pkg/timing"
)

// The data source has a limit of its own for how many queries one fetch sends at once (MaxConcurrency of the data source). It does not cover
// requests that arrive together: five people opening the history page, or a collection that runs while a page reads, each send their own queries,
// and a Prometheus that is asked for forty range queries over weeks at once answers every one of them slowly, so the pages that wait for it run
// out of their time. The gate is the limit for the whole process: at most the system parameter forecast.prometheus_total_concurrency queries to
// one data source are in flight, whoever sent them; the others wait their turn (and give up with their request when its time is over).

type gate struct {
	size int
	slot chan struct{}
}

var (
	gatesMu sync.Mutex
	gates   = map[uint]*gate{}
)

// gateFor is the gate of a data source, made again when the limit was changed (the queries in flight on the old one finish on it).
func gateFor(id uint, size int) *gate {
	gatesMu.Lock()
	defer gatesMu.Unlock()
	g := gates[id]
	if g == nil || g.size != size {
		g = &gate{size: size, slot: make(chan struct{}, size)}
		gates[id] = g
	}
	return g
}

// gatedQuerier lets a query through when the gate has room.
type gatedQuerier struct {
	inner collector.RangeQuerier
	gate  *gate
	// waited counts the time the queries spent waiting for the gate, for the log of a slow request.
	waited func(time.Duration)
}

func (q gatedQuerier) QueryRange(ctx context.Context, query string, start, end time.Time, step time.Duration) ([]collector.Point, error) {
	began := time.Now()
	select {
	case q.gate.slot <- struct{}{}:
		defer func() { <-q.gate.slot }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if q.waited != nil {
		q.waited(time.Since(began))
	}
	return q.inner.QueryRange(ctx, query, start, end, step)
}

// querierOf builds the querier of a data source with the process-wide gate in front of it. The demo source is not a Prometheus and is not gated.
func (s *Service) querierOf(ctx context.Context, ds *model.Datasource) (collector.RangeQuerier, error) {
	inner, err := s.NewQuerier(ds)
	if err != nil || ds.Type == model.DatasourceTypeDemo {
		return inner, err
	}
	size := int(s.intParam(ctx, timing.PrometheusTotalConcurrency))
	return gatedQuerier{inner: inner, gate: gateFor(ds.ID, size)}, nil
}

// intParam reads a system parameter that is a whole number, kept in its range.
func (s *Service) intParam(ctx context.Context, p timing.Param) int64 {
	if s.Config == nil {
		return int64(p.Default)
	}
	raw, err := s.Config.GetValueOrDefault(ctx, p.Key, "")
	if err != nil {
		return int64(p.Default)
	}
	return int64(p.Parse(raw))
}
