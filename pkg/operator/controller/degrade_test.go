package controller

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// degradePolicy is the policy of the outage tests: a ten-minute decision cache, then a five-minute hold, a fallback in steps five minutes
// apart, and after the platform is back two answers and five minutes before a scale-down.
func degradePolicy() CachePolicy {
	return CachePolicy{TTL: 10 * time.Minute, ConfigTTL: 24 * time.Hour, RecoverSuccesses: 2,
		DegradeHold: 5 * time.Minute, DegradeCooldown: 5 * time.Minute, RecoverStable: 5 * time.Minute}
}

func TestWithNothingUsableRememberedTheReplicasAreHeldBeforeAnyFallbackAndTheStateSaysSo(t *testing.T) {
	clock := now
	inner := &scriptedDecider{err: errDown}
	c := &CachingDecider{Inner: inner, Policy: degradePolicy(), Now: func() time.Time { return clock }}
	ps := cachedScaler()

	// the operator has just started in the middle of an outage: nothing is remembered. The hold counts from the first failure.
	_, err := c.Decide(context.Background(), ps)
	state, _ := DegradeStateOf(err)
	assert.Equal(t, StateHolding, state)
	assert.True(t, IsUnreachable(err), "a hold is still a failure to reach the platform")
	clock = now.Add(4*time.Minute + 59*time.Second)
	_, err = c.Decide(context.Background(), ps)
	state, _ = DegradeStateOf(err)
	assert.Equal(t, StateHolding, state, "still inside the hold")
	clock = now.Add(5 * time.Minute)
	_, err = c.Decide(context.Background(), ps)
	state, cooldown := DegradeStateOf(err)
	assert.Equal(t, StateDegraded, state, "the hold is over")
	assert.Equal(t, 5*time.Minute, cooldown)
	assert.Contains(t, err.Error(), "hold is over")

	// an error that is an answer of the platform is no degradation at all
	state, _ = DegradeStateOf(unreachable(nil))
	assert.Empty(t, state)
}

func TestTheHoldAfterAnExpiredDecisionCountsFromWhenTheCacheRanOut(t *testing.T) {
	clock := now
	inner := &scriptedDecider{decision: &Decision{Replicas: 6, Source: "platform:algorithm"}}
	c := &CachingDecider{Inner: inner, Policy: degradePolicy(), Now: func() time.Time { return clock }}
	ps := cachedScaler()
	_, err := c.Decide(context.Background(), ps)
	require.NoError(t, err)
	inner.err = errDown

	clock = now.Add(9 * time.Minute)
	d, err := c.Decide(context.Background(), ps)
	require.NoError(t, err)
	assert.True(t, d.Cached, "within the ten minutes the decision stands in")

	for _, tc := range []struct {
		at   time.Duration
		want string
	}{{11 * time.Minute, StateHolding}, {14*time.Minute + 59*time.Second, StateHolding}, {15 * time.Minute, StateDegraded}} {
		clock = now.Add(tc.at)
		_, err = c.Decide(context.Background(), ps)
		state, _ := DegradeStateOf(err)
		assert.Equal(t, tc.want, state, "after %s", tc.at)
	}
}

func TestAfterAnOutageScaleDownNeedsBothTheAnswersAndTheStablePeriod(t *testing.T) {
	clock := now
	inner := &scriptedDecider{decision: &Decision{Replicas: 5, Source: "platform:algorithm"}}
	c := &CachingDecider{Inner: inner, Policy: degradePolicy(), Now: func() time.Time { return clock }}
	ps := cachedScaler()
	_, err := c.Decide(context.Background(), ps)
	require.NoError(t, err)
	inner.err = errDown
	clock = now.Add(time.Minute)
	_, _ = c.Decide(context.Background(), ps)

	inner.err = nil
	recovering := func(after time.Duration) bool {
		clock = now.Add(time.Minute + after)
		d, err := c.Decide(context.Background(), ps)
		require.NoError(t, err)
		return d.Recovering
	}
	assert.True(t, recovering(0), "the first answer")
	assert.True(t, recovering(30*time.Second), "two answers are not enough before the stable period has passed")
	assert.True(t, recovering(2*time.Minute))
	assert.True(t, recovering(4*time.Minute+59*time.Second))
	assert.False(t, recovering(5*time.Minute), "enough answers and five minutes since the first one: the platform decides again")
	assert.False(t, recovering(6*time.Minute))

	// the platform goes away again before the period is over: the count starts again from the next first answer
	inner.err = errDown
	clock = now.Add(20 * time.Minute)
	_, _ = c.Decide(context.Background(), ps)
	inner.err = nil
	assert.True(t, recovering(19*time.Minute))
	assert.True(t, recovering(19*time.Minute+time.Second))
	assert.True(t, recovering(23*time.Minute))
	assert.False(t, recovering(24*time.Minute+time.Second))

	// with the time condition off only the answers count, as before
	policy := degradePolicy()
	policy.RecoverStable = 0
	one := &CachingDecider{Inner: inner, Policy: policy, Now: func() time.Time { return clock }}
	_, _ = one.Decide(context.Background(), ps)
	inner.err = errDown
	_, _ = one.Decide(context.Background(), ps)
	inner.err = nil
	d, _ := one.Decide(context.Background(), ps)
	assert.True(t, d.Recovering)
	d, _ = one.Decide(context.Background(), ps)
	assert.False(t, d.Recovering)
}

func TestHoldingKeepsTheReplicasAndOnlyADegradedModuleAppliesItsFallbackInSteps(t *testing.T) {
	fallback := int32(12)
	input := func(current int32, state string) PlanInput {
		in := PlanInput{Now: now, Spec: Spec{MinReplicas: 2, MaxReplicas: 20, FallbackReplicas: &fallback, Behavior: Behavior{ScaleUp: ScaleRule{MaxStep: 3}}},
			Current: current, Ready: current, DecisionErr: "platform unreachable", Degrade: state, FallbackCooldown: 5 * time.Minute}
		return in
	}

	held := MakePlan(input(4, StateHolding))
	assert.Equal(t, ActionNone, held.Action, "the fallback of 12 is not applied while the hold lasts")
	assert.EqualValues(t, 4, held.Desired)
	assert.Equal(t, "frozen", held.UsedSource)
	assert.Contains(t, held.Reason, "holding the current replicas")

	degraded := MakePlan(input(4, StateDegraded))
	assert.Equal(t, ActionUp, degraded.Action)
	assert.EqualValues(t, 7, degraded.Desired, "the fallback applies in steps of the module, not in one jump")
	assert.Equal(t, "fallback", degraded.UsedSource)
	assert.Contains(t, degraded.Reason, "degraded")

	// the next step waits for the degrade cooldown
	in := input(7, StateDegraded)
	last := now.Add(-2 * time.Minute)
	in.LastScale = &last
	waiting := MakePlan(in)
	assert.Equal(t, ActionNone, waiting.Action)
	assert.Contains(t, waiting.Blocked, "degraded")
	last = now.Add(-5 * time.Minute)
	assert.Equal(t, ActionUp, MakePlan(in).Action, "five minutes after the last step the next one is due")

	// a fallback below the current count never scales down, whatever the state
	below := input(15, StateDegraded)
	assert.Equal(t, ActionNone, MakePlan(below).Action)

	// without a state (the platform answered "no decision": bypass, module off) the module's own rules apply at once, as they always did
	plain := MakePlan(input(4, ""))
	assert.Equal(t, ActionUp, plain.Action)
	assert.EqualValues(t, 7, plain.Desired)
}

// The whole of an outage on the module sync, with the reports on the way: the workload is held while the platform is away and nothing
// usable is remembered, the fallback comes in steps after the hold, the platform's return does not shrink the workload before the stable
// period, and the platform finds every action that was taken while it was away, once.
func TestAnOutageHoldsThenDegradesInStepsRecoversWithoutShrinkingAtOnceAndKeepsEveryRecord(t *testing.T) {
	clock := now
	p := &fakePlatform{modules: []PlatformModule{apiModule()}, replicas: 4}
	p.modules[0].FallbackReplicas, p.modules[0].MaxReplicas = 12, 20
	srv := httptest.NewServer(p.handler())
	t.Cleanup(srv.Close)
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(deployment(4, 4)).Build()
	platform := &PlatformDecider{Endpoint: &PlatformEndpoint{URL: srv.URL, Credential: "ak:sk", Client: srv.Client()}}
	policy := degradePolicy()
	cached := &CachingDecider{Inner: platform, Policy: policy, Now: func() time.Time { return clock }}
	outbox := &Outbox{Sink: platform, Now: func() time.Time { return clock }, Operator: "op-0"}
	applier := &Applier{Client: c, Reporter: outbox, Now: func() time.Time { return clock }, Decider: cached}
	s := &ModuleSync{Applier: applier, Platform: platform, Cache: ModuleCache{Policy: policy, Now: func() time.Time { return clock }}}
	ctx := context.Background()

	round := func(advance time.Duration) int32 {
		clock = clock.Add(advance)
		// every replica the fake cluster has is Ready, so a scale-down is held only by the rules under test
		var d appsv1.Deployment
		require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "prod", Name: "api"}, &d))
		d.Status.ReadyReplicas, d.Status.Replicas = *d.Spec.Replicas, *d.Spec.Replicas
		require.NoError(t, c.Status().Update(ctx, &d))
		s.Once(ctx)
		outbox.Flush(ctx)
		return replicasOf(t, c)
	}
	set := func(down bool, replicas int32) {
		p.mu.Lock()
		p.down, p.replicas = down, replicas
		p.validUntil = clock.Add(10 * time.Minute)
		p.mu.Unlock()
	}

	assert.EqualValues(t, 4, round(0), "the platform asks for 4")

	set(true, 4)
	assert.EqualValues(t, 4, round(2*time.Minute), "cached: the remembered 4")
	assert.EqualValues(t, 4, round(7*time.Minute), "cached: nine minutes after the last answer")
	assert.EqualValues(t, 4, round(time.Minute), "the cache ran out at ten minutes: the replicas are held, not thrown to the fallback of 12")
	assert.EqualValues(t, 4, round(3*time.Minute), "still held")
	assert.EqualValues(t, 7, round(2*time.Minute), "the hold is over: the fallback in a step of 3")
	assert.EqualValues(t, 7, round(time.Minute), "the next step waits for the degrade cooldown")
	assert.EqualValues(t, 10, round(4*time.Minute), "five minutes after the first step")
	assert.EqualValues(t, 12, round(5*time.Minute), "the last step reaches the fallback")

	// nothing reached the platform while it was away
	p.mu.Lock()
	assert.Empty(t, p.reports)
	p.mu.Unlock()
	assert.Equal(t, 4, outbox.Pending(), "the three scale-ups taken while the platform was away, and the step that was held, wait in the outbox")

	// the platform is back and asks for 5: the workload is not shrunk at once
	set(false, 5)
	assert.EqualValues(t, 12, round(time.Minute), "first answer after the outage")
	assert.EqualValues(t, 12, round(time.Minute), "second answer: two answers are not enough before the stable period")
	assert.EqualValues(t, 12, round(2*time.Minute), "three minutes after the first answer")
	assert.EqualValues(t, 5, round(2*time.Minute+time.Second), "five minutes after the first answer: the platform decides again")

	// the platform got every action, once, in the order they were taken, with the state of the module and what the replicas followed
	p.mu.Lock()
	defer p.mu.Unlock()
	require.Len(t, p.reports, 5, "three scale-ups and one held step of the outage, and the scale-down after it")
	ids := map[string]bool{}
	for _, rep := range p.reports {
		require.NotEmpty(t, rep.RecordID)
		assert.False(t, ids[rep.RecordID], "record ids are unique")
		ids[rep.RecordID] = true
		assert.Equal(t, "op-0", rep.Operator)
		assert.False(t, rep.OccurredAt.IsZero())
	}
	assert.True(t, sort.SliceIsSorted(p.reports, func(i, j int) bool { return p.reports[i].OccurredAt.Before(p.reports[j].OccurredAt) }), "oldest first")
	var ups []Report
	for _, rep := range p.reports[:4] {
		assert.Equal(t, StateDegraded, rep.State)
		assert.Equal(t, "fallback", rep.DecisionSource)
		if rep.Result == "applied" {
			assert.Equal(t, "ScaleUp", rep.Action)
			ups = append(ups, rep)
		} else {
			assert.Equal(t, "held", rep.Result, "the step the degrade cooldown held back")
		}
	}
	require.Len(t, ups, 3)
	assert.Equal(t, now.Add(15*time.Minute), ups[0].OccurredAt, "the report carries the time the operator acted, not the time the platform heard of it")
	last := p.reports[4]
	assert.Equal(t, "ScaleDown", last.Action)
	assert.EqualValues(t, 12, last.Current)
	assert.EqualValues(t, 5, last.Desired)
	assert.Equal(t, StateNormal, last.State)
	assert.Zero(t, outbox.Pending())
}

func TestTheOutboxKeepsReportsWhileThePlatformIsAwayAndSendsEachOnceInOrder(t *testing.T) {
	p := &fakePlatform{modules: []PlatformModule{apiModule()}}
	srv := httptest.NewServer(p.handler())
	t.Cleanup(srv.Close)
	platform := &PlatformDecider{Endpoint: &PlatformEndpoint{URL: srv.URL, Credential: "ak:sk", Client: srv.Client()}}
	clock := now
	o := &Outbox{Sink: platform, Now: func() time.Time { return clock }}
	ctx := context.Background()
	ps := &Scaler{Module: "api"}

	p.mu.Lock()
	p.down = true
	p.mu.Unlock()
	for i := 0; i < 3; i++ {
		clock = now.Add(time.Duration(i) * time.Minute)
		require.NoError(t, o.Report(ctx, ps, Report{Action: "ScaleUp", Result: "applied", Desired: int32(5 + i)}))
	}
	o.Flush(ctx)
	assert.Equal(t, 3, o.Pending(), "the platform is away: every report is kept")

	p.mu.Lock()
	p.down = false
	p.mu.Unlock()
	o.Flush(ctx)
	o.Flush(ctx) // a second flush has nothing to send
	assert.Zero(t, o.Pending())
	p.mu.Lock()
	defer p.mu.Unlock()
	require.Len(t, p.reports, 3)
	for i, rep := range p.reports {
		assert.EqualValues(t, 5+i, rep.Desired, "oldest first")
		assert.Equal(t, now.Add(time.Duration(i)*time.Minute), rep.OccurredAt)
		assert.Equal(t, "api", rep.Module)
	}
	assert.NotEqual(t, p.reports[0].RecordID, p.reports[1].RecordID)
}

func TestTheOutboxSurvivesARestartThroughItsSpoolFileAndDropsOnlyWhatCannotBeKept(t *testing.T) {
	p := &fakePlatform{modules: []PlatformModule{apiModule()}, down: true}
	srv := httptest.NewServer(p.handler())
	t.Cleanup(srv.Close)
	platform := &PlatformDecider{Endpoint: &PlatformEndpoint{URL: srv.URL, Credential: "ak:sk", Client: srv.Client()}}
	file := filepath.Join(t.TempDir(), "spool", "reports.jsonl")
	ctx := context.Background()
	ps := &Scaler{Module: "api"}

	first := &Outbox{Sink: platform, File: file}
	require.NoError(t, first.Load())
	for i := 0; i < 3; i++ {
		require.NoError(t, first.Report(ctx, ps, Report{Action: "ScaleUp", Result: "applied", Desired: int32(5 + i)}))
	}
	require.Equal(t, 3, first.Pending())

	// the operator restarts in the middle of the outage; a torn last line (a crash while writing) does not spoil the rest
	f, err := os.OpenFile(file, os.O_APPEND|os.O_WRONLY, 0o644)
	require.NoError(t, err)
	_, err = f.WriteString(`{"module":"api","report":{"rec`)
	require.NoError(t, err)
	require.NoError(t, f.Close())

	second := &Outbox{Sink: platform, File: file}
	require.NoError(t, second.Load())
	assert.Equal(t, 3, second.Pending(), "what the first run had not delivered is still there")
	require.NoError(t, second.Report(ctx, ps, Report{Action: "ScaleUp", Result: "applied", Desired: 9}))
	assert.NotEqual(t, first.instance, second.instance)

	p.mu.Lock()
	p.down = false
	p.mu.Unlock()
	second.Flush(ctx)
	assert.Zero(t, second.Pending())
	raw, err := os.ReadFile(file)
	require.NoError(t, err)
	assert.Empty(t, string(raw), "what was delivered leaves the spool")
	p.mu.Lock()
	assert.Len(t, p.reports, 4)
	p.mu.Unlock()

	// the queue is bounded: past the bound the oldest goes, and it is counted
	small := &Outbox{Sink: platform, Max: 2, Metrics: NewMetrics(nil)}
	p.mu.Lock()
	p.down = true
	p.mu.Unlock()
	for i := 0; i < 4; i++ {
		require.NoError(t, small.Report(ctx, ps, Report{Action: "ScaleUp", Result: "applied", Desired: int32(i)}))
	}
	assert.Equal(t, 2, small.Pending())
	small.mu.Lock()
	assert.EqualValues(t, 2, small.queue[0].Report.Desired, "the two newest are kept")
	small.mu.Unlock()
}

func TestAReportThePlatformRefusesForGoodDoesNotBlockTheOnesBehindIt(t *testing.T) {
	p := &fakePlatform{modules: []PlatformModule{apiModule()}, rejectModule: "gone"}
	srv := httptest.NewServer(p.handler())
	t.Cleanup(srv.Close)
	platform := &PlatformDecider{Endpoint: &PlatformEndpoint{URL: srv.URL, Credential: "ak:sk", Client: srv.Client()}}
	o := &Outbox{Sink: platform}
	ctx := context.Background()
	require.NoError(t, o.Report(ctx, &Scaler{Module: "gone"}, Report{Action: "ScaleUp", Result: "applied"}))
	require.NoError(t, o.Report(ctx, &Scaler{Module: "api"}, Report{Action: "ScaleUp", Result: "applied"}))
	o.Flush(ctx)
	assert.Zero(t, o.Pending(), "the refused report is dropped, the next one is sent")
	p.mu.Lock()
	defer p.mu.Unlock()
	require.Len(t, p.reports, 1)
	assert.Equal(t, "api", p.reports[0].Module)
}

// The operator scales; it never touches what a Pod asks for. Requests and limits are set at admission only, and a scale writes the
// scale subresource and nothing else of the workload.
func TestScalingNeverChangesTheRequestsOrLimitsOfTheWorkload(t *testing.T) {
	dep := deployment(4, 4)
	dep.Spec.Template.Spec.Containers = []corev1.Container{{Name: "app", Resources: corev1.ResourceRequirements{
		Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m"), corev1.ResourceMemory: resource.MustParse("512Mi")},
		Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("614Mi")},
	}}}
	h := newHarness(t, fakeDecider{d: fresh(8)}, dep)
	h.evaluate(scaler())
	require.EqualValues(t, 8, h.replicas(t))

	var after appsv1.Deployment
	require.NoError(t, h.c.Get(context.Background(), types.NamespacedName{Namespace: "prod", Name: "api"}, &after))
	assert.Equal(t, dep.Spec.Template, after.Spec.Template, "the Pod template, and with it the requests and limits, is as it was")
}
