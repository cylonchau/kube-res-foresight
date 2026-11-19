package forecastsvc

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/collector"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type slowQuerier struct {
	running, peak atomic.Int32
	hold          time.Duration
}

func (q *slowQuerier) QueryRange(ctx context.Context, _ string, _, _ time.Time, _ time.Duration) ([]collector.Point, error) {
	n := q.running.Add(1)
	defer q.running.Add(-1)
	for {
		p := q.peak.Load()
		if n <= p || q.peak.CompareAndSwap(p, n) {
			break
		}
	}
	select {
	case <-time.After(q.hold):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return nil, nil
}

func TestTheQueriesToOneDataSourceNeverExceedTheGateWhoeverSentThem(t *testing.T) {
	inner := &slowQuerier{hold: 20 * time.Millisecond}
	g := gateFor(9001, 3)
	var wg sync.WaitGroup
	// ten callers, as ten pages open at once, each with its own querier over the same gate
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := gatedQuerier{inner: inner, gate: g}.QueryRange(context.Background(), "q", time.Now(), time.Now(), time.Minute)
			assert.NoError(t, err)
		}()
	}
	wg.Wait()
	assert.LessOrEqual(t, inner.peak.Load(), int32(3), "at most three in flight")
	assert.GreaterOrEqual(t, inner.peak.Load(), int32(2), "and they did run together")
}

func TestAQueryThatWaitsForTheGateGivesUpWithItsRequest(t *testing.T) {
	g := gateFor(9002, 1)
	g.slot <- struct{}{} // the only place is taken
	defer func() { <-g.slot }()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	began := time.Now()
	_, err := gatedQuerier{inner: &slowQuerier{}, gate: g}.QueryRange(ctx, "q", time.Now(), time.Now(), time.Minute)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Less(t, time.Since(began), time.Second, "it did not wait for the place: its time was over")
}

func TestTheGateIsMadeAgainWhenTheLimitChangesAndTheDemoSourceIsNotGated(t *testing.T) {
	a := gateFor(9003, 2)
	assert.Same(t, a, gateFor(9003, 2), "the same limit is the same gate")
	assert.NotSame(t, a, gateFor(9003, 5), "a new limit is a new gate")

	s := &Service{NewQuerier: func(*model.Datasource) (collector.RangeQuerier, error) { return &slowQuerier{}, nil }}
	demo, err := s.querierOf(context.Background(), &model.Datasource{ID: 9004, Type: model.DatasourceTypeDemo})
	require.NoError(t, err)
	_, gated := demo.(gatedQuerier)
	assert.False(t, gated)
	prom, err := s.querierOf(context.Background(), &model.Datasource{ID: 9005, Type: "prometheus"})
	require.NoError(t, err)
	_, gated = prom.(gatedQuerier)
	assert.True(t, gated)
}
