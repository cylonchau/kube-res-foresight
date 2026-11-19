package resultcache

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newCache(t *testing.T) (*Cache, *clock) {
	t.Helper()
	cl := &clock{t: time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)}
	c := New(1<<20, 100)
	c.SetClock(cl.now)
	return c, cl
}

type report struct {
	Rows []string `json:"rows"`
}

func counted(n *atomic.Int32, rows ...string) func(context.Context) (*report, error) {
	return func(context.Context) (*report, error) {
		n.Add(1)
		return &report{Rows: rows}, nil
	}
}

func TestAnAnswerMadeFromTheSameVersionOfTheDataIsKeptForItsTimeAndNotWorkedOutAgain(t *testing.T) {
	c, cl := newCache(t)
	var n atomic.Int32
	ctx := context.Background()

	first, res, err := Do(ctx, c, "effect|1|24", "v1", 30*time.Minute, counted(&n, "a"))
	require.NoError(t, err)
	assert.Equal(t, Computed, res.Source)
	assert.Equal(t, []string{"a"}, first.Rows)

	cl.advance(10 * time.Minute)
	again, res, err := Do(ctx, c, "effect|1|24", "v1", 30*time.Minute, counted(&n, "b"))
	require.NoError(t, err)
	assert.Equal(t, Cached, res.Source)
	assert.Equal(t, 10*time.Minute, res.Age)
	assert.Same(t, first, again, "the answer is the one that was kept")
	assert.EqualValues(t, 1, n.Load())

	cl.advance(25 * time.Minute)
	_, res, err = Do(ctx, c, "effect|1|24", "v1", 30*time.Minute, counted(&n, "c"))
	require.NoError(t, err)
	assert.Equal(t, Computed, res.Source, "35 minutes is more than 30: it is made again whatever the data says")
	assert.EqualValues(t, 2, n.Load())

	stats := c.Stats()
	assert.EqualValues(t, 1, stats.Hits)
	assert.EqualValues(t, 2, stats.Misses)
	assert.EqualValues(t, 1, stats.Expired)
	assert.InDelta(t, 1.0/3, stats.HitRate, 1e-9)
}

func TestWhenTheDataChangedTheCachedAnswerIsNeverGivenAndThePresentOneIsKeptInstead(t *testing.T) {
	c, _ := newCache(t)
	var n atomic.Int32
	ctx := context.Background()
	_, _, err := Do(ctx, c, "k", "v1", time.Hour, counted(&n, "old"))
	require.NoError(t, err)
	fresh, res, err := Do(ctx, c, "k", "v2", time.Hour, counted(&n, "new"))
	require.NoError(t, err)
	assert.Equal(t, Computed, res.Source)
	assert.Equal(t, []string{"new"}, fresh.Rows, "a new version is never answered with the old one")
	again, res, err := Do(ctx, c, "k", "v2", time.Hour, counted(&n, "never"))
	require.NoError(t, err)
	assert.Equal(t, Cached, res.Source)
	assert.Equal(t, []string{"new"}, again.Rows)
	// and an older version asked for later is not a hit either: one answer for each key
	stale, res, err := Do(ctx, c, "k", "v1", time.Hour, counted(&n, "again old"))
	require.NoError(t, err)
	assert.Equal(t, Computed, res.Source)
	assert.Equal(t, []string{"again old"}, stale.Rows)
	assert.EqualValues(t, 2, c.Stats().Changed)
}

func TestRequestsThatComeWhileOneIsBeingWorkedOutWaitForItsAnswer(t *testing.T) {
	c, _ := newCache(t)
	var n atomic.Int32
	release := make(chan struct{})
	started := make(chan struct{})
	compute := func(context.Context) (*report, error) {
		if n.Add(1) == 1 {
			close(started)
		}
		<-release
		return &report{Rows: []string{"once"}}, nil
	}
	var wg sync.WaitGroup
	results := make([]Result, 8)
	answers := make([]*report, 8)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			answers[i], results[i], _ = Do(context.Background(), c, "same", "v1", time.Hour, compute)
		}(i)
		if i == 0 {
			<-started // the first is the one that works it out
		}
	}
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()

	assert.EqualValues(t, 1, n.Load(), "eight requests, one piece of work")
	var computed, shared int
	for i, r := range results {
		require.NotNil(t, answers[i])
		assert.Equal(t, []string{"once"}, answers[i].Rows)
		switch r.Source {
		case Computed:
			computed++
		case Shared:
			shared++
		}
	}
	assert.Equal(t, 1, computed)
	assert.Equal(t, 7, shared)
	assert.EqualValues(t, 7, c.Stats().Shared)
}

func TestWhenTheRequestThatWasWorkingItOutIsStoppedTheOtherAsksForItself(t *testing.T) {
	c, _ := newCache(t)
	leaderCtx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	leaderDone := make(chan error, 1)
	go func() {
		_, _, err := Do(leaderCtx, c, "k", "v1", time.Hour, func(ctx context.Context) (*report, error) {
			close(started)
			<-ctx.Done()
			return nil, ctx.Err()
		})
		leaderDone <- err
	}()
	<-started

	var n atomic.Int32
	waiterDone := make(chan *report, 1)
	go func() {
		got, _, _ := Do(context.Background(), c, "k", "v1", time.Hour, counted(&n, "mine"))
		waiterDone <- got
	}()
	time.Sleep(50 * time.Millisecond)
	cancel() // the client of the first request went away

	require.ErrorIs(t, <-leaderDone, context.Canceled)
	select {
	case got := <-waiterDone:
		require.NotNil(t, got)
		assert.Equal(t, []string{"mine"}, got.Rows, "it was not given the error of somebody else's request")
	case <-time.After(5 * time.Second):
		t.Fatal("the waiting request was not answered")
	}
	assert.EqualValues(t, 1, n.Load())
}

func TestAnErrorIsNotKeptAndAWaiterWhoseOwnRequestEndedLeavesAtOnce(t *testing.T) {
	c, _ := newCache(t)
	boom := errors.New("boom")
	_, res, err := Do(context.Background(), c, "k", "v1", time.Hour, func(context.Context) (*report, error) { return nil, boom })
	require.ErrorIs(t, err, boom)
	assert.Equal(t, Computed, res.Source)
	var n atomic.Int32
	ok, res, err := Do(context.Background(), c, "k", "v1", time.Hour, counted(&n, "fine"))
	require.NoError(t, err)
	assert.Equal(t, Computed, res.Source, "the failure was not kept")
	assert.Equal(t, []string{"fine"}, ok.Rows)

	release := make(chan struct{})
	started := make(chan struct{})
	go func() {
		_, _, _ = Do(context.Background(), c, "slow", "v1", time.Hour, func(context.Context) (*report, error) {
			close(started)
			<-release
			return &report{}, nil
		})
	}()
	<-started
	gone, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err = Do(gone, c, "slow", "v1", time.Hour, counted(&n, "x"))
	assert.ErrorIs(t, err, context.Canceled)
	close(release)
}

func TestTheCacheIsBoundedBySizeAndKeepsTheAnswersUsedLast(t *testing.T) {
	c, _ := newCache(t)
	c.maxBytes = 200
	big := func(tag string) func(context.Context) (*report, error) {
		return func(context.Context) (*report, error) { return &report{Rows: []string{strings.Repeat(tag, 60)}}, nil }
	}
	ctx := context.Background()
	for _, k := range []string{"a", "b"} {
		_, _, err := Do(ctx, c, k, "v", time.Hour, big(k))
		require.NoError(t, err)
	}
	_, res, _ := Do(ctx, c, "a", "v", time.Hour, big("a"))
	assert.Equal(t, Cached, res.Source, "a is used: b is the one that goes")
	_, _, err := Do(ctx, c, "c", "v", time.Hour, big("c"))
	require.NoError(t, err)
	_, res, _ = Do(ctx, c, "b", "v", time.Hour, big("b"))
	assert.Equal(t, Computed, res.Source, "the answer used longest ago was dropped")
	_, res, _ = Do(ctx, c, "c", "v", time.Hour, big("c"))
	assert.Equal(t, Cached, res.Source, "the newest answers stay")
	assert.LessOrEqual(t, c.Stats().Bytes, int64(200))
	assert.Positive(t, c.Stats().Evictions)

	_, _, err = Do(ctx, c, "huge", "v", time.Hour, func(context.Context) (*report, error) { return &report{Rows: []string{strings.Repeat("x", 500)}}, nil })
	require.NoError(t, err)
	_, res, _ = Do(ctx, c, "huge", "v", time.Hour, big("h"))
	assert.Equal(t, Computed, res.Source, "an answer bigger than the cache is not kept")
}

func TestWithNoTimeTheCacheIsOffAndWhatIsKeptCanBeForgottenByPrefix(t *testing.T) {
	c, _ := newCache(t)
	var n atomic.Int32
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		_, res, err := Do(ctx, c, "k", "v", 0, counted(&n, "x"))
		require.NoError(t, err)
		assert.Equal(t, Bypassed, res.Source)
	}
	assert.EqualValues(t, 2, n.Load())
	_, res, err := Do[*report](ctx, nil, "k", "v", time.Hour, counted(&n, "x"))
	require.NoError(t, err)
	assert.Equal(t, Bypassed, res.Source, "no cache at all is no cache")

	for _, k := range []string{"effect|1|24", "effect|1|168", "effect|2|24"} {
		_, _, err := Do(ctx, c, k, "v", time.Hour, counted(&n, k))
		require.NoError(t, err)
	}
	c.Forget("effect|1|")
	assert.Equal(t, 1, c.Stats().Entries)
	_, res, _ = Do(ctx, c, "effect|2|24", "v", time.Hour, counted(&n, "x"))
	assert.Equal(t, Cached, res.Source)
}

func TestTwoKindsOfAnswerUnderOneKeyDoNotGetEachOthers(t *testing.T) {
	c, _ := newCache(t)
	ctx := context.Background()
	_, _, err := Do(ctx, c, "k", "v", time.Hour, func(context.Context) (string, error) { return "text", nil })
	require.NoError(t, err)
	got, res, err := Do(ctx, c, "k", "v", time.Hour, func(context.Context) (*report, error) { return &report{Rows: []string{"r"}}, nil })
	require.NoError(t, err)
	assert.Equal(t, []string{"r"}, got.Rows)
	assert.Equal(t, Computed, res.Source)
}
