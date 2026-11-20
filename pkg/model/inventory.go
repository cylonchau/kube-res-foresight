package model

import "time"

func init() {
	RegisterModel(&InventoryReport{})
	RegisterModel(&ClusterNodeSnapshot{})
	RegisterModel(&WorkloadConfig{})
	RegisterModel(&WorkloadConfigChange{})
	RegisterModel(&WorkloadSnapshot{})
}

// The inventory is what the operator reads from the cluster, read-only, and reports to the platform (P10 A1): the nodes of the cluster, and the Deployment,
// StatefulSet or DaemonSet of every configured module with its Pods and its Containers' requests and limits. Pods and workloads are only those of the
// modules: the platform has no other workload, and the operator reads no other. Usage is not in these tables: it is asked of Prometheus.

// InventoryReport is one report of the operator that the platform has taken, kept so that a report that is sent twice (the answer was lost, the operator
// started again before it knew) is stored once. It holds no inventory itself.
type InventoryReport struct {
	ID         uint      `gorm:"primaryKey" json:"id"`
	RecordID   string    `gorm:"type:varchar(64);not null;uniqueIndex" json:"record_id"`
	Operator   string    `gorm:"type:varchar(253)" json:"operator"`
	ObservedAt time.Time `gorm:"not null;index" json:"observed_at"`
	ReceivedAt time.Time `gorm:"not null" json:"received_at"`
	Nodes      int       `gorm:"not null;default:0" json:"nodes"`
	Workloads  int       `gorm:"not null;default:0" json:"workloads"`
}

func (InventoryReport) TableName() string { return "inventory_report" }

// ClusterNodeSnapshot is one node as one report saw it. The newest report is the cluster as it is; the older ones are the history of the number of nodes
// that were ready, and of their capacity. ModulePods, Req* and Lim* are the sums over the Pods of the configured modules that run on the node, not over
// every Pod of the node (the operator reads no other Pod). CPU is in millicores and memory in bytes.
type ClusterNodeSnapshot struct {
	ID         uint      `gorm:"primaryKey" json:"id"`
	ObservedAt time.Time `gorm:"not null;index:idx_node_snapshot_at;index:idx_node_snapshot_node,priority:2" json:"observed_at"`
	Cluster    string    `gorm:"type:varchar(64);not null;default:''" json:"cluster"`
	Node       string    `gorm:"type:varchar(253);not null;index:idx_node_snapshot_node,priority:1" json:"node"`

	Ready         bool   `gorm:"not null;default:false" json:"ready"`
	Unschedulable bool   `gorm:"not null;default:false" json:"unschedulable"`
	Pressure      string `gorm:"type:varchar(120);not null;default:''" json:"pressure"`

	Provider       string `gorm:"type:varchar(32);not null;default:''" json:"provider"`
	Region         string `gorm:"type:varchar(64);not null;default:''" json:"region"`
	Zone           string `gorm:"type:varchar(64);not null;default:''" json:"zone"`
	InstanceType   string `gorm:"type:varchar(64);not null;default:''" json:"instance_type"`
	KubeletVersion string `gorm:"type:varchar(32);not null;default:''" json:"kubelet_version"`

	CapCPUMilli      int64 `gorm:"not null;default:0" json:"cap_cpu_milli"`
	CapMemoryBytes   int64 `gorm:"not null;default:0" json:"cap_memory_bytes"`
	CapPods          int64 `gorm:"not null;default:0" json:"cap_pods"`
	AllocCPUMilli    int64 `gorm:"not null;default:0" json:"alloc_cpu_milli"`
	AllocMemoryBytes int64 `gorm:"not null;default:0" json:"alloc_memory_bytes"`
	AllocPods        int64 `gorm:"not null;default:0" json:"alloc_pods"`

	ModulePods     int   `gorm:"not null;default:0" json:"module_pods"`
	ReqCPUMilli    int64 `gorm:"not null;default:0" json:"req_cpu_milli"`
	ReqMemoryBytes int64 `gorm:"not null;default:0" json:"req_memory_bytes"`
	LimCPUMilli    int64 `gorm:"not null;default:0" json:"lim_cpu_milli"`
	LimMemoryBytes int64 `gorm:"not null;default:0" json:"lim_memory_bytes"`
}

func (ClusterNodeSnapshot) TableName() string { return "cluster_node_snapshot" }

// WorkloadConfig is the workload of one module as the newest report saw it: replicas, the requests and limits of each Container (JSON), its Pods (JSON) and
// the HPA or KEDA object that scales it, if there is one (JSON). One row for each module; a change of it is written to WorkloadConfigChange.
type WorkloadConfig struct {
	ID         uint   `gorm:"primaryKey" json:"id"`
	ModuleID   uint   `gorm:"not null;uniqueIndex" json:"module_id"`
	Cluster    string `gorm:"type:varchar(64);not null;default:''" json:"cluster"`
	Namespace  string `gorm:"type:varchar(253);not null" json:"namespace"`
	Kind       string `gorm:"type:varchar(16);not null" json:"kind"`
	Name       string `gorm:"type:varchar(253);not null" json:"name"`
	Desired    int    `gorm:"not null;default:0" json:"desired"`
	Ready      int    `gorm:"not null;default:0" json:"ready"`
	Available  int    `gorm:"not null;default:0" json:"available"`
	Updated    int    `gorm:"not null;default:0" json:"updated"`
	Generation int64  `gorm:"not null;default:0" json:"generation"`
	Containers string `gorm:"type:text" json:"-"`
	Pods       string `gorm:"type:text" json:"-"`
	HPA        string `gorm:"type:text" json:"-"`
	// Missing is true while the operator does not find the workload in the cluster; Error is why it could not be read.
	Missing    bool      `gorm:"not null;default:false" json:"missing"`
	Error      string    `gorm:"type:varchar(300);not null;default:''" json:"error"`
	ObservedAt time.Time `gorm:"not null" json:"observed_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

func (WorkloadConfig) TableName() string { return "workload_config" }

// WorkloadConfigChange is one change of the configuration of a workload: what was different, in the words of the path and the two values. It is the history
// of the requests and limits (and replicas and HPA bounds) that the page shows, and it is kept longer than the snapshots.
type WorkloadConfigChange struct {
	ID         uint      `gorm:"primaryKey" json:"id"`
	ModuleID   uint      `gorm:"not null;index:idx_workload_change,priority:1" json:"module_id"`
	ObservedAt time.Time `gorm:"not null;index:idx_workload_change,priority:2;index:idx_workload_change_at" json:"observed_at"`
	Cluster    string    `gorm:"type:varchar(64);not null;default:''" json:"cluster"`
	Kind       string    `gorm:"type:varchar(16);not null" json:"kind"`
	Name       string    `gorm:"type:varchar(253);not null" json:"name"`
	// Changes is a JSON list of {path, from, to}; Summary is the same as one line.
	Changes string `gorm:"type:text" json:"-"`
	Summary string `gorm:"type:varchar(500);not null;default:''" json:"summary"`
}

func (WorkloadConfigChange) TableName() string { return "workload_config_change" }

// WorkloadSnapshot is the replicas of a workload and of its HPA at one report: the trend of the replicas the HPA asked for and the workload had.
type WorkloadSnapshot struct {
	ID         uint      `gorm:"primaryKey" json:"id"`
	ModuleID   uint      `gorm:"not null;index:idx_workload_snapshot,priority:1" json:"module_id"`
	ObservedAt time.Time `gorm:"not null;index:idx_workload_snapshot,priority:2;index:idx_workload_snapshot_at" json:"observed_at"`
	Desired    int       `gorm:"not null;default:0" json:"desired"`
	Ready      int       `gorm:"not null;default:0" json:"ready"`
	HPAKind    string    `gorm:"type:varchar(8);not null;default:''" json:"hpa_kind"`
	HPAMin     int       `gorm:"not null;default:0" json:"hpa_min"`
	HPAMax     int       `gorm:"not null;default:0" json:"hpa_max"`
	HPACurrent int       `gorm:"not null;default:0" json:"hpa_current"`
	HPADesired int       `gorm:"not null;default:0" json:"hpa_desired"`
	Pods       int       `gorm:"not null;default:0" json:"pods"`
	Restarts   int       `gorm:"not null;default:0" json:"restarts"`
}

func (WorkloadSnapshot) TableName() string { return "workload_snapshot" }
