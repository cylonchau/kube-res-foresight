package controller

import (
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Metrics are the business metrics of kube-resource-fcst-operator, served by the controller-runtime metrics endpoint.
// Labels are bounded to the platform module (or the scaler name for a source without a module), the scaling
// direction, a fixed set of results and the kind of conflicting autoscaler; nothing is labelled by time or pod.
type Metrics struct {
	reconciles        *prometheus.CounterVec
	reconcileDuration prometheus.Histogram
	scaleActions      *prometheus.CounterVec
	conflicts         *prometheus.CounterVec
	platformRequests  *prometheus.CounterVec
	platformDuration  prometheus.Histogram

	desired, current, ready *prometheus.GaugeVec
	decisionValid           *prometheus.GaugeVec
	decisionExpiresIn       *prometheus.GaugeVec
	decisionAge             *prometheus.GaugeVec
	decisionSources         *prometheus.CounterVec
	cacheAge                *prometheus.GaugeVec
	outboxPending           prometheus.Gauge
	outboxDropped           *prometheus.CounterVec
}

// NewMetrics builds the instruments and registers them on reg.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		reconciles: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "kube_resource_fcst_operator_reconciles_total",
			Help: "Evaluations of a module by result (applied, observed, held, conflict, error)."}, []string{"result"}),
		reconcileDuration: prometheus.NewHistogram(prometheus.HistogramOpts{Name: "kube_resource_fcst_operator_reconcile_duration_seconds",
			Help: "Time one evaluation took.", Buckets: []float64{0.005, 0.025, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}}),
		scaleActions: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "kube_resource_fcst_operator_scale_actions_total",
			Help: "Scaling actions by direction (up, down) and result (applied, failed, observed when only reported)."}, []string{"direction", "result"}),
		conflicts: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "kube_resource_fcst_operator_conflicts_total",
			Help: "Evaluations skipped because another autoscaler controls the target, by source (hpa, keda)."}, []string{"source"}),
		platformRequests: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "kube_resource_fcst_operator_platform_requests_total",
			Help: "Requests to the kube-resource-fcst platform by result (ok, error)."}, []string{"result"}),
		platformDuration: prometheus.NewHistogram(prometheus.HistogramOpts{Name: "kube_resource_fcst_operator_platform_request_duration_seconds",
			Help: "Time one platform request took.", Buckets: []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}}),
		desired: prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "kube_resource_fcst_operator_desired_replicas",
			Help: "Replicas the operator wants after the safety checks."}, []string{"module"}),
		current: prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "kube_resource_fcst_operator_current_replicas",
			Help: "Replicas of the target as read."}, []string{"module"}),
		ready: prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "kube_resource_fcst_operator_ready_replicas",
			Help: "Ready replicas of the target as read."}, []string{"module"}),
		decisionValid: prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "kube_resource_fcst_operator_decision_valid",
			Help: "1 while the decision in use is fresh and within its validity."}, []string{"module"}),
		decisionExpiresIn: prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "kube_resource_fcst_operator_decision_expires_in_seconds",
			Help: "Seconds until the decision in use expires; negative once it has."}, []string{"module"}),
		decisionSources: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "kube_resource_fcst_operator_decision_source_total",
			Help: "Decisions read, by where they came from: platform, cache (the platform could not be reached and its last answer stood in), holding (nothing usable is remembered: the replicas are kept), degraded (the hold is over: the fallback may apply), recovering (the platform is back, scale-down still waits)."}, []string{"source"}),
		cacheAge: prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "kube_resource_fcst_operator_decision_cache_age_seconds",
			Help: "Age of the remembered decision standing in for an unreachable platform; 0 while the platform answers."}, []string{"module"}),
		outboxPending: prometheus.NewGauge(prometheus.GaugeOpts{Name: "kube_resource_fcst_operator_report_outbox_pending",
			Help: "Reports of scaling actions waiting to be delivered to the platform (it could not be reached); 0 when everything is delivered."}),
		outboxDropped: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "kube_resource_fcst_operator_report_outbox_dropped_total",
			Help: "Reports dropped from the outbox by reason: overflow (the queue was full) or rejected (the platform refused it for good)."}, []string{"reason"}),
		decisionAge: prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "kube_resource_fcst_operator_decision_age_seconds",
			Help: "Age of the decision in use, where the source states when it was made."}, []string{"module"}),
	}
	if reg != nil {
		reg.MustRegister(m.reconciles, m.reconcileDuration, m.scaleActions, m.conflicts, m.platformRequests, m.platformDuration,
			m.desired, m.current, m.ready, m.decisionValid, m.decisionExpiresIn, m.decisionAge, m.decisionSources, m.cacheAge, m.outboxPending, m.outboxDropped)
	}
	return m
}

// moduleLabel names the series of a scaler: the platform module it follows, else its own name.
func moduleLabel(s *Scaler) string {
	if s.Module != "" {
		return s.Module
	}
	return s.Name
}

// conflictSource classifies the conflict message of the reconciler.
func conflictSource(conflict string) string {
	switch {
	case strings.HasPrefix(conflict, "HorizontalPodAutoscaler"):
		return "hpa"
	case strings.HasPrefix(conflict, "KEDA"):
		return "keda"
	}
	return "other"
}

// The recording methods are safe on a nil *Metrics.

func (m *Metrics) recordReconcile(ps *Scaler, result string, took time.Duration, current, ready, desired int32, in PlanInput, plan Plan, now time.Time) {
	if m == nil {
		return
	}
	m.reconciles.WithLabelValues(result).Inc()
	m.reconcileDuration.Observe(took.Seconds())
	module := moduleLabel(ps)
	m.current.WithLabelValues(module).Set(float64(current))
	m.ready.WithLabelValues(module).Set(float64(ready))
	if result != "error" {
		m.desired.WithLabelValues(module).Set(float64(desired))
	}
	if in.Conflict != "" {
		m.conflicts.WithLabelValues(conflictSource(in.Conflict)).Inc()
	}
	m.decisionValid.WithLabelValues(module).Set(boolToFloat(plan.DecisionValid))
	if in.Decision != nil && in.Decision.ValidUntil != nil {
		m.decisionExpiresIn.WithLabelValues(module).Set(in.Decision.ValidUntil.Sub(now).Seconds())
	}
	if in.Decision != nil && in.Decision.CreatedAt != nil {
		m.decisionAge.WithLabelValues(module).Set(now.Sub(*in.Decision.CreatedAt).Seconds())
	}
}

// recordDecisionSource counts where a decision came from and sets the age of the remembered one that stood in.
func (m *Metrics) recordDecisionSource(module, source string, age time.Duration) {
	if m == nil {
		return
	}
	m.decisionSources.WithLabelValues(source).Inc()
	m.cacheAge.WithLabelValues(module).Set(age.Seconds())
}

// recordOutbox sets the number of reports waiting for the platform.
func (m *Metrics) recordOutbox(pending int) {
	if m == nil {
		return
	}
	m.outboxPending.Set(float64(pending))
}

// recordOutboxDrop counts reports dropped from the outbox.
func (m *Metrics) recordOutboxDrop(reason string, n int) {
	if m == nil {
		return
	}
	m.outboxDropped.WithLabelValues(reason).Add(float64(n))
}

// recordReconcileFailure counts an evaluation that failed before the target was read, so it has no replicas to report.
func (m *Metrics) recordReconcileFailure(result string, took time.Duration) {
	if m == nil {
		return
	}
	m.reconciles.WithLabelValues(result).Inc()
	m.reconcileDuration.Observe(took.Seconds())
}

func (m *Metrics) recordScale(action Action, result string) {
	if m == nil {
		return
	}
	direction := "up"
	if action == ActionDown {
		direction = "down"
	}
	m.scaleActions.WithLabelValues(direction, result).Inc()
}

func (m *Metrics) recordPlatform(err error, took time.Duration) {
	if m == nil {
		return
	}
	m.platformRequests.WithLabelValues(resultOf(err)).Inc()
	m.platformDuration.Observe(took.Seconds())
}

func resultOf(err error) string {
	if err != nil {
		return "error"
	}
	return "ok"
}

func boolToFloat(b bool) float64 {
	if b {
		return 1
	}
	return 0
}
