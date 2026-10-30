package controller

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// This file is the operator's local memory of what the platform last said, so that an unreachable platform does not throw the
// workload from the forecast-driven replica count to the default one (which can be far from it) and back.
//
// A module goes through these states as an outage of the platform goes on and ends. The state is kept per module, in memory; the
// operator reads the times it uses from the platform's settings (the system parameters operator.*), so an administrator tunes them
// without a new image.
//
//	normal      The platform answers. The answer is used and remembered (the decision, with the time).
//	cached      The platform cannot be reached (connection error, timeout, a status other than 200, an unreadable or error answer) and the
//	            remembered decision is no older than CachePolicy.TTL. It stands in for the platform: the replicas may still rise to it but
//	            never fall (it is stale, and shrinking on stale data is the risk); an urgent flag is not carried over (it says the load is
//	            above the forecast *now*).
//	holding     The platform cannot be reached and nothing usable is remembered (the decision is older than the TTL, or there is none, as
//	            after a restart in the middle of an outage). The replicas are kept exactly as they are, no fallback, for
//	            CachePolicy.DegradeHold, counted from the moment the cache ran out (or from the first failure when there was none). A short
//	            outage ends here without any change to the workload.
//	degraded    The hold is over and the platform is still away. The module's fallback replicas may now be applied, never below the current
//	            count: at most once in CachePolicy.DegradeCooldown and at most the module's step per action, so a long outage raises the
//	            workload in steps, not in one jump. With no fallback configured the replicas stay.
//	recovering  The platform answers again. Scale-ups follow its answers at once; scale-downs wait until it has answered
//	            CachePolicy.RecoverSuccesses times in a row and CachePolicy.RecoverStable has passed since the first of those answers, so one
//	            answer of a platform that has just come back (maybe from a restart with little history) does not shrink the workload,
//	            and neither is a fallback that was applied during the outage undone at once. Then the state is normal again.
//
// The module list (bounds, pace, fallback) is configuration, not a forecast: it is remembered longer (CachePolicy.ConfigTTL), so the
// degradation can be applied with the module's own bounds. When even that is too old, or was never read, nothing is evaluated and the
// replicas stay.
//
// A platform that answers "no decision" (bypass, module off, nothing usable) has answered: its answer outranks the cache, which is
// dropped, and the module's own rules (fallback included) apply as they always did. Only the failure to get an answer is bridged.

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
	// DegradeHold is how long the replicas are kept as they are once nothing usable is remembered (state "holding"); 0 goes on to the
	// fallback at once.
	DegradeHold time.Duration
	// DegradeCooldown is the least time between two applications of the fallback in state "degraded"; 0 leaves the pace to the module.
	DegradeCooldown time.Duration
	// RecoverStable is how long after the first answer of a platform that is back a scale-down stays held, besides RecoverSuccesses.
	RecoverStable time.Duration
}

// Defaults of the cache: ten minutes covers a platform restart or a short network fault and is shorter than a decision stays useful for
// the workloads that follow a quarter-hour forecast; two answers in a row after an outage.
const (
	DefaultCacheTTL         = 10 * time.Minute
	DefaultConfigTTL        = time.Hour
	DefaultRecoverSuccesses = 2
	DefaultDegradeHold      = 5 * time.Minute
	DefaultDegradeCooldown  = 5 * time.Minute
	DefaultRecoverStable    = 5 * time.Minute
)

// DefaultCachePolicy is the policy the operator starts with.
func DefaultCachePolicy() CachePolicy {
	return CachePolicy{TTL: DefaultCacheTTL, ConfigTTL: DefaultConfigTTL, RecoverSuccesses: DefaultRecoverSuccesses,
		DegradeHold: DefaultDegradeHold, DegradeCooldown: DefaultDegradeCooldown, RecoverStable: DefaultRecoverStable}
}

// Decision sources of the cache, for the metrics and the status; they follow the states above ("expired" and "unreachable" are kept
// for the two ways a hold can begin, and are no longer used by themselves).
const (
	DecisionFromPlatform   = "platform"
	DecisionFromCache      = "cache"
	DecisionCacheExpired   = "expired"
	DecisionNoCache        = "unreachable"
	DecisionFromRecovering = "recovering"
	DecisionHolding        = "holding"
	DecisionDegraded       = "degraded"
)

// The states of a module as an outage of the platform goes on and ends (see the top of this file).
const (
	StateNormal     = "normal"
	StateCached     = "cached"
	StateHolding    = "holding"
	StateDegraded   = "degraded"
	StateRecovering = "recovering"
)

// DegradedError is the failure to get a decision while the platform cannot be reached and nothing usable is remembered. It says which
// state the module is in (holding or degraded) and the pace of the fallback; it still reports IsUnreachable.
type DegradedError struct {
	State string
	// Cooldown is CachePolicy.DegradeCooldown at the time of the answer.
	Cooldown time.Duration
	Err      error
}

func (e *DegradedError) Error() string { return e.Err.Error() }
func (e *DegradedError) Unwrap() error { return e.Err }

// DegradeStateOf is the degradation state an error carries: StateHolding, StateDegraded, or "" for any other error.
func DegradeStateOf(err error) (state string, cooldown time.Duration) {
	var d *DegradedError
	if errors.As(err, &d) {
		return d.State, d.Cooldown
	}
	return "", 0
}

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
	// have is true once the platform has given a decision; at is when.
	have bool
	at   time.Time
	// outage is true from the first failure until the next answer, outageSince the time of that first failure.
	outage      bool
	outageSince time.Time
	// recovering is true from the first answer after an outage until the platform has answered enough times and long enough;
	// recoverFrom is the time of that first answer and streak counts the answers since.
	recovering  bool
	recoverFrom time.Time
	streak      int
}

// CachingDecider remembers what the inner decider answered and bridges a platform that cannot be reached (see the rules above).
type CachingDecider struct {
	Inner  Decider
	Policy CachePolicy
	// Source, when set, gives the policy in force at each use: the platform's settings can change while the operator runs. Policy is
	// what stands until there is a Source.
	Source  func() CachePolicy
	Metrics *Metrics
	// Now is the clock; nil uses the wall clock.
	Now func() time.Time

	mu      sync.Mutex
	entries map[string]*cacheEntry
}

func (c *CachingDecider) policy() CachePolicy {
	if c.Source != nil {
		return c.Source()
	}
	return c.Policy
}

func (c *CachingDecider) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func cacheKey(s *Scaler) string { return s.Namespace + "/" + s.Name }

// Decide implements Decider.
func (c *CachingDecider) Decide(ctx context.Context, s *Scaler) (*Decision, error) {
	d, err := c.Inner.Decide(ctx, s)
	policy := c.policy()
	now, key, module := c.now(), cacheKey(s), moduleLabel(s)
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
			e.outage = false
			e.recovering = policy.RecoverSuccesses > 1 || policy.RecoverStable > 0
			e.recoverFrom, e.streak = now, 0
		}
		if e.recovering {
			e.streak++
			if e.streak >= policy.RecoverSuccesses && now.Sub(e.recoverFrom) >= policy.RecoverStable {
				e.recovering = false
			}
		}
		if policy.TTL > 0 {
			e.decision, e.have, e.at = *d, true, now
		}
		out := *d
		out.Recovering = e.recovering
		if out.Recovering {
			out.Reason += fmt.Sprintf("; the platform is back (answer %d of %d, %s of %s): scale-down waits",
				e.streak, policy.RecoverSuccesses, now.Sub(e.recoverFrom).Round(time.Second), policy.RecoverStable)
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
		e = &cacheEntry{}
		c.entries[key] = e
	}
	if !e.outage {
		e.outage, e.outageSince = true, now
	}
	e.recovering, e.streak = false, 0

	if e.have && policy.TTL > 0 {
		age := now.Sub(e.at)
		// strictly inside the TTL: the decision is valid until e.at+TTL, and MakePlan treats it as expired from that moment on, so a
		// decision returned at exactly the TTL would be thrown to the fallback without the hold
		if age < policy.TTL {
			out := e.decision
			out.Cached, out.CacheAge = true, age
			out.Urgent = false // "the load is above the forecast now" is not true of a stale answer
			until := e.at.Add(policy.TTL)
			out.ValidUntil = &until
			out.Source = "cache:" + out.Source
			out.Reason = fmt.Sprintf("platform unreachable (%v); the decision of %s ago stands in until %s after the last answer: %s",
				err, age.Round(time.Second), policy.TTL, out.Reason)
			c.Metrics.recordDecisionSource(module, DecisionFromCache, age)
			return &out, nil
		}
	}

	// nothing usable is remembered: the hold counts from when the cache ran out, or from the first failure when there was no cache
	from, why := e.outageSince, "no decision is remembered"
	if e.have && policy.TTL > 0 {
		if end := e.at.Add(policy.TTL); end.After(from) {
			from = end
		}
		why = fmt.Sprintf("the remembered decision is %s old, past the %s it may stand in", now.Sub(e.at).Round(time.Second), policy.TTL)
	}
	if held := now.Sub(from); held < policy.DegradeHold {
		c.Metrics.recordDecisionSource(module, DecisionHolding, 0)
		return nil, &DegradedError{State: StateHolding, Cooldown: policy.DegradeCooldown,
			Err: fmt.Errorf("%w; %s: the current replicas are held", err, why)}
	}
	c.Metrics.recordDecisionSource(module, DecisionDegraded, 0)
	return nil, &DegradedError{State: StateDegraded, Cooldown: policy.DegradeCooldown,
		Err: fmt.Errorf("%w; %s, and the %s hold is over", err, why, policy.DegradeHold)}
}

// ModuleCache remembers the list of modules the platform last gave (the bounds, pace and fallback of each), so a sync round that cannot
// reach the platform keeps scaling with the configuration it last had, for at most the TTL.
type ModuleCache struct {
	Policy CachePolicy
	// Source, when set, gives the policy in force at each use (see CachingDecider).
	Source func() CachePolicy
	Now    func() time.Time

	mu      sync.Mutex
	modules []PlatformModule
	at      time.Time
	have    bool
}

func (m *ModuleCache) policy() CachePolicy {
	if m.Source != nil {
		return m.Source()
	}
	return m.Policy
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
	if m.policy().TTL <= 0 || !IsUnreachable(err) {
		return nil, 0, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.have {
		return nil, 0, false
	}
	ttl := m.policy().ConfigTTL
	if ttl <= 0 {
		ttl = m.policy().TTL
	}
	age = m.now().Sub(m.at)
	if age > ttl {
		return nil, age, false
	}
	return append([]PlatformModule(nil), m.modules...), age, true
}
