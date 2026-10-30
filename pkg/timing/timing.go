// Package timing holds the time limits of the platform and of the operator that an administrator may change. Each is a system
// parameter (one row of sys_config): the program carries only the default and the range it accepts, so a limit is never fixed in code.
// The platform reads its own limits from the database (see forecastsvc.Service.Timing); the operator reads its limits from the platform
// (GET /api/v1/operator/settings), keeps the last answer and asks again from time to time.
//
// The package has no dependencies, so the operator can use it without the database.
package timing

import (
	"strconv"
	"strings"
	"time"
)

// Param is one limit: the key of its system parameter, its default and the range it is kept in. A value that is missing, not a whole
// number or outside the range never reaches the program as it is: a missing or unreadable one is the default, one outside the range is
// brought to the nearest end.
type Param struct {
	Key     string
	Default int
	Min     int
	Max     int
	// Name and Remark are what the system parameter page shows.
	Name   string
	Remark string
}

// Platform limits (read by the platform from the database).
var (
	DecisionRefreshWait = Param{Key: "forecast.decision_refresh_wait_seconds", Default: 8, Min: 1, Max: 120,
		Name:   "Operator Request Refresh Wait (seconds)",
		Remark: "How long a request of the operator waits for the platform to make the recommendation again when the stored one is older than forecast.decision_max_age_seconds. When it runs out the stored recommendation is answered and the refresh goes on in the background. It is always kept below operator.request_timeout_seconds, so the answer comes before the operator hangs up (1-120)."}
	RefreshBudget = Param{Key: "forecast.refresh_budget_seconds", Default: 300, Min: 10, Max: 3600,
		Name:   "Recommendation Refresh Budget (seconds)",
		Remark: "The longest one refresh of a module may run (collect, forecast with every algorithm, decide). It runs on its own, not tied to the request of the operator that asked for it (10-3600)."}
	PipelineTimeout = Param{Key: "forecast.pipeline_timeout_seconds", Default: 300, Min: 10, Max: 3600,
		Name:   "Manual Pipeline Step Timeout (seconds)",
		Remark: "The longest a step triggered by hand from the pages (collect, predict, backtest, evaluate, run) may take (10-3600)."}
	PreviewBudget = Param{Key: "forecast.preview_budget_seconds", Default: 60, Min: 5, Max: 600,
		Name:   "Change Preview Budget (seconds)",
		Remark: "The longest a change preview may replay the past; a longer one is refused with a message, never answered half way (5-600)."}
	FineTuneBudget = Param{Key: "forecast.fine_tune_budget_seconds", Default: 1800, Min: 30, Max: 86400,
		Name:   "Model Service Fine-tune Budget (seconds)",
		Remark: "The longest one fine-tune request to a model service may take. A fine-tune runs in the background and forecasts never wait for it (30-86400)."}
	TrainBudget = Param{Key: "forecast.train_budget_seconds", Default: 120, Min: 5, Max: 3600,
		Name:   "Forecast Budget Per Algorithm (seconds)",
		Remark: "The longest one forecast of one algorithm may take when the algorithm sets no budget of its own on the algorithm page (5-3600)."}
)

// Operator limits (read by the operator from the platform).
var (
	OperatorRequestTimeout = Param{Key: "operator.request_timeout_seconds", Default: 10, Min: 1, Max: 120,
		Name:   "Operator Request Timeout (seconds)",
		Remark: "How long the operator waits for one answer of the platform before it counts the platform as unreachable and goes on with what it remembers. Keep forecast.decision_refresh_wait_seconds below it (1-120)."}
	OperatorSyncInterval = Param{Key: "operator.sync_interval_seconds", Default: 30, Min: 5, Max: 3600,
		Name:   "Operator Module Sync Interval (seconds)",
		Remark: "How often the operator reads the modules of the platform and applies their replicas (5-3600)."}
	OperatorDecisionCacheTTL = Param{Key: "operator.decision_cache_ttl_seconds", Default: 600, Min: 0, Max: 86400,
		Name:   "Operator Decision Cache (seconds)",
		Remark: "How long after the last answer the operator may still act on the decision it remembers while the platform cannot be reached (replicas may rise to it, never fall). 0 turns the cache off (0-86400)."}
	OperatorModuleCacheTTL = Param{Key: "operator.module_cache_ttl_seconds", Default: 3600, Min: 0, Max: 604800,
		Name:   "Operator Module List Cache (seconds)",
		Remark: "How long the last list of modules (bounds, pace, fallback) is used while the platform cannot be reached. Longer than the decision cache, so the safe degradation after the decision has expired still has the bounds and the fallback of the module (0-604800)."}
	OperatorRecoverAnswers = Param{Key: "operator.recover_answers", Default: 2, Min: 1, Max: 100,
		Name:   "Operator Answers Before Scaling Down Again",
		Remark: "How many answers in a row a platform that is back after an outage must give before the workload may be scaled down again; scaling up follows at once (1-100)."}
	OperatorSettingsRefresh = Param{Key: "operator.settings_refresh_seconds", Default: 60, Min: 10, Max: 3600,
		Name:   "Operator Settings Refresh (seconds)",
		Remark: "How often the operator asks the platform for these settings again. It keeps the last answer when the platform cannot be reached (10-3600)."}
)

// All lists every limit, for the system parameter seed.
func All() []Param {
	return []Param{DecisionRefreshWait, RefreshBudget, PipelineTimeout, PreviewBudget, FineTuneBudget, TrainBudget,
		OperatorRequestTimeout, OperatorSyncInterval, OperatorDecisionCacheTTL, OperatorModuleCacheTTL, OperatorRecoverAnswers, OperatorSettingsRefresh}
}

// Clamp brings a number into the range of the limit.
func (p Param) Clamp(n int) int {
	if n < p.Min {
		return p.Min
	}
	if n > p.Max {
		return p.Max
	}
	return n
}

// Parse reads the stored text of the limit: the default when it is empty or not a whole number, else the number kept in the range.
func (p Param) Parse(raw string) int {
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return p.Default
	}
	return p.Clamp(n)
}

// Seconds is Parse as a duration.
func (p Param) Seconds(raw string) time.Duration { return time.Duration(p.Parse(raw)) * time.Second }

// OperatorSettings are the limits the operator takes from the platform.
type OperatorSettings struct {
	RequestTimeoutSeconds   int `json:"request_timeout_seconds"`
	SyncIntervalSeconds     int `json:"sync_interval_seconds"`
	DecisionCacheTTLSeconds int `json:"decision_cache_ttl_seconds"`
	ModuleCacheTTLSeconds   int `json:"module_cache_ttl_seconds"`
	RecoverAnswers          int `json:"recover_answers"`
	SettingsRefreshSeconds  int `json:"settings_refresh_seconds"`
}

// DefaultOperatorSettings are the limits before the platform has been asked, and for any the platform does not send.
func DefaultOperatorSettings() OperatorSettings {
	return OperatorSettings{
		RequestTimeoutSeconds: OperatorRequestTimeout.Default, SyncIntervalSeconds: OperatorSyncInterval.Default,
		DecisionCacheTTLSeconds: OperatorDecisionCacheTTL.Default, ModuleCacheTTLSeconds: OperatorModuleCacheTTL.Default,
		RecoverAnswers: OperatorRecoverAnswers.Default, SettingsRefreshSeconds: OperatorSettingsRefresh.Default,
	}
}

// Normalize keeps every limit in its range, whoever sent it.
func (s OperatorSettings) Normalize() OperatorSettings {
	return OperatorSettings{
		RequestTimeoutSeconds:   OperatorRequestTimeout.Clamp(s.RequestTimeoutSeconds),
		SyncIntervalSeconds:     OperatorSyncInterval.Clamp(s.SyncIntervalSeconds),
		DecisionCacheTTLSeconds: OperatorDecisionCacheTTL.Clamp(s.DecisionCacheTTLSeconds),
		ModuleCacheTTLSeconds:   OperatorModuleCacheTTL.Clamp(s.ModuleCacheTTLSeconds),
		RecoverAnswers:          OperatorRecoverAnswers.Clamp(s.RecoverAnswers),
		SettingsRefreshSeconds:  OperatorSettingsRefresh.Clamp(s.SettingsRefreshSeconds),
	}
}

// Seconds converts a number of seconds to a duration.
func Seconds(n int) time.Duration { return time.Duration(n) * time.Second }

// LoadOperator reads the operator limits through read, which returns the stored text of a key (and whether there is one).
func LoadOperator(read func(key string) (string, bool)) OperatorSettings {
	get := func(p Param) int {
		raw, _ := read(p.Key)
		return p.Parse(raw)
	}
	return OperatorSettings{
		RequestTimeoutSeconds: get(OperatorRequestTimeout), SyncIntervalSeconds: get(OperatorSyncInterval),
		DecisionCacheTTLSeconds: get(OperatorDecisionCacheTTL), ModuleCacheTTLSeconds: get(OperatorModuleCacheTTL),
		RecoverAnswers: get(OperatorRecoverAnswers), SettingsRefreshSeconds: get(OperatorSettingsRefresh),
	}
}
