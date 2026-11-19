package timing

// The numbers of the over-provisioning analysis (P10 A2, in pkg/overprovision): the margins of the suggestions, the limits of throttling, the shares
// of the grades and of the confidence, and how long the results are kept. They are the proposals of the plan; no real data has tested them, which is
// why they are parameters.
var (
	OPCPUMargin = Param{Key: "analysis.cpu_margin_percent", Default: 20, Min: 0, Max: 200,
		Name:   "CPU Suggestion Margin (percent)",
		Remark: "The CPU a Pod is suggested is the larger of its P95 use plus this margin and its P99 use (0-200). No real data has tested it."}
	OPMemoryMargin = Param{Key: "analysis.memory_margin_percent", Default: 15, Min: 0, Max: 200,
		Name:   "Memory Suggestion Margin (percent)",
		Remark: "The memory a Pod is suggested is the larger of its P99 working set plus this margin and its highest working set times the peak factor (0-200)."}
	OPMemoryPeak = Param{Key: "analysis.memory_peak_percent", Default: 105, Min: 100, Max: 300,
		Name:   "Memory Peak Factor (percent)",
		Remark: "The highest working set of the window is multiplied by this to give the lower bound of the memory suggestion (100-300; 105 is 5% above the peak)."}
	OPOOMUplift = Param{Key: "analysis.oom_uplift_percent", Default: 120, Min: 100, Max: 300,
		Name:   "OOM Uplift (percent)",
		Remark: "When the memory was OOM-killed in the window, the suggestion is never below the working set measured at the kill times this, and never below the request it has now: lowering it is not suggested (100-300)."}
	OPThrottle = Param{Key: "analysis.throttle_percent", Default: 5, Min: 1, Max: 100,
		Name:   "CPU Throttle Limit (percent)",
		Remark: "When the CPU was throttled for more than this share of the time in the window, its use understates what it needed: the request is not called over-provisioned and not suggested lower (1-100). It needs a throttle metric (k8s_cpu_throttle, seconds throttled per second) on the module."}
	OPBaselineDays = Param{Key: "analysis.baseline_window_days", Default: 7, Min: 1, Max: 30,
		Name:   "Baseline Analysis Window (days)",
		Remark: "The window the resource dashboard shows, one of 1, 7, 15 or 30 days (another number is taken to the nearest). A result over a shorter window lowers its confidence by one level."}
	OPGradeSevere = Param{Key: "analysis.grade_severe_percent", Default: 50, Min: 10, Max: 100,
		Name:   "Severe Over-provision From (percent)",
		Remark: "A request is severely over-provisioned when this share of it, or more, is above the suggestion (10-100). The moderate and mild limits must be lower."}
	OPGradeModerate = Param{Key: "analysis.grade_moderate_percent", Default: 30, Min: 5, Max: 99,
		Name:   "Moderate Over-provision From (percent)",
		Remark: "A request is moderately over-provisioned from this share above the suggestion (5-99)."}
	OPGradeMild = Param{Key: "analysis.grade_mild_percent", Default: 15, Min: 1, Max: 98,
		Name:   "Mild Over-provision From (percent)",
		Remark: "A request is mildly over-provisioned from this share above the suggestion; under it the request is reasonable (1-98)."}
	OPCoverageHigh = Param{Key: "analysis.coverage_high_percent", Default: 80, Min: 10, Max: 100,
		Name:   "Data Coverage For High Confidence (percent)",
		Remark: "The share of the window that must have measured data for a high confidence (10-100)."}
	OPCoverageMid = Param{Key: "analysis.coverage_medium_percent", Default: 50, Min: 5, Max: 99,
		Name:   "Data Coverage For Any Suggestion (percent)",
		Remark: "Under this share of the window with data, the confidence is low and no suggestion is made; from it up the confidence is medium (5-99)."}
	OPRetentionDays = Param{Key: "analysis.result_retention_days", Default: 90, Min: 1, Max: 3650,
		Name:   "Analysis Result Retention (days)",
		Remark: "How long the stored results of the over-provisioning analysis are kept (1-3650). The scheduled analysis adds seven windows for each resource of each module on every run."}
)
