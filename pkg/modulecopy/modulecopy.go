// Package modulecopy takes the parameters of one module over to another: what is the same whichever workload it is (the bounds and the pace of the replicas, how the data is
// read and forecast, the coefficients of the recommendation, the metric sources and the periods) and nothing that says which workload it is. It is how a module that was
// registered by itself, and has none of this, is configured in one step; the person then changes what differs.
package modulecopy

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/cylonchau/kube-res-foresight/pkg/dao/rdb"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
)

// The groups of parameters that can be copied.
const (
	// GroupReplicas: the bounds and the pace of the replicas (minimum, maximum, the hard maximum and how it grows, the fallback and the fixed count, the size and cooldown of one step).
	GroupReplicas = "replicas"
	// GroupData: how much history is read and how, how far and how often it is forecast, and what is too poor to be used.
	GroupData = "data"
	// GroupResources: the bounds and the fallback of CPU, memory and GPU, the ratios of the limits, the capacity and the coefficients of the recommendation, the OOM protection.
	GroupResources = "resources"
	// GroupPredict: how the module predicts (the mode, the algorithm and the lowest score). Whether it predicts at all (PredictEnabled) is never copied.
	GroupPredict = "predict"
	// GroupSources: the metric sources, each on the same metric template (a template is written with {{namespace}}, {{workload}} and {{container}}, so it reads the workload of the module it is on).
	GroupSources = "sources"
	// GroupPeriods: the fixed periods of the week.
	GroupPeriods = "periods"
)

// Groups lists the groups in the order they are shown.
var Groups = []string{GroupReplicas, GroupData, GroupResources, GroupPredict, GroupSources, GroupPeriods}

// What is never copied, whatever the groups: who the module is (name, description, cluster, namespace, workload kind and name, service, container), whether it runs (enabled, mode,
// bypass), what the operator read of the workload (the Pod requests) and whether it predicts. The target keeps its own.

// Errors the copy refuses with.
var (
	ErrSame          = errors.New("a module cannot be copied onto itself")
	ErrTargetEnabled = errors.New("the module is switched on: switch it off before copying parameters onto it")
	ErrNoGroup       = errors.New("no group of parameters was chosen")
)

// Skip is something that was not copied, and why.
type Skip struct {
	What   string `json:"what"`
	Reason string `json:"reason"`
}

// Result says what was copied.
type Result struct {
	Copied  []string `json:"copied"`
	Skipped []Skip   `json:"skipped"`
}

// Copier copies between two modules stored in the database.
type Copier struct {
	Modules  *rdb.ModuleDAO
	Mappings *rdb.MetricMappingDAO
}

// Clean keeps the known groups, once each, in their order; an empty list is every group.
func Clean(groups []string) ([]string, error) {
	if len(groups) == 0 {
		return append([]string{}, Groups...), nil
	}
	want := map[string]bool{}
	for _, g := range groups {
		g = strings.TrimSpace(g)
		known := false
		for _, k := range Groups {
			known = known || k == g
		}
		if !known {
			return nil, fmt.Errorf("unknown group %q (the groups are %s)", g, strings.Join(Groups, ", "))
		}
		want[g] = true
	}
	out := []string{}
	for _, g := range Groups {
		if want[g] {
			out = append(out, g)
		}
	}
	if len(out) == 0 {
		return nil, ErrNoGroup
	}
	return out, nil
}

// Apply copies the fields of the groups that are columns of the module (not the sources and the periods, which are rows of their own) from src onto dst.
func Apply(dst, src *model.ForecastModule, groups []string) {
	for _, g := range groups {
		switch g {
		case GroupReplicas:
			dst.MinReplicas, dst.MaxReplicas, dst.HardMaxReplicas = src.MinReplicas, src.MaxReplicas, src.HardMaxReplicas
			dst.MaxReplicasMode, dst.ExpandStepRatio = src.MaxReplicasMode, src.ExpandStepRatio
			dst.FallbackReplicas, dst.FixedReplicas = src.FallbackReplicas, src.FixedReplicas
			dst.ScaleUpMaxStep, dst.ScaleDownMaxStep = src.ScaleUpMaxStep, src.ScaleDownMaxStep
			dst.ScaleUpCooldownSeconds, dst.ScaleDownCooldownSeconds = src.ScaleUpCooldownSeconds, src.ScaleDownCooldownSeconds
		case GroupData:
			dst.LookbackHours, dst.HorizonMinutes, dst.MaxFillSteps = src.LookbackHours, src.HorizonMinutes, src.MaxFillSteps
			dst.StepSeconds, dst.WindowSteps, dst.MinPoints, dst.RunIntervalMinutes = src.StepSeconds, src.WindowSteps, src.MinPoints, src.RunIntervalMinutes
			dst.FallbackMinHistoryRatio, dst.MaxMissingRatio, dst.TTLSeconds, dst.Timezone = src.FallbackMinHistoryRatio, src.MaxMissingRatio, src.TTLSeconds, src.Timezone
		case GroupResources:
			dst.MinCPUMilli, dst.MaxCPUMilli, dst.MinMemoryBytes, dst.MaxMemoryBytes = src.MinCPUMilli, src.MaxCPUMilli, src.MinMemoryBytes, src.MaxMemoryBytes
			dst.FallbackCPUMilli, dst.FallbackMemoryBytes = src.FallbackCPUMilli, src.FallbackMemoryBytes
			dst.MinGPU, dst.MaxGPU, dst.FallbackGPU = src.MinGPU, src.MaxGPU, src.FallbackGPU
			dst.LimitCPURatio, dst.LimitMemoryRatio = src.LimitCPURatio, src.LimitMemoryRatio
			dst.HeadroomRatio, dst.PodCapacityRPS, dst.TargetUtilization = src.HeadroomRatio, src.PodCapacityRPS, src.TargetUtilization
			dst.ReplicaMode, dst.CapacityMode = src.ReplicaMode, src.CapacityMode
			dst.OOMLookbackDays, dst.OOMMemoryUplift, dst.OOMWeightEnabled, dst.OOMWeight = src.OOMLookbackDays, src.OOMMemoryUplift, src.OOMWeightEnabled, src.OOMWeight
			dst.OOMCalcWorkingSetRatio, dst.OOMCalcLimitRatio, dst.OOMCalcHistoryRatio = src.OOMCalcWorkingSetRatio, src.OOMCalcLimitRatio, src.OOMCalcHistoryRatio
			dst.FloorRatio, dst.MaxShortfall, dst.JVMHeapPressure, dst.RecoverCycles = src.FloorRatio, src.MaxShortfall, src.JVMHeapPressure, src.RecoverCycles
			dst.RecP90Target, dst.RecP95Target, dst.RecP99Target, dst.RecLimitGuard = src.RecP90Target, src.RecP95Target, src.RecP99Target, src.RecLimitGuard
			dst.RecCPURequestHeadroom, dst.RecMemoryRequestHeadroom = src.RecCPURequestHeadroom, src.RecMemoryRequestHeadroom
			dst.RecCPULimitFactor, dst.RecMemoryLimitFactor, dst.RecSustainMinutes = src.RecCPULimitFactor, src.RecMemoryLimitFactor, src.RecSustainMinutes
		case GroupPredict:
			dst.PredictMode, dst.PredictAlgorithm, dst.MinScore = src.PredictMode, src.PredictAlgorithm, src.MinScore
		}
	}
}

// Copy copies the groups from the module from onto the module to. The target has to be switched off (a module that is running is not changed under its own feet). The columns
// are validated as the module form validates them. Sources and periods are added, not replaced: one the target already has by that name stays as it is and is said to be skipped.
func (c *Copier) Copy(ctx context.Context, fromID, toID uint, groups []string) (*Result, error) {
	if fromID == toID {
		return nil, ErrSame
	}
	groups, err := Clean(groups)
	if err != nil {
		return nil, err
	}
	src, err := c.Modules.Get(ctx, fromID)
	if err != nil {
		return nil, err
	}
	dst, err := c.Modules.Get(ctx, toID)
	if err != nil {
		return nil, err
	}
	if dst.Enabled {
		return nil, ErrTargetEnabled
	}
	res := &Result{Copied: []string{}, Skipped: []Skip{}}
	Apply(dst, src, groups)
	if err := dst.Validate(); err != nil {
		return nil, fmt.Errorf("the parameters of %q do not fit %q: %w", src.Name, dst.Name, err)
	}
	if err := c.Modules.Update(ctx, dst); err != nil {
		return nil, err
	}
	for _, g := range groups {
		switch g {
		case GroupSources:
			c.sources(ctx, src, dst, res)
		case GroupPeriods:
			if err := c.periods(ctx, src, dst, res); err != nil {
				return nil, err
			}
		default:
			res.Copied = append(res.Copied, g)
		}
	}
	return res, nil
}

// generic reports whether a metric template reads the module it is on: it has the variables of a module in it. A template written for one workload (its name in the query) would
// make the copy read the other module's data and call it this one's, so it is not copied.
func generic(query string) bool {
	return strings.Contains(query, "{{workload}}") || strings.Contains(query, "{{namespace}}")
}

func (c *Copier) sources(ctx context.Context, src, dst *model.ForecastModule, res *Result) {
	have := map[string]bool{}
	for _, s := range dst.Sources {
		have[strings.ToLower(s.Name)] = true
	}
	copied := 0
	for _, s := range src.Sources {
		if have[strings.ToLower(s.Name)] {
			res.Skipped = append(res.Skipped, Skip{What: "source " + s.Name, Reason: "the module already has a source of that name"})
			continue
		}
		if c.Mappings != nil {
			mp, err := c.Mappings.GetByID(ctx, s.MappingID)
			if err != nil {
				res.Skipped = append(res.Skipped, Skip{What: "source " + s.Name, Reason: "its metric template could not be read"})
				continue
			}
			if !generic(mp.Query) {
				res.Skipped = append(res.Skipped, Skip{What: "source " + s.Name, Reason: "its query names a workload (no {{namespace}} or {{workload}}): write the source for this module"})
				continue
			}
		}
		n := *s
		n.ID, n.ModuleID, n.Key = 0, dst.ID, ""
		if err := c.Modules.SaveSource(ctx, &n); err != nil {
			res.Skipped = append(res.Skipped, Skip{What: "source " + s.Name, Reason: err.Error()})
			continue
		}
		copied++
	}
	if copied > 0 {
		res.Copied = append(res.Copied, fmt.Sprintf("%s (%d)", GroupSources, copied))
	}
}

func (c *Copier) periods(ctx context.Context, src, dst *model.ForecastModule, res *Result) error {
	from, err := c.Modules.Periods(ctx, src.ID)
	if err != nil {
		return err
	}
	have, err := c.Modules.Periods(ctx, dst.ID)
	if err != nil {
		return err
	}
	names := map[string]bool{}
	for _, p := range have {
		names[strings.ToLower(p.Name)] = true
	}
	copied := 0
	for _, p := range from {
		if names[strings.ToLower(p.Name)] {
			res.Skipped = append(res.Skipped, Skip{What: "period " + p.Name, Reason: "the module already has a period of that name"})
			continue
		}
		n := *p
		n.ID, n.ModuleID = 0, dst.ID
		if err := c.Modules.SavePeriod(ctx, &n); err != nil {
			res.Skipped = append(res.Skipped, Skip{What: "period " + p.Name, Reason: err.Error()})
			continue
		}
		copied++
	}
	if copied > 0 {
		res.Copied = append(res.Copied, fmt.Sprintf("%s (%d)", GroupPeriods, copied))
	}
	return nil
}
