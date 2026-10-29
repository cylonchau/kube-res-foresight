// Package forecastsvc orchestrates the forecasting pipeline for a module (a Kubernetes workload): collect the
// metrics of its sources from Prometheus, run every enabled algorithm on them, backtest and evaluate the algorithms,
// score and rank them, and turn the forecast of the best one into a recommendation. Algorithms are configured once for
// the whole platform (see model.AlgorithmConfig) and applied to every module automatically. Both the background
// tasks and the HTTP handlers call it, so a manual run and a scheduled run do exactly the same thing.
package forecastsvc

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/cylonchau/kube-resource-fcst/pkg/collector"
	"github.com/cylonchau/kube-resource-fcst/pkg/dao/rdb"
	"github.com/cylonchau/kube-resource-fcst/pkg/demo"
	"github.com/cylonchau/kube-resource-fcst/pkg/forecast"
	"github.com/cylonchau/kube-resource-fcst/pkg/model"
	"github.com/cylonchau/kube-resource-fcst/pkg/promclient"
)

// ErrNotConfigured means the module lacks something the step needs (a source, a mapping, a datasource, an enabled
// algorithm, history).
var ErrNotConfigured = errors.New("not configured for this step")

// ErrPredictionOff means the module does not forecast: the prediction is switched off, or its mode is "none". Nothing is wrong; the
// module runs on its fixed default configuration.
var ErrPredictionOff = errors.New("the prediction of the module is off")

// QuerierFactory builds the range querier of a datasource; tests replace it.
type QuerierFactory func(ds *model.Datasource) (collector.RangeQuerier, error)

// Service wires the DAOs of the pipeline.
type Service struct {
	Modules     *rdb.ModuleDAO
	Algorithms  *rdb.AlgorithmDAO
	Mappings    *rdb.MetricMappingDAO
	Datasources *rdb.DatasourceDAO
	Samples     *rdb.SampleDAO
	// Config reads the system parameters the pipeline follows (the Pod resource spec policy); nil keeps the defaults.
	Config      *rdb.SysConfigDAO
	Predictions *rdb.PredictionDAO
	// RunStates holds what the scheduled pipeline remembers per module; built from the module DAO's handle.
	RunStates *rdb.RunStateDAO

	// Telemetry is optional: counters and histograms of the pipeline work.
	Telemetry *Telemetry

	NewQuerier QuerierFactory
	Now        func() time.Time
	// FetchOptions overrides the request budget; the datasource concurrency is applied on top.
	FetchOptions collector.Options

	// refreshes and pipelines are per module (see FreshRecommendation and pipelineLock); the zero value is ready to use.
	refreshes sync.Map
	pipelines sync.Map
	// tunings holds the series whose fine-tune is under way, so one series is never submitted twice at once.
	tunings sync.Map
	// TuneInline runs a fine-tune inside the forecast that asked for it instead of in the background (tests).
	TuneInline bool
}

// New builds a service on the shared database handle.
func New(modules *rdb.ModuleDAO, mappings *rdb.MetricMappingDAO, datasources *rdb.DatasourceDAO, samples *rdb.SampleDAO, predictions *rdb.PredictionDAO) *Service {
	return &Service{Modules: modules, Mappings: mappings, Datasources: datasources, Samples: samples, Predictions: predictions,
		NewQuerier: PromQuerierFactory, Now: time.Now, Telemetry: SharedTelemetry(), RunStates: rdb.NewRunStateDAO(modules.DB()), Config: rdb.NewSysConfigDAO(modules.DB()),
		Algorithms: rdb.NewAlgorithmDAO(modules.DB())}
}

// PromQuerierFactory builds a Prometheus querier from a stored datasource.
func PromQuerierFactory(ds *model.Datasource) (collector.RangeQuerier, error) {
	if ds.Type == model.DatasourceTypeDemo {
		return demo.Querier{}, nil
	}
	cfg, err := promclient.ConfigFromDatasource(ds)
	if err != nil {
		return nil, err
	}
	client, err := promclient.New(cfg)
	if err != nil {
		return nil, err
	}
	return collector.PromQuerier{Client: client}, nil
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

// DefaultEnabled says which algorithms are enabled when their config is first created: the baselines that need no
// parameter, which are cheap and the reference every other algorithm has to beat. The rest (the ones that need a
// parameter such as the season length, and the heavy ones) are enabled on the algorithm page.
func DefaultEnabled(id string) bool {
	def, ok := forecast.Lookup(id)
	if !ok || !def.Baseline {
		return false
	}
	for _, p := range def.Params {
		if p.Required {
			return false
		}
	}
	return true
}

// EnsureAlgorithms makes sure every built-in algorithm has its config row and that the custom algorithms of the
// database are registered. A custom algorithm that cannot be compiled does not stop the others from running.
func (s *Service) EnsureAlgorithms(ctx context.Context) error {
	if err := s.Algorithms.Ensure(ctx, forecast.IDs(), DefaultEnabled); err != nil {
		return err
	}
	if err := s.SyncCustom(ctx); err != nil {
		log.Printf("forecast: %v", err)
	}
	return nil
}

// runsOf returns what every enabled algorithm does for the module: a module is associated with all of them, so a
// newly enabled algorithm reaches every module on the next cycle without anything being created or bound.
func (s *Service) runsOf(ctx context.Context, m *model.ForecastModule) ([]*model.AlgorithmRun, error) {
	if !m.Predicts() {
		return nil, nil // prediction off, or mode "none": no algorithm runs for the module
	}
	if err := s.EnsureAlgorithms(ctx); err != nil {
		return nil, err
	}
	var configs []*model.AlgorithmConfig
	var err error
	if m.PredictMode == model.PredictFixed {
		// the module names its algorithm: only that one runs, whether or not the platform page enables it for the other modules
		c, err := s.Algorithms.Get(ctx, m.PredictAlgorithm)
		if err != nil {
			return nil, fmt.Errorf("%w: the algorithm %q of the fixed prediction mode: %v", ErrNotConfigured, m.PredictAlgorithm, err)
		}
		configs = []*model.AlgorithmConfig{c}
	} else if configs, err = s.Algorithms.Enabled(ctx); err != nil {
		return nil, err
	}
	runs := make([]*model.AlgorithmRun, 0, len(configs))
	for _, c := range configs {
		// the setting of a built-in algorithm that is no longer in the catalogue (a retired one) is not run: it would fail on
		// every cycle. A custom algorithm that cannot be compiled stays in the list, so its failure is seen.
		if _, known := forecast.Lookup(c.AlgorithmID); !known && c.Formula == "" && c.Base == "" {
			continue
		}
		runs = append(runs, model.NewRun(m, c))
	}
	cover, short := s.leadCoverMinutes(ctx, m), s.shortTermSettings(ctx)
	for _, r := range runs {
		r.LeadCoverMinutes = cover
		r.ShortTermRecentMinutes, r.ShortTermTrigger, r.ShortTermFadeMinutes, r.ShortTermDownShare = short.RecentMinutes, short.Trigger, short.FadeMinutes, short.DownShare
	}
	return runs, nil
}

// dataOf is the module as a data owner, for the steps that need no algorithm (collecting, history).
func dataOf(m *model.ForecastModule) *model.AlgorithmRun {
	return model.NewRun(m, &model.AlgorithmConfig{})
}

// target returns what the PromQL templates of a module's sources are rendered with.
func target(a *model.AlgorithmRun) collector.Target {
	return collector.Target{Namespace: a.Namespace, Workload: a.Workload, Service: a.Service, Container: a.Container}
}

// metricSetup resolves the mapping and datasource behind one metric source of an instance.
type metricSetup struct {
	metric  string
	mapping *model.MetricMapping
	ds      *model.Datasource
	step    time.Duration
}

func (s *Service) setup(ctx context.Context, a *model.AlgorithmRun, metric string) (*metricSetup, error) {
	id := a.MappingID(metric)
	if id == 0 {
		return nil, fmt.Errorf("%w: no %s mapping", ErrNotConfigured, metric)
	}
	mapping, err := s.Mappings.GetByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("%s mapping %d: %w", metric, id, err)
	}
	if !mapping.Enabled {
		return nil, fmt.Errorf("%w: %s mapping %q is disabled", ErrNotConfigured, metric, mapping.Name)
	}
	if kind := a.KindOf(metric); mapping.Metric != kind {
		return nil, fmt.Errorf("%w: mapping %q is a %s mapping, source %s is %s", ErrNotConfigured, mapping.Name, mapping.Metric, metric, kind)
	}
	ds, err := s.Datasources.GetByID(ctx, mapping.DatasourceID)
	if err != nil {
		return nil, fmt.Errorf("datasource of mapping %q: %w", mapping.Name, err)
	}
	if !ds.Enabled {
		return nil, fmt.Errorf("%w: datasource %q is disabled", ErrNotConfigured, ds.Name)
	}
	// the dataset is sampled at the step of the module; without one, at the step of the query of the source
	step := time.Duration(mapping.StepSeconds) * time.Second
	if a.StepSeconds > 0 {
		step = time.Duration(a.StepSeconds) * time.Second
	}
	return &metricSetup{metric: metric, mapping: mapping, ds: ds, step: step}, nil
}

// configuredMetrics lists the keys of the enabled sources of an instance.
func configuredMetrics(a *model.AlgorithmRun) []string {
	return a.ConfiguredMetrics()
}

// floorToStep rounds t down to the step grid anchored at the Unix epoch.
func floorToStep(t time.Time, step time.Duration) time.Time {
	sec := int64(step / time.Second)
	u := t.Unix()
	return time.Unix(u-u%sec, 0).UTC()
}

// horizonSteps converts the instance horizon to whole steps of a series, rounding up.
func horizonSteps(a *model.AlgorithmRun, step time.Duration) int {
	h := time.Duration(a.CoverMinutes()) * time.Minute
	n := int((h + step - 1) / step)
	if n < 1 {
		n = 1
	}
	return n
}

// MetricUnit is the default unit of a metric kind; a custom metric has the unit of its mapping (see unitOf).
func MetricUnit(kind string) string { return model.MetricKinds[kind] }

// unitOf is the unit of a source: the unit of its kind, or the unit its mapping declares for a custom metric.
func (s *Service) unitOf(ctx context.Context, a *model.AlgorithmRun, key string) string {
	kind := a.KindOf(key)
	if kind != model.CustomMetric {
		return model.MetricKinds[kind]
	}
	if id := a.MappingID(key); id != 0 {
		if mapping, err := s.Mappings.GetByID(ctx, id); err == nil {
			return mapping.Unit
		}
	}
	return ""
}

var moduleFilterAll = rdb.ModuleFilter{}

func evalFilter(instanceID uint, metric, algorithm string) rdb.EvaluationFilter {
	return rdb.EvaluationFilter{ModuleID: instanceID, Metric: metric, AlgorithmID: algorithm}
}
