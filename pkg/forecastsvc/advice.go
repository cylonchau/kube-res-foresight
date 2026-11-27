package forecastsvc

import (
	"context"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/cylonchau/kube-res-foresight/pkg/overprovision"
)

// The advice is what the AI reads: the analysis of the resource dashboard of the modules, as the dashboard works it out (the same engine, the same Prometheus reading, the same
// Pod spec from Kubernetes), in a form short enough to put in front of a model. Every figure in it is the dashboard's, so a report made of it says what the dashboard says; the
// model explains and does not calculate.

const (
	// MaxAdviceModules is how many enabled modules the advice reads and ranks, whatever the limit of the answer is: each is a reading of Prometheus.
	MaxAdviceModules = 40
	// MaxAdviceLimit is the most modules one answer holds.
	MaxAdviceLimit = 20
	// adviceModuleTimeout bounds the reading of one module.
	adviceModuleTimeout = 45 * time.Second
	adviceParallel      = 4
)

// AdviceResource is one resource of a module: the Pod's Requests and Limits now, the use over the recommendation period and what the platform recommends for one Pod.
type AdviceResource struct {
	Resource string  `json:"resource"`
	Request  float64 `json:"request"`
	Limit    float64 `json:"limit"`
	P50      float64 `json:"p50"`
	P95      float64 `json:"p95"`
	P99      float64 `json:"p99"`
	Max      float64 `json:"max"`
	// SuggestedRequest and SuggestedLimit are the recommendation for one Pod (0 = none was made); Status is over, ok, under, limited or unknown, Grade the grade of an
	// over-provision, Confidence and Risk the levels the engine gives; Reasons are its codes.
	SuggestedRequest float64  `json:"suggested_request"`
	SuggestedLimit   float64  `json:"suggested_limit"`
	Status           string   `json:"status"`
	Grade            string   `json:"grade,omitempty"`
	Confidence       string   `json:"confidence"`
	Risk             string   `json:"risk"`
	OOMKills         float64  `json:"oom_kills,omitempty"`
	Throttle         float64  `json:"throttle,omitempty"`
	Reasons          []string `json:"reasons"`
	// Certain says the verdict can be stated as it is: the engine's confidence is not low, the verdict is not unknown and every figure of the module was read. CanLower says the
	// Requests can be said to be lowered: the verdict is over, it is certain, and the recommendation is below what the Pod asks. A report never says more than these two.
	Certain  bool   `json:"certain"`
	CanLower bool   `json:"can_lower"`
	Caution  string `json:"caution,omitempty"`
}

// AdvicePlan is the replicas: what the workload has, what the use asks for and what one action reaches.
type AdvicePlan struct {
	Current     int      `json:"current"`
	Recommended int      `json:"recommended"`
	ThisRound   int      `json:"this_round"`
	Direction   string   `json:"direction"`
	Binding     string   `json:"binding,omitempty"`
	Reasons     []string `json:"reasons"`
}

// ModuleAdvice is one module.
type ModuleAdvice struct {
	ModuleID  uint   `json:"module_id"`
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Workload  string `json:"workload"`
	Cluster   string `json:"cluster,omitempty"`
	Period    string `json:"period"`
	// PodSpecFrom says where the Requests and Limits of a Pod come from: kubernetes (the Operator's report) or estimated.
	PodSpecFrom string          `json:"pod_spec_from,omitempty"`
	CPU         *AdviceResource `json:"cpu,omitempty"`
	Memory      *AdviceResource `json:"memory,omitempty"`
	Replicas    *AdvicePlan     `json:"replicas,omitempty"`
	// Partial says part of the figures could not be read (Prometheus answered some queries and not others).
	Partial bool `json:"partial,omitempty"`
	// Score orders the modules: a module that is short or limited first, then the one with the most to give back; 0 is nothing to change.
	Score float64 `json:"score"`
}

// AdviceSkip is a module that could not be read, and why.
type AdviceSkip struct {
	ModuleID uint   `json:"module_id"`
	Name     string `json:"name"`
	Reason   string `json:"reason"`
}

// AdviceReport is the answer.
type AdviceReport struct {
	At time.Time `json:"at"`
	// Read is how many modules were read, Enabled how many are enabled (more than MaxAdviceModules are not all read), Counts the verdicts of the modules read.
	Read    int             `json:"read"`
	Enabled int             `json:"enabled"`
	Counts  map[string]int  `json:"counts"`
	Modules []*ModuleAdvice `json:"modules"`
	Skipped []AdviceSkip    `json:"skipped"`
	Basis   string          `json:"basis"`
}

// certify says what a verdict may be told as. A low confidence, an unknown verdict or a module whose figures were not all read is not certain, and nothing is lowered on it.
func certify(r *AdviceResource, partial bool) {
	if r == nil {
		return
	}
	switch {
	case partial:
		r.Caution = "partial: some figures of the module could not be read"
	case r.Status == string(overprovision.StatusUnknown):
		r.Caution = "unknown: not enough data to say"
	case r.Confidence == string(overprovision.Low):
		r.Caution = "low confidence"
	}
	r.Certain = r.Caution == ""
	r.CanLower = r.Certain && r.Status == string(overprovision.StatusOver) && r.SuggestedRequest > 0 && r.SuggestedRequest < r.Request
}

// adviceOf makes the advice of one module out of its analysis.
func adviceOf(d *DetailOverprovision) *ModuleAdvice {
	out := &ModuleAdvice{ModuleID: d.ModuleID, Name: d.Name, Namespace: d.Namespace, Workload: d.Workload, PodSpecFrom: d.PodSpecFrom, Partial: d.Partial}
	if d.Identity != nil {
		out.Cluster = d.Identity.Cluster
	}
	rec := d.Recommend
	if rec == nil && len(d.Windows) > 0 {
		rec = d.Windows[0]
	}
	if rec == nil {
		return out
	}
	out.Period = rec.Label
	resource := func(r *overprovision.Result) *AdviceResource {
		if r == nil {
			return nil
		}
		reasons := r.Reasons
		if reasons == nil {
			reasons = []string{}
		}
		a := &AdviceResource{Resource: r.Resource, Request: r.Request, Limit: r.Limit, P50: r.Usage.P50, P95: r.Usage.P95, P99: r.Usage.P99, Max: r.Usage.Max,
			SuggestedRequest: r.Suggested, SuggestedLimit: r.SuggestedLimit, Status: string(r.Status), Grade: string(r.Grade), Confidence: string(r.Confidence), Risk: string(r.Risk),
			Throttle: math.Max(r.Throttle, 0), Reasons: reasons}
		if r.Resource == overprovision.Memory {
			a.OOMKills = r.OOMKills
		}
		return a
	}
	out.CPU, out.Memory = resource(rec.CPU), resource(rec.Memory)
	for _, r := range []*AdviceResource{out.CPU, out.Memory} {
		certify(r, d.Partial)
	}
	if plan := rec.ReplicaPlan; plan != nil && plan.Needed > 0 {
		reasons := plan.Reasons
		if reasons == nil {
			reasons = []string{}
		}
		out.Replicas = &AdvicePlan{Current: plan.Current, Recommended: plan.Needed, ThisRound: plan.ThisRound, Direction: plan.Direction, Binding: plan.Binding, Reasons: reasons}
	}
	// a module that is short (under, limited) comes before one that has something to give back, by how much
	for _, r := range []*AdviceResource{out.CPU, out.Memory} {
		if r == nil || !(r.Request > 0) {
			continue
		}
		switch r.Status {
		case string(overprovision.StatusUnder), string(overprovision.StatusLimited):
			out.Score = math.Max(out.Score, 1+math.Max(r.SuggestedRequest-r.Request, 0)/r.Request)
		case string(overprovision.StatusOver):
			if r.SuggestedRequest > 0 {
				out.Score = math.Max(out.Score, math.Max(r.Request-r.SuggestedRequest, 0)/r.Request)
			}
		}
	}
	return out
}

// ResourceAdvice reads the analysis of the dashboard of the module (every enabled module when moduleID is 0, at most MaxAdviceModules of them) and gives the limit modules
// that most need a look. Each module is a reading of Prometheus, a few at a time and each inside its own time limit; one that fails is listed as skipped, with the reason,
// and the others are used.
func (s *Service) ResourceAdvice(ctx context.Context, moduleID uint, limit int) (*AdviceReport, error) {
	if limit < 1 || limit > MaxAdviceLimit {
		limit = 10
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
	out := &AdviceReport{At: s.now(), Enabled: len(modules), Counts: map[string]int{}, Modules: []*ModuleAdvice{}, Skipped: []AdviceSkip{},
		Basis: "the analysis of the resource dashboard, read from Prometheus now: the P50, P95 and P99 of the use over the recommendation period against the Requests and Limits of one Pod " +
			"(from Kubernetes when the Operator reports, else estimated), the recommended Requests, Limits and replicas, and the verdict and confidence the engine gives. Nothing is forecast and nothing is changed."}
	if len(modules) > MaxAdviceModules {
		modules = modules[:MaxAdviceModules]
	}
	results := make([]*ModuleAdvice, len(modules))
	skipped := make([]*AdviceSkip, len(modules))
	var wg sync.WaitGroup
	slots := make(chan struct{}, adviceParallel)
	for i, m := range modules {
		slots <- struct{}{}
		wg.Add(1)
		go func(i int, m *model.ForecastModule) {
			defer wg.Done()
			defer func() { <-slots }()
			readCtx, cancel := context.WithTimeout(ctx, adviceModuleTimeout)
			defer cancel()
			d, err := s.OverprovisionDetail(readCtx, m.ID, 0, time.Time{}, time.Time{})
			if err != nil || d == nil {
				reason := "no analysis"
				if err != nil {
					reason = err.Error()
				}
				skipped[i] = &AdviceSkip{ModuleID: m.ID, Name: m.Name, Reason: reason}
				return
			}
			results[i] = adviceOf(d)
		}(i, m)
	}
	wg.Wait()
	for i := range modules {
		if skipped[i] != nil {
			out.Skipped = append(out.Skipped, *skipped[i])
		}
		if results[i] == nil {
			continue
		}
		out.Read++
		out.Modules = append(out.Modules, results[i])
		for _, r := range []*AdviceResource{results[i].CPU, results[i].Memory} {
			if r != nil {
				out.Counts[r.Resource+"_"+r.Status]++
			}
		}
	}
	sort.SliceStable(out.Modules, func(a, b int) bool { return out.Modules[a].Score > out.Modules[b].Score })
	if len(out.Modules) > limit {
		out.Modules = out.Modules[:limit]
	}
	return out, nil
}
