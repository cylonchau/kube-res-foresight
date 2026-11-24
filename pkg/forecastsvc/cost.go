package forecastsvc

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/cost"
	"github.com/cylonchau/kube-res-foresight/pkg/decision"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/cylonchau/kube-res-foresight/pkg/promclient"
	"github.com/cylonchau/kube-res-foresight/pkg/timing"
	"gorm.io/gorm"
)

// The cost side (P9 C1-C4): what the cluster costs, what the workloads the platform manages could give back, and how many nodes that could
// empty. It reads stored facts and one snapshot of the cluster; it changes no request, no replica and no recommendation.

// ErrCostCluster wraps what went wrong reading the cluster (the data source is down, or does not export kube-state-metrics).
var ErrCostCluster = errors.New("the cluster could not be read")

// ErrCostLevel says the level asked for is not one of the three candidate levels.
var ErrCostLevel = errors.New("the level is not one of aggressive, balanced, conservative")

// ErrCostNoDatasource says no data source was chosen for the cluster.
var ErrCostNoDatasource = errors.New("no data source is chosen for the cost: pick one on the page, or set the system parameter cost.datasource_id")

const (
	// DefaultCostWindowDays and MaxCostWindowDays bound the window the use of a workload is read over.
	DefaultCostWindowDays = 7
	MaxCostWindowDays     = 30
	// MinCostUsagePoints is how many measured points a window needs before the use is said to be known.
	MinCostUsagePoints = 100
	// costReplicaMaxAge is how old the replica sample may be that a measured total is divided by.
	costReplicaMaxAge = 30 * time.Minute
	// costConflictAge is how recent a conflict with another scaler must be to keep the target out of the candidates.
	costConflictAge = 24 * time.Hour
	// costSnapshotBudget is the longest the cluster may take to read.
	costSnapshotBudget = 90 * time.Second

	// codes of why money is missing
	MoneyNoCluster      = "no_cluster"      // no data source for the cluster, or it could not be read
	MoneyNoRules        = "no_price_rules"  // no enabled price rule
	MoneyNoPricedNodes  = "no_priced_nodes" // no node of the cluster is priced by a rule
	reasonNoReplicas    = "no_replicas_series"
	reasonNoRecommended = "no_recommendation"
)

// CostQuerierFactory builds the instant querier of a data source; tests replace it.
type CostQuerierFactory func(ds *model.Datasource) (cost.Querier, error)

// PromCostQuerier builds a Prometheus client from a stored data source.
func PromCostQuerier(ds *model.Datasource) (cost.Querier, error) {
	if ds.Type == model.DatasourceTypeDemo {
		return nil, errors.New("the demo data source has no cluster to read")
	}
	cfg, err := promclient.ConfigFromDatasource(ds)
	if err != nil {
		return nil, err
	}
	return promclient.New(cfg)
}

type costParams struct {
	cpuShare, margin, idle float64
	datasourceID           uint
}

func (s *Service) costParams(ctx context.Context) costParams {
	return costParams{
		cpuShare:     float64(s.targetPercent(ctx, timing.CostCPUShare)) / 100,
		margin:       float64(s.targetPercent(ctx, timing.CostCandidateMargin)) / 100,
		idle:         float64(s.targetPercent(ctx, timing.CostIdleShare)) / 100,
		datasourceID: uint(s.targetPercent(ctx, timing.CostDatasource)),
	}
}

// costRules are the enabled price rules.
func (s *Service) costRules(ctx context.Context) ([]cost.Rule, error) {
	if s.Prices == nil {
		return nil, nil
	}
	rows, err := s.Prices.Enabled(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]cost.Rule, 0, len(rows))
	for _, r := range rows {
		out = append(out, cost.Rule{ID: r.ID, Name: r.Name, MatchKind: r.MatchKind, LabelKey: r.LabelKey, LabelValue: r.LabelValue,
			NamePattern: r.NamePattern, HourlyPrice: r.HourlyPrice, Currency: r.Currency, Priority: r.Priority})
	}
	return out, nil
}

// readCluster reads the snapshot of the cluster from the data source (datasourceID, or the one of the system parameter when it is 0).
func (s *Service) readCluster(ctx context.Context, datasourceID uint, p costParams) (*model.Datasource, cost.Snapshot, error) {
	id := datasourceID
	if id == 0 {
		id = p.datasourceID
	}
	if id == 0 {
		return nil, cost.Snapshot{}, ErrCostNoDatasource
	}
	ds, err := s.Datasources.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, cost.Snapshot{}, fmt.Errorf("the data source %d does not exist", id)
		}
		return nil, cost.Snapshot{}, err
	}
	factory := s.NewCostQuerier
	if factory == nil {
		factory = PromCostQuerier
	}
	q, err := factory(ds)
	if err != nil {
		return ds, cost.Snapshot{}, fmt.Errorf("%w: %v", ErrCostCluster, err)
	}
	ctx, cancel := context.WithTimeout(ctx, costSnapshotBudget)
	defer cancel()
	snap, err := cost.Collect(ctx, q, s.now())
	if err != nil {
		return ds, snap, fmt.Errorf("%w: %v", ErrCostCluster, err)
	}
	return ds, snap, nil
}

// CostSource names the data source a figure was read from.
type CostSource struct {
	ID   uint   `json:"id"`
	Name string `json:"name"`
}

// CostOverview is what the cluster costs now (C1).
type CostOverview struct {
	Datasource CostSource `json:"datasource"`
	At         time.Time  `json:"at"`
	Nodes      int        `json:"nodes"`
	Pods       int        `json:"pods"`
	// Notes say what the snapshot could not see.
	Notes []string `json:"notes"`
	// CPUSharePercent is the share of a node price put on CPU, HoursPerMonth the hours of a month: the two assumptions behind the figures.
	CPUSharePercent int     `json:"cpu_share_percent"`
	HoursPerMonth   float64 `json:"hours_per_month"`
	Rules           int     `json:"rules"`
	cost.Allocation `json:"allocation"`
}

// CostOverview prices the cluster and shares the price among the workloads by their requests.
func (s *Service) CostOverview(ctx context.Context, datasourceID uint) (*CostOverview, error) {
	p := s.costParams(ctx)
	rules, err := s.costRules(ctx)
	if err != nil {
		return nil, err
	}
	ds, snap, err := s.readCluster(ctx, datasourceID, p)
	if err != nil {
		return nil, err
	}
	out := &CostOverview{
		Datasource: CostSource{ID: ds.ID, Name: ds.Name}, At: snap.At, Nodes: len(snap.Nodes), Pods: len(snap.Pods),
		Notes: append([]string{}, snap.Notes...), CPUSharePercent: int(math.Round(p.cpuShare * 100)), HoursPerMonth: cost.HoursPerMonth, Rules: len(rules),
		Allocation: cost.Allocate(snap, rules, p.cpuShare),
	}
	out.Notes = append(out.Notes, out.Allocation.Notes...)
	return out, nil
}

// ResourceCost is what a workload could give back of one resource (C2 and C3), per Pod unless said otherwise. CPU is in cores and
// memory in bytes.
type ResourceCost struct {
	cost.Waste
	Candidates []CandidateCost `json:"candidates"`
	// CandidateNote says why there is no candidate.
	CandidateNote string `json:"candidate_note,omitempty"`
	// SurplusMonthly is what the surplus of all the Pods costs a month, in the currency of the answer; 0 when no price is known.
	SurplusMonthly float64 `json:"surplus_monthly"`
}

// CandidateCost is a candidate with what it would save a month over all the Pods (in the currency of the answer; 0 when no price is known).
type CandidateCost struct {
	cost.Candidate
	MonthlySaving float64 `json:"monthly_saving"`
}

// ModuleCost is the diagnosis of one module (one workload).
type ModuleCost struct {
	ModuleID     uint   `json:"module_id"`
	Name         string `json:"name"`
	Namespace    string `json:"namespace"`
	WorkloadKind string `json:"workload_kind"`
	Workload     string `json:"workload"`
	// Replicas is how many replicas the savings are counted over (the measured ready replicas, else the recommended ones); 0 when unknown.
	Replicas int `json:"replicas"`
	// External is true when another scaler (an HPA or KEDA) lately competed for the replicas: such a target is left alone.
	External bool          `json:"external"`
	CPU      *ResourceCost `json:"cpu"`
	Memory   *ResourceCost `json:"memory"`
}

// CostModules is the diagnosis of every enabled module over a window.
type CostModules struct {
	WindowDays int `json:"window_days"`
	// Currency is the currency of the money in the answer (the one most of the cluster is priced in); empty when no price is known, and
	// MoneyNote then says why. CPUCoreHour and MemoryGiBHour are the unit prices the money was made with.
	Currency      string        `json:"currency"`
	MoneyNote     string        `json:"money_note,omitempty"`
	CPUCoreHour   float64       `json:"cpu_core_hour"`
	MemoryGiBHour float64       `json:"memory_gib_hour"`
	IdlePercent   int           `json:"idle_percent"`
	MarginPercent int           `json:"margin_percent"`
	Modules       []*ModuleCost `json:"modules"`
}

type unitPrices struct {
	currency      string
	cpuCoreHour   float64
	memoryGiBHour float64
	note          string
}

// pricesOf is the unit price of the cluster: the average price of a core-hour and a GiB-hour over the nodes of the currency the cluster
// is mostly priced in. note says why there is none.
func (s *Service) pricesOf(ctx context.Context, datasourceID uint, p costParams) unitPrices {
	rules, err := s.costRules(ctx)
	if err != nil || len(rules) == 0 {
		return unitPrices{note: MoneyNoRules}
	}
	_, snap, err := s.readCluster(ctx, datasourceID, p)
	if err != nil {
		return unitPrices{note: MoneyNoCluster}
	}
	alloc := cost.Allocate(snap, rules, p.cpuShare)
	var best *cost.CurrencyCost
	for _, c := range alloc.Currencies {
		if best == nil || c.Hourly > best.Hourly {
			best = c
		}
	}
	if best == nil {
		return unitPrices{note: MoneyNoPricedNodes}
	}
	return unitPrices{currency: best.Currency, cpuCoreHour: best.CPUCoreHour, memoryGiBHour: best.MemoryGiBHour}
}

// CostModules diagnoses every enabled module: what a Pod requests against what it used, how much of the request is more than the
// platform's recommendation, what it could be lowered to, and what that would be worth. The money needs prices and a cluster; without
// them the figures are still given, without money.
func (s *Service) CostModules(ctx context.Context, windowDays int, datasourceID uint) (*CostModules, error) {
	if windowDays <= 0 {
		windowDays = DefaultCostWindowDays
	}
	if windowDays > MaxCostWindowDays {
		windowDays = MaxCostWindowDays
	}
	p := s.costParams(ctx)
	prices := s.pricesOf(ctx, datasourceID, p)
	return s.costModules(ctx, windowDays, p, prices)
}

func (s *Service) costModules(ctx context.Context, windowDays int, p costParams, prices unitPrices) (*CostModules, error) {
	modules, err := s.Modules.ListEnabled(ctx)
	if err != nil {
		return nil, err
	}
	out := &CostModules{WindowDays: windowDays, Currency: prices.currency, MoneyNote: prices.note, CPUCoreHour: prices.cpuCoreHour,
		MemoryGiBHour: prices.memoryGiBHour, IdlePercent: int(math.Round(p.idle * 100)), MarginPercent: int(math.Round(p.margin * 100)),
		Modules: make([]*ModuleCost, 0, len(modules))}
	now := s.now()
	for _, m := range modules {
		out.Modules = append(out.Modules, s.moduleCost(ctx, m, windowDays, p, prices, now))
	}
	// the biggest surplus first: it is the first to look at
	sort.SliceStable(out.Modules, func(i, j int) bool { return surplusOf(out.Modules[i]) > surplusOf(out.Modules[j]) })
	return out, nil
}

func surplusOf(m *ModuleCost) float64 {
	t := 0.0
	if m.CPU != nil {
		t += m.CPU.SurplusMonthly
	}
	if m.Memory != nil {
		t += m.Memory.SurplusMonthly
	}
	return t
}

func (s *Service) moduleCost(ctx context.Context, m *model.ForecastModule, windowDays int, p costParams, prices unitPrices, now time.Time) *ModuleCost {
	mc := &ModuleCost{ModuleID: m.ID, Name: m.Name, Namespace: m.Namespace, WorkloadKind: m.WorkloadKind, Workload: m.Workload}
	from := now.Add(-time.Duration(windowDays) * 24 * time.Hour)
	run := &model.AlgorithmRun{ID: m.ID, Sources: m.Sources}

	rec, _ := s.Predictions.LatestRecommendation(ctx, m.ID)
	mc.Replicas = s.costReplicas(ctx, m, run, rec, now)
	if execs, _, err := s.Predictions.ListExecutions(ctx, m.ID, model.ExecConflict, 1, 1); err == nil && len(execs) > 0 {
		mc.External = now.Sub(execs[0].CreatedAt) <= costConflictAge
	}

	safe := func(set bool, source string, value float64) float64 {
		if rec == nil || !set || value <= 0 {
			return 0
		}
		if source != string(decision.SourceAlgorithm) && source != string(decision.SourceOOMFloor) {
			return 0 // a fixed value, a fallback or the original: not what the evidence supports
		}
		return value
	}
	cpuSafe, memSafe := 0.0, 0.0
	if rec != nil {
		cpuSafe = safe(rec.CPUSet, rec.CPUSource, float64(rec.CPUMilli)/1000)
		memSafe = safe(rec.MemorySet, rec.MemorySource, float64(rec.MemoryBytes))
	}

	mc.CPU = s.resourceCost(ctx, m, run, "cpu", float64(m.PodCPURequestMilli)/1000, cpuSafe, from, now, p, mc,
		prices.cpuCoreHour, func(v float64) float64 { return float64(RoundCPUMilli(int64(math.Ceil(v*1000)))) / 1000 })
	mc.Memory = s.resourceCost(ctx, m, run, "memory", float64(m.PodMemoryRequestBytes), memSafe, from, now, p, mc,
		prices.memoryGiBHour/float64(1<<30), func(v float64) float64 { return float64(RoundMemoryBytes(int64(math.Ceil(v)))) })
	return mc
}

// costReplicas is how many replicas the money is counted over: the newest ready-replica sample, else the replicas the newest
// recommendation asked for.
func (s *Service) costReplicas(ctx context.Context, m *model.ForecastModule, run *model.AlgorithmRun, rec *model.Recommendation, now time.Time) int {
	if w := s.workloadReplicas(ctx, m, now); w.OK {
		return w.Desired
	}
	if src := run.FirstOfKind("replicas"); src != nil {
		if series, err := s.Samples.Series(ctx, m.ID, src.Key); err == nil {
			if rows, err := s.Samples.Range(ctx, series.ID, now.Add(-costReplicaMaxAge), now); err == nil && len(rows) > 0 {
				if v := rows[len(rows)-1].Value; v >= 1 && !math.IsNaN(v) {
					return int(math.Round(v))
				}
			}
		}
	}
	if rec != nil && rec.ReplicasSet && rec.Replicas > 0 {
		return rec.Replicas
	}
	return 0
}

// podUsage reads the use of one Pod over the window: a workload-total series is divided by the ready replicas of the same moment, a
// series that is already per Pod is taken as it is. note says why nothing could be read.
func (s *Service) podUsage(ctx context.Context, m *model.ForecastModule, run *model.AlgorithmRun, kind string, from, to time.Time) ([]float64, string) {
	src := run.FirstOfKind(kind)
	if src == nil {
		return nil, cost.ReasonNoUsage
	}
	series, err := s.Samples.Series(ctx, m.ID, src.Key)
	if err != nil {
		return nil, cost.ReasonNoUsage
	}
	rows, err := s.Samples.Range(ctx, series.ID, from, to)
	if err != nil || len(rows) == 0 {
		return nil, cost.ReasonNoUsage
	}
	mapping, err := s.Mappings.GetByID(ctx, src.MappingID)
	if err != nil {
		return nil, cost.ReasonNoUsage
	}
	if mapping.Level != "workload" {
		out := make([]float64, 0, len(rows))
		for _, r := range rows {
			out = append(out, r.Value)
		}
		return out, ""
	}
	rsrc := run.FirstOfKind("replicas")
	if rsrc == nil {
		return nil, reasonNoReplicas
	}
	rseries, err := s.Samples.Series(ctx, m.ID, rsrc.Key)
	if err != nil {
		return nil, reasonNoReplicas
	}
	reps, err := s.Samples.Range(ctx, rseries.ID, from.Add(-costReplicaMaxAge), to)
	if err != nil || len(reps) == 0 {
		return nil, reasonNoReplicas
	}
	out := make([]float64, 0, len(rows))
	j := -1
	for _, r := range rows {
		for j+1 < len(reps) && reps[j+1].TS <= r.TS {
			j++
		}
		if j < 0 || r.TS-reps[j].TS > int64(costReplicaMaxAge/time.Second) || reps[j].Value < 1 {
			continue
		}
		out = append(out, r.Value/reps[j].Value)
	}
	return out, ""
}

func (s *Service) resourceCost(ctx context.Context, m *model.ForecastModule, run *model.AlgorithmRun, kind string, request, safe float64, from, to time.Time,
	p costParams, mc *ModuleCost, unitPerHour float64, round func(float64) float64) *ResourceCost {
	values, why := s.podUsage(ctx, m, run, kind, from, to)
	usage := cost.UsageOf(values)
	w := cost.Diagnose(kind, request, safe, usage, p.idle, MinCostUsagePoints)
	if why != "" && w.Note != cost.ReasonNoRequest {
		w.Note = why
	}
	rc := &ResourceCost{Waste: w, Candidates: []CandidateCost{}}
	money := func(perPod float64) float64 {
		if unitPerHour <= 0 || mc.Replicas <= 0 {
			return 0
		}
		return perPod * float64(mc.Replicas) * unitPerHour * cost.HoursPerMonth
	}
	rc.SurplusMonthly = money(w.Surplus)
	switch {
	case !w.Enough:
		rc.CandidateNote = w.Note
	case mc.External:
		rc.CandidateNote = cost.ReasonExternalScaler
	default:
		cands, note := cost.Candidates(request, safe, usage, p.margin, MinCostUsagePoints, round)
		rc.CandidateNote = note
		for _, c := range cands {
			rc.Candidates = append(rc.Candidates, CandidateCost{Candidate: c, MonthlySaving: money(c.Saving)})
		}
	}
	return rc
}

// SimulatedChange says what a workload was given in the simulation.
type SimulatedChange struct {
	ModuleID    uint   `json:"module_id"`
	Name        string `json:"name"`
	Namespace   string `json:"namespace"`
	Kind        string `json:"kind"`
	Workload    string `json:"workload"`
	CPUMilli    int64  `json:"cpu_milli"`
	MemoryBytes int64  `json:"memory_bytes"`
}

// CostSimulation is the answer of a what-if (C4): what the nodes could give back as the cluster is, and with the requests of a level of
// candidates. The difference between the two is what the candidates add.
type CostSimulation struct {
	Datasource CostSource `json:"datasource"`
	At         time.Time  `json:"at"`
	Level      string     `json:"level"`
	WindowDays int        `json:"window_days"`
	Notes      []string   `json:"notes"`
	// Changes are the workloads whose requests were lowered in the second run.
	Changes []SimulatedChange `json:"changes"`
	// Now is the run on the requests as they are, With the run on the lowered ones.
	Now  cost.SimResult `json:"now"`
	With cost.SimResult `json:"with"`
}

// Simulate asks how many nodes could be emptied now, and with the candidates of the level (aggressive, balanced or conservative) of every
// module that has them. It reads; nothing is changed in the cluster or in the platform.
func (s *Service) Simulate(ctx context.Context, datasourceID uint, level string, windowDays int) (*CostSimulation, error) {
	switch level {
	case cost.LevelAggressive, cost.LevelBalanced, cost.LevelConservative:
	default:
		return nil, ErrCostLevel
	}
	if windowDays <= 0 {
		windowDays = DefaultCostWindowDays
	}
	if windowDays > MaxCostWindowDays {
		windowDays = MaxCostWindowDays
	}
	p := s.costParams(ctx)
	rules, err := s.costRules(ctx)
	if err != nil {
		return nil, err
	}
	ds, snap, err := s.readCluster(ctx, datasourceID, p)
	if err != nil {
		return nil, err
	}
	mods, err := s.costModules(ctx, windowDays, p, unitPrices{})
	if err != nil {
		return nil, err
	}
	out := &CostSimulation{Datasource: CostSource{ID: ds.ID, Name: ds.Name}, At: snap.At, Level: level, WindowDays: windowDays,
		Notes: append([]string{}, snap.Notes...), Changes: []SimulatedChange{}}
	var changes []cost.Change
	for _, mc := range mods.Modules {
		ch := cost.Change{Namespace: mc.Namespace, Kind: mc.WorkloadKind, Workload: mc.Workload}
		if v, ok := candidateAt(mc.CPU, level); ok {
			ch.CPUMilli = int64(math.Round(v * 1000))
		}
		if v, ok := candidateAt(mc.Memory, level); ok {
			ch.MemoryBytes = int64(math.Round(v))
		}
		if ch.CPUMilli == 0 && ch.MemoryBytes == 0 {
			continue
		}
		changes = append(changes, ch)
		out.Changes = append(out.Changes, SimulatedChange{ModuleID: mc.ModuleID, Name: mc.Name, Namespace: mc.Namespace, Kind: mc.WorkloadKind,
			Workload: mc.Workload, CPUMilli: ch.CPUMilli, MemoryBytes: ch.MemoryBytes})
	}
	out.Now = cost.Simulate(snap, nil, rules)
	out.With = cost.Simulate(snap, changes, rules)
	return out, nil
}

func candidateAt(rc *ResourceCost, level string) (float64, bool) {
	if rc == nil {
		return 0, false
	}
	for _, c := range rc.Candidates {
		if c.Level == level {
			return c.Value, true
		}
	}
	return 0, false
}
