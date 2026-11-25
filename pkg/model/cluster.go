package model

import "time"

func init() {
	RegisterModel(&Cluster{})
	RegisterModel(&ClusterEvent{})
}

// A Cluster is a Kubernetes cluster an operator (the Cluster Agent) reports from (P12 D1). The platform keeps one row for each: its name is what an operator is started
// with (--cluster) and what every report carries, an access key is bound to one cluster, and a module belongs to one. The cluster that existed before clusters were
// registered is the default one (IsDefault): an operator that is started without --cluster, and every module and every row of data written before, belong to it.
type Cluster struct {
	ID        uint      `gorm:"primaryKey" json:"id" column_setting:"true"`
	CreatedAt time.Time `json:"created_at" column_setting:"true"`
	UpdatedAt time.Time `json:"updated_at" column_setting:"true"`

	Name        string `gorm:"type:varchar(64);uniqueIndex;not null" json:"name" column_setting:"true"`
	Description string `gorm:"type:varchar(255)" json:"description" column_setting:"true"`
	Enabled     bool   `gorm:"not null;default:true" json:"enabled" column_setting:"true"`
	IsDefault   bool   `gorm:"not null;default:false" json:"is_default" column_setting:"true"`
	// DatasourceID is the Prometheus data source the usage of this cluster is asked of (0 = the one of the system parameter cluster.datasource_id).
	DatasourceID uint `gorm:"not null;default:0" json:"datasource_id" column_setting:"true"`
	// NamespaceInclude and NamespaceExclude are JSON lists of namespace names (a trailing * is a prefix): what the operator collects of the cluster's namespaced objects
	// (the events, today) is limited to the included namespaces (all of them when the list is empty) minus the excluded ones. They are set on the platform after the
	// operator is connected and reach it through its settings; the replicas of a module are never limited by them.
	NamespaceInclude string `gorm:"type:text" json:"-"`
	NamespaceExclude string `gorm:"type:text" json:"-"`

	// Last contact of an operator of this cluster: when, and which Pod.
	LastSeenAt *time.Time `json:"last_seen_at" column_setting:"true"`
	Operator   string     `gorm:"type:varchar(253)" json:"operator" column_setting:"true"`

	// Not stored: filled by the handler.
	NamespaceIncludeList []string `gorm:"-" json:"namespace_include" column_setting:"false"`
	NamespaceExcludeList []string `gorm:"-" json:"namespace_exclude" column_setting:"false"`
	KeyIDs               []int64  `gorm:"-" json:"key_ids" column_setting:"false"`
	Modules              int64    `gorm:"-" json:"modules" column_setting:"false"`
}

func (Cluster) TableName() string { return "cluster" }

// ClusterEvent is a Kubernetes Event as the operator saw it, kept as it was said: reason, message, the component that said it, the object it is about, how many
// times and when. Origin and Category are what the platform's rules (see package clusterevent) make of the component and the reason; the event itself is never
// changed. One row for each event of a cluster (the uid of the Event object), updated when the same event happens again. Rows are kept for the retention of the
// system parameter cluster.retention_days (3 days unless changed) and deleted after.
type ClusterEvent struct {
	ID      uint   `gorm:"primaryKey" json:"id"`
	Cluster string `gorm:"type:varchar(64);not null;uniqueIndex:idx_cluster_event_uid,priority:1;index:idx_cluster_event_last,priority:1" json:"cluster"`
	UID     string `gorm:"type:varchar(64);not null;uniqueIndex:idx_cluster_event_uid,priority:2" json:"uid"`

	// Type is Normal or Warning, as Kubernetes says.
	Type    string `gorm:"type:varchar(16);not null;default:''" json:"type"`
	Reason  string `gorm:"type:varchar(128);not null;default:'';index" json:"reason"`
	Message string `gorm:"type:text" json:"message"`
	// Component is the component that reported it (the reporting controller, or else the source component), Host the node it said it on.
	Component string `gorm:"type:varchar(253);not null;default:'';index" json:"component"`
	Host      string `gorm:"type:varchar(253)" json:"host"`
	Count     int    `gorm:"not null;default:1" json:"count"`

	// The object it is about.
	Kind      string `gorm:"type:varchar(64);not null;default:'';index:idx_cluster_event_object,priority:2" json:"kind"`
	Namespace string `gorm:"type:varchar(253);not null;default:'';index" json:"namespace"`
	Name      string `gorm:"type:varchar(253);not null;default:'';index:idx_cluster_event_object,priority:3" json:"name"`
	ObjectUID string `gorm:"type:varchar(64)" json:"object_uid"`

	FirstAt time.Time `gorm:"not null" json:"first_at"`
	// LastAt is when it last happened; the retention counts from it.
	LastAt     time.Time `gorm:"not null;index:idx_cluster_event_last,priority:2;index" json:"last_at"`
	ReceivedAt time.Time `gorm:"not null" json:"received_at"`
	Operator   string    `gorm:"type:varchar(253)" json:"operator"`

	// Origin, Category and Rule are the platform's reading of it: auto, manual or unclassified; what kind of component (autoscaler, controller, kubelet, scheduler,
	// cloud, ...); and the name of the rule that said so (empty when none did).
	Origin   string `gorm:"type:varchar(16);not null;default:'unclassified';index" json:"origin"`
	Category string `gorm:"type:varchar(32);not null;default:''" json:"category"`
	Rule     string `gorm:"type:varchar(128)" json:"rule"`
	// Raw is the Event as it came (JSON, only the fields the operator keeps), so a rule that is added later can read an event that was taken before it.
	Raw string `gorm:"type:text" json:"-"`
}

func (ClusterEvent) TableName() string { return "cluster_event" }
