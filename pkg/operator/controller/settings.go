package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sync/atomic"
	"time"

	"github.com/cylonchau/kube-resource-fcst/pkg/timing"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// SettingsStore holds the time limits the operator runs on. They are system parameters of the platform: the operator asks for them
// (GET /api/v1/operator/settings), keeps the last answer, and asks again every settings_refresh_seconds. Until the first answer, and
// whenever the platform cannot be reached, the last known values stand (the first ones are the command line flags, kept as the
// operator's own starting values). Nothing here is fixed in code: a change made on the platform reaches a running operator.
type SettingsStore struct {
	cur atomic.Pointer[timing.OperatorSettings]
	// fetchedAt is when the platform last answered, the zero time before that.
	fetchedAt atomic.Pointer[time.Time]
}

// NewSettingsStore starts from the given values (kept in range).
func NewSettingsStore(initial timing.OperatorSettings) *SettingsStore {
	s := &SettingsStore{}
	n := initial.Normalize()
	s.cur.Store(&n)
	return s
}

// Get is the settings in force.
func (s *SettingsStore) Get() timing.OperatorSettings { return *s.cur.Load() }

// Set replaces the settings (kept in range) and says whether anything changed.
func (s *SettingsStore) Set(next timing.OperatorSettings) (changed bool) {
	n := next.Normalize()
	prev := s.cur.Swap(&n)
	return *prev != n
}

// FetchedAt is when the platform last answered; the zero time while it never has.
func (s *SettingsStore) FetchedAt() time.Time {
	if t := s.fetchedAt.Load(); t != nil {
		return *t
	}
	return time.Time{}
}

// RequestTimeout is how long the operator waits for one answer of the platform.
func (s *SettingsStore) RequestTimeout() time.Duration {
	return timing.Seconds(s.Get().RequestTimeoutSeconds)
}

// SyncInterval is how often the modules of the platform are read and applied.
func (s *SettingsStore) SyncInterval() time.Duration {
	return timing.Seconds(s.Get().SyncIntervalSeconds)
}

// RefreshInterval is how often the settings are asked for again.
func (s *SettingsStore) RefreshInterval() time.Duration {
	return timing.Seconds(s.Get().SettingsRefreshSeconds)
}

// CachePolicy is the cache policy the settings give.
func (s *SettingsStore) CachePolicy() CachePolicy {
	g := s.Get()
	return CachePolicy{TTL: timing.Seconds(g.DecisionCacheTTLSeconds), ConfigTTL: timing.Seconds(g.ModuleCacheTTLSeconds), RecoverSuccesses: g.RecoverAnswers,
		DegradeHold: timing.Seconds(g.DegradeHoldSeconds), DegradeCooldown: timing.Seconds(g.DegradeCooldownSeconds), RecoverStable: timing.Seconds(g.RecoverStableSeconds)}
}

// SettingsRefresher keeps a SettingsStore up to date. It runs on every replica (the settings are needed by the one that is elected,
// and a replica that becomes the leader should not start from the flags).
type SettingsRefresher struct {
	Store    *SettingsStore
	Platform *PlatformDecider
	// Now is the clock; nil uses the wall clock.
	Now func() time.Time
}

// NeedLeaderElection is false: every replica keeps its own copy.
func (r *SettingsRefresher) NeedLeaderElection() bool { return false }

// Start asks at once, then every refresh interval of the settings in force, until ctx ends.
func (r *SettingsRefresher) Start(ctx context.Context) error {
	for {
		r.Once(ctx)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(r.Store.RefreshInterval()):
		}
	}
}

// Once asks the platform for the settings. A platform that cannot be reached, or that answers something unreadable, leaves the
// settings as they are.
func (r *SettingsRefresher) Once(ctx context.Context) {
	log := logf.FromContext(ctx).WithName("settings")
	next, err := r.fetch(ctx)
	if err != nil {
		log.Info("could not read the settings from the platform: keeping the last known ones", "error", err.Error(),
			"lastAnswer", r.Store.FetchedAt().Format(time.RFC3339))
		return
	}
	now := time.Now()
	if r.Now != nil {
		now = r.Now()
	}
	r.Store.fetchedAt.Store(&now)
	prev := r.Store.Get()
	if r.Store.Set(next) {
		log.Info("the settings of the platform changed", "from", fmt.Sprintf("%+v", prev), "to", fmt.Sprintf("%+v", r.Store.Get()))
	}
}

func (r *SettingsRefresher) fetch(ctx context.Context) (timing.OperatorSettings, error) {
	data, err := r.Platform.do(ctx, http.MethodGet, "/api/v1/operator/settings", url.Values{}, nil)
	if err != nil {
		return timing.OperatorSettings{}, err
	}
	var env struct {
		Code int                     `json:"code"`
		Msg  string                  `json:"msg"`
		Data timing.OperatorSettings `json:"data"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		return timing.OperatorSettings{}, fmt.Errorf("unreadable platform response: %w", err)
	}
	if env.Code != 10000 {
		return timing.OperatorSettings{}, fmt.Errorf("platform error %d: %s", env.Code, env.Msg)
	}
	// a field the platform does not send (an older platform) keeps the value in force, not zero
	merged := r.Store.Get()
	var raw map[string]json.RawMessage
	var wrapper struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	if json.Unmarshal(data, &wrapper) == nil {
		raw = wrapper.Data
	}
	set := func(key string, to *int, v int) {
		if _, ok := raw[key]; ok {
			*to = v
		}
	}
	set("request_timeout_seconds", &merged.RequestTimeoutSeconds, env.Data.RequestTimeoutSeconds)
	set("sync_interval_seconds", &merged.SyncIntervalSeconds, env.Data.SyncIntervalSeconds)
	set("decision_cache_ttl_seconds", &merged.DecisionCacheTTLSeconds, env.Data.DecisionCacheTTLSeconds)
	set("module_cache_ttl_seconds", &merged.ModuleCacheTTLSeconds, env.Data.ModuleCacheTTLSeconds)
	set("recover_answers", &merged.RecoverAnswers, env.Data.RecoverAnswers)
	set("settings_refresh_seconds", &merged.SettingsRefreshSeconds, env.Data.SettingsRefreshSeconds)
	set("degrade_hold_seconds", &merged.DegradeHoldSeconds, env.Data.DegradeHoldSeconds)
	set("degrade_cooldown_seconds", &merged.DegradeCooldownSeconds, env.Data.DegradeCooldownSeconds)
	set("recover_stable_seconds", &merged.RecoverStableSeconds, env.Data.RecoverStableSeconds)
	return merged, nil
}
