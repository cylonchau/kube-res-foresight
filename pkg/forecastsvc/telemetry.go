package forecastsvc

import (
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Telemetry holds the counters and histograms the pipeline updates while it works. They complement the state
// gauges of the Collector: those say what is stored now, these say how the work has gone. Labels are bounded
// to the forecast metric kind, the registered algorithms and a fixed set of results.
type Telemetry struct {
	collectRuns      *prometheus.CounterVec
	collectDuration  *prometheus.HistogramVec
	predictRuns      *prometheus.CounterVec
	predictDuration  *prometheus.HistogramVec
	backtestRuns     *prometheus.CounterVec
	backtestDuration *prometheus.HistogramVec
	evaluationRuns   *prometheus.CounterVec
	evaluationDelay  *prometheus.HistogramVec
	recommendRuns    *prometheus.CounterVec
	fallbackRuns     *prometheus.CounterVec
}

var durationBuckets = []float64{0.01, 0.05, 0.1, 0.5, 1, 2.5, 5, 15, 60, 300}

// delayBuckets span a minute to a day: the delay between the end of a forecast horizon and its evaluation.
var delayBuckets = []float64{60, 300, 900, 1800, 3600, 7200, 21600, 43200, 86400}

// NewTelemetry builds the instruments; register them with Collectors.
func NewTelemetry() *Telemetry {
	return &Telemetry{
		collectRuns: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "kube_res_foresight_collect_runs_total",
			Help: "History collections by forecast metric and result (ok, error)."}, []string{"metric", "result"}),
		collectDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "kube_res_foresight_collect_duration_seconds",
			Help: "Time to collect the history of one metric of one module.", Buckets: durationBuckets}, []string{"metric"}),
		predictRuns: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "kube_res_foresight_predict_runs_total",
			Help: "Prediction runs by metric, algorithm and result (ok, skipped, failed)."}, []string{"metric", "algorithm", "result"}),
		predictDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "kube_res_foresight_predict_duration_seconds",
			Help: "Time one algorithm took to forecast.", Buckets: durationBuckets}, []string{"metric", "algorithm"}),
		backtestRuns: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "kube_res_foresight_backtest_runs_total",
			Help: "Backtests by metric, algorithm and result (promoted, rejected, skipped, failed)."}, []string{"metric", "algorithm", "result"}),
		backtestDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "kube_res_foresight_backtest_duration_seconds",
			Help: "Time one backtest pass over a metric took, all algorithms together.", Buckets: durationBuckets}, []string{"metric"}),
		evaluationRuns: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "kube_res_foresight_evaluation_runs_total",
			Help: "Prediction runs scored against actuals, by metric and result (scored, no_actuals)."}, []string{"metric", "result"}),
		evaluationDelay: prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "kube_res_foresight_evaluation_delay_seconds",
			Help: "How long after the end of its horizon a prediction run was scored.", Buckets: delayBuckets}, []string{"metric"}),
		recommendRuns: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "kube_res_foresight_recommend_runs_total",
			Help: "Recommendations by result (ok, degraded: a field fell back, oom_floor: memory raised by OOM protection)."}, []string{"result"}),
		fallbackRuns: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "kube_res_foresight_fallback_total",
			Help: "Recommendation fields that did not come from a usable forecast, by reason code and field."}, []string{"reason", "field"}),
	}
}

var (
	sharedTelemetry     *Telemetry
	sharedTelemetryOnce sync.Once
)

// SharedTelemetry is the process-wide instrument set. Every Service built by New records into it, so the
// scheduled tasks and the manual runs of the HTTP handlers land in the same counters; the server registers it
// once on the metrics registry.
func SharedTelemetry() *Telemetry {
	sharedTelemetryOnce.Do(func() { sharedTelemetry = NewTelemetry() })
	return sharedTelemetry
}

// Collectors returns the instruments for registration.
func (t *Telemetry) Collectors() []prometheus.Collector {
	return []prometheus.Collector{t.collectRuns, t.collectDuration, t.predictRuns, t.predictDuration, t.backtestRuns,
		t.backtestDuration, t.evaluationRuns, t.evaluationDelay, t.recommendRuns, t.fallbackRuns}
}

// The recording methods are safe on a nil Telemetry so the pipeline works without metrics.

func (t *Telemetry) recordCollect(metric string, took time.Duration, err error) {
	if t == nil {
		return
	}
	result := "ok"
	if err != nil {
		result = "error"
	}
	t.collectRuns.WithLabelValues(metric, result).Inc()
	t.collectDuration.WithLabelValues(metric).Observe(took.Seconds())
}

func (t *Telemetry) recordRecommend(degraded, oomFloor bool) {
	if t == nil {
		return
	}
	result := "ok"
	if degraded {
		result = "degraded"
	}
	t.recommendRuns.WithLabelValues(result).Inc()
	if oomFloor {
		t.recommendRuns.WithLabelValues("oom_floor").Inc()
	}
}

func (t *Telemetry) recordFallback(reason, field string) {
	if t == nil {
		return
	}
	t.fallbackRuns.WithLabelValues(reason, field).Inc()
}

func (t *Telemetry) recordPredict(metric, algorithm, result string, took time.Duration) {
	if t == nil {
		return
	}
	t.predictRuns.WithLabelValues(metric, algorithm, result).Inc()
	t.predictDuration.WithLabelValues(metric, algorithm).Observe(took.Seconds())
}

func (t *Telemetry) recordBacktest(metric, algorithm, result string) {
	if t == nil {
		return
	}
	t.backtestRuns.WithLabelValues(metric, algorithm, result).Inc()
}

func (t *Telemetry) recordBacktestPass(metric string, took time.Duration) {
	if t == nil {
		return
	}
	t.backtestDuration.WithLabelValues(metric).Observe(took.Seconds())
}

func (t *Telemetry) recordEvaluation(metric, result string, delay time.Duration) {
	if t == nil {
		return
	}
	t.evaluationRuns.WithLabelValues(metric, result).Inc()
	if result == "scored" && delay >= 0 {
		t.evaluationDelay.WithLabelValues(metric).Observe(delay.Seconds())
	}
}
