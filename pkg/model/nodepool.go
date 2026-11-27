package model

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

func init() { RegisterModel(&NodePool{}) }

// The kinds of a node pool: what the nodes of the pool are good for.
const (
	// NodePoolCPU is a pool of CPU-intensive nodes (a lot of cores for the memory: 8 cores and 16 GiB).
	NodePoolCPU = "cpu"
	// NodePoolMemory is a pool of memory-intensive nodes (a lot of memory for the cores: 4 cores and 32 GiB).
	NodePoolMemory = "memory"
	// NodePoolGPU is a pool of nodes with GPUs; only a workload that asks for a GPU is given one.
	NodePoolGPU = "gpu"
	// NodePoolGeneral is a pool of nodes with no leaning.
	NodePoolGeneral = "general"
)

// NodePoolKinds lists the kinds.
var NodePoolKinds = []string{NodePoolCPU, NodePoolMemory, NodePoolGPU, NodePoolGeneral}

// NodePool is a group of Kubernetes nodes of one shape that a workload can be steered to: the labels that select them (a Node Selector), the size of one node, and what the nodes are for. The
// platform recommends the pool whose nodes the Pod of a module fills best, next to the CPU, memory and replicas it recommends; whether a module is moved to it is decided by its run mode
// (see ForecastModule.NodePoolEnabled).
type NodePool struct {
	ID        uint      `gorm:"primaryKey" json:"id" column_setting:"true"`
	CreatedAt time.Time `json:"created_at" column_setting:"true"`
	UpdatedAt time.Time `json:"updated_at" column_setting:"true"`

	// Cluster is the cluster the pool is in; empty = every cluster (a pool with the same labels in each of them).
	Cluster string `gorm:"type:varchar(64);not null;default:'';uniqueIndex:idx_node_pool,priority:1" json:"cluster" column_setting:"true"`
	Name    string `gorm:"type:varchar(100);not null;uniqueIndex:idx_node_pool,priority:2" json:"name" column_setting:"true"`
	Kind    string `gorm:"type:varchar(10);not null;default:'general'" json:"kind" column_setting:"true"`
	// NodeSelector is the labels that select the nodes of the pool (the Node Selector of the Pod spec).
	NodeSelector map[string]string `gorm:"serializer:json;type:text" json:"node_selector" column_setting:"true"`
	// NodeCPUMilli, NodeMemoryBytes and NodeGPU are what one node of the pool can give to Pods (the allocatable of a node), 0 = not known (a pool with none of the first two is not
	// recommended: a Pod cannot be fitted to a node of an unknown size).
	NodeCPUMilli    int64 `gorm:"not null;default:0" json:"node_cpu_milli" column_setting:"true"`
	NodeMemoryBytes int64 `gorm:"not null;default:0" json:"node_memory_bytes" column_setting:"true"`
	NodeGPU         int   `gorm:"not null;default:0" json:"node_gpu" column_setting:"true"`
	Enabled         bool  `gorm:"not null" json:"enabled" column_setting:"true"`
	// Priority breaks a tie between pools that fit a Pod equally well: the higher one is recommended.
	Priority int    `gorm:"not null;default:0" json:"priority" column_setting:"true"`
	Remark   string `gorm:"type:varchar(255)" json:"remark" column_setting:"false"`
}

func (NodePool) TableName() string { return "node_pool" }

var (
	labelName   = regexp.MustCompile(`^[A-Za-z0-9]([-A-Za-z0-9_.]*[A-Za-z0-9])?$`)
	labelPrefix = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`)
	labelValue  = regexp.MustCompile(`^([A-Za-z0-9]([-A-Za-z0-9_.]*[A-Za-z0-9])?)?$`)
)

// validLabelKey reports whether k is a Kubernetes label key: a name of up to 63 characters, with an optional DNS prefix and a slash before it.
func validLabelKey(k string) bool {
	prefix, name := "", k
	if i := strings.Index(k, "/"); i >= 0 {
		prefix, name = k[:i], k[i+1:]
		if prefix == "" || len(prefix) > 253 || !labelPrefix.MatchString(prefix) {
			return false
		}
	}
	return name != "" && len(name) <= 63 && labelName.MatchString(name)
}

// Validate checks the pool and fills its defaults.
func (p *NodePool) Validate() error {
	p.Name, p.Cluster = strings.TrimSpace(p.Name), strings.TrimSpace(p.Cluster)
	if p.Name == "" {
		return fmt.Errorf("the name of the node pool is required")
	}
	if p.Kind == "" {
		p.Kind = NodePoolGeneral
	}
	if !oneOf(p.Kind, NodePoolKinds) {
		return fmt.Errorf("kind must be one of %v", NodePoolKinds)
	}
	if len(p.NodeSelector) == 0 {
		return fmt.Errorf("the labels that select the nodes of the pool (the Node Selector) are required")
	}
	clean := map[string]string{}
	for k, v := range p.NodeSelector {
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if !validLabelKey(k) {
			return fmt.Errorf("%q is not a valid label key", k)
		}
		if len(v) > 63 || !labelValue.MatchString(v) {
			return fmt.Errorf("%q is not a valid label value", v)
		}
		clean[k] = v
	}
	p.NodeSelector = clean
	if p.NodeCPUMilli < 0 || p.NodeMemoryBytes < 0 || p.NodeGPU < 0 {
		return fmt.Errorf("the size of a node must not be negative")
	}
	if p.Kind == NodePoolGPU && p.NodeGPU < 1 {
		return fmt.Errorf("a GPU pool needs the number of GPUs of one node")
	}
	if p.Kind != NodePoolGPU && p.NodeGPU > 0 {
		return fmt.Errorf("only a GPU pool has GPUs")
	}
	if p.Enabled && (p.NodeCPUMilli == 0 || p.NodeMemoryBytes == 0) {
		return fmt.Errorf("a pool that is switched on needs the CPU and the memory of one node: a Pod cannot be fitted to a node of an unknown size")
	}
	return nil
}
