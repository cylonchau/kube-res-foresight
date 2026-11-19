package forecastsvc

import (
	"context"
	"math"
	"sort"

	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/cylonchau/kube-res-foresight/pkg/timing"
)

// The resource analysis (P11 C6): for each module, what a Pod requests against what it used over a window (the quantiles of the measured use), how
// much of the request is more than the recommendation of the platform asks for, and what it could be lowered to. It is the diagnosis of the parked
// cost module without the money: the figures come from the samples the platform stored and the request the module records, and nothing is read
// from the cluster. It reads only the modules that are set up (nodes and workloads that are not modules are not analysed: P10 E2, E3). It changes
// no request, no replica and no recommendation.

// Verdicts of one resource, and the window of the analysis.
const (
	VerdictUnknown         = "unknown"
	VerdictIdle            = "idle"
	VerdictOverprovisioned = "overprovisioned"
	VerdictOK              = "ok"
	VerdictTight           = "tight"
	DefaultAnalysisWindow  = 7
)

// ResourceFinding is the analysis of one resource of one module (per Pod; CPU in cores, memory in bytes).
type ResourceFinding struct {
	*ResourceCost
	// Verdict is idle, overprovisioned, ok, tight or unknown (see Why).
	Verdict string `json:"verdict"`
	// Why says in words what the verdict rests on, or why there is none.
	Why string `json:"why"`
	// SurplusShare is the share of the request that is above the recommendation of the platform (0 when it is not known).
	SurplusShare float64 `json:"surplus_share"`
}

// ModuleFinding is the analysis of one module.
type ModuleFinding struct {
	ModuleID  uint             `json:"module_id"`
	Name      string           `json:"name"`
	Namespace string           `json:"namespace"`
	Workload  string           `json:"workload"`
	Replicas  int              `json:"replicas"`
	External  bool             `json:"external_scaler"`
	CPU       *ResourceFinding `json:"cpu"`
	Memory    *ResourceFinding `json:"memory"`
}

// ResourceAnalysis is the answer: the modules, the module with the most to give back first.
type ResourceAnalysis struct {
	WindowDays    int `json:"window_days"`
	MarginPercent int `json:"margin_percent"`
	IdlePercent   int `json:"idle_percent"`
	// OverprovisionedBelow and TightAbove are the utilization limits (percent) the verdicts were made with.
	OverprovisionedBelow int `json:"overprovisioned_below_percent"`
	TightAbove           int `json:"tight_above_percent"`
	// Basis says what the figures rest on, in a sentence the answer carries so nobody reads more into it.
	Basis   string           `json:"basis"`
	Modules []*ModuleFinding `json:"modules"`
}

func finding(rc *ResourceCost, below, above float64) *ResourceFinding {
	if rc == nil {
		return nil
	}
	f := &ResourceFinding{ResourceCost: rc}
	switch {
	case !rc.Enough:
		f.Verdict, f.Why = VerdictUnknown, rc.Note
		return f
	case rc.Idle:
		f.Verdict, f.Why = VerdictIdle, "even the P99 use stays under the idle share of the request"
	case rc.Utilization < below:
		f.Verdict, f.Why = VerdictOverprovisioned, "the P95 use is under the over-provisioned limit of the request"
	case rc.Utilization > above:
		f.Verdict, f.Why = VerdictTight, "the P95 use is over the tight limit of the request: little room for a peak"
	default:
		f.Verdict, f.Why = VerdictOK, "the P95 use is between the two limits"
	}
	if rc.Request > 0 && rc.Surplus > 0 {
		f.SurplusShare = rc.Surplus / rc.Request
	}
	return f
}

// ResourceAnalysis analyses one module, or every enabled module when moduleID is 0, over windowDays (7 when 0, at most 30).
func (s *Service) ResourceAnalysis(ctx context.Context, moduleID uint, windowDays int) (*ResourceAnalysis, error) {
	if windowDays <= 0 {
		windowDays = DefaultAnalysisWindow
	}
	if windowDays > MaxCostWindowDays {
		windowDays = MaxCostWindowDays
	}
	var modules []*model.ForecastModule
	if moduleID != 0 {
		m, err := s.Modules.Get(ctx, moduleID)
		if err != nil {
			return nil, err
		}
		modules = []*model.ForecastModule{m}
	} else {
		var err error
		if modules, err = s.Modules.ListEnabled(ctx); err != nil {
			return nil, err
		}
	}
	p := s.costParams(ctx)
	below := float64(s.targetPercent(ctx, timing.OverprovisionUtilization)) / 100
	above := float64(s.targetPercent(ctx, timing.TightUtilization)) / 100
	out := &ResourceAnalysis{WindowDays: windowDays, MarginPercent: int(math.Round(p.margin * 100)), IdlePercent: int(math.Round(p.idle * 100)),
		OverprovisionedBelow: int(math.Round(below * 100)), TightAbove: int(math.Round(above * 100)), Modules: make([]*ModuleFinding, 0, len(modules)),
		Basis: "the quantiles of the use the platform stored for each module over the window, against the Pod request recorded on the module and the recommendation of the platform; nothing was read from the cluster, only modules that are set up are analysed, and no money is counted"}
	now := s.now()
	for _, m := range modules {
		mc := s.moduleCost(ctx, m, windowDays, p, unitPrices{}, now)
		out.Modules = append(out.Modules, &ModuleFinding{ModuleID: m.ID, Name: m.Name, Namespace: m.Namespace, Workload: m.Workload, Replicas: mc.Replicas, External: mc.External,
			CPU: finding(mc.CPU, below, above), Memory: finding(mc.Memory, below, above)})
	}
	share := func(m *ModuleFinding) float64 {
		best := 0.0
		for _, f := range []*ResourceFinding{m.CPU, m.Memory} {
			if f != nil && f.SurplusShare > best {
				best = f.SurplusShare
			}
		}
		return best
	}
	// the module with the most to give back first
	sort.SliceStable(out.Modules, func(i, j int) bool { return share(out.Modules[i]) > share(out.Modules[j]) })
	return out, nil
}

// MetricInfo is one metric of a module the platform stores, with how much of it there is.
type MetricInfo struct {
	Key      string `json:"key"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Resource string `json:"resource"`
	Unit     string `json:"unit"`
	// Stat reduces the values to one demand; Basis says whether it reads the forecast or the measured window.
	Stat    string `json:"stat"`
	Basis   string `json:"basis"`
	Enabled bool   `json:"enabled"`
	// StepSeconds is the step of the stored series; Earliest and Latest bound the samples (RFC 3339, UTC); empty when nothing was stored yet.
	StepSeconds int    `json:"step_seconds"`
	Earliest    string `json:"earliest,omitempty"`
	Latest      string `json:"latest,omitempty"`
}

// MetricCatalog lists the metrics a module has, with their units and the span of what is stored: what a question about the module can be
// answered from. It is what the platform holds, never a query of the cluster.
func (s *Service) MetricCatalog(ctx context.Context, moduleID uint) ([]MetricInfo, error) {
	sources, err := s.Modules.Sources(ctx, moduleID)
	if err != nil {
		return nil, err
	}
	out := make([]MetricInfo, 0, len(sources))
	for _, src := range sources {
		info := MetricInfo{Key: src.Key, Name: src.Name, Kind: src.Kind, Resource: src.Resource, Stat: src.Stat, Basis: src.Basis, Enabled: src.Enabled}
		if src.MappingID != 0 {
			if mp, err := s.Mappings.GetByID(ctx, src.MappingID); err == nil {
				info.Unit = mp.Unit
			}
		}
		if series, err := s.Samples.Series(ctx, moduleID, src.Key); err == nil && series != nil {
			info.StepSeconds = series.StepSeconds
			if t, ok, err := s.Samples.Earliest(ctx, series.ID); err == nil && ok {
				info.Earliest = t.UTC().Format("2006-01-02T15:04:05Z")
			}
			if t, ok, err := s.Samples.Latest(ctx, series.ID); err == nil && ok {
				info.Latest = t.UTC().Format("2006-01-02T15:04:05Z")
			}
		}
		out = append(out, info)
	}
	return out, nil
}
