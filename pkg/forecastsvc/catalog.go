package forecastsvc

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/cylonchau/kube-resource-fcst/pkg/dao/rdb"
	"github.com/cylonchau/kube-resource-fcst/pkg/model"
)

// Metric groups of the catalog.
const (
	GroupUsage       = "usage"       // what the workload used, as collected
	GroupPrediction  = "prediction"  // what the algorithms forecast
	GroupPerformance = "performance" // how right the forecasts were
	GroupExecution   = "execution"   // what was recommended and what the operator did
)

// Aggregations applied to the points that fall into one step of a query.
const (
	AggAvg  = "avg"
	AggMax  = "max"
	AggLast = "last"
)

// Text is a bilingual string.
type Text struct {
	ZH string `json:"zh"`
	EN string `json:"en"`
}

// Export says how a catalog metric appears on /metrics: the newest value of each series, under a Prometheus
// name, with the catalog labels plus the fixed ones. The query API and /metrics read the same stored rows, so
// the last point of a query equals the exported value (a test asserts it).
type Export struct {
	Name  string            `json:"name"`
	Fixed map[string]string `json:"fixed,omitempty"`
	// Window is how far back the newest value is looked up; older data is not exported.
	Window time.Duration `json:"-"`
}

// MetricDef describes one queryable forecasting metric. The catalog is the single source: the query API, the
// metrics page and the /metrics exposition are all derived from it.
type MetricDef struct {
	Name string `json:"name"`
	// Group is one of the Group constants.
	Group string `json:"group"`
	Help  Text   `json:"help"`
	// Unit is the unit of the values; for the prediction group it is the unit of the forecast metric.
	Unit string `json:"unit"`
	// Labels are the dimensions a query result is split by, besides the module.
	Labels []string `json:"labels"`
	// Table is the table the values are read from.
	Table string `json:"table"`
	// Time says which time the points are placed at.
	Time string `json:"time"`
	// Agg is how points inside a step are combined.
	Agg string `json:"agg"`
	// RetentionDays is how long the source rows are kept; 0 means the module's lookback_hours.
	RetentionDays int `json:"retention_days"`
	// Value says what one point means.
	Value  Text    `json:"value"`
	Export *Export `json:"export,omitempty"`
	load   loader
}

// rawPoint is one stored value with its dimension values.
type rawPoint struct {
	labels map[string]string
	ts     time.Time
	value  float64
}

// loadParams are what a loader reads: every metric belongs to a module.
type loadParams struct {
	ownerID   uint
	metric    string // the source key filter
	algorithm string
	from, to  time.Time
}

type loader func(ctx context.Context, s *Service, p loadParams) ([]rawPoint, bool, error)

var retention = rdb.DefaultRetention

func days(d time.Duration) int { return int(d / (24 * time.Hour)) }

const (
	table1d = 24 * time.Hour
	table7d = 7 * table1d
)

// evaluation-based metrics share one loader shape.
func evaluationMetric(name string, help, value Text, unit string, pick func(model.PredictionEvaluation) float64, export string) MetricDef {
	d := MetricDef{
		Name: name, Group: GroupPerformance, Help: help, Value: value, Unit: unit, Labels: []string{"metric", "algorithm"},
		Table: "prediction_evaluation", Time: "prediction_time", Agg: AggAvg, RetentionDays: days(retention.Evaluations),
		load: func(ctx context.Context, s *Service, p loadParams) ([]rawPoint, bool, error) {
			rows, truncated, err := s.Predictions.EvaluationsBetween(ctx, p.ownerID, p.metric, p.algorithm, p.from, p.to)
			out := make([]rawPoint, 0, len(rows))
			for _, r := range rows {
				out = append(out, rawPoint{labels: map[string]string{"metric": r.Metric, "algorithm": r.AlgorithmID}, ts: r.PredictionTime, value: pick(r)})
			}
			return out, truncated, err
		},
	}
	if export != "" {
		d.Export = &Export{Name: export, Window: table7d}
	}
	return d
}

func backtestMetric(name string, help, value Text, pick func(model.BacktestRun, backtestScore) (float64, bool), export string) MetricDef {
	d := MetricDef{
		Name: name, Group: GroupPerformance, Help: help, Value: value, Unit: "ratio", Labels: []string{"metric", "algorithm"},
		Table: "backtest_run", Time: "created_at", Agg: AggAvg, RetentionDays: days(retention.Backtests),
		load: func(ctx context.Context, s *Service, p loadParams) ([]rawPoint, bool, error) {
			rows, truncated, err := s.Predictions.BacktestsBetween(ctx, p.ownerID, p.metric, p.algorithm, p.from, p.to)
			out := make([]rawPoint, 0, len(rows))
			for _, r := range rows {
				var score backtestScore
				_ = json.Unmarshal([]byte(r.Metrics), &score)
				if v, ok := pick(r, score); ok {
					out = append(out, rawPoint{labels: map[string]string{"metric": r.Metric, "algorithm": r.AlgorithmID}, ts: r.CreatedAt, value: v})
				}
			}
			return out, truncated, err
		},
	}
	if export != "" {
		d.Export = &Export{Name: export, Window: table7d}
	}
	return d
}

// backtestScore is the part of a stored backtest result the catalog reads.
type backtestScore struct {
	WAPE      float64 `json:"wape"`
	PeakUnder float64 `json:"peak_under"`
	Points    int     `json:"points"`
	Skipped   string  `json:"skipped"`
}

func usageMetric(name, metric, unit string, help Text) MetricDef {
	return MetricDef{
		Name: name, Group: GroupUsage, Help: help, Value: Text{"采集到的原始值", "the collected value"}, Unit: unit, Labels: []string{},
		Table: "metric_sample", Time: "ts", Agg: AggAvg, RetentionDays: 0,
		Export: &Export{Name: "kube_resource_fcst_usage_value", Fixed: map[string]string{"metric": metric}, Window: 3 * time.Hour},
		load: func(ctx context.Context, s *Service, p loadParams) ([]rawPoint, bool, error) {
			series, err := s.Samples.Series(ctx, p.ownerID, metric)
			if err != nil {
				return nil, false, nil // nothing collected yet is an empty answer, not an error
			}
			rows, err := s.Samples.Range(ctx, series.ID, p.from, p.to)
			out := make([]rawPoint, 0, len(rows))
			for _, r := range rows {
				out = append(out, rawPoint{labels: map[string]string{}, ts: time.Unix(r.TS, 0).UTC(), value: r.Value})
			}
			return out, false, err
		},
	}
}

func recommendationMetric(name, resource, unit string, help Text, pick func(model.Recommendation) (float64, bool)) MetricDef {
	return MetricDef{
		Name: name, Group: GroupExecution, Help: help, Value: Text{"该版本推荐的值；保留原值时没有点", "the value of that version; no point when the original is kept"},
		Unit: unit, Labels: []string{}, Table: "recommendation", Time: "created_at", Agg: AggLast, RetentionDays: days(retention.Recommendations),
		Export: &Export{Name: "kube_resource_fcst_recommendation_value", Fixed: map[string]string{"resource": resource}, Window: table1d},
		load: func(ctx context.Context, s *Service, p loadParams) ([]rawPoint, bool, error) {
			rows, truncated, err := s.Predictions.RecommendationsBetween(ctx, p.ownerID, p.from, p.to)
			out := make([]rawPoint, 0, len(rows))
			for _, r := range rows {
				if v, ok := pick(r); ok {
					out = append(out, rawPoint{labels: map[string]string{}, ts: r.CreatedAt, value: v})
				}
			}
			return out, truncated, err
		},
	}
}

func scalingMetric(name, state string, help Text, pick func(model.ScalingExecution) int) MetricDef {
	return MetricDef{
		Name: name, Group: GroupExecution, Help: help, Value: Text{"operator 上报时的副本数", "replica count at the operator report"},
		Unit: "count", Labels: []string{}, Table: "scaling_execution", Time: "created_at", Agg: AggLast, RetentionDays: 90,
		Export: &Export{Name: "kube_resource_fcst_scaling_replicas", Fixed: map[string]string{"state": state}, Window: table1d},
		load: func(ctx context.Context, s *Service, p loadParams) ([]rawPoint, bool, error) {
			rows, truncated, err := s.Predictions.ExecutionsBetween(ctx, p.ownerID, p.from, p.to)
			out := make([]rawPoint, 0, len(rows))
			for _, r := range rows {
				out = append(out, rawPoint{labels: map[string]string{}, ts: r.CreatedAt, value: float64(pick(r))})
			}
			return out, truncated, err
		},
	}
}

func quantileLabel(permille int) string {
	if permille == 0 || permille == 500 {
		return ""
	}
	return strconv.Itoa(permille)
}

// catalog is the list of every forecasting metric. Add a metric here and it is queryable, shown on the metrics
// page and, when it has an Export, scraped from /metrics.
var catalog = []MetricDef{
	{
		Name: "usage_source", Group: GroupUsage, Help: Text{"任意指标来源的采集值", "The collected value of any metric source"},
		Value: Text{"按来源键读取；单位随来源类型", "read by source key; the unit follows the source kind"}, Unit: "source", Labels: []string{"metric"},
		Table: "metric_sample", Time: "ts", Agg: AggAvg, RetentionDays: 0,
		load: func(ctx context.Context, s *Service, p loadParams) ([]rawPoint, bool, error) {
			series, err := s.Samples.Series(ctx, p.ownerID, p.metric)
			if err != nil {
				return nil, false, nil
			}
			rows, err := s.Samples.Range(ctx, series.ID, p.from, p.to)
			out := make([]rawPoint, 0, len(rows))
			for _, r := range rows {
				out = append(out, rawPoint{labels: map[string]string{"metric": p.metric}, ts: time.Unix(r.TS, 0).UTC(), value: r.Value})
			}
			return out, false, err
		},
	},
	usageMetric("usage_cpu_cores", model.MetricCPU, "cores", Text{"CPU 使用量", "CPU usage"}),
	usageMetric("usage_memory_bytes", model.MetricMemory, "bytes", Text{"内存使用量", "Memory usage"}),
	usageMetric("usage_rps", model.MetricRPS, "rps", Text{"请求速率", "Request rate"}),
	usageMetric("usage_replicas", model.MetricReplicas, "count", Text{"Ready 副本数", "Ready replicas"}),
	usageMetric("usage_latency_seconds", "latency", "seconds", Text{"请求延迟", "Request latency"}),
	usageMetric("usage_oom_events", "oom", "count", Text{"OOM 次数", "OOM events"}),
	usageMetric("usage_oom_working_set_bytes", "oom_working_set", "bytes", Text{"内存工作集峰值", "Memory working-set peak"}),
	usageMetric("usage_jvm_heap_used_bytes", "jvm_heap_used", "bytes", Text{"JVM 堆已用", "JVM heap used"}),
	usageMetric("usage_jvm_heap_max_bytes", "jvm_heap_max", "bytes", Text{"JVM 堆上限", "JVM heap maximum"}),
	usageMetric("usage_jvm_nonheap_used_bytes", "jvm_nonheap_used", "bytes", Text{"JVM 非堆内存已用", "JVM non-heap used"}),
	usageMetric("usage_jvm_gc_pause_seconds", "jvm_gc_pause", "seconds", Text{"JVM GC 暂停时长", "JVM GC pause time"}),
	usageMetric("usage_jvm_threads", "jvm_threads", "count", Text{"JVM 存活线程数", "JVM live threads"}),
	usageMetric("usage_k8s_cpu_requests_cores", "k8s_cpu_requests", "cores", Text{"CPU Requests", "CPU requests"}),
	usageMetric("usage_k8s_cpu_limits_cores", "k8s_cpu_limits", "cores", Text{"CPU Limits", "CPU limits"}),
	usageMetric("usage_k8s_memory_requests_bytes", "k8s_mem_requests", "bytes", Text{"内存 Requests", "Memory requests"}),
	usageMetric("usage_k8s_memory_limits_bytes", "k8s_mem_limits", "bytes", Text{"内存 Limits", "Memory limits"}),
	usageMetric("usage_k8s_cpu_throttled_seconds", "k8s_cpu_throttle", "seconds", Text{"CPU 被限流时长", "CPU throttled time"}),
	{
		Name: "prediction_value", Group: GroupPrediction, Help: Text{"预测值", "Forecast value"},
		Value: Text{"每个目标时间取最新一次预测；分位数预测带 quantile 标签", "per target time, the newest prediction; quantile forecasts carry a quantile label"},
		Unit:  "metric", Labels: []string{"metric", "algorithm", "quantile"}, Table: "forecast_prediction", Time: "target_time", Agg: AggAvg,
		RetentionDays: days(retention.Predictions),
		load: func(ctx context.Context, s *Service, p loadParams) ([]rawPoint, bool, error) {
			rows, truncated, err := s.Predictions.PredictionsBetween(ctx, p.ownerID, p.metric, p.algorithm, p.from, p.to)
			if err != nil {
				return nil, false, err
			}
			type key struct {
				metric, algorithm string
				quantile          int
				target            int64
			}
			// rows come oldest prediction first, so the last write per key is the newest prediction
			latest := map[key]model.Prediction{}
			for _, r := range rows {
				latest[key{r.Metric, r.AlgorithmID, r.QuantilePermille, r.TargetTime.Unix()}] = r
			}
			out := make([]rawPoint, 0, len(latest))
			for _, r := range latest {
				labels := map[string]string{"metric": r.Metric, "algorithm": r.AlgorithmID}
				if q := quantileLabel(r.QuantilePermille); q != "" {
					labels["quantile"] = q
				}
				out = append(out, rawPoint{labels: labels, ts: r.TargetTime, value: r.Value})
			}
			return out, truncated, nil
		},
	},
	{
		Name: "prediction_peak", Group: GroupPrediction, Help: Text{"预测峰值", "Forecast peak"},
		Value: Text{"每次预测运行在其整个 horizon 内的最大值，放在预测时间", "the maximum of each prediction run over its horizon, placed at the prediction time"},
		Unit:  "metric", Labels: []string{"metric", "algorithm", "quantile"}, Table: "forecast_prediction", Time: "prediction_time", Agg: AggMax,
		RetentionDays: days(retention.Predictions),
		Export:        &Export{Name: "kube_resource_fcst_prediction_peak", Window: 6 * time.Hour},
		load: func(ctx context.Context, s *Service, p loadParams) ([]rawPoint, bool, error) {
			// the stored rows are keyed by target time; a run is every row sharing a prediction time
			from := p.from
			rows, truncated, err := s.Predictions.PredictionsBetween(ctx, p.ownerID, p.metric, p.algorithm, from, p.to.Add(24*time.Hour))
			if err != nil {
				return nil, false, err
			}
			type key struct {
				metric, algorithm string
				quantile          int
				at                int64
			}
			peaks := map[key]rawPoint{}
			for _, r := range rows {
				if r.PredictionTime.Before(p.from) || r.PredictionTime.After(p.to) {
					continue
				}
				k := key{r.Metric, r.AlgorithmID, r.QuantilePermille, r.PredictionTime.Unix()}
				if cur, ok := peaks[k]; !ok || r.Value > cur.value {
					labels := map[string]string{"metric": r.Metric, "algorithm": r.AlgorithmID}
					if q := quantileLabel(r.QuantilePermille); q != "" {
						labels["quantile"] = q
					}
					peaks[k] = rawPoint{labels: labels, ts: r.PredictionTime, value: r.Value}
				}
			}
			out := make([]rawPoint, 0, len(peaks))
			for _, pt := range peaks {
				out = append(out, pt)
			}
			return out, truncated, nil
		},
	},
	evaluationMetric("evaluation_wape", Text{"实测 WAPE", "Measured WAPE"}, Text{"每个已评估的预测运行一个点", "one point per evaluated prediction run"}, "ratio",
		func(e model.PredictionEvaluation) float64 { return e.WAPE }, "kube_resource_fcst_prediction_wape"),
	evaluationMetric("evaluation_mae", Text{"实测 MAE", "Measured MAE"}, Text{"每个已评估的预测运行一个点", "one point per evaluated prediction run"}, "metric",
		func(e model.PredictionEvaluation) float64 { return e.MAE }, "kube_resource_fcst_prediction_mae"),
	evaluationMetric("evaluation_bias", Text{"实测偏差", "Measured bias"}, Text{"预测减实测的均值，正数为高估", "mean of prediction minus actual; positive = over-prediction"}, "metric",
		func(e model.PredictionEvaluation) float64 { return e.Bias }, "kube_resource_fcst_prediction_bias"),
	evaluationMetric("evaluation_peak_under", Text{"实测峰值低估", "Measured peak underestimation"}, Text{"预测峰值比实测峰值低的相对幅度", "relative shortfall of the predicted peak"}, "ratio",
		func(e model.PredictionEvaluation) float64 { return e.PeakUnder }, "kube_resource_fcst_prediction_peak_under"),
	evaluationMetric("evaluation_coverage", Text{"实测覆盖率", "Measured coverage"}, Text{"实测值不超过预测值的占比", "share of actuals at or below the forecast"}, "ratio",
		func(e model.PredictionEvaluation) float64 { return e.Coverage }, "kube_resource_fcst_prediction_coverage"),
	evaluationMetric("evaluation_points", Text{"实测周期数", "Measured steps"}, Text{"该预测运行中有实测值的步数", "forecast steps that had an actual"}, "count",
		func(e model.PredictionEvaluation) float64 { return float64(e.Points) }, "kube_resource_fcst_prediction_points"),
	backtestMetric("backtest_wape", Text{"回测 WAPE", "Backtest WAPE"}, Text{"每次回测一个点", "one point per backtest run"},
		func(_ model.BacktestRun, s backtestScore) (float64, bool) {
			return s.WAPE, s.Skipped == "" && s.Points > 0
		}, "kube_resource_fcst_backtest_wape"),
	backtestMetric("backtest_peak_under", Text{"回测峰值低估", "Backtest peak underestimation"}, Text{"每次回测一个点", "one point per backtest run"},
		func(_ model.BacktestRun, s backtestScore) (float64, bool) {
			return s.PeakUnder, s.Skipped == "" && s.Points > 0
		}, "kube_resource_fcst_backtest_peak_under"),
	backtestMetric("backtest_promoted", Text{"回测晋升", "Backtest promotion"}, Text{"1 表示该次回测晋升了算法", "1 when the run promoted the algorithm"},
		func(r model.BacktestRun, _ backtestScore) (float64, bool) { return boolFloat(r.Promoted), true }, "kube_resource_fcst_backtest_promoted"),
	recommendationMetric("recommendation_replicas", "replicas", "count", Text{"推荐副本数", "Recommended replicas"},
		func(r model.Recommendation) (float64, bool) { return float64(r.Replicas), r.ReplicasSet }),
	recommendationMetric("recommendation_cpu_millicores", "cpu_millicores", "millicores", Text{"推荐 CPU", "Recommended CPU"},
		func(r model.Recommendation) (float64, bool) { return float64(r.CPUMilli), r.CPUSet }),
	recommendationMetric("recommendation_memory_bytes", "memory_bytes", "bytes", Text{"推荐内存", "Recommended memory"},
		func(r model.Recommendation) (float64, bool) { return float64(r.MemoryBytes), r.MemorySet }),
	recommendationMetric("recommendation_degraded", "degraded", "flag", Text{"推荐是否降级", "Recommendation degraded"},
		func(r model.Recommendation) (float64, bool) { return boolFloat(r.Degraded), true }),
	recommendationMetric("recommendation_oom_floor", "oom_floor", "flag", Text{"推荐是否被 OOM 地板抬高", "Recommendation raised by the OOM floor"},
		func(r model.Recommendation) (float64, bool) { return boolFloat(r.OOMFloor), true }),
	scalingMetric("scaling_desired", "desired", Text{"期望副本数", "Desired replicas"}, func(e model.ScalingExecution) int { return e.Desired }),
	scalingMetric("scaling_current", "current", Text{"当前副本数", "Current replicas"}, func(e model.ScalingExecution) int { return e.Current }),
	scalingMetric("scaling_ready", "ready", Text{"Ready 副本数", "Ready replicas"}, func(e model.ScalingExecution) int { return e.Ready }),
}

func init() {
	sort.SliceStable(catalog, func(i, j int) bool { return groupRank(catalog[i].Group) < groupRank(catalog[j].Group) })
}

func groupRank(g string) int {
	for i, name := range []string{GroupUsage, GroupPrediction, GroupPerformance, GroupExecution} {
		if g == name {
			return i
		}
	}
	return 99
}

// Catalog returns the metric definitions.
func Catalog() []MetricDef {
	out := make([]MetricDef, len(catalog))
	copy(out, catalog)
	return out
}

// LookupMetric finds a definition by name.
func LookupMetric(name string) (MetricDef, bool) {
	for _, d := range catalog {
		if d.Name == name {
			return d, true
		}
	}
	return MetricDef{}, false
}

// exportLabels are the label names of an exported metric: the module, the catalog labels
// without the optional quantile, then the fixed ones in a stable order.
func (e *Export) labelNames(d MetricDef) []string {
	names := []string{"module"}
	for _, l := range d.Labels {
		if l != "quantile" {
			names = append(names, l)
		}
	}
	fixed := make([]string, 0, len(e.Fixed))
	for k := range e.Fixed {
		fixed = append(fixed, k)
	}
	sort.Strings(fixed)
	return append(names, fixed...)
}

// latest returns the newest value of every series of the metric for an owner, at most window old.
func (s *Service) latest(ctx context.Context, d MetricDef, ownerID uint, now time.Time) ([]rawPoint, error) {
	if d.Export == nil {
		return nil, fmt.Errorf("metric %s is not exported", d.Name)
	}
	points, _, err := d.load(ctx, s, loadParams{ownerID: ownerID, from: now.Add(-d.Export.Window), to: now})
	if err != nil {
		return nil, err
	}
	newest := map[string]rawPoint{}
	for _, p := range points {
		if math.IsNaN(p.value) || math.IsInf(p.value, 0) {
			continue
		}
		k := labelKey(p.labels)
		if cur, ok := newest[k]; !ok || p.ts.After(cur.ts) {
			newest[k] = p
		}
	}
	out := make([]rawPoint, 0, len(newest))
	for _, p := range newest {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return labelKey(out[i].labels) < labelKey(out[j].labels) })
	return out, nil
}

func labelKey(labels map[string]string) string {
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(labels[k])
		b.WriteByte(',')
	}
	return b.String()
}
