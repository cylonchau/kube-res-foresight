package collector

import (
	"context"
	"fmt"
	"time"

	"github.com/cylonchau/kubeforecast/pkg/promclient"
)

// PromQuerier adapts a Prometheus client to RangeQuerier.
type PromQuerier struct{ Client *promclient.Client }

func (p PromQuerier) QueryRange(ctx context.Context, query string, start, end time.Time, step time.Duration) ([]Point, error) {
	samples, err := p.Client.QueryRange(ctx, query, start, end, step)
	if err != nil {
		if promclient.IsPermanent(err) {
			return nil, fmt.Errorf("%w: %v", ErrPermanent, err)
		}
		return nil, err
	}
	points := make([]Point, len(samples))
	for i, s := range samples {
		points[i] = Point{Time: s.Time, Value: s.Value}
	}
	return points, nil
}
