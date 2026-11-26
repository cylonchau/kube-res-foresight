package resultcache

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// slow is work that ends when release is closed, and counts how many times it started.
func slow(started *atomic.Int32, release <-chan struct{}, rows ...string) func(context.Context) (*report, error) {
	return func(ctx context.Context) (*report, error) {
		started.Add(1)
		select {
		case <-release:
			return &report{Rows: rows}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// Work that is done within the wait is answered as Do answers it, and kept.
func TestWithinAnswersWorkThatEndsInTimeAndKeepsIt(t *testing.T) {
	c, _ := newCache(t)
	var n atomic.Int32
	got, res, err := Within(context.Background(), c, "k", "v1", time.Minute, time.Second, time.Minute, nil, counted(&n, "a"))
	require.NoError(t, err)
	assert.Equal(t, Computed, res.Source)
	assert.Equal(t, []string{"a"}, got.Rows)
	got, res, err = Within(context.Background(), c, "k", "v1", time.Minute, time.Second, time.Minute, nil, counted(&n, "b"))
	require.NoError(t, err)
	assert.Equal(t, Cached, res.Source)
	assert.Equal(t, []string{"a"}, got.Rows)
	assert.EqualValues(t, 1, n.Load())
}

// When the wait runs out the request is answered as pending and the work is not stopped: it ends, is kept, and the next ask gets it.
func TestWithinGoesOnWorkingAfterTheRequestStoppedWaiting(t *testing.T) {
	c, _ := newCache(t)
	var started atomic.Int32
	release := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	_, _, err := Within(ctx, c, "k", "v1", time.Minute, 20*time.Millisecond, time.Minute, nil, slow(&started, release, "late"))
	require.ErrorIs(t, err, ErrPending, "the wait ran out")
	cancel() // the client or the gateway went away: the work is not theirs
	_, _, err = Within(context.Background(), c, "k", "v1", time.Minute, 20*time.Millisecond, time.Minute, nil, slow(&started, release, "again"))
	require.ErrorIs(t, err, ErrPending, "asking while it works joins it and waits again")
	assert.EqualValues(t, 1, started.Load(), "one piece of work for both asks")

	close(release)
	require.Eventually(t, func() bool {
		got, res, err := Within(context.Background(), c, "k", "v1", time.Minute, time.Second, time.Minute, nil, slow(&started, release, "again"))
		return err == nil && res.Source == Cached && got.Rows[0] == "late"
	}, 2*time.Second, 10*time.Millisecond, "the answer of the work that went on is kept for the next ask")
	assert.EqualValues(t, 1, started.Load())
}

// Many asks at once make one piece of work.
func TestWithinMakesAsksThatComeTogetherShareOneWork(t *testing.T) {
	c, _ := newCache(t)
	var started atomic.Int32
	release := make(chan struct{})
	var wg sync.WaitGroup
	results := make([]*report, 8)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, _, err := Within(context.Background(), c, "k", "v1", time.Minute, 2*time.Second, time.Minute, nil, slow(&started, release, "shared"))
			if err == nil {
				results[i] = got
			}
		}()
	}
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	assert.EqualValues(t, 1, started.Load())
	for _, r := range results {
		require.NotNil(t, r)
		assert.Equal(t, []string{"shared"}, r.Rows)
	}
}

// The work has a budget of its own: it ends when it runs out, whatever the request did.
func TestWithinStopsTheWorkWhenItsBudgetRunsOut(t *testing.T) {
	c, _ := newCache(t)
	var started atomic.Int32
	_, _, err := Within(context.Background(), c, "k", "v1", time.Minute, 20*time.Millisecond, 50*time.Millisecond, nil, slow(&started, make(chan struct{})))
	require.ErrorIs(t, err, ErrPending)
	require.Eventually(t, func() bool {
		_, _, err := Within(context.Background(), c, "k", "v1", time.Minute, time.Second, 50*time.Millisecond, nil, slow(&started, make(chan struct{})))
		return errors.Is(err, context.DeadlineExceeded)
	}, 2*time.Second, 10*time.Millisecond, "the reason is given to the ask that comes after the work ended, and the work is not started again at once")
	assert.EqualValues(t, 1, started.Load())
}

// A change of the version of the data is not answered from the old work.
func TestWithinWorksAgainWhenTheVersionChanges(t *testing.T) {
	c, _ := newCache(t)
	var n atomic.Int32
	_, _, err := Within(context.Background(), c, "k", "v1", time.Minute, time.Second, time.Minute, nil, counted(&n, "a"))
	require.NoError(t, err)
	got, res, err := Within(context.Background(), c, "k", "v2", time.Minute, time.Second, time.Minute, nil, counted(&n, "b"))
	require.NoError(t, err)
	assert.Equal(t, Computed, res.Source)
	assert.Equal(t, []string{"b"}, got.Rows)
}

// A partial answer can be kept for less than a whole one.
func TestWithinKeepsAPartialAnswerForItsOwnShorterTime(t *testing.T) {
	c, cl := newCache(t)
	var n atomic.Int32
	ttlOf := func(r *report) time.Duration {
		if len(r.Rows) == 1 {
			return 10 * time.Second
		}
		return time.Hour
	}
	_, _, err := Within(context.Background(), c, "k", "v1", time.Hour, time.Second, time.Minute, ttlOf, counted(&n, "partial"))
	require.NoError(t, err)
	cl.advance(5 * time.Second)
	_, res, _ := Within(context.Background(), c, "k", "v1", time.Hour, time.Second, time.Minute, ttlOf, counted(&n, "partial"))
	assert.Equal(t, Cached, res.Source)
	cl.advance(10 * time.Second)
	_, res, _ = Within(context.Background(), c, "k", "v1", time.Hour, time.Second, time.Minute, ttlOf, counted(&n, "partial"))
	assert.Equal(t, Computed, res.Source, "the partial answer was kept for ten seconds only")
}

func TestWithinWithTheCacheOffWorksInTheRequest(t *testing.T) {
	var n atomic.Int32
	_, res, err := Within(context.Background(), nil, "k", "v1", time.Minute, time.Second, time.Minute, nil, counted(&n, "a"))
	require.NoError(t, err)
	assert.Equal(t, Bypassed, res.Source)
	_, res, err = Within(context.Background(), New(1<<10, 4), "k", "v1", 0, time.Second, time.Minute, nil, counted(&n, "a"))
	require.NoError(t, err)
	assert.Equal(t, Bypassed, res.Source)
	assert.EqualValues(t, 2, n.Load())
}
