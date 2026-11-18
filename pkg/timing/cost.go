package timing

// The parameters of the cost side (see package cost and requirements/references/cost-closure-p9.md). They are kept with the limits because
// they are read and bounded the same way (a whole number, kept in its range).
var (
	// CostDatasource is the data source (a Prometheus that scrapes kube-state-metrics) the cluster is read from for the cost.
	CostDatasource = Param{Key: "cost.datasource_id", Default: 0, Min: 0, Max: 1 << 30,
		Name:   "Cost: Data Source Id",
		Remark: "The id of the data source whose kube-state-metrics describe the cluster (nodes, Pods, requests). The cost, the node simulation and the money of the waste figures are read from it. 0 means none is chosen: the cost pages then ask for one. A data source can also be picked on the page for a single look."}
	// CostCPUShare is the part of the price of a node put on its CPU; the rest is put on its memory.
	CostCPUShare = Param{Key: "cost.cpu_share_percent", Default: 50, Min: 1, Max: 99,
		Name:   "Cost: Share Of A Node Price Put On CPU (percent)",
		Remark: "The price of a node is shared out by what the Pods request: this percent of it by CPU, the rest by memory. 50 is an assumption, not a measurement; set it to the ratio at which your provider prices a core against a GiB (1-99)."}
	// CostCandidateMargin is the room kept above the measured use by the balanced and the conservative candidates.
	CostCandidateMargin = Param{Key: "cost.candidate_margin_percent", Default: 10, Min: 0, Max: 100,
		Name:   "Cost: Margin Above The Measured Use For Candidates (percent)",
		Remark: "The balanced candidate is the P99 of the measured use of a Pod plus this percent, the conservative one the highest measured use plus this percent; both are kept between the platform's recommendation and what the Pod requests now (0-100)."}
	// CostIdleShare is the share of the request under which even the P99 use makes a workload idle.
	CostIdleShare = Param{Key: "cost.idle_percent", Default: 10, Min: 1, Max: 50,
		Name:   "Cost: Idle Threshold (percent of the request)",
		Remark: "A workload is idle in a resource when even the P99 of its use over the window stays under this percent of what a Pod requests (1-50)."}
)
