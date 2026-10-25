package model

import "time"

func init() {
	RegisterModel(&Datasource{})
}

// Datasource types, auth modes and probe statuses. Only Prometheus-compatible HTTP APIs are supported.
const (
	DatasourceTypePrometheus = "prometheus"
	// DatasourceTypeDemo serves the built-in synthetic data; it is created only by the demo and cannot be edited.
	DatasourceTypeDemo = "demo"

	DatasourceAuthNone   = "none"
	DatasourceAuthBasic  = "basic"
	DatasourceAuthBearer = "bearer"

	DatasourceStatusUnknown = "unknown"
	DatasourceStatusOK      = "ok"
	DatasourceStatusError   = "error"
)

// Datasource is a registered metrics source. Rows are hard-deleted so the unique name
// can be reused. Secrets are stored encrypted and never serialized.
type Datasource struct {
	ID        uint      `gorm:"primaryKey" json:"id" column_setting:"true"`
	CreatedAt time.Time `json:"created_at" column_setting:"true"`
	UpdatedAt time.Time `json:"updated_at" column_setting:"false"`

	Name string `gorm:"type:varchar(100);uniqueIndex;not null" json:"name" column_setting:"true"`
	Type string `gorm:"type:varchar(20);not null;default:'prometheus'" json:"type" column_setting:"true"`
	// BaseURL has no credentials, query or fragment; a path prefix (Thanos/Mimir) is allowed.
	BaseURL string `gorm:"type:varchar(512);not null" json:"base_url" column_setting:"true"`

	AuthType             string `gorm:"type:varchar(20);not null;default:'none'" json:"auth_type" column_setting:"true"`
	Username             string `gorm:"type:varchar(128)" json:"username" column_setting:"false"`
	PasswordEncrypted    string `gorm:"type:text" json:"-" column_setting:"false"`
	BearerTokenEncrypted string `gorm:"type:text" json:"-" column_setting:"false"`

	TLSSkipVerify bool   `gorm:"not null;default:false" json:"tls_skip_verify" column_setting:"true"`
	CACert        string `gorm:"type:text" json:"-" column_setting:"false"`

	// Request budget applied by collectors; conservative serial default.
	TimeoutSeconds int `gorm:"not null;default:30" json:"timeout_seconds" column_setting:"true"`
	MaxConcurrency int `gorm:"not null;default:1" json:"max_concurrency" column_setting:"true"`

	Enabled bool `gorm:"not null" json:"enabled" column_setting:"true"`

	Status        string     `gorm:"type:varchar(16);not null;default:'unknown';index" json:"status" column_setting:"true"`
	ServerVersion string     `gorm:"type:varchar(64)" json:"server_version" column_setting:"true"`
	LastCheckedAt *time.Time `json:"last_checked_at" column_setting:"true"`
	LastLatencyMs int        `gorm:"not null;default:0" json:"last_latency_ms" column_setting:"true"`
	LastError     string     `gorm:"type:varchar(512)" json:"last_error" column_setting:"true"`

	Remark string `gorm:"type:text" json:"remark" column_setting:"true"`
}

func (Datasource) TableName() string { return "datasource" }
