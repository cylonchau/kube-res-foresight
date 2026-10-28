package controller

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/cylonchau/kube-resource-fcst/pkg/operator/api/v1alpha1"
)

// This file is the operator's local memory of what the platform last said, so that an unreachable platform does not throw the
// workload from the forecast-driven replica count to the default one (which can be far from it) and back.
//
// The rules, in the order they apply as an outage goes on and ends:
//
//  1. Platform answers: the answer is used and remembered (the decision and the list of modules, with the time).
//  2. Platform cannot be reached (connection error, timeout, a status other than 200, an unreadable or error answer): the remembered
//     decision stands in for it for at most CachePolicy.TTL after the last answer. While it does, the replicas may still rise to it but
//     never fall (what the platform said last is stale, and shrinking on stale data is the risk), an urgent flag is not carried over (it
//     says the load is above the forecast *now*), and the decision reports itself as cached with its age.
//  3. The cache is older than the TTL, or there is none: the decision is unavailable and MakePlan applies the safe degradation it always
//     had: the fallback replicas (never below the current count), else the current replicas frozen. The module list (bounds, pace,
//     fallback) is configuration, not a forecast: it is remembered longer (CachePolicy.ConfigTTL), so this degradation can be applied
//     with the module's own bounds. When even that is too old, or was never read, nothing is evaluated and the replicas stay.
//  4. Platform answers again: the first RecoverSuccesses answers are "recovering": scale-ups follow them at once, scale-downs wait, so
//     one answer of a platform that has just come back (maybe from a restart with little history) does not shrink the workload. The
//     step and the cooldown of the module still pace every change. After that the platform's answer alone decides again.
//
// A platform that answers "no decision" (bypass, module off, nothing usable) has answered: its answer outranks the cache, which is
// dropped. Only the failure to get an answer is bridged.

// CachePolicy sets how long and how the remembered answers stand in for the platform.
type CachePolicy struct {
	// TTL is how long after the last answer of the platform the remembered decision (and module list) may be used. 0 turns the cache off.
	TTL time.Duration
	// ConfigTTL is how long the remembered list of modules (their bounds, pace and fallback) may be used; 0 takes TTL. It is longer than TTL
	// because the safe degradation after the decision has expired needs the bounds and the fallback of the module.
	ConfigTTL time.Duration
	// RecoverSuccesses is how many answers in a row the platform must give after an outage before a scale-down is allowed again;
	// 1 or less makes the first answer count in full.
	RecoverSuccesses int
}

// Defaults of the cache: ten minutes covers a platform restart or a short network fault and is shorter than a decision stays useful for
// the workloads that follow a quarter-hour forecast; two answers in a row after an outage.
const (
	DefaultCacheTTL         = 10 * time.Minute
	DefaultConfigTTL        = time.Hour
	DefaultRecoverSuccesses = 2
)

// DefaultCachePolicy is the policy the operator starts with.
func DefaultCachePolicy() CachePolicy {
	return CachePolicy{TTL: DefaultCacheTTL, ConfigTTL: DefaultConfigTTL, RecoverSuccesses: DefaultRecoverSuccesses}
}

// Decision sources of the cache, for the metrics and the status.
const (
	DecisionFromPlatform   = "platform"
	DecisionFromCache      = "cache"
	DecisionCacheExpired   = "expired"
	DecisionNoCache        = "unreachable"
	DecisionFromRecovering = "recovering"
)

// unreachableError marks a failure to get an answer from the platform, as opposed to an answer that says there is no decision.
type unreachableError struct{ err error }

func (e unreachableError) Error() string { return e.err.Error() }
func (e unreachableError) Unwrap() error { return e.err }

func unreachable(err error) error {
	if err == nil {
		return nil
	}
	return unreachableError{err: err}
}

// IsUnreachable reports whether err is a failure to get an answer from the platform.
func IsUnreachable(err error) bool {
	var u unreachableError
	return errors.As(err, &u)
}

type cacheEntry struct {
	decision Decision
	at       time.Time
	// outage is true from the first failure until the next answer; streak counts the answers since the outage ended.
	outage     bool
	recovering bool
	streak     int
}

// CachingDecider remembers what the inner decider answered and bridges a platform that cannot be reached (see the rules above).
type CachingDecider struct {
	Inner   Decider
	Policy  CachePolicy
	Metrics *Metrics
	// Now is the clock; nil uses the wall clock.
	Now func() time.Time

	mu      sync.Mutex
	entries map[string]*cacheEntry
}

func (c *CachingDecider) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func cacheKey(ps *v1alpha1.PredictiveScaler) string { return ps.Namespace + "/" + ps.Name }

// Decide implements Decider.
func (c *CachingDecider) Decide(ctx context.Context, ps *v1alpha1.PredictiveScaler) (*Decision, error) {
	d, err := c.Inner.Decide(ctx, ps)
	if c.Policy.TTL <= 0 {
		return d, err
	}
	now, key, module := c.now(), cacheKey(ps), moduleLabel(ps)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = map[string]*cacheEntry{}
	}
	e := c.entries[key]

	switch {
	case err == nil:
		if e == nil {
			e = &cacheEntry{}
			c.entries[key] = e
		}
		if e.outage {
			e.outage, e.recovering, e.streak = false, c.Policy.RecoverSuccesses > 1, 0
		}
		if e.recovering {
			e.streak++
			if e.streak >= c.Policy.RecoverSuccesses {
				e.recovering = false
			}
		}
		e.decision, e.at = *d, now
		out := *d
		out.Recovering = e.recovering
		if out.Recovering {
			out.Reason += fmt.Sprintf("; the platform is back (answer %d of %d): scale-down waits", e.streak, c.Policy.RecoverSuccesses)
			c.Metrics.recordDecisionSource(module, DecisionFromRecovering, 0)
		} else {
			c.Metrics.recordDecisionSource(module, DecisionFromPlatform, 0)
		}
		return &out, nil

	case !IsUnreachable(err):
		// the platform answered that it has no decision: that outranks anything remembered
		delete(c.entries, key)
		return d, err
	}

	// the platform could not be reached
	if e == nil {
		c.Metrics.recordDecisionSource(module, DecisionNoCache, 0)
		return nil, fmt.Errorf("%w; no decision is remembered", err)
	}
	e.outage, e.recovering, e.streak = true, false, 0
	age := now.Sub(e.at)
	if age > c.Policy.TTL {
		c.Metrics.recordDecisionSource(module, DecisionCacheExpired, age)
		return nil, fmt.Errorf("%w; the remembered decision is %s old, past the %s it may stand in", err, age.Round(time.Second), c.Policy.TTL)
	}
	out := e.decision
	out.Cached, out.CacheAge = true, age
	out.Urgent = false // "the load is above the forecast now" is not true of a stale answer
	until := e.at.Add(c.Policy.TTL)
	out.ValidUntil = &until
	out.Source = "cache:" + out.Source
	out.Reason = fmt.Sprintf("platform unreachable (%v); the decision of %s ago stands in until %s after the last answer: %s",
		err, age.Round(time.Second), c.Policy.TTL, out.Reason)
	c.Metrics.recordDecisionSource(module, DecisionFromCache, age)
	return &out, nil
}

// ModuleCache remembers the list of modules the platform last gave (the bounds, pace and fallback of each), so a sync round that cannot
// reach the platform keeps scaling with the configuration it last had, for at most the TTL.
type ModuleCache struct {
	Policy CachePolicy
	Now    func() time.Time

	mu      sync.Mutex
	modules []PlatformModule
	at      time.Time
	have    bool
}

func (m *ModuleCache) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

// Remember stores the list the platform just gave.
func (m *ModuleCache) Remember(modules []PlatformModule) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.modules, m.at, m.have = append([]PlatformModule(nil), modules...), m.now(), true
}

// Stand returns the remembered list when err is a failure to reach the platform and the list is no older than the config TTL; the age is
// returned with it. Otherwise ok is false and the caller leaves the replicas as they are.
func (m *ModuleCache) Stand(err error) (modules []PlatformModule, age time.Duration, ok bool) {
	if m.Policy.TTL <= 0 || !IsUnreachable(err) {
		return nil, 0, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.have {
		return nil, 0, false
	}
	ttl := m.Policy.ConfigTTL
	if ttl <= 0 {
		ttl = m.Policy.TTL
	}
	age = m.now().Sub(m.at)
	if age > ttl {
		return nil, age, false
	}
	return append([]PlatformModule(nil), m.modules...), age, true
}
