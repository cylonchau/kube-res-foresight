package model

import "time"

func init() {
	RegisterModel(&RegistrationTombstone{})
}

// RegistrationTombstone remembers a workload whose module was registered by itself (from the label clusteragent.LabelRegister) and then deleted by a person. While it is there the
// registration does not make the module again, however often the operator reports the workload and however long the label stays on it: a deleted module that comes back is a
// deletion that did not hold. It is removed by a person (the workload may be registered again), and it is not made for a module somebody made by hand.
type RegistrationTombstone struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	CreatedAt time.Time `json:"created_at"`

	// Cluster is empty for the default cluster, as a module's is.
	Cluster      string `gorm:"type:varchar(64);not null;default:'';uniqueIndex:idx_registration_tombstone,priority:1" json:"cluster"`
	Namespace    string `gorm:"type:varchar(253);not null;uniqueIndex:idx_registration_tombstone,priority:2" json:"namespace"`
	WorkloadKind string `gorm:"type:varchar(16);not null;uniqueIndex:idx_registration_tombstone,priority:3" json:"workload_kind"`
	Workload     string `gorm:"type:varchar(253);not null;uniqueIndex:idx_registration_tombstone,priority:4" json:"workload"`
	// ModuleName is the name the deleted module had.
	ModuleName string `gorm:"type:varchar(100);not null;default:''" json:"module_name"`
}

func (RegistrationTombstone) TableName() string { return "registration_tombstone" }
