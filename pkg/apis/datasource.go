package apis

import "time"

// DatasourceDTO is the admin-facing representation of a datasource. Secrets and the CA
// bundle body are never included; HasPassword/HasBearerToken/HasCACert report presence.
type DatasourceDTO struct {
	ID        uint      `json:"id"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	Name           string `json:"name"`
	Type           string `json:"type"`
	BaseURL        string `json:"base_url"`
	AuthType       string `json:"auth_type"`
	Username       string `json:"username"`
	HasPassword    bool   `json:"has_password"`
	HasBearerToken bool   `json:"has_bearer_token"`
	TLSSkipVerify  bool   `json:"tls_skip_verify"`
	HasCACert      bool   `json:"has_ca_cert"`
	TimeoutSeconds int    `json:"timeout_seconds"`
	MaxConcurrency int    `json:"max_concurrency"`
	Enabled        bool   `json:"enabled"`

	Status        string     `json:"status"`
	ServerVersion string     `json:"server_version"`
	LastCheckedAt *time.Time `json:"last_checked_at"`
	LastLatencyMs int        `json:"last_latency_ms"`
	LastError     string     `json:"last_error"`

	MappingCount int64  `json:"mapping_count"`
	Remark       string `json:"remark"`
}

// DatasourceOptionDTO is the compact form used by selectors.
type DatasourceOptionDTO struct {
	ID   uint   `json:"id"`
	Name string `json:"name"`
}

type DatasourceListReq struct {
	ListOptions
	Status  string `form:"status" json:"status"`
	Enabled string `form:"enabled" json:"enabled"`
}

// DatasourceCreateReq creates a datasource. Password/BearerToken/CACert are write-only.
type DatasourceCreateReq struct {
	Name           string `json:"name" binding:"required"`
	Type           string `json:"type"`
	BaseURL        string `json:"base_url" binding:"required"`
	AuthType       string `json:"auth_type"`
	Username       string `json:"username"`
	Password       string `json:"password"`
	BearerToken    string `json:"bearer_token"`
	TLSSkipVerify  bool   `json:"tls_skip_verify"`
	CACert         string `json:"ca_cert"`
	TimeoutSeconds int    `json:"timeout_seconds"`
	MaxConcurrency int    `json:"max_concurrency"`
	Enabled        *bool  `json:"enabled"`
	Remark         string `json:"remark"`
}

// DatasourceUpdateReq patches a datasource. A nil/empty Password, BearerToken or CACert
// keeps the stored value; ClearCACert removes the CA bundle.
type DatasourceUpdateReq struct {
	Name           *string `json:"name"`
	BaseURL        *string `json:"base_url"`
	AuthType       *string `json:"auth_type"`
	Username       *string `json:"username"`
	Password       *string `json:"password"`
	BearerToken    *string `json:"bearer_token"`
	TLSSkipVerify  *bool   `json:"tls_skip_verify"`
	CACert         *string `json:"ca_cert"`
	ClearCACert    bool    `json:"clear_ca_cert"`
	TimeoutSeconds *int    `json:"timeout_seconds"`
	MaxConcurrency *int    `json:"max_concurrency"`
	Enabled        *bool   `json:"enabled"`
	Remark         *string `json:"remark"`
}

// DatasourceTestResult is returned by the connection test; it is persisted on the datasource.
type DatasourceTestResult struct {
	Status        string `json:"status"`
	ServerVersion string `json:"server_version"`
	LatencyMs     int    `json:"latency_ms"`
	Error         string `json:"error"`
}

// MetricMappingDTO is the admin-facing representation of a metric mapping.
type MetricMappingDTO struct {
	ID        uint      `json:"id"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	DatasourceID   uint     `json:"datasource_id"`
	DatasourceName string   `json:"datasource_name"`
	Name           string   `json:"name"`
	Metric         string   `json:"metric"`
	Level          string   `json:"level"`
	Query          string   `json:"query"`
	Variables      []string `json:"variables"`
	Unit           string   `json:"unit"`
	StepSeconds    int      `json:"step_seconds"`
	Enabled        bool     `json:"enabled"`
	Remark         string   `json:"remark"`
	PresetID       string   `json:"preset_id"`
}

type MetricMappingListReq struct {
	ListOptions
	DatasourceID uint   `form:"datasource_id" json:"datasource_id"`
	Metric       string `form:"metric" json:"metric"`
	Level        string `form:"level" json:"level"`
	Enabled      string `form:"enabled" json:"enabled"`
}

type MetricMappingCreateReq struct {
	DatasourceID uint   `json:"datasource_id" binding:"required"`
	Name         string `json:"name" binding:"required"`
	Metric       string `json:"metric" binding:"required"`
	Level        string `json:"level" binding:"required"`
	Query        string `json:"query" binding:"required"`
	Unit         string `json:"unit"`
	StepSeconds  int    `json:"step_seconds"`
	Enabled      *bool  `json:"enabled"`
	Remark       string `json:"remark"`
}

type MetricMappingUpdateReq struct {
	DatasourceID *uint   `json:"datasource_id"`
	Name         *string `json:"name"`
	Metric       *string `json:"metric"`
	Level        *string `json:"level"`
	Query        *string `json:"query"`
	Unit         *string `json:"unit"`
	StepSeconds  *int    `json:"step_seconds"`
	Enabled      *bool   `json:"enabled"`
	Remark       *string `json:"remark"`
}

// MetricCatalogDTO describes what a mapping may use so forms need not hard-code it.
type MetricCatalogDTO struct {
	Metrics   []MetricCatalogMetric `json:"metrics"`
	Levels    []MetricCatalogLevel  `json:"levels"`
	Variables []string              `json:"variables"`
	MinStep   int                   `json:"min_step_seconds"`
	MaxStep   int                   `json:"max_step_seconds"`
}

type MetricCatalogMetric struct {
	Metric string `json:"metric"`
	Unit   string `json:"unit"`
}

type MetricCatalogLevel struct {
	Level    string   `json:"level"`
	Required []string `json:"required_variables"`
}
