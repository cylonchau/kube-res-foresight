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
	DecisionForecastMaxAge = Param{Key: "forecast.decision_forecast_max_age_seconds", Default: 600, Min: 60, Max: 86400,
		Name:   "Forecast Reuse Age On Operator Refresh (seconds)",
		Remark: "When the operator asks and the stored recommendation is too old, the platform decides again on the newest measured load. It reuses the forecasts the scheduled task made if they are no older than this, and only forecasts again with every algorithm when they are older (for example when the scheduled task is not running). Keep it above forecast.run period so the algorithms are not run a second time between two scheduled runs (60-86400)."}
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

// CapacitySLOTarget is not a time but the target the capacity SLO is judged against, kept with the limits because it is read and bounded the
// same way: the share of the time at least as many replicas were Ready as the measured load needed (see forecastsvc EffectReport).
var CapacitySLOTarget = Param{Key: "forecast.slo_capacity_target_percent", Default: 99, Min: 50, Max: 100,
	Name:   "Capacity SLO Target (percent)",
	Remark: "The capacity SLO of a module is the share of the measured time in which at least as many replicas were Ready as the measured load needed (at the target utilization, with no safety margin). The effect report says it is met when the share reaches this percent. It is a capacity target, not a latency or error-rate target (50-100)."}

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
		Remark: "How many answers in a row a platform that is back after an outage must give (and for how long, see the stable period) before the workload may be scaled down again; scaling up follows at once (1-100)."}
	OperatorDegradeHold = Param{Key: "operator.degrade_hold_seconds", Default: 300, Min: 0, Max: 86400,
		Name:   "Operator Degrade Hold (seconds)",
		Remark: "When the platform cannot be reached and no usable decision is remembered (it expired, or the operator restarted during the outage), the operator keeps the current replicas for this long before it applies the module's fallback replicas. An outage shorter than the decision cache plus this hold changes nothing. 0 applies the fallback at once (0-86400)."}
	OperatorDegradeCooldown = Param{Key: "operator.degrade_cooldown_seconds", Default: 300, Min: 0, Max: 86400,
		Name:   "Operator Degrade Cooldown (seconds)",
		Remark: "Once the hold has ended, the fallback replicas are applied at most once in this time (and still at most the module's step per action), so a long outage raises the workload in steps and never in one jump. 0 leaves the pace to the module's own scale-up cooldown (0-86400)."}
	OperatorRecoverStable = Param{Key: "operator.recover_stable_seconds", Default: 300, Min: 0, Max: 86400,
		Name:   "Operator Stable Period After Recovery (seconds)",
		Remark: "After an outage the platform must keep answering for this long, and give the number of answers above, before the workload may be scaled down again; scaling up follows at once. 0 turns the time condition off (0-86400)."}
	OperatorSettingsRefresh = Param{Key: "operator.settings_refresh_seconds", Default: 60, Min: 10, Max: 3600,
		Name:   "Operator Settings Refresh (seconds)",
		Remark: "How often the operator asks the platform for these settings again. It keeps the last answer when the platform cannot be reached (10-3600)."}
)

// The assistant (the chat that reads the platform's state for the user): how long one answer of the language model may take, how many
// times it may ask for data before it must answer, and how long a reply may be.
var (
	AssistantRequestTimeout = Param{Key: "assistant.request_timeout_seconds", Default: 60, Min: 5, Max: 300,
		Name:   "Assistant Request Timeout (seconds)",
		Remark: "How long one question to the assistant may take in all: every call to the language model and every read of the platform's state inside it (5-300)."}
	AssistantMaxToolRounds = Param{Key: "assistant.max_tool_rounds", Default: 6, Min: 1, Max: 12,
		Name:   "Assistant Reads Per Question",
		Remark: "How many times the language model may ask the platform for data while it answers one question; past it, it must answer with what it has (1-12)."}
	AssistantMaxReplyTokens = Param{Key: "assistant.max_reply_tokens", Default: 1500, Min: 128, Max: 8192,
		Name:   "Assistant Reply Length (tokens)",
		Remark: "The most tokens one reply of the language model may have; it bounds what a question costs (128-8192)."}
)

// All lists every limit, for the system parameter seed.
func All() []Param {
	return []Param{DecisionRefreshWait, DecisionForecastMaxAge, CapacitySLOTarget, RefreshBudget, PipelineTimeout, PreviewBudget, FineTuneBudget, TrainBudget,
		OperatorRequestTimeout, OperatorSyncInterval, OperatorDecisionCacheTTL, OperatorModuleCacheTTL, OperatorRecoverAnswers, OperatorSettingsRefresh,
		OperatorDegradeHold, OperatorDegradeCooldown, OperatorRecoverStable,
		AssistantRequestTimeout, AssistantMaxToolRounds, AssistantMaxReplyTokens}
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
	// DegradeHoldSeconds, DegradeCooldownSeconds and RecoverStableSeconds are the time rules of an outage of the platform and of its end.
	DegradeHoldSeconds     int `json:"degrade_hold_seconds"`
	DegradeCooldownSeconds int `json:"degrade_cooldown_seconds"`
	RecoverStableSeconds   int `json:"recover_stable_seconds"`
}

// DefaultOperatorSettings are the limits before the platform has been asked, and for any the platform does not send.
func DefaultOperatorSettings() OperatorSettings {
	return OperatorSettings{
		RequestTimeoutSeconds: OperatorRequestTimeout.Default, SyncIntervalSeconds: OperatorSyncInterval.Default,
		DecisionCacheTTLSeconds: OperatorDecisionCacheTTL.Default, ModuleCacheTTLSeconds: OperatorModuleCacheTTL.Default,
		RecoverAnswers: OperatorRecoverAnswers.Default, SettingsRefreshSeconds: OperatorSettingsRefresh.Default,
		DegradeHoldSeconds: OperatorDegradeHold.Default, DegradeCooldownSeconds: OperatorDegradeCooldown.Default,
		RecoverStableSeconds: OperatorRecoverStable.Default,
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
		DegradeHoldSeconds:      OperatorDegradeHold.Clamp(s.DegradeHoldSeconds),
		DegradeCooldownSeconds:  OperatorDegradeCooldown.Clamp(s.DegradeCooldownSeconds),
		RecoverStableSeconds:    OperatorRecoverStable.Clamp(s.RecoverStableSeconds),
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
		DegradeHoldSeconds: get(OperatorDegradeHold), DegradeCooldownSeconds: get(OperatorDegradeCooldown),
		RecoverStableSeconds: get(OperatorRecoverStable),
	}
}
