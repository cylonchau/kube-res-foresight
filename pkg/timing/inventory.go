package timing

// InventorySnapshotDays is how long the snapshots of what the operator read from the cluster are kept (P10 A1): the nodes of every report, and the replicas of
// the workloads at every report. The report of an hour is what the trend of ready nodes and of the replicas of an HPA is made of.
var InventorySnapshotDays = Param{Key: "inventory.snapshot_retention_days", Default: 30, Min: 1, Max: 365,
	Name:   "Cluster Snapshots Kept (days)",
	Remark: "How many days the node snapshots and the replica snapshots the operator reports are kept: the trend of ready nodes and of the replicas of an HPA. The newest report is the cluster as it is and is never cleaned by this (1-365)."}

// InventoryChangeDays is how long the history of the changes of the configuration of a workload is kept.
var InventoryChangeDays = Param{Key: "inventory.change_retention_days", Default: 180, Min: 1, Max: 3650,
	Name:   "Workload Configuration Changes Kept (days)",
	Remark: "How many days the history of the changes of a workload's replicas, container requests and limits, and HPA bounds is kept (1-3650)."}

// ClusterDatasource is the data source the cluster and node figures (what is used, what is allocatable) are asked of.
var ClusterDatasource = Param{Key: "cluster.datasource_id", Default: 0, Min: 0, Max: 1 << 30,
	Name:   "Cluster Figures Data Source (id)",
	Remark: "The id of the Prometheus data source the cluster and node figures of the resource dashboard are asked of. 0 takes the data source of the first enabled module that has a metric source."}

// ClusterRetentionDays is how long what the Cluster Agent reports of the events of a cluster is kept (P12 E4). Rows older than this are deleted by the clean-up task.
var ClusterRetentionDays = Param{Key: "cluster.retention_days", Default: 3, Min: 1, Max: 365,
	Name:   "Cluster Events Kept (days)",
	Remark: "How many days the Kubernetes events the operator reports (the reasons of what happened to nodes, Pods, workloads and autoscalers) are kept; older ones are deleted by the task cluster.prune (1-365). A longer time keeps more rows."}
