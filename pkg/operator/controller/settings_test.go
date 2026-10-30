package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/cylonchau/kube-resource-fcst/pkg/timing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// settingsPlatform serves GET /operator/settings with whatever the test sets, and can be slow or down.
type settingsPlatform struct {
	mu       sync.Mutex
	settings map[string]any
	down     bool
	delay    time.Duration
	asked    int
}

func (p *settingsPlatform) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/operator/settings", func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		p.asked++
		down, delay, settings := p.down, p.delay, p.settings
		p.mu.Unlock()
		if delay > 0 {
			select {
			case <-time.After(delay):
			case <-r.Context().Done():
				return
			}
		}
		if down {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 10000, "msg": "success", "data": settings})
	})
	return mux
}

func (p *settingsPlatform) set(settings map[string]any, down bool, delay time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.settings, p.down, p.delay = settings, down, delay
}

func refresher(t *testing.T, p *settingsPlatform, initial timing.OperatorSettings) *SettingsRefresher {
	t.Helper()
	srv := httptest.NewServer(p.handler())
	t.Cleanup(srv.Close)
	store := NewSettingsStore(initial)
	platform := &PlatformDecider{Endpoint: &PlatformEndpoint{URL: srv.URL, Credential: "ak:sk", Client: srv.Client()}, Settings: store}
	return &SettingsRefresher{Store: store, Platform: platform}
}

// The operator starts on its own values, takes the platform's once it answers, and goes on with them when the platform is gone.
func TestTheOperatorTakesItsTimeLimitsFromThePlatformAndKeepsTheLastWhenItIsGone(t *testing.T) {
	p := &settingsPlatform{}
	initial := timing.DefaultOperatorSettings()
	initial.SyncIntervalSeconds = 45 // what the flag said
	r := refresher(t, p, initial)
	ctx := context.Background()
	assert.Equal(t, 45*time.Second, r.Store.SyncInterval(), "before the first answer: the starting value")
	assert.True(t, r.Store.FetchedAt().IsZero())

	p.set(map[string]any{"request_timeout_seconds": 20, "sync_interval_seconds": 15, "decision_cache_ttl_seconds": 120, "module_cache_ttl_seconds": 900,
		"recover_answers": 4, "settings_refresh_seconds": 30, "degrade_hold_seconds": 120, "degrade_cooldown_seconds": 90, "recover_stable_seconds": 240}, false, 0)
	r.Once(ctx)
	assert.Equal(t, 20*time.Second, r.Store.RequestTimeout())
	assert.Equal(t, 15*time.Second, r.Store.SyncInterval())
	assert.Equal(t, 30*time.Second, r.Store.RefreshInterval())
	assert.Equal(t, CachePolicy{TTL: 2 * time.Minute, ConfigTTL: 15 * time.Minute, RecoverSuccesses: 4,
		DegradeHold: 2 * time.Minute, DegradeCooldown: 90 * time.Second, RecoverStable: 4 * time.Minute}, r.Store.CachePolicy())
	assert.False(t, r.Store.FetchedAt().IsZero())

	// the platform is gone: the last answer stands
	p.set(nil, true, 0)
	r.Once(ctx)
	assert.Equal(t, 15*time.Second, r.Store.SyncInterval())
	assert.Equal(t, 20*time.Second, r.Store.RequestTimeout())

	// it comes back with other values: they take over
	p.set(map[string]any{"request_timeout_seconds": 5, "sync_interval_seconds": 60, "decision_cache_ttl_seconds": 0, "module_cache_ttl_seconds": 900,
		"recover_answers": 1, "settings_refresh_seconds": 10}, false, 0)
	r.Once(ctx)
	assert.Equal(t, 5*time.Second, r.Store.RequestTimeout())
	assert.Equal(t, 60*time.Second, r.Store.SyncInterval())
	assert.Zero(t, r.Store.CachePolicy().TTL, "a cache lifetime of 0 turns the cache off")
}

// An answer outside the range, or missing a field, never leaves the operator on a limit nobody could have meant.
func TestSettingsOutsideTheRangeAreBroughtInAndAMissingFieldKeepsTheValueInForce(t *testing.T) {
	p := &settingsPlatform{}
	r := refresher(t, p, timing.DefaultOperatorSettings())
	p.set(map[string]any{"request_timeout_seconds": 100000, "sync_interval_seconds": 0, "recover_answers": 0}, false, 0)
	r.Once(context.Background())
	got := r.Store.Get()
	assert.Equal(t, timing.OperatorRequestTimeout.Max, got.RequestTimeoutSeconds)
	assert.Equal(t, timing.OperatorSyncInterval.Min, got.SyncIntervalSeconds)
	assert.Equal(t, timing.OperatorRecoverAnswers.Min, got.RecoverAnswers)
	assert.Equal(t, timing.DefaultOperatorSettings().ModuleCacheTTLSeconds, got.ModuleCacheTTLSeconds, "not sent (an older platform): the value in force stays, not zero")
}

// The refresher asks again by itself, at the interval the settings give, and a change reaches it without a restart.
func TestTheRefresherAsksAgainAtTheIntervalOfTheSettings(t *testing.T) {
	p := &settingsPlatform{}
	p.set(map[string]any{"settings_refresh_seconds": 10, "sync_interval_seconds": 20}, false, 0)
	r := refresher(t, p, timing.DefaultOperatorSettings())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = r.Start(ctx) }()

	require.Eventually(t, func() bool { return r.Store.SyncInterval() == 20*time.Second }, 5*time.Second, 10*time.Millisecond, "asked at start")
	p.mu.Lock()
	first := p.asked
	p.mu.Unlock()
	assert.Equal(t, 1, first)
	assert.Equal(t, 10*time.Second, r.Store.RefreshInterval(), "it will ask again in 10 seconds, the setting, not a number of its own")
	cancel()
	<-done
}

// How long one request may take is the setting: a platform slower than that counts as unreachable, so the operator goes on with what it
// remembers; with a longer limit the same answer is read.
func TestARequestIsBoundedByTheRequestTimeoutOfTheSettings(t *testing.T) {
	p := &settingsPlatform{}
	p.set(map[string]any{"request_timeout_seconds": 1}, false, 1500*time.Millisecond)
	initial := timing.DefaultOperatorSettings()
	initial.RequestTimeoutSeconds = 1
	r := refresher(t, p, initial)
	started := time.Now()
	r.Once(context.Background())
	assert.Less(t, time.Since(started), 1400*time.Millisecond, "cut at the limit, not after the slow answer")
	assert.True(t, r.Store.FetchedAt().IsZero(), "no answer was read")

	r.Store.Set(timing.OperatorSettings{RequestTimeoutSeconds: 5, SyncIntervalSeconds: 30, DecisionCacheTTLSeconds: 600, ModuleCacheTTLSeconds: 3600, RecoverAnswers: 2, SettingsRefreshSeconds: 60})
	r.Once(context.Background())
	assert.False(t, r.Store.FetchedAt().IsZero(), "with a longer limit the same answer is read")
}

// A cache policy that comes from the settings is read at each use: the cache is switched off, and on, while the operator runs.
func TestTheCachePolicyFollowsTheSettingsWhileTheOperatorRuns(t *testing.T) {
	store := NewSettingsStore(timing.DefaultOperatorSettings())
	cache := ModuleCache{Source: store.CachePolicy}
	cache.Remember([]PlatformModule{apiModule()})
	_, _, ok := cache.Stand(unreachable(assert.AnError))
	assert.True(t, ok, "the cache stands in while it is on")

	changed := store.Get()
	changed.DecisionCacheTTLSeconds = 0
	store.Set(changed)
	_, _, ok = cache.Stand(unreachable(assert.AnError))
	assert.False(t, ok, "switched off on the platform: no longer used, with no restart")
}

// The sync round follows the interval of the settings, and the flag value stands without them.
func TestTheSyncIntervalFollowsTheSettings(t *testing.T) {
	assert.Equal(t, 40*time.Second, (&ModuleSync{Interval: 40 * time.Second}).interval(), "without settings: the interval it was given")
	assert.Equal(t, defaultEvaluationInterval, (&ModuleSync{Interval: time.Second}).interval(), "never faster than the least it allows")
	store := NewSettingsStore(timing.DefaultOperatorSettings())
	s := &ModuleSync{Interval: 40 * time.Second, Settings: store}
	assert.Equal(t, 30*time.Second, s.interval())
	changed := store.Get()
	changed.SyncIntervalSeconds = 90
	store.Set(changed)
	assert.Equal(t, 90*time.Second, s.interval(), "the platform changed it: the next round waits as long")
}

// The defaults of the cache in the operator are the defaults of the parameters: one number, not two.
func TestTheOperatorsDefaultsAreTheDefaultsOfTheParameters(t *testing.T) {
	d := timing.DefaultOperatorSettings()
	assert.Equal(t, DefaultCacheTTL, timing.Seconds(d.DecisionCacheTTLSeconds))
	assert.Equal(t, DefaultConfigTTL, timing.Seconds(d.ModuleCacheTTLSeconds))
	assert.Equal(t, DefaultRecoverSuccesses, d.RecoverAnswers)
	assert.Equal(t, defaultEvaluationInterval, timing.Seconds(d.SyncIntervalSeconds))
}
