package controller

import (
	"context"
	"fmt"
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv1 "k8s.io/api/autoscaling/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

const defaultEvaluationInterval = 30 * time.Second

// minEvaluationInterval keeps a misconfigured interval from hammering the API server and the platform.
const minEvaluationInterval = 5 * time.Second

// Applier reads the target of a scaler, asks the platform for the decision, applies the scaling rules and writes the scale subresource
// of the target. It stores nothing in the cluster but the replicas.
type Applier struct {
	client.Client
	Now func() time.Time
	// Decider gives the desired replicas of a scaler.
	Decider Decider
	// Reporter is optional.
	Reporter Reporter
	// Metrics is optional.
	Metrics *Metrics

	reportMu   sync.Mutex
	lastReport map[string]string
}

func (a *Applier) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

// target is a scalable workload.
type target struct {
	obj   client.Object
	ready func() int32
}

func newTarget(ref TargetRef, namespace string) (*target, error) {
	key := metav1.ObjectMeta{Name: ref.Name, Namespace: namespace}
	switch ref.Kind {
	case "Deployment":
		d := &appsv1.Deployment{ObjectMeta: key}
		return &target{obj: d, ready: func() int32 { return d.Status.ReadyReplicas }}, nil
	case "StatefulSet":
		s := &appsv1.StatefulSet{ObjectMeta: key}
		return &target{obj: s, ready: func() int32 { return s.Status.ReadyReplicas }}, nil
	}
	return nil, fmt.Errorf("unsupported kind %q", ref.Kind)
}

// Evaluate reads the target, asks the decision source, applies the scaling rules and writes the scale subresource of one scaler. When it
// scales the target it records the time on the scaler (LastScale), which the caller keeps for the cooldown of the next round.
func (a *Applier) Evaluate(ctx context.Context, s *Scaler, key string) {
	log := logf.FromContext(ctx)
	started := time.Now()
	now := a.now()
	// what the evaluation learned, for the metrics; early failures leave it unknown
	var (
		in             PlanInput
		plan           Plan
		observed       bool
		current, ready int32
	)
	finish := func(res string, rep Report) {
		if observed {
			a.Metrics.recordReconcile(s, res, time.Since(started), current, ready, plan.Desired, in, plan, now)
		} else {
			a.Metrics.recordReconcileFailure(res, time.Since(started))
		}
		rep.Namespace, rep.Scaler, rep.TargetKind, rep.TargetName = s.Namespace, s.Name, s.Target.Kind, s.Target.Name
		rep.Result = res
		rep.OccurredAt, rep.DecisionSource, rep.State = now, plan.UsedSource, stateOf(in)
		if a.shouldReport(key, rep) {
			if err := a.Reporter.Report(ctx, s, rep); err != nil {
				log.Info("could not report to the platform", "error", err.Error())
				a.forgetReport(key)
			}
		}
	}

	tgt, err := newTarget(s.Target, s.Namespace)
	if err != nil {
		finish("error", Report{Reason: err.Error()})
		return
	}
	if err := a.Get(ctx, client.ObjectKeyFromObject(tgt.obj), tgt.obj); err != nil {
		finish("error", Report{Reason: err.Error()})
		return
	}
	scale := &autoscalingv1.Scale{}
	if err := a.SubResource("scale").Get(ctx, tgt.obj, scale); err != nil {
		finish("error", Report{Reason: err.Error()})
		return
	}
	current, ready = scale.Spec.Replicas, tgt.ready()
	observed = true

	in = PlanInput{Now: now, Spec: s.Spec, Current: current, Ready: ready, LastScale: s.LastScale}
	conflict, err := a.conflict(ctx, s)
	if err != nil {
		finish("error", Report{Current: current, Ready: ready, Reason: err.Error()})
		return
	}
	in.Conflict = conflict

	if in.Conflict == "" {
		if a.Decider == nil {
			in.DecisionErr = "no decision source"
		} else if d, err := a.Decider.Decide(ctx, s); err != nil {
			in.DecisionErr = err.Error()
			in.Degrade, in.FallbackCooldown = DegradeStateOf(err)
		} else {
			in.Decision = d
		}
	}
	plan = MakePlan(in)

	rep := Report{Current: current, Ready: ready, Desired: plan.Desired, Action: string(plan.Action), Reason: plan.Reason}
	if in.Decision != nil {
		rep.DecisionVersion = in.Decision.Version
	}
	if in.Conflict != "" {
		finish("conflict", rep)
		return
	}

	outcome := "observed"
	switch {
	case plan.Action == ActionNone:
		if plan.Blocked != "" {
			outcome = "held"
			rep.Reason += "; " + plan.Blocked
		}
	default:
		scale.Spec.Replicas = plan.Desired
		if err := a.SubResource("scale").Update(ctx, tgt.obj, client.WithSubResourceBody(scale)); err != nil {
			a.Metrics.recordScale(plan.Action, "failed")
			rep.Reason = err.Error()
			finish("error", rep)
			return
		}
		t := now
		s.LastScale = &t
		outcome = "applied"
		a.Metrics.recordScale(plan.Action, "applied")
		log.Info("scaled target", "target", s.Target.Name, "from", current, "to", plan.Desired, "reason", plan.Reason)
	}
	finish(outcome, rep)
}

// stateOf is the state of the module in an outage of the platform that the evaluation saw (see CachingDecider).
func stateOf(in PlanInput) string {
	switch {
	case in.Degrade != "":
		return in.Degrade
	case in.Decision != nil && in.Decision.Cached:
		return StateCached
	case in.Decision != nil && in.Decision.Recovering:
		return StateRecovering
	}
	return StateNormal
}

var scaledObjectList = schema.GroupVersionKind{Group: "keda.sh", Version: "v1alpha1", Kind: "ScaledObjectList"}

// conflict names another autoscaler that controls the same target, or "" when this scaler may act.
func (a *Applier) conflict(ctx context.Context, s *Scaler) (string, error) {
	ref := s.Target

	var hpas autoscalingv2.HorizontalPodAutoscalerList
	if err := a.List(ctx, &hpas, client.InNamespace(s.Namespace)); err != nil {
		return "", fmt.Errorf("list HorizontalPodAutoscalers: %w", err)
	}
	for _, h := range hpas.Items {
		if h.Spec.ScaleTargetRef.Kind == ref.Kind && h.Spec.ScaleTargetRef.Name == ref.Name {
			return fmt.Sprintf("HorizontalPodAutoscaler %s also targets %s %s", h.Name, ref.Kind, ref.Name), nil
		}
	}

	so := &unstructured.UnstructuredList{}
	so.SetGroupVersionKind(scaledObjectList)
	if err := a.List(ctx, so, client.InNamespace(s.Namespace)); err != nil {
		if !meta.IsNoMatchError(err) && !errors.IsNotFound(err) {
			return "", fmt.Errorf("list KEDA ScaledObjects: %w", err)
		}
		return "", nil
	}
	for _, item := range so.Items {
		name, _, _ := unstructured.NestedString(item.Object, "spec", "scaleTargetRef", "name")
		kind, found, _ := unstructured.NestedString(item.Object, "spec", "scaleTargetRef", "kind")
		if !found || kind == "" {
			kind = "Deployment"
		}
		if name == ref.Name && kind == ref.Kind {
			return fmt.Sprintf("KEDA ScaledObject %s also targets %s %s", item.GetName(), ref.Kind, ref.Name), nil
		}
	}
	return "", nil
}

// shouldReport keeps the audit trail to what matters: a scaling action, a hold, a conflict or an error. Quiet "nothing to do" evaluations
// are not reported, and an unchanged repeat of the previous report is dropped, so a steady state costs no platform traffic.
func (a *Applier) shouldReport(key string, rep Report) bool {
	if a.Reporter == nil {
		return false
	}
	if rep.Action == string(ActionNone) && rep.Result == "observed" {
		return false
	}
	signature := fmt.Sprintf("%s|%s|%d|%d|%s", rep.Result, rep.Action, rep.Current, rep.Desired, rep.Reason)
	a.reportMu.Lock()
	defer a.reportMu.Unlock()
	if a.lastReport == nil {
		a.lastReport = map[string]string{}
	}
	if a.lastReport[key] == signature {
		return false
	}
	a.lastReport[key] = signature
	return true
}

func (a *Applier) forgetReport(key string) {
	a.reportMu.Lock()
	defer a.reportMu.Unlock()
	delete(a.lastReport, key)
}
