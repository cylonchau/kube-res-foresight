package forecastsvc

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/logger"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/prometheus/client_golang/prometheus"
)

// Label cardinality is bounded by the number of modules, the three forecast metrics and the registered
// algorithms; nothing is labelled by pod, time or request.
var (
	descModuleEnabled   = prometheus.NewDesc("kube_res_foresight_module_enabled", "1 when the module is enabled.", []string{"module", "mode"}, nil)
	descModuleBypass    = prometheus.NewDesc("kube_res_foresight_module_bypass", "1 when the module keeps every original value.", []string{"module"}, nil)
	descRecSource       = prometheus.NewDesc("kube_res_foresight_recommendation_source", "1 for the source that decided each resource of the latest recommendation.", []string{"module", "resource", "source"}, nil)
	descRecAge          = prometheus.NewDesc("kube_res_foresight_recommendation_age_seconds", "Age of the latest recommendation.", []string{"module"}, nil)
	descRecValid        = prometheus.NewDesc("kube_res_foresight_recommendation_valid", "1 while the latest recommendation has not expired.", []string{"module"}, nil)
	descSampleAge       = prometheus.NewDesc("kube_res_foresight_series_latest_sample_age_seconds", "Age of the newest stored sample.", []string{"module", "metric"}, nil)
	descPredictionAge   = prometheus.NewDesc("kube_res_foresight_prediction_age_seconds", "Age of the newest prediction run.", []string{"module", "metric", "algorithm"}, nil)
	descDatasourceUp    = prometheus.NewDesc("kube_res_foresight_datasource_up", "1 when the last probe of the datasource succeeded.", []string{"datasource"}, nil)
	descDatasourceDelay = prometheus.NewDesc("kube_res_foresight_datasource_latency_seconds", "Latency of the last probe of the datasource.", []string{"datasource"}, nil)
	descAlgoScore       = prometheus.NewDesc("kube_res_foresight_algorithm_score", "Latest composite score (0-100) of an algorithm instance.", []string{"module", "algorithm"}, nil)
	descAlgoSelected    = prometheus.NewDesc("kube_res_foresight_algorithm_selected", "1 for the algorithm instance in use.", []string{"module", "algorithm"}, nil)
	descEffectiveMax    = prometheus.NewDesc("kube_res_foresight_effective_max_replicas", "Replica ceiling in force (the fixed maximum, or the dynamically expanded one).", []string{"module"}, nil)
	descHardMax         = prometheus.NewDesc("kube_res_foresight_hard_max_replicas", "Safety ceiling the dynamic expansion never passes.", []string{"module"}, nil)
	descConsecutiveFail = prometheus.NewDesc("kube_res_foresight_consecutive_failures", "Pipeline cycles in a row that failed.", []string{"module"}, nil)
	descCollectorError  = prometheus.NewDesc("kube_res_foresight_forecast_metrics_error", "1 when the last scrape of forecast state failed.", nil, nil)
)

// maxMetricModules bounds the modules exported in one scrape.
const maxMetricModules = 500

// Collector exports the forecasting state of every module. It reads the database on scrape and caches the
// result briefly so a burst of scrapes costs one set of queries.
type Collector struct {
	svc *Service
	ttl time.Duration

	mu      sync.Mutex
	at      time.Time
	cached  []prometheus.Metric
	lastErr bool
}

// NewCollector builds the collector; register it on the process registry.
func NewCollector(svc *Service) *Collector { return &Collector{svc: svc, ttl: 10 * time.Second} }

// Describe sends nothing: the collector is unchecked because the series set depends on the data.
func (c *Collector) Describe(chan<- *prometheus.Desc) {}

func (c *Collector) Collect(ch chan<- prometheus.Metric) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cached == nil || c.svc.now().Sub(c.at) > c.ttl {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		metrics, err := c.build(ctx)
		if err != nil {
			logger.Warn("forecast metrics scrape failed", logger.Err(err))
			c.lastErr = true
		} else {
			c.lastErr, c.cached, c.at = false, metrics, c.svc.now()
		}
	}
	for _, m := range c.cached {
		ch <- m
	}
	failed := 0.0
	if c.lastErr {
		failed = 1
	}
	ch <- prometheus.MustNewConstMetric(descCollectorError, prometheus.GaugeValue, failed)
}

func boolFloat(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

func (c *Collector) build(ctx context.Context) ([]prometheus.Metric, error) {
	modules, _, err := c.svc.Modules.List(ctx, moduleFilterAll, 1, maxMetricModules)
	if err != nil {
		return nil, err
	}
	now := c.svc.now()
	var out []prometheus.Metric
	gauge := func(d *prometheus.Desc, v float64, labels ...string) {
		out = append(out, prometheus.MustNewConstMetric(d, prometheus.GaugeValue, v, labels...))
	}
	datasources, err := c.svc.Datasources.ListEnabled(ctx)
	if err != nil {
		return nil, err
	}
	for _, ds := range datasources {
		gauge(descDatasourceUp, boolFloat(ds.Status == model.DatasourceStatusOK), ds.Name)
		if ds.LastCheckedAt != nil {
			gauge(descDatasourceDelay, float64(ds.LastLatencyMs)/1000, ds.Name)
		}
	}
	for _, m := range modules {
		gauge(descModuleEnabled, boolFloat(m.Enabled), m.Name, m.Mode)
		gauge(descModuleBypass, boolFloat(m.Bypass), m.Name)

		if rec, err := c.svc.Predictions.LatestRecommendation(ctx, m.ID); err != nil {
			return nil, err
		} else if rec != nil {
			fields := []struct {
				resource string
				set      bool
				value    float64
				source   string
			}{
				{"cpu_millicores", rec.CPUSet, float64(rec.CPUMilli), rec.CPUSource},
				{"memory_bytes", rec.MemorySet, float64(rec.MemoryBytes), rec.MemorySource},
				{"replicas", rec.ReplicasSet, float64(rec.Replicas), rec.ReplicasSource},
				{"gpu", rec.GPUSet, float64(rec.GPU), rec.GPUSource},
			}
			for _, f := range fields {
				gauge(descRecSource, 1, m.Name, f.resource, f.source)
			}
			gauge(descRecAge, now.Sub(rec.CreatedAt).Seconds(), m.Name)
			gauge(descRecValid, boolFloat(now.Before(rec.ValidUntil)), m.Name)
		}

		scores, err := c.svc.Predictions.LatestScores(ctx, m.ID)
		if err != nil {
			return nil, err
		}
		state, err := c.svc.RunStates.RunState(ctx, m.ID)
		if err != nil {
			return nil, err
		}
		for name, sc := range scores {
			gauge(descAlgoScore, sc.Score, m.Name, name)
			gauge(descAlgoSelected, boolFloat(name == state.Selected), m.Name, name)
		}
		effective := m.MaxReplicas
		if m.MaxReplicasMode == model.MaxReplicasDynamic && state.EffectiveMaxReplicas > effective {
			effective = min(state.EffectiveMaxReplicas, max(m.HardMaxReplicas, m.MaxReplicas))
		}
		gauge(descEffectiveMax, float64(effective), m.Name)
		if m.HardMaxReplicas > 0 {
			gauge(descHardMax, float64(m.HardMaxReplicas), m.Name)
		}
		gauge(descConsecutiveFail, float64(state.ConsecutiveFailures), m.Name)

		// what the module reads and forecasts, and what it executes
		for _, metric := range configuredMetrics(dataOf(m)) {
			if series, err := c.svc.Samples.Series(ctx, m.ID, metric); err == nil {
				if latest, ok, err := c.svc.Samples.Latest(ctx, series.ID); err != nil {
					return nil, err
				} else if ok {
					gauge(descSampleAge, now.Sub(latest).Seconds(), m.Name, metric)
				}
			}
			algos, err := c.svc.Predictions.AlgorithmsWithPredictions(ctx, m.ID, metric)
			if err != nil {
				return nil, err
			}
			for _, algo := range algos {
				if rows, err := c.svc.Predictions.LatestRun(ctx, m.ID, metric, algo); err != nil {
					return nil, err
				} else if len(rows) > 0 {
					gauge(descPredictionAge, now.Sub(rows[0].PredictionTime).Seconds(), m.Name, metric, algo)
				}
			}
		}
		if err := c.exportCatalog(ctx, now, m.ID, m.Name, gauge); err != nil {
			return nil, err
		}
	}
	return out, nil
}

var exportDescs = map[string]*prometheus.Desc{}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// exportDesc returns the descriptor of a catalog export. Metrics that share an exported name share the label
// names, so one descriptor serves all of them.
func exportDesc(def MetricDef) *prometheus.Desc {
	exportDescsMu.Lock()
	defer exportDescsMu.Unlock()
	if d, ok := exportDescs[def.Export.Name]; ok {
		return d
	}
	d := prometheus.NewDesc(def.Export.Name, def.Help.EN+" (newest value; see the metric catalog).", def.Export.labelNames(def), nil)
	exportDescs[def.Export.Name] = d
	return d
}

var exportDescsMu sync.Mutex

// Ready reports whether the forecasting store can be read; it backs /readyz.
func (s *Service) Ready(ctx context.Context) error {
	_, _, err := s.Modules.List(ctx, moduleFilterAll, 1, 1)
	return err
}

// exportCatalog exports the newest value of every series of the catalog metrics of one module, under the names the
// catalog declares.
func (c *Collector) exportCatalog(ctx context.Context, now time.Time, ownerID uint, ownerName string, gauge func(*prometheus.Desc, float64, ...string)) error {
	for _, def := range Catalog() {
		if def.Export == nil {
			continue
		}
		points, err := c.svc.latest(ctx, def, ownerID, now)
		if err != nil {
			return err
		}
		desc := exportDesc(def)
		for _, p := range points {
			if p.labels["quantile"] != "" {
				continue // quantile forecasts would collide with the point forecast of the same algorithm
			}
			values := []string{ownerName}
			for _, l := range def.Labels {
				if l != "quantile" {
					values = append(values, p.labels[l])
				}
			}
			for _, k := range sortedKeys(def.Export.Fixed) {
				values = append(values, def.Export.Fixed[k])
			}
			gauge(desc, p.value, values...)
		}
	}
	return nil
}
