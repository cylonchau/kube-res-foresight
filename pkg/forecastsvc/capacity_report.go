package forecastsvc

import (
	"context"
	"errors"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/decision"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
)

// The state of a capacity figure of module management: ready (a figure, with its source), not_set (nothing was entered for it), no_source (the module lacks a metric
// the estimate reads), no_history (the metric has no collected history yet), insufficient (history exists but too little of it says anything) or error (the figure could
// not be worked out). Nothing but ready carries a number.
const (
	CapacityReady        = "ready"
	CapacityNotSet       = "not_set"
	CapacityNoSource     = "no_source"
	CapacityNoHistory    = "no_history"
	CapacityInsufficient = "insufficient"
	CapacityError        = "error"
)

// CapacityReport is the capacity of one Pod of the module as module management shows it, with where each figure comes from, so no card is an unexplained number.
type CapacityReport struct {
	// Mode is where the safe load of a Pod comes from: manual (entered) or estimate (worked out from the history).
	Mode string `json:"mode"`
	// State is the state of the figure in force (see the constants), Value the safe requests per second of one Pod when ready, Note the sentence behind the state.
	State string  `json:"state"`
	Value float64 `json:"value"`
	Note  string  `json:"note,omitempty"`
	// Manual is the entered figure; Estimate what the history says (shown beside the entered figure as a reference, with its own state).
	Manual        float64 `json:"manual"`
	Estimate      float64 `json:"estimate"`
	EstimateState string  `json:"estimate_state"`
	EstimateNote  string  `json:"estimate_note,omitempty"`
	// Samples are the moments the estimate looked at and Used the ones that were busy enough to count (see the rule below).
	Samples int `json:"samples"`
	Used    int `json:"used"`
	// The rule of the estimate: history of LookbackDays; a moment counts when the CPU is at least BusyShare of the capacity reserve coefficient times the Pod's CPU
	// request; at least MinSamples such moments are needed; the figure is the P50 of requests per second per Pod scaled to the coefficient.
	LookbackDays int     `json:"lookback_days"`
	BusyShare    float64 `json:"busy_share"`
	MinSamples   int     `json:"min_samples"`
	Rule         string  `json:"rule,omitempty"`
	// Reserve is the capacity reserve coefficient in force and ReserveSet whether the module sets one.
	Reserve    float64 `json:"reserve"`
	ReserveSet bool    `json:"reserve_set"`
	// PodCPURequestMilli and PodMemoryBytes are the Pod requests entered for the dynamic replicas (0 = not entered).
	PodCPURequestMilli int64 `json:"pod_cpu_request_milli"`
	PodMemoryBytes     int64 `json:"pod_memory_request_bytes"`
}

// CapacityReportOf reads the capacity of the module: the entered figure, and what the collected history says of it. It never fails because of the history: a module without
// any is a state, not an error.
func (s *Service) CapacityReportOf(ctx context.Context, moduleID uint) (*CapacityReport, error) {
	m, err := s.Modules.Get(ctx, moduleID)
	if err != nil {
		return nil, err
	}
	rep := &CapacityReport{
		Mode: m.CapacityMode, Manual: m.PodCapacityRPS, LookbackDays: int(capacityLookback / (24 * time.Hour)), BusyShare: decision.BusyShare, MinSamples: decision.MinCapacitySamples,
		Reserve: m.CapacityReserve(), ReserveSet: m.TargetUtilization > 0 && m.TargetUtilization <= 1, PodCPURequestMilli: m.PodCPURequestMilli, PodMemoryBytes: m.PodMemoryRequestBytes,
		Rule: decision.CapacityRule,
	}
	if rep.Mode == "" {
		rep.Mode = model.CapacityModeManual
	}
	switch {
	case m.PodCPURequestMilli <= 0:
		rep.EstimateState, rep.EstimateNote = CapacityNotSet, "the CPU request of a Pod is not entered, and the estimate needs it to tell a busy Pod from an idle one"
	default:
		bs, err := s.boundSet(ctx, m)
		if err != nil {
			return nil, err
		}
		d, err := s.estimateCapacity(ctx, m, bs, s.now())
		rep.Samples, rep.Used = d.Samples, d.Used
		switch {
		case err == nil:
			rep.EstimateState, rep.Estimate = CapacityReady, d.RPSPerPod
			rep.EstimateNote = d.Note
		case errors.Is(err, errCapacityNoSource):
			rep.EstimateState, rep.EstimateNote = CapacityNoSource, err.Error()
		case errors.Is(err, errCapacityNoHistory), d.Samples == 0:
			rep.EstimateState, rep.EstimateNote = CapacityNoHistory, err.Error()
		case d.Used < decision.MinCapacitySamples:
			rep.EstimateState, rep.EstimateNote = CapacityInsufficient, err.Error()
		default:
			rep.EstimateState, rep.EstimateNote = CapacityError, err.Error()
		}
	}
	if rep.Mode == model.CapacityModeEstimate {
		rep.State, rep.Value, rep.Note = rep.EstimateState, rep.Estimate, rep.EstimateNote
		return rep, nil
	}
	if m.PodCapacityRPS > 0 {
		rep.State, rep.Value = CapacityReady, m.PodCapacityRPS
	} else {
		rep.State, rep.Note = CapacityNotSet, "the safe requests per second of a Pod is not entered"
	}
	return rep, nil
}
