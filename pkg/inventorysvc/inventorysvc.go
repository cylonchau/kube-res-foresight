// Package inventorysvc takes what the operator reports of the cluster and of the workloads of the modules (P10 A1), stores it, keeps the history of the changes
// of the configuration, and answers the pages that show it. It never reads the cluster itself: the operator does, read-only, and the platform keeps what it
// was told. Usage is not here, it is asked of Prometheus.
package inventorysvc

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/dao/rdb"
	"github.com/cylonchau/kube-res-foresight/pkg/inventory"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/cylonchau/kube-res-foresight/pkg/timing"
	"gorm.io/gorm"
)

// The system parameters that are text.
const (
	KeyClusterName   = "cluster.name"
	KeyGrafanaURL    = "grafana.base_url"
	KeyGrafanaUID    = "grafana.dashboard_uid"
	KeyPlatformURL   = "platform.public_url"
	DefaultCluster   = "default"
	DefaultGrafanaID = "kube-res-foresight-resources"
)

// DefaultConfigs are the text parameters for the seed.
func DefaultConfigs() []model.SysConfig {
	return []model.SysConfig{
		{ConfigKey: KeyClusterName, ConfigValue: DefaultCluster, ConfigName: "Cluster Name", Remark: "The name of the cluster the operator reports on. The platform manages one cluster; the name is in the node and workload data, on the resource pages and as the cluster variable of the Grafana dashboard."},
		{ConfigKey: KeyGrafanaURL, ConfigValue: "", ConfigName: "Grafana Address", Remark: "The address of your Grafana (https://grafana.example.com). When it is set, the resource pages have an \"open in Grafana\" link for the cluster, a node, a workload and a Pod. Empty hides the links."},
		{ConfigKey: KeyGrafanaUID, ConfigValue: DefaultGrafanaID, ConfigName: "Grafana Dashboard UID", Remark: "The uid of the resource dashboard in your Grafana (the one the platform generates keeps the uid it has by default)."},
		{ConfigKey: KeyPlatformURL, ConfigValue: "", ConfigName: "Platform Public Address", Remark: "The address people reach this platform at (https://fcst.example.com). The Grafana dashboard links back to the resource dashboard of a workload with it. Empty leaves those links out."},
	}
}

// Service stores and reads the inventory.
type Service struct {
	DAO     *rdb.InventoryDAO
	Modules *rdb.ModuleDAO
	Config  *rdb.SysConfigDAO
	Now     func() time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s *Service) text(ctx context.Context, key, fallback string) string {
	if s.Config == nil {
		return fallback
	}
	v, err := s.Config.GetValueOrDefault(ctx, key, fallback)
	if err != nil || strings.TrimSpace(v) == "" {
		return fallback
	}
	return strings.TrimSpace(v)
}

func (s *Service) days(ctx context.Context, p timing.Param) int {
	if s.Config == nil {
		return p.Default
	}
	raw, _ := s.Config.GetValueOrDefault(ctx, p.Key, "")
	return p.Parse(raw)
}

// ClusterName is the name of the cluster, from the system parameter.
func (s *Service) ClusterName(ctx context.Context) string {
	return s.text(ctx, KeyClusterName, DefaultCluster)
}

// Targets are the modules the operator reads the workload of: every enabled module, whatever its mode (a module that is only observed has a workload to
// show as much as one that is scaled).
func (s *Service) Targets(ctx context.Context) ([]inventory.Target, error) {
	modules, err := s.Modules.ListEnabled(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]inventory.Target, 0, len(modules))
	for _, m := range modules {
		out = append(out, inventory.Target{Name: m.Name, Namespace: m.Namespace, Kind: m.WorkloadKind, Workload: m.Workload})
	}
	return out, nil
}

// IngestResult says what a report did.
type IngestResult struct {
	// Duplicate is true when the report had been taken before: nothing was stored.
	Duplicate bool `json:"duplicate"`
	Nodes     int  `json:"nodes"`
	Workloads int  `json:"workloads"`
	Changes   int  `json:"changes"`
	// Unknown are the modules of the report the platform does not have (deleted since the operator read its list).
	Unknown []string `json:"unknown,omitempty"`
}

// ErrTooOld is a report from before the snapshots are kept.
var ErrTooOld = errors.New("the report is older than the snapshots are kept")

// Ingest stores one report: the nodes, the workload of each module (its current configuration, a change if it differs from what was seen, and its replicas at
// this time). A report with a record id that was taken is a repeat and changes nothing. A report that is older than the one the platform holds for a workload
// is kept as a snapshot but does not replace its current configuration or write a change: it arrives late, after an outage, and the newer one is the truth.
func (s *Service) Ingest(ctx context.Context, snap inventory.Snapshot) (*IngestResult, error) {
	if err := snap.Validate(); err != nil {
		return nil, err
	}
	now := s.now()
	at := snap.ObservedAt.UTC()
	if at.IsZero() || at.After(now) {
		at = now
	}
	if at.Before(now.AddDate(0, 0, -s.days(ctx, timing.InventorySnapshotDays))) {
		return nil, ErrTooOld
	}
	out := &IngestResult{}
	fresh, err := s.DAO.InsertReport(ctx, &model.InventoryReport{RecordID: clip(snap.RecordID, 64), Operator: clip(snap.Operator, 253), ObservedAt: at, ReceivedAt: now,
		Nodes: len(snap.Nodes), Workloads: len(snap.Workloads)})
	if err != nil {
		return nil, err
	}
	if !fresh {
		out.Duplicate = true
		return out, nil
	}
	cluster := s.ClusterName(ctx)

	nodes := make([]model.ClusterNodeSnapshot, 0, len(snap.Nodes))
	for _, n := range snap.Nodes {
		nodes = append(nodes, model.ClusterNodeSnapshot{ObservedAt: at, Cluster: cluster, Node: clip(n.Name, 253), Ready: n.Ready, Unschedulable: n.Unschedulable,
			Pressure: clip(strings.Join(n.Pressure, ","), 120), Provider: clip(n.Provider, 32), Region: clip(n.Region, 64), Zone: clip(n.Zone, 64), InstanceType: clip(n.InstanceType, 64),
			KubeletVersion: clip(n.KubeletVersion, 32), CapCPUMilli: n.Capacity.CPUMilli, CapMemoryBytes: n.Capacity.MemoryBytes, CapPods: n.Capacity.Pods,
			AllocCPUMilli: n.Allocatable.CPUMilli, AllocMemoryBytes: n.Allocatable.MemoryBytes, AllocPods: n.Allocatable.Pods, ModulePods: n.Pods,
			ReqCPUMilli: n.Requests.CPUMilli, ReqMemoryBytes: n.Requests.MemoryBytes, LimCPUMilli: n.Limits.CPUMilli, LimMemoryBytes: n.Limits.MemoryBytes})
	}
	if err := s.DAO.AddNodes(ctx, nodes); err != nil {
		return nil, err
	}
	out.Nodes = len(nodes)

	var snapshots []model.WorkloadSnapshot
	for _, w := range snap.Workloads {
		m, err := s.Modules.ByName(ctx, w.Module)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				out.Unknown = append(out.Unknown, w.Module)
				continue
			}
			return nil, err
		}
		changed, err := s.saveWorkload(ctx, m.ID, cluster, at, w)
		if err != nil {
			return nil, err
		}
		out.Changes += changed
		row := model.WorkloadSnapshot{ModuleID: m.ID, ObservedAt: at, Desired: int(w.Desired), Ready: int(w.Ready), Pods: len(w.Pods)}
		for _, p := range w.Pods {
			row.Restarts += p.Restarts
		}
		if w.HPA != nil {
			row.HPAKind, row.HPAMin, row.HPAMax, row.HPACurrent, row.HPADesired = clip(w.HPA.Kind, 8), int(w.HPA.Min), int(w.HPA.Max), int(w.HPA.Current), int(w.HPA.Desired)
		}
		snapshots = append(snapshots, row)
		out.Workloads++
	}
	if err := s.DAO.AddWorkloadSnapshots(ctx, snapshots); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Service) saveWorkload(ctx context.Context, moduleID uint, cluster string, at time.Time, w inventory.Workload) (int, error) {
	prev, err := s.DAO.GetWorkload(ctx, moduleID)
	if err != nil {
		return 0, err
	}
	if prev != nil && at.Before(prev.ObservedAt) {
		return 0, nil // late: the newer reading stands
	}
	containers, _ := json.Marshal(nonNil(w.Containers))
	pods, _ := json.Marshal(nonNilPods(w.Pods))
	hpa := ""
	if w.HPA != nil {
		raw, _ := json.Marshal(w.HPA)
		hpa = string(raw)
	}
	row := &model.WorkloadConfig{ModuleID: moduleID, Cluster: cluster, Namespace: clip(w.Namespace, 253), Kind: clip(w.Kind, 16), Name: clip(w.Name, 253), Desired: int(w.Desired),
		Ready: int(w.Ready), Available: int(w.Available), Updated: int(w.Updated), Generation: w.Generation, Containers: string(containers), Pods: string(pods), HPA: hpa,
		Missing: w.Missing, Error: clip(w.Error, 300), ObservedAt: at, UpdatedAt: s.now()}
	if err := s.DAO.SaveWorkload(ctx, row); err != nil {
		return 0, err
	}
	var changes []inventory.Change
	switch {
	case prev == nil:
		changes = []inventory.Change{{Path: "workload", From: "-", To: "first reported"}}
	case prev.Missing != w.Missing:
		from, to := "present", "missing"
		if prev.Missing {
			from, to = "missing", "present"
		}
		changes = []inventory.Change{{Path: "workload", From: from, To: to}}
	case !w.Missing:
		changes = inventory.Diff(workloadOf(prev), w)
	}
	if len(changes) == 0 {
		return 0, nil
	}
	raw, _ := json.Marshal(changes)
	err = s.DAO.AddChange(ctx, &model.WorkloadConfigChange{ModuleID: moduleID, ObservedAt: at, Cluster: cluster, Kind: clip(w.Kind, 16), Name: clip(w.Name, 253), Changes: string(raw),
		Summary: inventory.Summary(changes)})
	return 1, err
}

func nonNil(c []inventory.Container) []inventory.Container {
	if c == nil {
		return []inventory.Container{}
	}
	return c
}

func nonNilPods(p []inventory.Pod) []inventory.Pod {
	if p == nil {
		return []inventory.Pod{}
	}
	return p
}

// workloadOf is the stored workload as the wire type, for the comparison with a new reading.
func workloadOf(r *model.WorkloadConfig) inventory.Workload {
	w := inventory.Workload{Namespace: r.Namespace, Kind: r.Kind, Name: r.Name, Desired: int32(r.Desired), Ready: int32(r.Ready), Available: int32(r.Available),
		Updated: int32(r.Updated), Generation: r.Generation, Missing: r.Missing, Error: r.Error}
	_ = json.Unmarshal([]byte(r.Containers), &w.Containers)
	_ = json.Unmarshal([]byte(r.Pods), &w.Pods)
	if r.HPA != "" {
		w.HPA = &inventory.HPA{}
		if json.Unmarshal([]byte(r.HPA), w.HPA) != nil {
			w.HPA = nil
		}
	}
	return w
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// Prune deletes the snapshots and the history that are past their retention.
func (s *Service) Prune(ctx context.Context) (rdb.PrunedInventory, error) {
	now := s.now()
	return s.DAO.Prune(ctx, now.AddDate(0, 0, -s.days(ctx, timing.InventorySnapshotDays)), now.AddDate(0, 0, -s.days(ctx, timing.InventoryChangeDays)))
}

// StaleAfter is how long after its last report the inventory is said to be old: the operator reports at least once an hour.
const StaleAfter = 2 * time.Hour

// NodeTotals are the sums over the nodes of the newest report.
type NodeTotals struct {
	Nodes          int   `json:"nodes"`
	Ready          int   `json:"ready"`
	Unschedulable  int   `json:"unschedulable"`
	AllocCPUMilli  int64 `json:"alloc_cpu_milli"`
	AllocMemory    int64 `json:"alloc_memory_bytes"`
	CapCPUMilli    int64 `json:"cap_cpu_milli"`
	CapMemory      int64 `json:"cap_memory_bytes"`
	ModulePods     int   `json:"module_pods"`
	ReqCPUMilli    int64 `json:"req_cpu_milli"`
	ReqMemoryBytes int64 `json:"req_memory_bytes"`
	LimCPUMilli    int64 `json:"lim_cpu_milli"`
	LimMemoryBytes int64 `json:"lim_memory_bytes"`
}

// NodeRow is a node with its pressure as a list.
type NodeRow struct {
	model.ClusterNodeSnapshot
	PressureList []string `json:"pressure_list"`
}

// NodesView is the cluster as the newest report saw it.
type NodesView struct {
	Cluster string     `json:"cluster"`
	At      *time.Time `json:"at"`
	// Reported is false until the operator has reported once; Stale when the last report is older than StaleAfter.
	Reported bool       `json:"reported"`
	Stale    bool       `json:"stale"`
	Totals   NodeTotals `json:"totals"`
	Nodes    []NodeRow  `json:"nodes"`
	// Providers, Regions, Zones and InstanceTypes count the nodes of each.
	Providers     map[string]int `json:"providers"`
	Regions       map[string]int `json:"regions"`
	Zones         map[string]int `json:"zones"`
	InstanceTypes map[string]int `json:"instance_types"`
}

// Nodes is the nodes of the newest report with their sums.
func (s *Service) Nodes(ctx context.Context) (*NodesView, error) {
	rows, at, err := s.DAO.LatestNodes(ctx)
	if err != nil {
		return nil, err
	}
	out := &NodesView{Cluster: s.ClusterName(ctx), Nodes: []NodeRow{}, Providers: map[string]int{}, Regions: map[string]int{}, Zones: map[string]int{}, InstanceTypes: map[string]int{}}
	if len(rows) == 0 {
		return out, nil
	}
	out.Reported, out.At = true, &at
	out.Stale = s.now().Sub(at) > StaleAfter
	count := func(m map[string]int, key string) {
		if key != "" {
			m[key]++
		}
	}
	for _, n := range rows {
		t := &out.Totals
		t.Nodes++
		if n.Ready {
			t.Ready++
		}
		if n.Unschedulable {
			t.Unschedulable++
		}
		t.AllocCPUMilli += n.AllocCPUMilli
		t.AllocMemory += n.AllocMemoryBytes
		t.CapCPUMilli += n.CapCPUMilli
		t.CapMemory += n.CapMemoryBytes
		t.ModulePods += n.ModulePods
		t.ReqCPUMilli += n.ReqCPUMilli
		t.ReqMemoryBytes += n.ReqMemoryBytes
		t.LimCPUMilli += n.LimCPUMilli
		t.LimMemoryBytes += n.LimMemoryBytes
		count(out.Providers, n.Provider)
		count(out.Regions, n.Region)
		count(out.Zones, n.Zone)
		count(out.InstanceTypes, n.InstanceType)
		list := []string{}
		if n.Pressure != "" {
			list = strings.Split(n.Pressure, ",")
		}
		out.Nodes = append(out.Nodes, NodeRow{ClusterNodeSnapshot: n, PressureList: list})
	}
	return out, nil
}

// NodeTrend is the number of nodes, and of those ready, at every report of the last hours.
func (s *Service) NodeTrend(ctx context.Context, hours int) ([]rdb.NodeTrendPoint, error) {
	if hours < 1 || hours > 24*365 {
		hours = 24
	}
	return s.DAO.NodeTrend(ctx, s.now().Add(-time.Duration(hours)*time.Hour))
}

// ChangeView is a change of the configuration of a workload.
type ChangeView struct {
	ObservedAt time.Time          `json:"observed_at"`
	Summary    string             `json:"summary"`
	Changes    []inventory.Change `json:"changes"`
}

// WorkloadView is the workload of one module as the operator reported it.
type WorkloadView struct {
	ModuleID uint   `json:"module_id"`
	Cluster  string `json:"cluster"`
	// Reported is false until the operator has reported the workload; Stale when its last reading is older than StaleAfter.
	Reported   bool                  `json:"reported"`
	Stale      bool                  `json:"stale"`
	ObservedAt *time.Time            `json:"observed_at"`
	Namespace  string                `json:"namespace"`
	Kind       string                `json:"kind"`
	Name       string                `json:"name"`
	Desired    int                   `json:"desired"`
	Ready      int                   `json:"ready"`
	Available  int                   `json:"available"`
	Updated    int                   `json:"updated"`
	Missing    bool                  `json:"missing"`
	Error      string                `json:"error"`
	Containers []inventory.Container `json:"containers"`
	Pods       []inventory.Pod       `json:"pods"`
	HPA        *inventory.HPA        `json:"hpa"`
	Changes    []ChangeView          `json:"changes"`
	Trend      []TrendPoint          `json:"trend"`
}

// TrendPoint is the replicas of a workload, and of its HPA, at one report.
type TrendPoint struct {
	ObservedAt time.Time `json:"observed_at"`
	Desired    int       `json:"desired"`
	Ready      int       `json:"ready"`
	HPAKind    string    `json:"hpa_kind"`
	HPAMin     int       `json:"hpa_min"`
	HPAMax     int       `json:"hpa_max"`
	HPACurrent int       `json:"hpa_current"`
	HPADesired int       `json:"hpa_desired"`
	Restarts   int       `json:"restarts"`
}

// Workload is what the operator reported of the workload of a module: now, the history of its configuration and the trend of its replicas over the hours.
func (s *Service) Workload(ctx context.Context, moduleID uint, hours int) (*WorkloadView, error) {
	if hours < 1 || hours > 24*365 {
		hours = 24 * 7
	}
	out := &WorkloadView{ModuleID: moduleID, Containers: []inventory.Container{}, Pods: []inventory.Pod{}, Changes: []ChangeView{}, Trend: []TrendPoint{}}
	row, err := s.DAO.GetWorkload(ctx, moduleID)
	if err != nil {
		return nil, err
	}
	if row != nil {
		w := workloadOf(row)
		at := row.ObservedAt
		out.Reported, out.ObservedAt = true, &at
		out.Stale = s.now().Sub(at) > StaleAfter
		out.Cluster, out.Namespace, out.Kind, out.Name = row.Cluster, row.Namespace, row.Kind, row.Name
		out.Desired, out.Ready, out.Available, out.Updated = row.Desired, row.Ready, row.Available, row.Updated
		out.Missing, out.Error = row.Missing, row.Error
		if w.Containers != nil {
			out.Containers = w.Containers
		}
		if w.Pods != nil {
			sort.Slice(w.Pods, func(i, j int) bool { return w.Pods[i].Name < w.Pods[j].Name })
			out.Pods = w.Pods
		}
		out.HPA = w.HPA
	}
	changes, err := s.DAO.Changes(ctx, moduleID, 50)
	if err != nil {
		return nil, err
	}
	for _, c := range changes {
		v := ChangeView{ObservedAt: c.ObservedAt, Summary: c.Summary, Changes: []inventory.Change{}}
		_ = json.Unmarshal([]byte(c.Changes), &v.Changes)
		out.Changes = append(out.Changes, v)
	}
	trend, err := s.DAO.WorkloadTrend(ctx, moduleID, s.now().Add(-time.Duration(hours)*time.Hour))
	if err != nil {
		return nil, err
	}
	for _, t := range trend {
		out.Trend = append(out.Trend, TrendPoint{ObservedAt: t.ObservedAt, Desired: t.Desired, Ready: t.Ready, HPAKind: t.HPAKind, HPAMin: t.HPAMin, HPAMax: t.HPAMax,
			HPACurrent: t.HPACurrent, HPADesired: t.HPADesired, Restarts: t.Restarts})
	}
	return out, nil
}

// Workloads are the workloads of every module as they were last seen, by module id.
func (s *Service) Workloads(ctx context.Context) (map[uint]*WorkloadView, error) {
	rows, err := s.DAO.Workloads(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[uint]*WorkloadView, len(rows))
	for i := range rows {
		r := rows[i]
		w := workloadOf(&r)
		at := r.ObservedAt
		out[r.ModuleID] = &WorkloadView{ModuleID: r.ModuleID, Cluster: r.Cluster, Reported: true, ObservedAt: &at, Stale: s.now().Sub(at) > StaleAfter, Namespace: r.Namespace,
			Kind: r.Kind, Name: r.Name, Desired: r.Desired, Ready: r.Ready, Available: r.Available, Updated: r.Updated, Missing: r.Missing, Error: r.Error,
			Containers: nonNil(w.Containers), Pods: nonNilPods(w.Pods), HPA: w.HPA, Changes: []ChangeView{}, Trend: []TrendPoint{}}
	}
	return out, nil
}

// GrafanaSettings are the addresses the resource pages link with.
type GrafanaSettings struct {
	// BaseURL is the address of the Grafana (empty: the pages show no link); UID is the uid of the resource dashboard in it.
	BaseURL string `json:"base_url"`
	UID     string `json:"dashboard_uid"`
	// Cluster is the name of the cluster, the value of the cluster variable; PlatformURL is where people reach the platform (the dashboard links back to it).
	Cluster     string `json:"cluster"`
	PlatformURL string `json:"platform_url"`
	// Configured is true when there is a Grafana address to link to.
	Configured bool `json:"configured"`
}

// Grafana reads the Grafana settings from the system parameters.
func (s *Service) Grafana(ctx context.Context) GrafanaSettings {
	base := strings.TrimRight(s.text(ctx, KeyGrafanaURL, ""), "/")
	return GrafanaSettings{BaseURL: base, UID: s.text(ctx, KeyGrafanaUID, DefaultGrafanaID), Cluster: s.ClusterName(ctx), PlatformURL: strings.TrimRight(s.text(ctx, KeyPlatformURL, ""), "/"),
		Configured: base != ""}
}
