package timing

// The verdicts of the resource analysis: a request whose P95 use is under OverprovisionUtilization of it is over-provisioned, one whose P95 use is
// over TightUtilization of it leaves too little room. They are starting points that no real data has tested, which is why they are parameters.
var (
	OverprovisionUtilization = Param{Key: "analysis.overprovision_utilization_percent", Default: 50, Min: 5, Max: 95,
		Name:   "Over-provisioned Below Utilization (percent)",
		Remark: "The resource analysis calls a Pod request over-provisioned when the P95 use of the window is below this share of the request (5-95). A starting point that no real data has tested: change it with what you see."}
	TightUtilization = Param{Key: "analysis.tight_utilization_percent", Default: 85, Min: 50, Max: 100,
		Name:   "Tight Above Utilization (percent)",
		Remark: "The resource analysis calls a Pod request tight when the P95 use of the window is above this share of the request (50-100): there is little room left for a peak. A starting point that no real data has tested."}
)
