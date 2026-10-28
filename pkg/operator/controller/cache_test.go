package controller

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/cylonchau/kube-resource-fcst/pkg/operator/api/v1alpha1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// scriptedDecider answers from a script: a decision, an error that is a failure to reach the platform, or an error that is an answer.
type scriptedDecider struct {
	decision *Decision
	err      error
}

func (s *scriptedDecider) Decide(context.Context, *v1alpha1.PredictiveScaler) (*Decision, error) {
	if s.err != nil {
		return nil, s.err
	}
	d := *s.decision
	return &d, nil
}

func cachedScaler() *v1alpha1.PredictiveScaler {
	return &v1alpha1.PredictiveScaler{ObjectMeta: metav1.ObjectMeta{Name: "module-api", Namespace: "prod"},
		Spec: v1alpha1.PredictiveScalerSpec{Decision: v1alpha1.Decision{PlatformAPI: &v1alpha1.PlatformAPIDecision{Module: "api"}}}}
}

var errDown = unreachable(errors.New("connection refused"))

func TestAnUnreachablePlatformIsBridgedByTheLastAnswerForTheTTLOnly(t *testing.T) {
	clock := now
	inner := &scriptedDecider{decision: &Decision{Replicas: 6, Source: "platform:algorithm", Urgent: true, Reason: "forecast", Version: 7}}
	c := &CachingDecider{Inner: inner, Policy: CachePolicy{TTL: 10 * time.Minute, RecoverSuccesses: 2}, Now: func() time.Time { return clock }}
	ps := cachedScaler()

	d, err := c.Decide(context.Background(), ps)
	require.NoError(t, err)
	assert.False(t, d.Cached)
	assert.True(t, d.Urgent, "an answer is passed on as it is")

	// the platform goes away: the last answer stands in, without the urgent flag, valid until the TTL after the last answer
	inner.err = errDown
	clock = now.Add(3 * time.Minute)
	d, err = c.Decide(context.Background(), ps)
	require.NoError(t, err)
	assert.True(t, d.Cached)
	assert.EqualValues(t, 6, d.Replicas)
	assert.False(t, d.Urgent, "the load is above the forecast *now* is not true of a stale answer")
	assert.Equal(t, 3*time.Minute, d.CacheAge)
	assert.Equal(t, now.Add(10*time.Minute), *d.ValidUntil, "the TTL counts from the last answer, not from the first failure")
	assert.Equal(t, "cache:platform:algorithm", d.Source)
	assert.Contains(t, d.Reason, "connection refused")

	// past the TTL the cache no longer stands in: the decision is unavailable and MakePlan degrades safely
	clock = now.Add(10*time.Minute + time.Second)
	d, err = c.Decide(context.Background(), ps)
	assert.Nil(t, d)
	require.Error(t, err)
	assert.True(t, IsUnreachable(err))
	assert.Contains(t, err.Error(), "past the 10m0s")
}

func TestWithNothingRememberedAnUnreachablePlatformGivesNoDecisionAndAnAnswerOfNoDecisionDropsTheMemory(t *testing.T) {
	clock := now
	inner := &scriptedDecider{err: errDown}
	c := &CachingDecider{Inner: inner, Policy: DefaultCachePolicy(), Now: func() time.Time { return clock }}
	_, err := c.Decide(context.Background(), cachedScaler())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no decision is remembered")

	// a decision, then the platform answers that it has none (bypass, module off): that is an answer, and outranks the memory
	inner.err, inner.decision = nil, &Decision{Replicas: 5, Source: "platform:algorithm"}
	_, err = c.Decide(context.Background(), cachedScaler())
	require.NoError(t, err)
	inner.err = errors.New("platform has no decision: bypass") // not wrapped: the platform answered
	_, err = c.Decide(context.Background(), cachedScaler())
	require.Error(t, err)
	assert.False(t, IsUnreachable(err))
	inner.err = errDown
	_, err = c.Decide(context.Background(), cachedScaler())
	require.Error(t, err, "the decision from before the bypass is not brought back")
}

func TestAfterAnOutageTheFirstAnswersRaiseButDoNotShrinkUntilEnoughHaveComeInARow(t *testing.T) {
	clock := now
	inner := &scriptedDecider{decision: &Decision{Replicas: 5, Source: "platform:algorithm"}}
	c := &CachingDecider{Inner: inner, Policy: CachePolicy{TTL: 10 * time.Minute, RecoverSuccesses: 3}, Now: func() time.Time { return clock }}
	ps := cachedScaler()
	_, err := c.Decide(context.Background(), ps)
	require.NoError(t, err)

	inner.err = errDown
	_, _ = c.Decide(context.Background(), ps)
	inner.err = nil
	for i, recovering := range []bool{true, true, false, false} {
		d, err := c.Decide(context.Background(), ps)
		require.NoError(t, err)
		assert.Equal(t, recovering, d.Recovering, "answer %d after the outage", i+1)
	}

	// a second outage starts the count again; an outage that is bridged and ends with one answer is recovering as well
	inner.err = errDown
	_, _ = c.Decide(context.Background(), ps)
	inner.err = nil
	d, err := c.Decide(context.Background(), ps)
	require.NoError(t, err)
	assert.True(t, d.Recovering)
	assert.Contains(t, d.Reason, "scale-down waits")

	// RecoverSuccesses of 1 means the first answer counts in full
	one := &CachingDecider{Inner: inner, Policy: CachePolicy{TTL: time.Minute, RecoverSuccesses: 1}, Now: func() time.Time { return clock }}
	_, _ = one.Decide(context.Background(), ps)
	inner.err = errDown
	_, _ = one.Decide(context.Background(), ps)
	inner.err = nil
	d, _ = one.Decide(context.Background(), ps)
	assert.False(t, d.Recovering)

	// the cache switched off passes everything through
	off := &CachingDecider{Inner: &scriptedDecider{err: errDown}, Policy: CachePolicy{}}
	_, err = off.Decide(context.Background(), ps)
	assert.Error(t, err)
}

func TestARememberedOrRecoveringDecisionRaisesTheReplicasButNeverLowersThem(t *testing.T) {
	until := now.Add(5 * time.Minute)
	cached := func(replicas int32) *Decision {
		return &Decision{Replicas: replicas, Source: "cache:platform:algorithm", ValidUntil: &until, Cached: true, CacheAge: 4 * time.Minute}
	}
	up := MakePlan(in(4, 4, cached(8)))
	assert.Equal(t, ActionUp, up.Action)
	assert.EqualValues(t, 8, up.Desired)
	assert.Equal(t, "cache", up.UsedSource)
	assert.True(t, up.DecisionValid)
	assert.Contains(t, up.Reason, "platform unreachable")

	down := MakePlan(in(8, 8, cached(4)))
	assert.Equal(t, ActionNone, down.Action, "a remembered answer is stale: it does not shrink the workload")
	assert.EqualValues(t, 8, down.Desired)
	assert.Contains(t, down.Reason, "never done on a remembered decision")

	recovering := fresh(4)
	recovering.Recovering = true
	held := MakePlan(in(8, 8, recovering))
	assert.Equal(t, ActionNone, held.Action)
	assert.Contains(t, held.Reason, "waits until the platform has answered again")
	raised := fresh(12)
	raised.Recovering = true
	assert.Equal(t, ActionUp, MakePlan(in(8, 8, raised)).Action, "scaling up follows at once")

	// without the urgent flag the cooldown of the module applies to a remembered scale-up
	pi := in(4, 4, cached(8))
	pi.Spec.Behavior.ScaleUp = v1alpha1.ScaleRule{Cooldown: &metav1.Duration{Duration: 5 * time.Minute}}
	last := now.Add(-time.Minute)
	pi.LastScale = &last
	assert.Equal(t, ActionNone, MakePlan(pi).Action)
}

func TestTheModuleListIsRememberedForTheTTLAndOnlyForAnUnreachablePlatform(t *testing.T) {
	clock := now
	c := ModuleCache{Policy: CachePolicy{TTL: 10 * time.Minute, ConfigTTL: time.Hour}, Now: func() time.Time { return clock }}
	_, _, ok := c.Stand(errDown)
	assert.False(t, ok, "nothing remembered yet")
	c.Remember([]PlatformModule{apiModule()})
	clock = now.Add(4 * time.Minute)
	got, age, ok := c.Stand(errDown)
	require.True(t, ok)
	assert.Len(t, got, 1)
	assert.Equal(t, 4*time.Minute, age)
	_, _, ok = c.Stand(errors.New("platform error 10001: forbidden"))
	assert.False(t, ok, "an answer that is an error is not bridged")
	clock = now.Add(11 * time.Minute)
	_, _, ok = c.Stand(errDown)
	assert.True(t, ok, "the module list outlives the decision: the safe degradation needs the bounds and the fallback")
	clock = now.Add(61 * time.Minute)
	_, _, ok = c.Stand(errDown)
	assert.False(t, ok, "past the config TTL")
	same := ModuleCache{Policy: CachePolicy{TTL: 10 * time.Minute}, Now: func() time.Time { return clock }}
	same.Remember([]PlatformModule{apiModule()})
	clock = clock.Add(11 * time.Minute)
	_, _, ok = same.Stand(errDown)
	assert.False(t, ok, "no config TTL: the decision TTL applies")
	off := ModuleCache{}
	off.Remember([]PlatformModule{apiModule()})
	_, _, ok = off.Stand(errDown)
	assert.False(t, ok, "the cache is off")
}

// The whole of an outage on the module sync: the workload holds the forecast-driven count while the platform is away, does not jump to the
// (far higher) fallback, goes to the safe degradation once the memory is too old, and shrinks again only after the platform has answered
// twice.
func TestAnOutageOfThePlatformDoesNotThrowTheWorkloadToTheFallbackAndBack(t *testing.T) {
	clock := now
	p := &fakePlatform{modules: []PlatformModule{apiModule()}, replicas: 4}
	p.modules[0].FallbackReplicas = 9
	p.modules[0].ScaleUpMaxStep = 3
	srv := httptest.NewServer(p.handler())
	t.Cleanup(srv.Close)
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(deployment(4, 4)).Build()
	platform := &PlatformDecider{Reader: c, Endpoint: &PlatformEndpoint{URL: srv.URL, Credential: "ak:sk", Client: srv.Client()}}
	policy := CachePolicy{TTL: 10 * time.Minute, ConfigTTL: time.Hour, RecoverSuccesses: 2}
	cached := &CachingDecider{Inner: platform, Policy: policy, Now: func() time.Time { return clock }}
	rec := &Reconciler{Client: c, Scheme: testScheme(t), Reporter: platform, NoCRD: true, Now: func() time.Time { return clock },
		Deciders: map[v1alpha1.DecisionSource]Decider{v1alpha1.SourcePlatformAPI: cached}}
	s := &ModuleSync{Reconciler: rec, Platform: platform, Cache: ModuleCache{Policy: policy, Now: func() time.Time { return clock }}}
	round := func(advance time.Duration) int32 {
		clock = clock.Add(advance)
		s.Once(context.Background())
		return replicasOf(t, c)
	}
	setDown := func(down bool, replicas int32) {
		p.mu.Lock()
		p.down, p.replicas = down, replicas
		p.validUntil = clock.Add(10 * time.Minute) // a decision is valid for ten minutes from when it is given
		p.mu.Unlock()
	}

	assert.EqualValues(t, 4, round(0), "the platform asks for 4")
	setDown(true, 4)
	assert.EqualValues(t, 4, round(2*time.Minute), "unreachable: the remembered 4 holds, not the fallback of 9")
	assert.EqualValues(t, 4, round(5*time.Minute), "still within the 10 minutes of the last answer")
	assert.EqualValues(t, 7, round(4*time.Minute), "the memory is too old: the safe degradation, the fallback of 9 paced by the step of 3")

	// even the module list has a limit: past it nothing is evaluated and the replicas stay where they are
	assert.EqualValues(t, 7, round(time.Hour), "the module list is too old too: the workload is left alone")

	// the new Pods have come up (the fake cluster does not start them): a scale-down is held while not every replica is Ready
	var dep appsv1.Deployment
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Namespace: "prod", Name: "api"}, &dep))
	dep.Status.ReadyReplicas = *dep.Spec.Replicas
	require.NoError(t, c.Status().Update(context.Background(), &dep))

	// the platform is back and asks for 5: the first answer cannot shrink the workload, the second can
	setDown(false, 5)
	assert.EqualValues(t, 7, round(time.Minute), "first answer after the outage: scale-down waits")
	assert.EqualValues(t, 5, round(time.Minute), "second answer: the platform decides again")

}
