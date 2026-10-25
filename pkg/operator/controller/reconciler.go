package controller

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/cylonchau/kubeforecast/pkg/operator/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv1 "k8s.io/api/autoscaling/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

const defaultEvaluationInterval = 30 * time.Second

// minEvaluationInterval keeps a misconfigured interval from hammering the API server and the platform.
const minEvaluationInterval = 5 * time.Second

// Reconciler turns a PredictiveScaler into scale subresource writes on its target.
type Reconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
	Now      func() time.Time
	// Deciders maps a decision source to its implementation.
	Deciders map[v1alpha1.DecisionSource]Decider
	// Reporter is optional.
	Reporter Reporter
	// Metrics is optional.
	Metrics *Metrics
	// NoCRD is set when the PredictiveScaler CRD is not installed: the scalers then all come from platform modules and
	// there is no object to look for when checking for a second controller of a target.
	NoCRD bool

	reportMu   sync.Mutex
	lastReport map[string]string
}

func (r *Reconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// target is a scalable workload.
type target struct {
	obj   client.Object
	ready func() int32
}

func newTarget(ref v1alpha1.TargetRef, namespace string) (*target, error) {
	if ref.APIVersion != "" && ref.APIVersion != "apps/v1" {
		return nil, fmt.Errorf("unsupported apiVersion %q: only apps/v1 workloads are supported", ref.APIVersion)
	}
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

// Reconcile implements reconcile.Reconciler.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var ps v1alpha1.PredictiveScaler
	if err := r.Get(ctx, req.NamespacedName, &ps); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !ps.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}
	return r.evaluate(ctx, &ps, req.NamespacedName.String())
}

// evaluate reads the target, asks the decision source, applies the scaling rules and writes the scale subresource of
// one scaler. A scaler built from a platform module (see IsModuleDriven) exists only in memory: nothing is written
// back to the cluster for it except the replicas of its target, and its status is left on the object for the caller.
func (r *Reconciler) evaluate(ctx context.Context, ps *v1alpha1.PredictiveScaler, key string) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	started := time.Now()
	interval := defaultEvaluationInterval
	if ps.Spec.EvaluationInterval != nil && ps.Spec.EvaluationInterval.Duration > 0 {
		interval = ps.Spec.EvaluationInterval.Duration
	}
	if interval < minEvaluationInterval {
		interval = minEvaluationInterval
	}
	result := ctrl.Result{RequeueAfter: interval}
	now := r.now()
	status := ps.Status.DeepCopy()
	status.ObservedGeneration = ps.Generation

	setCondition := func(t string, ok bool, reason, msg string) {
		s := metav1.ConditionFalse
		if ok {
			s = metav1.ConditionTrue
		}
		meta.SetStatusCondition(&status.Conditions, metav1.Condition{Type: t, Status: s, Reason: reason, Message: msg, ObservedGeneration: ps.Generation})
	}
	// what the evaluation learned, for the metrics; early failures leave it unknown
	var (
		in             PlanInput
		plan           Plan
		observed       bool
		current, ready int32
	)
	finish := func(res string, rep Report) (ctrl.Result, error) {
		if observed {
			r.Metrics.recordReconcile(ps, res, time.Since(started), current, ready, status.DesiredReplicas, in, plan, now)
		} else {
			r.Metrics.recordReconcileFailure(res, time.Since(started))
		}
		if err := r.persistStatus(ctx, ps, status); err != nil {
			return result, err
		}
		rep.Namespace, rep.Scaler, rep.TargetKind, rep.TargetName = ps.Namespace, ps.Name, ps.Spec.ScaleTargetRef.Kind, ps.Spec.ScaleTargetRef.Name
		rep.Result = res
		if r.shouldReport(key, rep) {
			if err := r.Reporter.Report(ctx, ps, rep); err != nil {
				log.Info("could not report to the platform", "error", err.Error())
				r.forgetReport(key)
			}
		}
		return result, nil
	}

	tgt, err := newTarget(ps.Spec.ScaleTargetRef, ps.Namespace)
	if err != nil {
		setCondition(v1alpha1.ConditionReady, false, "InvalidTarget", err.Error())
		return finish("error", Report{Reason: err.Error()})
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(tgt.obj), tgt.obj); err != nil {
		reason := "TargetUnavailable"
		if errors.IsNotFound(err) {
			reason = "TargetNotFound"
		}
		setCondition(v1alpha1.ConditionReady, false, reason, err.Error())
		return finish("error", Report{Reason: err.Error()})
	}
	scale := &autoscalingv1.Scale{}
	if err := r.SubResource("scale").Get(ctx, tgt.obj, scale); err != nil {
		setCondition(v1alpha1.ConditionReady, false, "ScaleUnavailable", err.Error())
		return finish("error", Report{Reason: err.Error()})
	}
	current, ready = scale.Spec.Replicas, tgt.ready()
	observed = true
	status.CurrentReplicas, status.ReadyReplicas = current, ready

	in = PlanInput{Now: now, Spec: ps.Spec, Current: current, Ready: ready}
	if ps.Status.LastScaleTime != nil {
		t := ps.Status.LastScaleTime.Time
		in.LastScale = &t
	}
	if conflict, err := r.conflict(ctx, ps); err != nil {
		setCondition(v1alpha1.ConditionReady, false, "ConflictCheckFailed", err.Error())
		return finish("error", Report{Current: current, Ready: ready, Reason: err.Error()})
	} else {
		in.Conflict = conflict
	}
	setCondition(v1alpha1.ConditionConflict, in.Conflict != "", conflictReason(in.Conflict), in.Conflict)

	if in.Conflict == "" {
		decider := r.Deciders[ps.Spec.Decision.Source]
		if decider == nil {
			in.DecisionErr = fmt.Sprintf("no decider for source %q", ps.Spec.Decision.Source)
		} else if d, err := decider.Decide(ctx, ps); err != nil {
			in.DecisionErr = err.Error()
		} else {
			in.Decision = d
		}
	}
	plan = MakePlan(in)
	status.DesiredReplicas = plan.Desired
	if in.Decision != nil {
		ds := &v1alpha1.DecisionStatus{Replicas: in.Decision.Replicas, Source: in.Decision.Source, Version: in.Decision.Version, Reason: in.Decision.Reason,
			Algorithm: in.Decision.Algorithm}
		if in.Decision.Algorithm != "" {
			ds.Score = strconv.FormatFloat(in.Decision.Score, 'f', 1, 64)
		}
		if in.Decision.ValidUntil != nil {
			vu := metav1.NewTime(*in.Decision.ValidUntil)
			ds.ValidUntil = &vu
		}
		status.LastDecision = ds
	}
	setCondition(v1alpha1.ConditionDecisionValid, plan.DecisionValid, decisionReason(plan, in), plan.Reason)

	rep := Report{Current: current, Ready: ready, Desired: plan.Desired, Action: string(plan.Action), Reason: plan.Reason}
	if in.Decision != nil {
		rep.DecisionVersion = in.Decision.Version
	}
	if in.Conflict != "" {
		setCondition(v1alpha1.ConditionReady, false, "Conflict", in.Conflict)
		return finish("conflict", rep)
	}

	outcome := "observed"
	switch {
	case plan.Action == ActionNone:
		if plan.Blocked != "" {
			outcome = "held"
			rep.Reason += "; " + plan.Blocked
		}
	case ps.Spec.Mode != v1alpha1.ModeAuto:
		rep.Reason += "; Observe mode: not applied"
		r.Metrics.recordScale(plan.Action, "observed")
	default:
		scale.Spec.Replicas = plan.Desired
		if err := r.SubResource("scale").Update(ctx, tgt.obj, client.WithSubResourceBody(scale)); err != nil {
			r.Metrics.recordScale(plan.Action, "failed")
			setCondition(v1alpha1.ConditionReady, false, "ScaleFailed", err.Error())
			r.event(ps, "Warning", "ScaleFailed", err.Error())
			rep.Reason = err.Error()
			return finish("error", rep)
		}
		t := metav1.NewTime(now)
		status.LastScaleTime = &t
		outcome = "applied"
		r.Metrics.recordScale(plan.Action, "applied")
		msg := fmt.Sprintf("%s from %d to %d replicas: %s", plan.Action, current, plan.Desired, plan.Reason)
		r.event(ps, "Normal", string(plan.Action), msg)
		log.Info("scaled target", "target", ps.Spec.ScaleTargetRef.Name, "from", current, "to", plan.Desired)
	}
	setCondition(v1alpha1.ConditionReady, true, "Reconciled", plan.Reason)
	return finish(outcome, rep)
}

func conflictReason(conflict string) string {
	if conflict == "" {
		return "NoConflict"
	}
	return "OtherAutoscaler"
}

func decisionReason(p Plan, in PlanInput) string {
	switch {
	case p.DecisionValid:
		return "Fresh"
	case in.Decision != nil:
		return "Expired"
	}
	return "Unavailable"
}

func (r *Reconciler) event(ps *v1alpha1.PredictiveScaler, kind, reason, msg string) {
	// a scaler built from a platform module is not an object of the cluster: the audit trail of its scaling is the report
	// to the platform and the log
	if r.Recorder != nil && !IsModuleDriven(ps) {
		r.Recorder.Event(ps, kind, reason, msg)
	}
}

func (r *Reconciler) persistStatus(ctx context.Context, ps *v1alpha1.PredictiveScaler, status *v1alpha1.PredictiveScalerStatus) error {
	if equalStatus(&ps.Status, status) {
		return nil
	}
	ps.Status = *status
	if IsModuleDriven(ps) {
		return nil
	}
	return r.Status().Update(ctx, ps)
}

func equalStatus(a, b *v1alpha1.PredictiveScalerStatus) bool {
	return a.ObservedGeneration == b.ObservedGeneration && a.CurrentReplicas == b.CurrentReplicas && a.ReadyReplicas == b.ReadyReplicas &&
		a.DesiredReplicas == b.DesiredReplicas && fmt.Sprint(a.LastDecision) == fmt.Sprint(b.LastDecision) &&
		fmt.Sprint(a.LastScaleTime) == fmt.Sprint(b.LastScaleTime) && fmt.Sprint(a.Conditions) == fmt.Sprint(b.Conditions)
}

var scaledObjectList = schema.GroupVersionKind{Group: "keda.sh", Version: "v1alpha1", Kind: "ScaledObjectList"}

// conflict names another autoscaler that controls the same target, or "" when this scaler may act. Two
// PredictiveScalers on one target are resolved in favour of the older one.
func (r *Reconciler) conflict(ctx context.Context, ps *v1alpha1.PredictiveScaler) (string, error) {
	ref := ps.Spec.ScaleTargetRef

	var hpas autoscalingv2.HorizontalPodAutoscalerList
	if err := r.List(ctx, &hpas, client.InNamespace(ps.Namespace)); err != nil {
		return "", fmt.Errorf("list HorizontalPodAutoscalers: %w", err)
	}
	for _, h := range hpas.Items {
		if h.Spec.ScaleTargetRef.Kind == ref.Kind && h.Spec.ScaleTargetRef.Name == ref.Name {
			return fmt.Sprintf("HorizontalPodAutoscaler %s also targets %s %s", h.Name, ref.Kind, ref.Name), nil
		}
	}

	so := &unstructured.UnstructuredList{}
	so.SetGroupVersionKind(scaledObjectList)
	if err := r.List(ctx, so, client.InNamespace(ps.Namespace)); err != nil {
		if !meta.IsNoMatchError(err) && !errors.IsNotFound(err) {
			return "", fmt.Errorf("list KEDA ScaledObjects: %w", err)
		}
	} else {
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
	}

	if r.NoCRD {
		return "", nil
	}
	var scalers v1alpha1.PredictiveScalerList
	if err := r.List(ctx, &scalers, client.InNamespace(ps.Namespace)); err != nil {
		return "", fmt.Errorf("list PredictiveScalers: %w", err)
	}
	var same []v1alpha1.PredictiveScaler
	for _, s := range scalers.Items {
		if s.Spec.ScaleTargetRef.Kind == ref.Kind && s.Spec.ScaleTargetRef.Name == ref.Name && s.DeletionTimestamp.IsZero() {
			same = append(same, s)
		}
	}
	if IsModuleDriven(ps) && len(same) > 0 {
		return fmt.Sprintf("PredictiveScaler %s also targets %s %s and takes precedence over the platform module", same[0].Name, ref.Kind, ref.Name), nil
	}
	sort.Slice(same, func(i, j int) bool {
		if !same[i].CreationTimestamp.Equal(&same[j].CreationTimestamp) {
			return same[i].CreationTimestamp.Before(&same[j].CreationTimestamp)
		}
		return same[i].Name < same[j].Name
	})
	if len(same) > 1 && same[0].Name != ps.Name {
		return fmt.Sprintf("PredictiveScaler %s is the older scaler of %s %s", same[0].Name, ref.Kind, ref.Name), nil
	}
	return "", nil
}

// SetupWithManager registers the reconciler.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).For(&v1alpha1.PredictiveScaler{}).Complete(r)
}

// shouldReport keeps the audit trail to what matters: a scaling action (or the one it would have taken in
// Observe mode), a hold, a conflict or an error. Quiet "nothing to do" evaluations are not reported, and an
// unchanged repeat of the previous report is dropped, so a steady state costs no platform traffic.
func (r *Reconciler) shouldReport(key string, rep Report) bool {
	if r.Reporter == nil {
		return false
	}
	if rep.Action == string(ActionNone) && rep.Result == "observed" {
		return false
	}
	signature := fmt.Sprintf("%s|%s|%d|%d|%s", rep.Result, rep.Action, rep.Current, rep.Desired, rep.Reason)
	r.reportMu.Lock()
	defer r.reportMu.Unlock()
	if r.lastReport == nil {
		r.lastReport = map[string]string{}
	}
	if r.lastReport[key] == signature {
		return false
	}
	r.lastReport[key] = signature
	return true
}

func (r *Reconciler) forgetReport(key string) {
	r.reportMu.Lock()
	defer r.reportMu.Unlock()
	delete(r.lastReport, key)
}
