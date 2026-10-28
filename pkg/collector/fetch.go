package collector

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"sort"
	"sync"
	"time"
)

// Point is one sample. Value is NaN where the source reported NaN.
type Point struct {
	Time  time.Time
	Value float64
}

// RangeQuerier runs one query_range. It must return the single series the query produces and fail when the
// query returns several, so series from different workloads are never mixed.
type RangeQuerier interface {
	QueryRange(ctx context.Context, query string, start, end time.Time, step time.Duration) ([]Point, error)
}

// Options is the request budget for one fetch.
type Options struct {
	MaxPointsPerRequest int           // default 5000, Prometheus rejects more than 11000
	Concurrency         int           // default 1 (serial)
	Attempts            int           // tries per segment, default 3
	BaseBackoff         time.Duration // first retry delay, doubled each time with jitter; default 500ms
	MaxBackoff          time.Duration // default 10s
	Sleep               func(context.Context, time.Duration) error
}

func (o Options) withDefaults() Options {
	if o.MaxPointsPerRequest <= 0 || o.MaxPointsPerRequest > 11000 {
		o.MaxPointsPerRequest = 5000
	}
	if o.Concurrency <= 0 {
		o.Concurrency = 1
	}
	if o.Attempts <= 0 {
		o.Attempts = 3
	}
	if o.BaseBackoff <= 0 {
		o.BaseBackoff = 500 * time.Millisecond
	}
	if o.MaxBackoff <= 0 {
		o.MaxBackoff = 10 * time.Second
	}
	if o.Sleep == nil {
		o.Sleep = sleepContext
	}
	return o
}

func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// ErrPermanent marks an error that retrying cannot fix (bad query, ambiguous series).
var ErrPermanent = errors.New("permanent query error")

type segment struct{ start, end time.Time }

// Segments splits [start, end] into windows of at most maxPoints steps. Boundaries fall on the step grid
// anchored at start, so adjacent segments neither overlap nor leave a hole.
func Segments(start, end time.Time, step time.Duration, maxPoints int) []segment {
	if step <= 0 || maxPoints <= 0 || end.Before(start) {
		return nil
	}
	span := time.Duration(maxPoints-1) * step
	var out []segment
	for cur := start; !cur.After(end); {
		segEnd := cur.Add(span)
		if segEnd.After(end) {
			segEnd = end
		}
		out = append(out, segment{cur, segEnd})
		cur = segEnd.Add(step)
	}
	return out
}

// Fetch pulls [start, end] in segments within the request budget, retrying transient failures with
// exponential backoff and jitter, and returns the merged points ordered by time without duplicates.
func Fetch(ctx context.Context, q RangeQuerier, query string, start, end time.Time, step time.Duration, opt Options) ([]Point, error) {
	opt = opt.withDefaults()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	segs := Segments(start, end, step, opt.MaxPointsPerRequest)
	results := make([][]Point, len(segs))
	errs := make([]error, len(segs))

	workers := opt.Concurrency // never more requests at once than segments to fetch, however high the setting is
	if workers > len(segs) {
		workers = len(segs)
	}
	sem := make(chan struct{}, max(workers, 1))
	var wg sync.WaitGroup
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	for i, s := range segs {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			errs[i] = ctx.Err()
			continue
		}
		wg.Add(1)
		go func(i int, s segment) {
			defer wg.Done()
			defer func() { <-sem }()
			results[i], errs[i] = fetchSegment(ctx, q, query, s, step, opt)
			if errs[i] != nil {
				cancel() // a failed segment makes the rest of the window useless
			}
		}(i, s)
	}
	wg.Wait()
	// A failed segment cancels the others, so report the real failure rather than a later "context canceled".
	var canceled error
	for _, err := range errs {
		if err == nil {
			continue
		}
		if errors.Is(err, context.Canceled) {
			canceled = err
			continue
		}
		return nil, err
	}
	if canceled != nil {
		return nil, canceled
	}
	return merge(results), nil
}

func fetchSegment(ctx context.Context, q RangeQuerier, query string, s segment, step time.Duration, opt Options) ([]Point, error) {
	var lastErr error
	delay := opt.BaseBackoff
	for attempt := 1; attempt <= opt.Attempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		points, err := q.QueryRange(ctx, query, s.start, s.end, step)
		if err == nil {
			return points, nil
		}
		lastErr = err
		if errors.Is(err, ErrPermanent) || ctx.Err() != nil || attempt == opt.Attempts {
			break
		}
		wait := delay + time.Duration(rand.Int63n(int64(delay)/2+1))
		if wait > opt.MaxBackoff {
			wait = opt.MaxBackoff
		}
		if err := opt.Sleep(ctx, wait); err != nil {
			return nil, err
		}
		delay *= 2
	}
	return nil, fmt.Errorf("query_range %s..%s: %w", s.start.UTC().Format(time.RFC3339), s.end.UTC().Format(time.RFC3339), lastErr)
}

func merge(parts [][]Point) []Point {
	var all []Point
	for _, p := range parts {
		all = append(all, p...)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Time.Before(all[j].Time) })
	out := all[:0]
	for _, p := range all {
		if n := len(out); n > 0 && out[n-1].Time.Equal(p.Time) {
			if math.IsNaN(out[n-1].Value) && !math.IsNaN(p.Value) {
				out[n-1] = p
			}
			continue
		}
		out = append(out, p)
	}
	return out
}
