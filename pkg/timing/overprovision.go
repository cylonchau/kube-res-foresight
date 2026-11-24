package timing

// The numbers of the over-provisioning analysis (P10 A2, in pkg/overprovision): the margins of the suggestions, the limits of throttling, the shares
// of the grades and of the confidence, and how long the results are kept. They are the proposals of the plan; no real data has tested them, which is
// why they are parameters.
var (
	// The recommendation rules (version 2): where the use of the request has to stand. Every share is a percent of the request of one Pod. For each of P90, P95 and P99 the
	// target is the share the percentile takes once the request is the recommended one, and the use is reasonable between low and high. The recommended request is the
	// least one that puts every percentile at or under its target; below low the request is over-provisioned, above high it is under-provisioned.
	OPP90Target = Param{Key: "analysis.rec_p90_target_percent", Default: 80, Min: 10, Max: 200,
		Name:   "Recommendation P90 Target (percent of Requests)",
		Remark: "The share of the Requests the P90 use should take at the recommended Requests (10-200). The recommended Requests is the largest of what the P90, P95 and P99 each ask for: use divided by its target."}
	OPP90Low = Param{Key: "analysis.rec_p90_low_percent", Default: 70, Min: 5, Max: 200,
		Name:   "Recommendation P90 Reasonable From (percent of Requests)",
		Remark: "The Requests is over-provisioned when the P90 use is below this share of it (5-200). Keep it below the high limit."}
	OPP90High = Param{Key: "analysis.rec_p90_high_percent", Default: 85, Min: 10, Max: 300,
		Name:   "Recommendation P90 Reasonable Up To (percent of Requests)",
		Remark: "The Requests is under-provisioned when the P90 use is above this share of it (10-300)."}
	OPP95Target = Param{Key: "analysis.rec_p95_target_percent", Default: 85, Min: 10, Max: 200,
		Name:   "Recommendation P95 Target (percent of Requests)",
		Remark: "The share of the Requests the P95 use should take at the recommended Requests (10-200)."}
	OPP95Low = Param{Key: "analysis.rec_p95_low_percent", Default: 80, Min: 5, Max: 200,
		Name:   "Recommendation P95 Reasonable From (percent of Requests)",
		Remark: "The Requests is over-provisioned when the P95 use is below this share of it, for example under 80% (5-200)."}
	OPP95High = Param{Key: "analysis.rec_p95_high_percent", Default: 90, Min: 10, Max: 300,
		Name:   "Recommendation P95 Reasonable Up To (percent of Requests)",
		Remark: "The Requests is under-provisioned when the P95 use is above this share of it, for example over 90% (10-300)."}
	OPP99Target = Param{Key: "analysis.rec_p99_target_percent", Default: 110, Min: 10, Max: 300,
		Name:   "Recommendation P99 Target (percent of Requests)",
		Remark: "The share of the Requests the P99 use should take at the recommended Requests (10-300). Above 100 lets the tail go over the Requests, which a CPU may do up to its Limits."}
	OPP99Low = Param{Key: "analysis.rec_p99_low_percent", Default: 80, Min: 5, Max: 300,
		Name:   "Recommendation P99 Reasonable From (percent of Requests)",
		Remark: "The Requests is over-provisioned when the P99 use is below this share of it (5-300)."}
	OPP99High = Param{Key: "analysis.rec_p99_high_percent", Default: 120, Min: 10, Max: 400,
		Name:   "Recommendation P99 Reasonable Up To (percent of Requests)",
		Remark: "The tail is above its range over this share of the Requests. With a steady use that is a real one the Requests is under-provisioned; with a low steady use it is a burst that comes now and then, and the Requests is not lowered for it (10-400)."}
	OPLimitHeadroom = Param{Key: "analysis.rec_limit_headroom_percent", Default: 20, Min: 0, Max: 300,
		Name:   "Recommendation Limits Headroom (percent)",
		Remark: "The recommended Limits is the P99 use plus this margin, and never below the recommended Requests (0-300). The Limits of a module that admission sets from the Requests are the Requests times its limit ratio, not this."}
	OPLimitGuard = Param{Key: "analysis.rec_limit_guard_percent", Default: 90, Min: 30, Max: 100,
		Name:   "Recommendation Limits Guard (percent of Limits)",
		Remark: "The P99 use of all the Pods together must stay under this share of their Limits at the recommended replicas, so a replica count is never taken so low that the tail runs into the Limits (30-100). It is also the share of the Limits above which the peak counts as near the Limits in the risk of a result. A module can set its own in module management."}
	OPSustain = Param{Key: "analysis.rec_sustain_minutes", Default: 30, Min: 0, Max: 720,
		Name:   "Recommendation Sustain Window (minutes)",
		Remark: "The percentiles the recommended Requests and replicas are made of are taken of the use held for this many minutes (every point is the mean of the use over the window that ends at it), so a peak that lasts for less than the window does not raise a long-term recommendation, and a use that lasts for the whole window does. The Limits is still sized for the raw peak. 0 takes the percentiles of the raw points (0-720). A module can set its own in module management."}
	OPOOMUplift = Param{Key: "analysis.oom_uplift_percent", Default: 120, Min: 100, Max: 300,
		Name:   "OOM Uplift (percent)",
		Remark: "When the memory was OOM-killed in the window, the suggestion is never below the working set measured at the kill times this, and never below the request it has now: lowering it is not suggested (100-300)."}
	OPThrottle = Param{Key: "analysis.throttle_percent", Default: 5, Min: 1, Max: 100,
		Name:   "CPU Throttle Limit (percent)",
		Remark: "When the CPU was throttled for more than this share of the time in the window, its use understates what it needed: the request is not called over-provisioned and not suggested lower (1-100). It needs a throttle metric (k8s_cpu_throttle, seconds throttled per second) on the module."}
	OPBaselineDays = Param{Key: "analysis.baseline_window_days", Default: 7, Min: 1, Max: 30,
		Name:   "Recommendation Analysis Period (days)",
		Remark: "The period the verdict and the recommended Requests and Limits are worked out over, one of 1, 7, 15 or 30 days (another number is taken to the nearest). It does not follow the window chosen for the charts of the resource dashboard: a day of peak in a month does not decide the recommendation. A result over a shorter period lowers its confidence by one level."}
	OPGradeSevere = Param{Key: "analysis.grade_severe_percent", Default: 50, Min: 10, Max: 100,
		Name:   "Severe Over-provision From (percent)",
		Remark: "An over-provisioned request is severely so when this share of it, or more, is above the recommendation (10-100). The moderate limit must be lower."}
	OPGradeModerate = Param{Key: "analysis.grade_moderate_percent", Default: 30, Min: 5, Max: 99,
		Name:   "Moderate Over-provision From (percent)",
		Remark: "An over-provisioned request is moderately so from this share above the recommendation, and mildly so under it (5-99)."}
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
