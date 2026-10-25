package forecastsvc

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/cylonchau/kube-resource-fcst/pkg/collector"
	"github.com/cylonchau/kube-resource-fcst/pkg/model"
)

// QueryTest asks for a PromQL to be checked and run before it is saved as the metric of a dataset.
type QueryTest struct {
	DatasourceID uint   `json:"datasource_id"`
	Query        string `json:"query"`
	Level        string `json:"level"`
	// Metric is the kind of the metric (cpu, memory, ... or custom); it decides the unit.
	Metric      string `json:"metric"`
	Unit        string `json:"unit"`
	StepSeconds int    `json:"step_seconds"`
	// ModuleID fills the template variables from the workload of the module; otherwise the fields below do.
	ModuleID  uint   `json:"module_id"`
	Namespace string `json:"namespace"`
	Workload  string `json:"workload"`
	Service   string `json:"service"`
	Container string `json:"container"`
	// From and To default to the last hour. Scale and Precision shape the values like a dataset does (0 = 1 and 6).
	From      time.Time `json:"from"`
	To        time.Time `json:"to"`
	Scale     float64   `json:"scale"`
	Precision *int      `json:"precision"`
}

// QueryTestResult is what a test of a PromQL found. Stage says how far the query got: template (the variables of the
// template are wrong), datasource, syntax (Prometheus did not parse the query), query (Prometheus refused or failed to run it),
// empty (it ran and returned no sample) or ok.
type QueryTestResult struct {
	OK       bool           `json:"ok"`
	Stage    string         `json:"stage"`
	Error    string         `json:"error,omitempty"`
	Rendered string         `json:"rendered,omitempty"`
	Unit     string         `json:"unit,omitempty"`
	Samples  int            `json:"samples"`
	Min      float64        `json:"min"`
	Max      float64        `json:"max"`
	Mean     float64        `json:"mean"`
	Last     float64        `json:"last"`
	Points   []HistoryPoint `json:"points"`
	From     time.Time      `json:"from"`
	To       time.Time      `json:"to"`
}

const (
	testMaxWindow = 7 * 24 * time.Hour
	testMaxPoints = 600
)

// TestQuery validates the template, renders it for the workload, runs it against the datasource and shapes the answer with
// the scale and precision of the dataset. It never stores anything.
func (s *Service) TestQuery(ctx context.Context, q QueryTest) (*QueryTestResult, error) {
	res := &QueryTestResult{Stage: "template", Points: []HistoryPoint{}}
	fail := func(stage string, err error) (*QueryTestResult, error) {
		res.Stage, res.Error = stage, err.Error()
		return res, nil
	}
	kind := q.Metric
	if kind == "" {
		kind = model.CustomMetric
	}
	level := q.Level
	if level == "" {
		level = "workload"
	}
	step := q.StepSeconds
	if step == 0 {
		step = 60
	}
	mapping := &model.MetricMapping{Name: "test", Metric: kind, Level: level, Query: q.Query, StepSeconds: step, Unit: q.Unit, DatasourceID: q.DatasourceID}
	if err := model.ValidateMetricMapping(mapping); err != nil {
		return fail("template", err)
	}
	res.Unit = mapping.Unit

	ds, err := s.Datasources.GetByID(ctx, q.DatasourceID)
	if err != nil {
		return fail("datasource", fmt.Errorf("datasource: %w", err))
	}
	if !ds.Enabled {
		return fail("datasource", fmt.Errorf("datasource %q is disabled", ds.Name))
	}

	t := collector.Target{Namespace: q.Namespace, Workload: q.Workload, Container: q.Container, Service: q.Service}
	if q.ModuleID != 0 {
		mod, err := s.Modules.Get(ctx, q.ModuleID)
		if err != nil {
			return fail("template", fmt.Errorf("module: %w", err))
		}
		t = target(dataOf(mod))
	}
	rendered, err := collector.RenderMapping(mapping, t)
	if err != nil {
		return fail("template", err)
	}
	res.Rendered = rendered

	to := q.To
	if to.IsZero() {
		to = s.now()
	}
	from := q.From
	if from.IsZero() {
		from = to.Add(-time.Hour)
	}
	if !from.Before(to) {
		return fail("template", fmt.Errorf("from must be before to"))
	}
	if to.Sub(from) > testMaxWindow {
		return fail("template", fmt.Errorf("a test runs over at most 7 days"))
	}
	stepD := time.Duration(step) * time.Second
	from, to = floorToStep(from, stepD), floorToStep(to, stepD)
	res.From, res.To = from, to

	querier, err := s.NewQuerier(ds)
	if err != nil {
		return fail("datasource", err)
	}
	opt := s.FetchOptions
	if opt.Concurrency == 0 {
		opt.Concurrency = ds.MaxConcurrency
	}
	fetched, err := collector.Fetch(ctx, querier, rendered, from, to, stepD, opt)
	if err != nil {
		stage := "query"
		if low := strings.ToLower(err.Error()); strings.Contains(low, "parse error") || strings.Contains(low, "bad_data") || strings.Contains(low, "unexpected") {
			stage = "syntax"
		}
		return fail(stage, err)
	}
	scale, precision := q.Scale, 6
	if scale == 0 {
		scale = 1
	}
	if q.Precision != nil {
		precision = *q.Precision
	}
	shaping := collector.Shaping{Step: stepD, QueryStep: stepD, Aggregation: "avg", Scale: scale, Precision: precision}
	points, err := collector.Shape(fetched, shaping)
	if err != nil {
		return fail("query", err)
	}
	var sum float64
	res.Min, res.Max = math.Inf(1), math.Inf(-1)
	kept := make([]HistoryPoint, 0, len(points))
	for _, p := range points {
		if math.IsNaN(p.Value) || math.IsInf(p.Value, 0) {
			continue
		}
		kept = append(kept, HistoryPoint{Time: p.Time, Value: p.Value})
		sum += p.Value
		res.Min, res.Max, res.Last = math.Min(res.Min, p.Value), math.Max(res.Max, p.Value), p.Value
	}
	res.Samples = len(kept)
	if res.Samples == 0 {
		res.Min, res.Max = 0, 0
		res.Stage = "empty"
		res.Error = "the query ran but returned no sample in the window: check the labels, the window and that the metric is scraped"
		return res, nil
	}
	res.Mean = sum / float64(res.Samples)
	if len(kept) > testMaxPoints {
		stride := (len(kept) + testMaxPoints - 1) / testMaxPoints
		thin := make([]HistoryPoint, 0, testMaxPoints)
		for i := 0; i < len(kept); i += stride {
			thin = append(thin, kept[i])
		}
		kept = thin
	}
	res.Points, res.OK, res.Stage = kept, true, "ok"
	return res, nil
}
