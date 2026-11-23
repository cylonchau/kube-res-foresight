package forecastsvc

import (
	"context"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/collector"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
)

// The CPU and memory requests and limits of each module for the table of the resource dashboard: each is the answer of the Prometheus data source of the module's
// metric mapping (the kinds k8s_cpu_requests, k8s_cpu_limits, k8s_mem_requests and k8s_mem_limits) to an instant query now. Nothing is taken from the module's
// own settings or from what the platform stored: a module without such a metric has no figure, and the table says which metric is missing.

// ModuleResources are the figures of one module, of one Pod (CPU in cores, memory in bytes). A figure that could not be asked for is nil, and Errors says why.
type ModuleResources struct {
	ModuleID      uint              `json:"module_id"`
	CPURequest    *float64          `json:"cpu_request"`
	CPULimit      *float64          `json:"cpu_limit"`
	MemoryRequest *float64          `json:"memory_request"`
	MemoryLimit   *float64          `json:"memory_limit"`
	Errors        map[string]string `json:"errors"`
}

// ResourceList is the figures of every enabled module.
type ResourceList struct {
	At   time.Time          `json:"at"`
	Rows []*ModuleResources `json:"rows"`
	// Partial is true when the time limit of the request stopped some modules (Skipped counts them): the others are complete.
	Partial bool `json:"partial"`
	Skipped int  `json:"skipped"`
}

var resourceKinds = []struct{ kind, field string }{
	{"k8s_cpu_requests", "cpu_request"}, {"k8s_cpu_limits", "cpu_limit"}, {"k8s_mem_requests", "memory_request"}, {"k8s_mem_limits", "memory_limit"},
}

// OverprovisionResources asks Prometheus for the requests and limits of every module of the module management (the dashboard lists them all), a few modules at a time and only as long as the time limit of the
// request allows.
func (s *Service) OverprovisionResources(ctx context.Context) (*ResourceList, error) {
	modules, err := s.Modules.ListAll(ctx)
	if err != nil {
		return nil, err
	}
	at := s.now()
	out := &ResourceList{At: at, Rows: make([]*ModuleResources, len(modules))}
	budget := s.readBudget(ctx)
	askCtx, cancel := ctx, context.CancelFunc(func() {})
	if budget > 0 {
		reserve := promReserve
		if reserve > budget/4 {
			reserve = budget / 4
		}
		askCtx, cancel = context.WithTimeout(ctx, budget-reserve)
	}
	defer cancel()

	var wg sync.WaitGroup
	slots := make(chan struct{}, promParallel)
	for i, m := range modules {
		row := &ModuleResources{ModuleID: m.ID, Errors: map[string]string{}}
		out.Rows[i] = row
		select {
		case slots <- struct{}{}:
		case <-askCtx.Done():
			row.Errors["all"] = promNotReached
			continue
		}
		wg.Add(1)
		go func(m *model.ForecastModule, row *ModuleResources) {
			defer wg.Done()
			defer func() { <-slots }()
			s.moduleResources(askCtx, m, at, row)
		}(m, row)
	}
	wg.Wait()
	// a module the time limit stopped (before or halfway) says so, as one that was not reached
	for _, row := range out.Rows {
		for _, text := range row.Errors {
			if text == promNotReached || strings.Contains(text, "context deadline exceeded") || strings.Contains(text, "context canceled") {
				row.Errors = map[string]string{"all": promNotReached}
				out.Skipped++
				break
			}
		}
	}
	out.Partial = out.Skipped > 0
	return out, nil
}

// moduleResources fills the four figures of a module. A metric of the workload level is the sum over the Pods and is divided by the replicas (the module's
// replicas metric, asked the same way), one of the container level is already of one Pod.
func (s *Service) moduleResources(ctx context.Context, m *model.ForecastModule, at time.Time, row *ModuleResources) {
	run := &model.AlgorithmRun{ID: m.ID, AllSources: true, Sources: m.Sources, Namespace: m.Namespace, Workload: m.Workload, Service: m.Service, Container: m.Container}
	replicas := math.NaN()
	for _, f := range resourceKinds {
		value, level, err := s.instantOfKind(ctx, run, f.kind, at)
		if err != nil {
			row.Errors[f.field] = err.Error()
			continue
		}
		if level == model.PerTotal {
			if math.IsNaN(replicas) {
				r, _, rerr := s.instantOfKind(ctx, run, "replicas", at)
				if rerr != nil || !(r >= 1) {
					row.Errors[f.field] = "the metric is of the whole workload: the module needs a replicas metric to give one Pod's figure"
					replicas = 0
					continue
				}
				replicas = r
			}
			if replicas < 1 {
				row.Errors[f.field] = "the metric is of the whole workload: the module needs a replicas metric to give one Pod's figure"
				continue
			}
			value /= replicas
		}
		v := value
		switch f.field {
		case "cpu_request":
			row.CPURequest = &v
		case "cpu_limit":
			row.CPULimit = &v
		case "memory_request":
			row.MemoryRequest = &v
		case "memory_limit":
			row.MemoryLimit = &v
		}
	}
}

// instantOfKind asks the data source of the module's source of a kind for its value now; what the query answers (one Pod or all the replicas) comes with it.
func (s *Service) instantOfKind(ctx context.Context, run *model.AlgorithmRun, kind string, at time.Time) (float64, string, error) {
	src := run.FirstOfKind(kind)
	if src == nil {
		return 0, "", fmt.Errorf("the module has no %s metric: add it to the module (metric template kind %s)", kind, kind)
	}
	setup, err := s.setup(ctx, run, src.Key)
	if err != nil {
		return 0, "", err
	}
	q, err := s.vectorQuerierOf(ctx, setup.ds)
	if err != nil {
		return 0, "", err
	}
	expr, err := collector.RenderMapping(setup.mapping, target(run))
	if err != nil {
		return 0, "", err
	}
	value, err := scalarOf(ctx, q, strings.TrimSpace(expr), at)
	if err != nil {
		return 0, "", err
	}
	scale := src.Scale
	if scale <= 0 {
		scale = 1
	}
	return value * scale, src.PerOf(setup.mapping.Level), nil
}
